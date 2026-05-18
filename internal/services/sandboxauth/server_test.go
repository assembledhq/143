package sandboxauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
)

// stubResolver is a programmable Resolver that records the args it was
// called with so tests can assert the server passes session/repo/settings
// through unchanged on every credential request.
type stubResolver struct {
	resolution *identity.Resolution
	err        error
	calls      int
}

func (s *stubResolver) Resolve(_ context.Context, _ *models.Session, _ *models.Repository, _ models.OrgSettings, _ string) (*identity.Resolution, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.resolution, nil
}

type errListener struct {
	err error
}

func (l *errListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *errListener) Close() error              { return nil }
func (l *errListener) Addr() net.Addr            { return &net.UnixAddr{Name: "test.sock", Net: "unix"} }

// shortSocketDir returns a short host directory for AF_UNIX sockets. macOS
// limits AF_UNIX paths to ~104 bytes (108 on Linux); the default
// `os.TempDir()` on macOS is `/var/folders/...` which can exceed 80 chars
// before we even append a UUID/sock suffix. We sit directly under /tmp so
// production-like UUID-based path lengths fit.
//
// The dir is chmod'd to 0750 to match what production provision.sh creates
// — the server's startup assertion rejects anything looser.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "143a-*")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o750))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestServer_ServesResolverResponse(t *testing.T) {
	t.Parallel()
	githubLogin := "alice"
	resolver := &stubResolver{
		resolution: &identity.Resolution{
			Token:     "ghs_test",
			Source:    identity.SourceUser,
			User:      &models.User{GitHubLogin: &githubLogin},
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	srv := NewServer(resolver, shortSocketDir(t), zerolog.Nop())

	sessionID := uuid.New()
	sock, err := srv.Listen(
		context.Background(),
		sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer srv.Close(sessionID)

	resp, err := NewClient(sock).Get(context.Background(), ActionPush)
	require.NoError(t, err)
	require.Equal(t, "ghs_test", resp.Token)
	require.Equal(t, IdentityUser, resp.Identity)
	require.Equal(t, "alice", resp.Login)
	require.Equal(t, DefaultUsername, resp.Username)
}

func TestServer_RefreshesPerCall(t *testing.T) {
	t.Parallel()
	resolver := &stubResolver{
		resolution: &identity.Resolution{
			Token:  "ghs_v1",
			Source: identity.SourceApp,
		},
	}
	srv := NewServer(resolver, shortSocketDir(t), zerolog.Nop())

	sessionID := uuid.New()
	sock, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer srv.Close(sessionID)

	for i := 0; i < 3; i++ {
		resp, err := NewClient(sock).Get(context.Background(), ActionPush)
		require.NoError(t, err)
		require.Equal(t, "ghs_v1", resp.Token)
	}
	require.Equal(t, 3, resolver.calls, "each credential request must trigger a fresh Resolve so refreshes propagate")
}

func TestServer_SurfacesResolverErrors(t *testing.T) {
	t.Parallel()
	resolver := &stubResolver{err: errors.New("token revoked")}
	srv := NewServer(resolver, shortSocketDir(t), zerolog.Nop())

	sessionID := uuid.New()
	sock, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer srv.Close(sessionID)

	resp, err := NewClient(sock).Get(context.Background(), ActionPush)
	require.NoError(t, err, "transport should still succeed even when the host returns an Error payload")
	require.Empty(t, resp.Token)
	require.Contains(t, resp.Error, "token revoked")
}

func TestServer_RemovesStaleSocketOnListen(t *testing.T) {
	t.Parallel()
	dir := shortSocketDir(t)
	sessionID := uuid.New()
	staleDir := dir + "/" + sessionID.String()
	require.NoError(t, os.MkdirAll(staleDir, 0o750))
	stalePath := staleDir + "/" + SocketFileName

	// Simulate a leftover from a crashed orchestrator: empty file at the
	// socket path. The next Listen() must clean it up rather than fail.
	require.NoError(t, os.WriteFile(stalePath, []byte("stale"), 0o600))

	resolver := &stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}
	srv := NewServer(resolver, dir, zerolog.Nop())

	sock, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err, "stale socket file must not block Listen")
	require.Equal(t, stalePath, sock)
	defer srv.Close(sessionID)

	// Verify the listener actually works post-recovery.
	resp, err := NewClient(sock).Get(context.Background(), ActionPush)
	require.NoError(t, err)
	require.Equal(t, "tok", resp.Token)
}

// TestServer_ListenAfterClose_ReusesSessionDir verifies that a close+reopen
// cycle on the same sessionID is supported, which is the core of the
// directory-bind-mount design: the per-session dir survives close so an
// alive container's bind-mount keeps resolving across the recreate, even
// though the socket file itself was unlinked.
func TestServer_ListenAfterClose_ReusesSessionDir(t *testing.T) {
	t.Parallel()
	dir := shortSocketDir(t)
	sessionID := uuid.New()

	resolver := &stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}
	srv := NewServer(resolver, dir, zerolog.Nop())

	sock1, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: sessionID, OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	srv.Close(sessionID)

	sock2, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: sessionID, OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err, "Listen must succeed after a prior close on the same session")
	defer srv.Close(sessionID)
	require.Equal(t, sock1, sock2, "the per-session socket path must be deterministic so recreates land at the same in-container bind-mount target")

	// Connections to the recreated socket must still work.
	resp, err := NewClient(sock2).Get(context.Background(), ActionPush)
	require.NoError(t, err)
	require.Equal(t, "tok", resp.Token)
}

func TestServer_ListenCreatesSocketWithOwnerOnlyPerms(t *testing.T) {
	t.Parallel()

	resolver := &stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}
	srv := NewServer(resolver, shortSocketDir(t), zerolog.Nop())

	sessionID := uuid.New()
	sock, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err, "Listen should create the per-session auth socket")
	defer srv.Close(sessionID)

	info, err := os.Stat(sock)
	require.NoError(t, err, "Listen should leave a socket inode on disk")
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "sandbox auth socket should be owner-only")
}

func TestServer_ListenRejectsMissingSocketDirConfig(t *testing.T) {
	t.Parallel()

	srv := NewServer(&stubResolver{}, "", zerolog.Nop())
	_, err := srv.Listen(context.Background(), uuid.New(), &models.Session{}, &models.Repository{}, models.OrgSettings{})
	require.Error(t, err, "Listen should reject an empty socket directory")
	require.Contains(t, err.Error(), "socket directory not configured", "Listen should explain the missing configuration")
}

func TestServer_ListenReplacesExistingSessionListener(t *testing.T) {
	t.Parallel()

	dir := shortSocketDir(t)
	sessionID := uuid.New()
	srv := NewServer(&stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}, dir, zerolog.Nop())

	_, err := srv.Listen(context.Background(), sessionID, &models.Session{ID: sessionID, OrgID: uuid.New()}, &models.Repository{InstallationID: 1, FullName: "owner/repo"}, models.OrgSettings{})
	require.NoError(t, err, "first Listen should succeed")

	// Second Listen on the same sessionID atomically detaches and closes
	// the prior entry inside the Server, so we don't keep a separate
	// closeFn for the first one.
	sock, err := srv.Listen(context.Background(), sessionID, &models.Session{ID: sessionID, OrgID: uuid.New()}, &models.Repository{InstallationID: 1, FullName: "owner/repo"}, models.OrgSettings{})
	require.NoError(t, err, "second Listen on the same session should replace the prior listener")
	defer srv.Close(sessionID)

	resp, err := NewClient(sock).Get(context.Background(), ActionPush)
	require.NoError(t, err, "replaced listener should still serve credentials")
	require.Equal(t, "tok", resp.Token, "replacement listener should serve the configured token")
}

// TestServer_ListenRejectsLooseDirPerms is the deploy-regression net: if
// provision.sh ever drifts and creates the socket dir with world-readable
// or world-executable bits, Listen must refuse rather than silently expose
// every session's socket to local processes.
func TestServer_ListenRejectsLooseDirPerms(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("/tmp", "143a-loose-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// 0755 has the world-readable + world-executable bits, which would let
	// any local process traverse into the per-session dirs.
	require.NoError(t, os.Chmod(dir, 0o755))

	resolver := &stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}
	srv := NewServer(resolver, dir, zerolog.Nop())

	_, err = srv.Listen(context.Background(), uuid.New(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insecure perms")
}

func TestValidateSocketDirForStartup(t *testing.T) {
	t.Parallel()

	t.Run("accepts provisioned socket dir", func(t *testing.T) {
		t.Parallel()

		dir := shortSocketDir(t)
		err := ValidateSocketDirForStartup(dir)
		require.NoError(t, err, "startup validation should accept a provisioned 0750 socket dir")
	})

	t.Run("rejects docker-created bind mount source", func(t *testing.T) {
		t.Parallel()

		dir, err := os.MkdirTemp("/tmp", "143a-docker-created-*")
		require.NoError(t, err, "test should create a docker-like bind mount source")
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		require.NoError(t, os.Chmod(dir, 0o755), "test should mimic Docker's default bind source permissions")

		err = ValidateSocketDirForStartup(dir)
		require.Error(t, err, "startup validation should reject a world-traversable socket dir")
		require.Contains(t, err.Error(), "insecure perms", "startup validation error should explain the bad permissions")
	})
}

func TestServer_CloseRemovesSocketAndStopsAccepting(t *testing.T) {
	t.Parallel()
	resolver := &stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}
	srv := NewServer(resolver, shortSocketDir(t), zerolog.Nop())

	sessionID := uuid.New()
	sock, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)

	// Sanity check that the socket exists.
	_, err = os.Stat(sock)
	require.NoError(t, err)

	srv.Close(sessionID)
	_, err = os.Stat(sock)
	require.True(t, os.IsNotExist(err), "socket must be removed by close")

	// Idempotency: second Close on the same sessionID is a no-op (Server
	// drops it from active map after first close).
	srv.Close(sessionID)

	// Connections to the now-removed socket should fail.
	_, err = net.Dial("unix", sock)
	require.Error(t, err)
}

func TestServer_CloseUnknownSessionIsNoOp(t *testing.T) {
	t.Parallel()

	srv := NewServer(&stubResolver{}, shortSocketDir(t), zerolog.Nop())
	srv.Close(uuid.New())
}

// TestServer_ShutdownDrainsAllListeners simulates a graceful orchestrator
// shutdown with multiple sessions in flight: every socket must be removed
// and every accept loop must exit, with no leftover listeners in the
// active map. A second Shutdown call must be a no-op.
func TestServer_ShutdownDrainsAllListeners(t *testing.T) {
	t.Parallel()

	dir := shortSocketDir(t)
	resolver := &stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}
	srv := NewServer(resolver, dir, zerolog.Nop())

	const sessionCount = 4
	socks := make([]string, 0, sessionCount)
	for i := 0; i < sessionCount; i++ {
		sock, err := srv.Listen(
			context.Background(),
			uuid.New(),
			&models.Session{ID: uuid.New(), OrgID: uuid.New()},
			&models.Repository{InstallationID: 1, FullName: "owner/repo"},
			models.OrgSettings{},
		)
		require.NoError(t, err, "Listen %d should succeed", i)
		socks = append(socks, sock)
	}

	srv.Shutdown()

	for _, sock := range socks {
		_, err := os.Stat(sock)
		require.True(t, os.IsNotExist(err), "Shutdown should remove socket %s; got err=%v", sock, err)
	}
	srv.mu.Lock()
	require.Empty(t, srv.active, "Shutdown should drain the active listener map")
	srv.mu.Unlock()

	// Idempotency: a second Shutdown after the map is drained must be a no-op.
	require.NotPanics(t, func() { srv.Shutdown() }, "Shutdown should be idempotent")
}

func TestServer_CloseLeavesNonEmptySessionDirForInspection(t *testing.T) {
	t.Parallel()

	dir := shortSocketDir(t)
	sessionID := uuid.New()
	srv := NewServer(&stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}, dir, zerolog.Nop())

	sock, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err, "Listen should succeed")

	sessionDir := filepath.Dir(sock)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "still-mounted"), []byte("busy"), 0o600), "test should create a file that blocks session-dir removal")

	srv.Close(sessionID)

	_, err = os.Stat(sessionDir)
	require.NoError(t, err, "close should leave a non-empty session directory in place for later cleanup")
}

// TestServer_EndToEnd_HandleSubcommand wires the complete in-sandbox path
// (HandleSubcommand → Client.Get) against the real Server (not a hand-rolled
// socket handler). It is the regression net for protocol drift between the
// CLI dispatch and the server: a future change to the wire shape that
// breaks one half but not the other will fail here.
func TestServer_EndToEnd_HandleSubcommand(t *testing.T) {
	githubLogin := "alice"
	// No t.Parallel: t.Setenv mutates process-global env and panics under
	// parallel tests.
	resolver := &stubResolver{
		resolution: &identity.Resolution{
			Token:  "ghs_e2e",
			Source: identity.SourceUser,
			User:   &models.User{GitHubLogin: &githubLogin},
		},
	}
	srv := NewServer(resolver, shortSocketDir(t), zerolog.Nop())

	sessionID := uuid.New()
	sock, err := srv.Listen(
		context.Background(),
		sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer srv.Close(sessionID)

	t.Setenv(SocketEnvVar, sock)

	// git-credential get: should print the username/password pair.
	stdin := bytes.NewBufferString("protocol=https\nhost=github.com\n\n")
	var stdout, stderr bytes.Buffer
	handled, code := HandleSubcommand([]string{"git-credential", "get"}, stdin, &stdout, &stderr)
	require.True(t, handled)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	require.Equal(t, "username=x-access-token\npassword=ghs_e2e\n", stdout.String())

	// auth-token: should print just the token (the gh wrapper relies on this).
	stdout.Reset()
	stderr.Reset()
	handled, code = HandleSubcommand([]string{"auth-token", "--action=api"}, nil, &stdout, &stderr)
	require.True(t, handled)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	require.Equal(t, "ghs_e2e\n", stdout.String())

	// Both calls should have re-resolved against the host.
	require.Equal(t, 2, resolver.calls)
}

func TestServer_RejectsUnknownOp(t *testing.T) {
	t.Parallel()
	resolver := &stubResolver{}
	srv := NewServer(resolver, shortSocketDir(t), zerolog.Nop())

	sessionID := uuid.New()
	sock, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer srv.Close(sessionID)

	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, json.NewEncoder(conn).Encode(map[string]string{"op": "delete-everything"}))

	var resp Response
	require.NoError(t, json.NewDecoder(conn).Decode(&resp))
	require.Empty(t, resp.Token)
	require.Contains(t, resp.Error, "unsupported op")
}

func TestServer_AcceptLoop_IgnoresClosedListenerAndReturnsOnOtherErrors(t *testing.T) {
	t.Parallel()

	srv := NewServer(&stubResolver{}, shortSocketDir(t), zerolog.Nop())
	srv.acceptLoop(context.Background(), &errListener{err: net.ErrClosed}, &models.Session{}, &models.Repository{}, models.OrgSettings{}, zerolog.Nop())
	srv.acceptLoop(context.Background(), &errListener{err: errors.New("accept failed")}, &models.Session{}, &models.Repository{}, models.OrgSettings{}, zerolog.Nop())
}

func TestServer_HandleConn_ErrorBranches(t *testing.T) {
	t.Parallel()

	t.Run("decode request failure returns structured error", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := net.Pipe()
		defer clientConn.Close()

		done := make(chan struct{})
		go func() {
			defer close(done)
			NewServer(&stubResolver{}, shortSocketDir(t), zerolog.Nop()).
				handleConn(context.Background(), serverConn, &models.Session{}, &models.Repository{}, models.OrgSettings{}, zerolog.Nop())
		}()

		_, err := clientConn.Write([]byte("not-json\n"))
		require.NoError(t, err, "client should be able to send an invalid payload")

		var resp Response
		require.NoError(t, json.NewDecoder(clientConn).Decode(&resp), "server should respond with a structured error")
		require.Contains(t, resp.Error, "decode request", "server should explain the decode failure")
		<-done
	})

	t.Run("write response failure is tolerated", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := net.Pipe()
		resolver := &stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}
		done := make(chan struct{})

		go func() {
			defer close(done)
			NewServer(resolver, shortSocketDir(t), zerolog.Nop()).
				handleConn(context.Background(), serverConn, &models.Session{}, &models.Repository{}, models.OrgSettings{}, zerolog.Nop())
		}()

		require.NoError(t, json.NewEncoder(clientConn).Encode(&Request{Op: OpGet}), "client should send a valid request")
		require.NoError(t, clientConn.Close(), "client should close its side before the server writes the response")
		<-done
		require.Equal(t, 1, resolver.calls, "server should still resolve identity before the write fails")
	})
}

func TestAssertParentDirPerms_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("missing path", func(t *testing.T) {
		t.Parallel()

		err := assertParentDirPerms(filepath.Join(t.TempDir(), "missing"))
		require.Error(t, err, "assertParentDirPerms should fail for a missing directory")
		require.Contains(t, err.Error(), "stat socket dir", "error should mention the stat failure")
	})

	t.Run("path is not a directory", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(path, []byte("file"), 0o600), "test should create a regular file")

		err := assertParentDirPerms(path)
		require.Error(t, err, "assertParentDirPerms should reject regular files")
		require.Contains(t, err.Error(), "is not a directory", "error should explain the type mismatch")
	})
}

// TestServer_SweepStaleSessionDirs covers the post-rehydrate hygiene pass:
// per-session subdirs whose UUIDs aren't in `keep` get removed, dirs that
// are kept survive, and non-UUID entries are left alone (the contract is
// that socketDir is single-writer, but the sweep should still defend against
// stray files rather than blow them away).
func TestServer_SweepStaleSessionDirs(t *testing.T) {
	t.Parallel()
	dir := shortSocketDir(t)

	keepID := uuid.New()
	staleID1 := uuid.New()
	staleID2 := uuid.New()

	for _, id := range []uuid.UUID{keepID, staleID1, staleID2} {
		subdir := filepath.Join(dir, id.String())
		require.NoError(t, os.MkdirAll(subdir, 0o750))
		// Drop a leftover socket-shaped file inside each subdir to mimic the
		// crash scenario this sweep is for: files left behind by a crashed
		// orchestrator that didn't reach its Shutdown closer.
		require.NoError(t, os.WriteFile(filepath.Join(subdir, SocketFileName), []byte{}, 0o600))
	}
	// A non-UUID entry: must be left untouched even though it's not in keep,
	// so the sweep can't clobber stray files dropped by ops/tooling.
	strayPath := filepath.Join(dir, "not-a-uuid")
	require.NoError(t, os.WriteFile(strayPath, []byte("manual probe"), 0o600))

	srv := NewServer(&stubResolver{}, dir, zerolog.Nop())
	srv.SweepStaleSessionDirs(map[uuid.UUID]struct{}{keepID: {}})

	_, err := os.Stat(filepath.Join(dir, keepID.String()))
	require.NoError(t, err, "kept session dir should still exist after sweep")

	_, err = os.Stat(filepath.Join(dir, staleID1.String()))
	require.True(t, os.IsNotExist(err), "stale session dir 1 should be removed")
	_, err = os.Stat(filepath.Join(dir, staleID2.String()))
	require.True(t, os.IsNotExist(err), "stale session dir 2 should be removed")

	_, err = os.Stat(strayPath)
	require.NoError(t, err, "non-UUID entries should be left alone")
}

// TestServer_SweepStaleSessionDirs_EmptyKeep verifies sweep with an empty
// keep set removes every UUID-named subdir — the case where a worker boots
// with no live preview-held containers.
func TestServer_SweepStaleSessionDirs_EmptyKeep(t *testing.T) {
	t.Parallel()
	dir := shortSocketDir(t)

	id := uuid.New()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, id.String()), 0o750))

	srv := NewServer(&stubResolver{}, dir, zerolog.Nop())
	srv.SweepStaleSessionDirs(nil)

	_, err := os.Stat(filepath.Join(dir, id.String()))
	require.True(t, os.IsNotExist(err), "every UUID-named subdir should be swept when keep is empty")
}

// TestServer_SweepStaleSessionDirs_NoSocketDir is the early-return path:
// when socketDir is unset (local-dev with no SANDBOX_AUTH_SOCKET_DIR), the
// sweep must be a no-op rather than panicking on os.ReadDir("").
func TestServer_SweepStaleSessionDirs_NoSocketDir(t *testing.T) {
	t.Parallel()

	srv := NewServer(&stubResolver{}, "", zerolog.Nop())
	require.NotPanics(t, func() {
		srv.SweepStaleSessionDirs(map[uuid.UUID]struct{}{uuid.New(): {}})
	}, "sweep must short-circuit on an unset socketDir without touching the filesystem")
}

// TestServer_SweepStaleSessionDirs_ReadDirFailure covers the branch where
// ReadDir returns an error (e.g. socketDir was removed out from under us
// between NewServer-time stat probe and the boot-time sweep). The function
// must log and return cleanly — there's nothing useful to sweep against an
// inaccessible directory.
func TestServer_SweepStaleSessionDirs_ReadDirFailure(t *testing.T) {
	t.Parallel()
	// Point socketDir at a path under /tmp that exists at NewServer time
	// (so the perms-probe doesn't fail) but gets deleted before sweep —
	// ReadDir then returns a stat-like error.
	dir, err := os.MkdirTemp("/tmp", "143a-sweep-readdir-*")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o750))

	srv := NewServer(&stubResolver{}, dir, zerolog.Nop())
	require.NoError(t, os.RemoveAll(dir), "test setup: remove the dir so ReadDir fails")

	require.NotPanics(t, func() {
		srv.SweepStaleSessionDirs(nil)
	}, "ReadDir failure must be swallowed — the sweep is best-effort hygiene, not a hard precondition for boot")
}

// TestServer_SweepStaleSessionDirs_NonUUIDDirectoryEntry exercises the
// branch where a directory entry's name doesn't parse as a UUID. The sweep
// must skip it (counted as `skipped`, not `swept`) so a stray dir from
// future tooling or a manual ops probe survives — the contract is that the
// sweep only owns UUID-named subdirs.
func TestServer_SweepStaleSessionDirs_NonUUIDDirectoryEntry(t *testing.T) {
	t.Parallel()
	dir := shortSocketDir(t)

	// A directory (not a file) with a non-UUID name. The earlier sweep test
	// covers a non-UUID FILE, which short-circuits at the !IsDir check; this
	// one specifically lands in the uuid.Parse error branch.
	strayDir := filepath.Join(dir, "manual-ops-probe")
	require.NoError(t, os.MkdirAll(strayDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(strayDir, "marker"), []byte("preserve me"), 0o600))

	srv := NewServer(&stubResolver{}, dir, zerolog.Nop())
	srv.SweepStaleSessionDirs(nil)

	_, err := os.Stat(strayDir)
	require.NoError(t, err, "non-UUID directory entries must survive sweep so future tooling/ops dirs aren't clobbered")
	_, err = os.Stat(filepath.Join(strayDir, "marker"))
	require.NoError(t, err, "the contents of a non-UUID dir must also be preserved")
}
