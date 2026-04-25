package sandboxauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
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

	sock, closeFn, err := srv.Listen(
		context.Background(),
		uuid.New(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer closeFn()

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

	sock, closeFn, err := srv.Listen(context.Background(), uuid.New(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer closeFn()

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

	sock, closeFn, err := srv.Listen(context.Background(), uuid.New(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer closeFn()

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

	sock, closeFn, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err, "stale socket file must not block Listen")
	require.Equal(t, stalePath, sock)
	defer closeFn()

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

	sock1, closeFn1, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: sessionID, OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	closeFn1()

	sock2, closeFn2, err := srv.Listen(context.Background(), sessionID,
		&models.Session{ID: sessionID, OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err, "Listen must succeed after a prior close on the same session")
	defer closeFn2()
	require.Equal(t, sock1, sock2, "the per-session socket path must be deterministic so recreates land at the same in-container bind-mount target")

	// Connections to the recreated socket must still work.
	resp, err := NewClient(sock2).Get(context.Background(), ActionPush)
	require.NoError(t, err)
	require.Equal(t, "tok", resp.Token)
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

	_, _, err = srv.Listen(context.Background(), uuid.New(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insecure perms")
}

func TestServer_CloseRemovesSocketAndStopsAccepting(t *testing.T) {
	t.Parallel()
	resolver := &stubResolver{resolution: &identity.Resolution{Token: "tok", Source: identity.SourceApp}}
	srv := NewServer(resolver, shortSocketDir(t), zerolog.Nop())

	sock, closeFn, err := srv.Listen(context.Background(), uuid.New(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)

	// Sanity check that the socket exists.
	_, err = os.Stat(sock)
	require.NoError(t, err)

	closeFn()
	_, err = os.Stat(sock)
	require.True(t, os.IsNotExist(err), "socket must be removed by close")

	// Idempotency: second call is a no-op, must not panic.
	closeFn()

	// Connections to the now-removed socket should fail.
	_, err = net.Dial("unix", sock)
	require.Error(t, err)
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

	sock, closeFn, err := srv.Listen(
		context.Background(),
		uuid.New(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer closeFn()

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

	sock, closeFn, err := srv.Listen(context.Background(), uuid.New(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{},
		models.OrgSettings{},
	)
	require.NoError(t, err)
	defer closeFn()

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
