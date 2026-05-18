// server.go: host-side Unix-domain socket server that the in-sandbox
// 143-tools helpers call into for fresh GitHub credentials. One socket per
// session, opened by the orchestrator just before container creation and
// closed when the run ends. Identity is resolved on every request via the
// shared identity.Resolver, so refreshes happen automatically and any
// change in org PR-authorship policy takes effect on the next git push
// without restarting the session.

package sandboxauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
)

// Resolver is the slice of identity.Resolver the server actually uses.
// Defined as an interface so the server is unit-testable without spinning
// up the full identity package's dependencies.
type Resolver interface {
	Resolve(ctx context.Context, run *models.Session, repo *models.Repository, orgSettings models.OrgSettings, mode string) (*identity.Resolution, error)
}

// Server owns the on-disk socket directory and the goroutines for active
// per-session listeners. Construct with NewServer; call Listen at session
// start and Close(sessionID) at session end.
type Server struct {
	resolver  Resolver
	socketDir string
	logger    zerolog.Logger

	// resolveTimeout bounds a single host-side Resolve call (which may make
	// outbound HTTP). Less than CallTimeout so the in-sandbox client sees a
	// clean response rather than a transport timeout when the resolver is
	// slow.
	resolveTimeout time.Duration

	mu     sync.Mutex
	active map[uuid.UUID]*activeListener
}

type activeListener struct {
	close func()
}

// NewServer constructs a Server. socketDir is the on-host directory where
// per-session sockets are created — must already exist (provisioned by
// deploy/scripts/provision.sh) and be writable by the orchestrator process.
// Sockets are removed on session-end, but a stale socket from a crashed
// orchestrator is detected and cleaned up by Listen on next use.
func NewServer(resolver Resolver, socketDir string, logger zerolog.Logger) *Server {
	probe := logger.Info().Str("socket_dir", socketDir)
	if info, err := os.Stat(socketDir); err != nil {
		probe = logger.Warn().Str("socket_dir", socketDir).Err(err)
		probe.Msg("sandboxauth: socket dir stat failed at startup (will retry on first Listen via MkdirAll); ensure deploy/scripts/provision.sh ran on this host")
	} else {
		probe.
			Str("mode", fmt.Sprintf("%#o", info.Mode().Perm())).
			Bool("is_dir", info.IsDir()).
			Msg("sandboxauth: socket dir present at startup (expected mode 0750 owned 1000:1000; see provision.sh)")
	}
	return &Server{
		resolver:       resolver,
		socketDir:      socketDir,
		logger:         logger,
		resolveTimeout: 25 * time.Second,
		active:         make(map[uuid.UUID]*activeListener),
	}
}

// Listen opens a per-session socket and starts an accept loop in a
// background goroutine. The returned socketPath is the on-host path the
// caller should bind-mount into the container; teardown goes through
// Close(sessionID), not a per-call closer. We chose this single-owner
// model after early iterations exposed a per-call closeFn alongside
// Close(sessionID): callers had to decide which to invoke from each
// error branch, and the two paths could disagree about which entry was
// active in s.active. Routing all teardown through Close(sessionID) means
// the Server alone decides what's currently bound to a session.
//
// Each session gets its own subdirectory (<socketDir>/<sessionID>/) which
// is the bind-mount source inside the container. The socket file lives
// inside that subdir as `sock`. The subdir is reused across turns so the
// container's bind-mount target keeps resolving to the live socket file
// even after a turn-end close+reopen cycle.
//
// The capture closure (run, repo, orgSettings) is held in memory for the
// listener's lifetime. Each credential request still re-runs the resolver,
// so the user-token vs installation-token decision picks up fresh OAuth
// state (token refreshes, repo-access changes) on every push without a
// DB hit. The org settings snapshot, however, is fixed for the listener:
// the orchestrator opens a new listener at every turn boundary (including
// the reusedExisting / preview-held branch), so a PR-authorship-mode flip
// takes effect on the next turn — not within an in-flight turn. That
// tradeoff matches what the PR-creation flow gives users today.
func (s *Server) Listen(
	ctx context.Context,
	sessionID uuid.UUID,
	run *models.Session,
	repo *models.Repository,
	orgSettings models.OrgSettings,
) (string, error) {
	if s.socketDir == "" {
		return "", errors.New("sandboxauth: socket directory not configured")
	}
	if err := os.MkdirAll(s.socketDir, 0o750); err != nil {
		return "", fmt.Errorf("sandboxauth: ensure socket dir: %w", err)
	}
	// Defense-in-depth: the parent dir is the gate that decides which host
	// processes can even reach the socket inodes. Cross-tenant isolation
	// relies on it being mode 0750 (or stricter) and owned by the
	// orchestrator user. If a deploy-script regression ever loosens it,
	// fail fast at startup rather than silently expose every session's
	// socket to local processes.
	if err := assertParentDirPerms(s.socketDir); err != nil {
		return "", fmt.Errorf("sandboxauth: %w", err)
	}
	if prev := s.detach(sessionID); prev != nil {
		prev()
	}
	sessionDir := filepath.Join(s.socketDir, sessionID.String())
	if err := os.MkdirAll(sessionDir, 0o750); err != nil {
		return "", fmt.Errorf("sandboxauth: ensure session dir: %w", err)
	}
	sockPath := filepath.Join(sessionDir, SocketFileName)

	// Clean up any leftover socket from a previous (likely-crashed) run for
	// this same session, or from a clean turn-end teardown that left the
	// per-session dir intact for the bind-mount. AF_UNIX `bind` fails if
	// the path exists, so this is the difference between recovering
	// automatically and refusing to start.
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return "", fmt.Errorf("sandboxauth: listen on %s: %w", sockPath, err)
	}
	// Keep the socket owner-only. The orchestrator (running as appuser in
	// the worker server image) and the in-sandbox client (running as the
	// `sandbox` user in the sandbox image) are both pinned to UID 1000,
	// so the bind-mounted socket file's preserved owner UID matches at
	// both ends without granting group/world access. See the Dockerfile
	// and sandbox/Dockerfile for the pinning, and provision.sh for the
	// matching ownership on the host bind-mount source.
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(sockPath)
		return "", fmt.Errorf("sandboxauth: chmod %s: %w", sockPath, err)
	}

	logger := s.logger.With().
		Str("session_id", sessionID.String()).
		Str("socket", sockPath).
		Logger()

	loopCtx, cancel := context.WithCancel(context.Background())
	go s.acceptLoop(loopCtx, ln, run, repo, orgSettings, logger)

	var closeOnce sync.Once
	entry := &activeListener{}
	closeFn := func() {
		closeOnce.Do(func() {
			cancel()
			_ = ln.Close()
			if err := os.Remove(sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				logger.Warn().Err(err).Msg("sandboxauth: failed to remove socket")
			}
			// Best-effort: remove the per-session dir too so we don't leak
			// empty dirs across thousands of sessions. os.Remove (not
			// RemoveAll) refuses to delete a non-empty dir, which is the
			// safety belt: if a future bind-mount artifact lingers, we
			// preserve it for inspection rather than blowing it away.
			if err := os.Remove(sessionDir); err != nil && !errors.Is(err, os.ErrNotExist) {
				logger.Debug().Err(err).Msg("sandboxauth: per-session dir not removed (likely still bind-mounted; cleanup will retry on next session-end)")
			}
			s.mu.Lock()
			if current := s.active[sessionID]; current == entry {
				delete(s.active, sessionID)
			}
			s.mu.Unlock()
			logger.Info().Msg("sandboxauth: listener closed")
		})
	}
	entry.close = closeFn
	s.mu.Lock()
	s.active[sessionID] = entry
	s.mu.Unlock()
	logger.Info().Msg("sandboxauth: listener started")
	return sockPath, nil
}

// Close stops and removes the active listener for sessionID, if any.
// Idempotent: calling it for an unknown session does nothing.
func (s *Server) Close(sessionID uuid.UUID) {
	if closeFn := s.detach(sessionID); closeFn != nil {
		closeFn()
	}
}

// Shutdown stops every active listener and removes their sockets. Used at
// graceful-orchestrator-shutdown time so sockets and per-session subdirs
// don't outlive the process they're bound to. Tests also call it to drain
// a Server before asserting cleanup.
//
// Idempotent: a second call after the map is drained is a no-op.
func (s *Server) Shutdown() {
	for {
		s.mu.Lock()
		var (
			sessionID uuid.UUID
			closeFn   func()
		)
		for id, entry := range s.active {
			sessionID = id
			closeFn = entry.close
			break
		}
		s.mu.Unlock()
		if closeFn == nil {
			return
		}
		// Detach removes the entry from s.active and returns the closer
		// (may be the same closer we already grabbed; closeOnce makes the
		// double-call safe).
		if detached := s.detach(sessionID); detached != nil {
			detached()
		} else {
			closeFn()
		}
	}
}

// SweepStaleSessionDirs removes per-session subdirectories under socketDir
// whose UUID isn't in keep. Called by the worker boot path after rehydrate
// has re-Listen'd for every still-alive container, so anything left over is
// either (a) a session whose container is dead (orphaned by a worker crash
// before Close ran) or (b) a session whose row was already deleted. Either
// way, the subdir is no longer load-bearing and accumulates indefinitely
// without this sweep.
//
// Boot-time sequencing assumption: this is called synchronously from the
// worker boot path after RehydrateSandboxAuthListeners returns and BEFORE
// processWorkers begin claiming jobs. Because of that ordering, no
// concurrent Listen() can race the os.ReadDir → os.RemoveAll loop below —
// the only Listens that could be in flight while sweep runs are the ones
// rehydrate itself just made, and those session IDs are exactly what's in
// `keep`. If a future refactor backgrounds rehydrate or moves the sweep
// off the boot path, this assumption breaks and we'd need a per-session
// mutex (or to read s.active under the server's lock).
//
// Best-effort: a directory we can't read or remove is logged and skipped.
// Subdirs in keep are left untouched even if they look empty — a freshly
// rehydrated listener may not have written its socket file yet.
//
// Names that don't parse as UUIDs are also skipped: socketDir is shared with
// no other writers by contract, but defending against a stray file (e.g. a
// future tmpfile artifact, an op-team manual probe) is cheap and prevents
// this sweep from accidentally deleting something it shouldn't.
func (s *Server) SweepStaleSessionDirs(keep map[uuid.UUID]struct{}) {
	if s.socketDir == "" {
		return
	}
	entries, err := os.ReadDir(s.socketDir)
	if err != nil {
		s.logger.Warn().Err(err).Str("socket_dir", s.socketDir).Msg("sandboxauth: sweep failed to read socket dir")
		return
	}
	var swept, kept, skipped int
	for _, entry := range entries {
		if !entry.IsDir() {
			skipped++
			continue
		}
		id, err := uuid.Parse(entry.Name())
		if err != nil {
			skipped++
			continue
		}
		if _, ok := keep[id]; ok {
			kept++
			continue
		}
		dirPath := filepath.Join(s.socketDir, entry.Name())
		// RemoveAll because the subdir typically contains a leftover socket
		// file (and possibly a stale lock or temp artifact). The contract
		// for socketDir is "Server owns it" — nothing outside this package
		// writes here, so removing the whole tree is safe.
		if err := os.RemoveAll(dirPath); err != nil {
			s.logger.Warn().Err(err).Str("session_dir", dirPath).Msg("sandboxauth: sweep failed to remove stale session dir")
			continue
		}
		swept++
	}
	if swept > 0 || kept > 0 {
		s.logger.Info().
			Int("swept", swept).
			Int("kept", kept).
			Int("skipped", skipped).
			Msg("sandboxauth: stale session dir sweep complete")
	}
}

func (s *Server) detach(sessionID uuid.UUID) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.active[sessionID]
	if entry == nil {
		return nil
	}
	delete(s.active, sessionID)
	return entry.close
}

// acceptLoop runs until the listener is closed. Each connection is handled
// in its own goroutine so a slow Resolve call doesn't block other
// in-flight credential requests (e.g. an agent running `git push` and
// `gh pr comment` concurrently).
func (s *Server) acceptLoop(
	ctx context.Context,
	ln net.Listener,
	run *models.Session,
	repo *models.Repository,
	orgSettings models.OrgSettings,
	logger zerolog.Logger,
) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// errors.Is(err, net.ErrClosed) is the expected exit path;
			// anything else is worth surfacing for debugging.
			if !errors.Is(err, net.ErrClosed) {
				logger.Warn().Err(err).Msg("sandboxauth: accept error")
			}
			return
		}
		go s.handleConn(ctx, conn, run, repo, orgSettings, logger)
	}
}

// handleConn reads exactly one request, resolves identity, writes one
// response. Per the wire protocol's one-request-per-connection convention,
// we close the connection after replying — the client opens a fresh
// connection for each credential request.
func (s *Server) handleConn(
	ctx context.Context,
	conn net.Conn,
	run *models.Session,
	repo *models.Repository,
	orgSettings models.OrgSettings,
	logger zerolog.Logger,
) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(CallTimeout))

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		s.writeError(conn, fmt.Sprintf("decode request: %s", err))
		logger.Warn().Err(err).Msg("sandboxauth: bad request")
		return
	}
	if req.Op != OpGet {
		s.writeError(conn, fmt.Sprintf("unsupported op %q", req.Op))
		return
	}

	resolveCtx, cancel := context.WithTimeout(ctx, s.resolveTimeout)
	defer cancel()
	res, err := s.resolver.Resolve(resolveCtx, run, repo, orgSettings, "")
	if err != nil {
		s.writeError(conn, fmt.Sprintf("resolve identity: %s", err))
		logger.Warn().Err(err).Str("action", string(req.Action)).Msg("sandboxauth: resolve failed")
		return
	}
	resp := Response{
		Token:     res.Token,
		Username:  DefaultUsername,
		Identity:  Identity(res.AuthoredBy()),
		ExpiresAt: res.ExpiresAt,
	}
	if res.User != nil && res.User.GitHubLogin != nil {
		resp.Login = *res.User.GitHubLogin
	}
	if err := json.NewEncoder(conn).Encode(&resp); err != nil {
		logger.Warn().Err(err).Msg("sandboxauth: write response")
		return
	}
	logger.Debug().
		Str("identity", string(resp.Identity)).
		Str("action", string(req.Action)).
		Msg("sandboxauth: served credential")
}

// writeError best-effort returns a structured error to the client. Failures
// are swallowed because the connection is about to close anyway.
func (s *Server) writeError(conn net.Conn, msg string) {
	_ = json.NewEncoder(conn).Encode(&Response{Error: msg})
}

// ValidateSocketDirForStartup is the process-start preflight for production
// workers. Listen also checks this immediately before binding a per-session
// socket, but checking at startup keeps a misprovisioned host from registering
// as healthy and claiming jobs it cannot run.
func ValidateSocketDirForStartup(dir string) error {
	if err := assertParentDirPerms(dir); err != nil {
		return err
	}
	probeDir, err := os.MkdirTemp(dir, ".startup-probe-*")
	if err != nil {
		return fmt.Errorf("socket dir %s is not writable by uid %d: %w", dir, os.Geteuid(), err)
	}
	if err := os.RemoveAll(probeDir); err != nil {
		return fmt.Errorf("remove socket dir startup probe %s: %w", probeDir, err)
	}
	return nil
}

// assertParentDirPerms verifies that the socket directory is mode 0750 or
// stricter (no world-readable bit, no world-executable bit). This is the
// gate that decides which host processes can even see the per-session
// subdirs and the socket inodes inside them.
//
// If the dir doesn't exist yet (first boot before MkdirAll), the caller
// has already MkdirAll'd it with 0750, so this assertion is layered on
// top to catch deploy-script regressions.
func assertParentDirPerms(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat socket dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("socket dir %s is not a directory", dir)
	}
	mode := info.Mode().Perm()
	const allowedMask os.FileMode = 0o750
	if mode&^allowedMask != 0 {
		return fmt.Errorf("socket dir %s has insecure perms %#o; expected 0750 or stricter (deploy/scripts/provision.sh provisions this directory)", dir, mode)
	}
	return nil
}
