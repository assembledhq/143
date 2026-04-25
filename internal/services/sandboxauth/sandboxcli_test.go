package sandboxauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// gitAvailable reports whether `git` is on PATH. Bootstrap tests skip when
// it's not — sandboxauth runs inside the sandbox image where git is always
// present, so the tests are integration-grade by design.
func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// startSocketServer spins up a Unix-domain socket server in a goroutine that
// runs handler once per accepted connection. Returns the socket path; the
// listener and goroutine are cleaned up via t.Cleanup so each test can
// share-nothing.
func startSocketServer(t *testing.T, handler func(conn net.Conn)) string {
	t.Helper()
	// AF_UNIX socket paths are limited to ~104 bytes on macOS (108 on Linux).
	// `t.TempDir()` returns long paths under /var/folders on macOS, so we
	// build a short, test-unique path under os.TempDir() and clean it up
	// ourselves rather than nesting it inside the framework's temp dir.
	f, err := os.CreateTemp("", "143auth-*.sock")
	require.NoError(t, err)
	sock := f.Name()
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(sock)) // listen wants the path to not exist
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(sock)
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			handler(conn)
		}
	}()
	return sock
}

func TestClient_GetSuccess(t *testing.T) {
	t.Parallel()

	sock := startSocketServer(t, func(conn net.Conn) {
		defer conn.Close()
		var req Request
		require.NoError(t, json.NewDecoder(conn).Decode(&req))
		require.Equal(t, OpGet, req.Op)
		require.Equal(t, ActionPush, req.Action)
		require.NoError(t, json.NewEncoder(conn).Encode(&Response{
			Token:    "ghs_test",
			Username: DefaultUsername,
			Identity: IdentityUser,
			Login:    "alice",
		}))
	})

	resp, err := NewClient(sock).Get(context.Background(), ActionPush)
	require.NoError(t, err)
	require.Equal(t, "ghs_test", resp.Token)
	require.Equal(t, IdentityUser, resp.Identity)
	require.Equal(t, "alice", resp.Login)
}

func TestClient_GetMissingSocket(t *testing.T) {
	t.Parallel()
	_, err := NewClient("").Get(context.Background(), ActionPush)
	require.Error(t, err)
	require.Contains(t, err.Error(), SocketEnvVar)
}

func TestClient_GetDialFailure(t *testing.T) {
	t.Parallel()
	_, err := NewClient("/tmp/no-such-socket-143-test").Get(context.Background(), ActionPush)
	require.Error(t, err)
	require.Contains(t, err.Error(), "dial")
}

func TestRunGitCredential_GetReturnsCreds(t *testing.T) {
	sock := startSocketServer(t, func(conn net.Conn) {
		defer conn.Close()
		var req Request
		require.NoError(t, json.NewDecoder(conn).Decode(&req))
		_ = json.NewEncoder(conn).Encode(&Response{Token: "tok", Username: "x-access-token"})
	})
	t.Setenv(SocketEnvVar, sock)

	stdin := strings.NewReader("protocol=https\nhost=github.com\n\n")
	var stdout, stderr bytes.Buffer
	code := runGitCredential([]string{"get"}, stdin, &stdout, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	require.Equal(t, "username=x-access-token\npassword=tok\n", stdout.String())
}

func TestRunGitCredential_StoreAndEraseAreNoOps(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"store", "erase"} {
		verb := verb
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			stdin := strings.NewReader("protocol=https\nhost=github.com\nusername=x\npassword=y\n\n")
			var stdout, stderr bytes.Buffer
			code := runGitCredential([]string{verb}, stdin, &stdout, &stderr)
			require.Equal(t, 0, code, "stderr: %s", stderr.String())
			require.Empty(t, stdout.String(), "store/erase must not print credentials")
		})
	}
}

func TestRunGitCredential_HostError(t *testing.T) {
	sock := startSocketServer(t, func(conn net.Conn) {
		defer conn.Close()
		var req Request
		require.NoError(t, json.NewDecoder(conn).Decode(&req))
		_ = json.NewEncoder(conn).Encode(&Response{Error: "resolver: token revoked"})
	})
	t.Setenv(SocketEnvVar, sock)

	stdin := strings.NewReader("\n")
	var stdout, stderr bytes.Buffer
	code := runGitCredential([]string{"get"}, stdin, &stdout, &stderr)
	require.NotEqual(t, 0, code)
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "token revoked")
}

func TestRunAuthToken_PrintsToken(t *testing.T) {
	sock := startSocketServer(t, func(conn net.Conn) {
		defer conn.Close()
		var req Request
		require.NoError(t, json.NewDecoder(conn).Decode(&req))
		_ = json.NewEncoder(conn).Encode(&Response{Token: "abcd1234"})
	})
	t.Setenv(SocketEnvVar, sock)

	var stdout, stderr bytes.Buffer
	code := runAuthToken(nil, &stdout, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	require.Equal(t, "abcd1234\n", stdout.String())
}

func TestRunGitBootstrap_AppliesConfigAndHook(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available on this runner")
	}

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir))
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	t.Setenv(GitNameEnvVar, "Alice Hub")
	t.Setenv(GitEmailEnvVar, "1+alicehub@users.noreply.github.com")
	t.Setenv(CoAuthorEnvVar, "Co-authored-by: Alice <alice@example.com>")

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--workdir=" + workdir}, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	cfg := readGitConfig(t, workdir)
	require.Contains(t, cfg, "user.name=Alice Hub")
	require.Contains(t, cfg, "user.email=1+alicehub@users.noreply.github.com")
	require.Contains(t, cfg, "credential.helper=!143-tools git-credential")

	hook, err := os.ReadFile(filepath.Join(workdir, ".git", "hooks", "prepare-commit-msg"))
	require.NoError(t, err)
	require.Contains(t, string(hook), "Co-authored-by: Alice <alice@example.com>")
	stat, err := os.Stat(filepath.Join(workdir, ".git", "hooks", "prepare-commit-msg"))
	require.NoError(t, err)
	require.NotZero(t, stat.Mode()&0o100, "hook must be executable")

	ghWrapper, err := os.ReadFile(filepath.Join(homeDir, ".local", "bin", "gh"))
	require.NoError(t, err)
	require.Contains(t, string(ghWrapper), "143-tools auth-token --action=api")
	require.Contains(t, string(ghWrapper), "exec /usr/local/bin/gh \"$@\"")
}

// TestRunGitBootstrap_Idempotent guards the resume path: a session that
// recovers and re-bootstraps the workspace must end up with the same git
// config and hook as a single fresh bootstrap, never with stacked
// credential.helper entries or duplicated trailers.
func TestRunGitBootstrap_Idempotent(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available on this runner")
	}

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir))
	t.Setenv("HOME", t.TempDir())

	t.Setenv(GitNameEnvVar, "Alice Hub")
	t.Setenv(GitEmailEnvVar, "1+alicehub@users.noreply.github.com")
	t.Setenv(CoAuthorEnvVar, "Co-authored-by: Alice <alice@example.com>")

	for i := 0; i < 3; i++ {
		var stderr bytes.Buffer
		code := runGitBootstrap([]string{"--workdir=" + workdir}, &stderr)
		require.Equal(t, 0, code, "bootstrap %d failed: %s", i, stderr.String())
	}

	// `git config --local --get-all` lists each value of a multi-valued
	// key from the repo's own config (excluding global / system files —
	// dev machines often have `credential.helper = osxkeychain` set
	// globally, which would otherwise leak in). The credential helper key
	// must appear exactly once even after multiple bootstraps; a
	// regression to `--add` would surface here as duplicate entries.
	out, err := exec.Command("git", "-C", workdir, "config", "--local", "--get-all", "credential.helper").Output()
	require.NoError(t, err)
	helpers := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.Len(t, helpers, 1, "credential.helper must have exactly one value after repeated bootstraps, got %v", helpers)
	require.Equal(t, "!143-tools git-credential", helpers[0])

	// User identity stays a single value.
	cfg := readGitConfig(t, workdir)
	require.Equal(t, 1, strings.Count(cfg, "user.name="), "user.name must not stack")
	require.Equal(t, 1, strings.Count(cfg, "user.email="), "user.email must not stack")

	// Hook content is overwritten in place — three runs of the bootstrap
	// must leave a single hook with a single trailer line.
	hookBytes, err := os.ReadFile(filepath.Join(workdir, ".git", "hooks", "prepare-commit-msg"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(hookBytes), "Co-authored-by: Alice <alice@example.com>"),
		"prepare-commit-msg must contain the trailer literal exactly once")

	// Sanity: the hook itself protects against in-message duplication too.
	// Apply the hook to a fake commit message twice and confirm the trailer
	// is appended only once.
	msgPath := filepath.Join(t.TempDir(), "msg")
	require.NoError(t, os.WriteFile(msgPath, []byte("fix: things\n"), 0o600))
	hookPath := filepath.Join(workdir, ".git", "hooks", "prepare-commit-msg")
	for i := 0; i < 2; i++ {
		out, err := exec.Command("sh", hookPath, msgPath, "message").CombinedOutput()
		require.NoError(t, err, "hook run %d failed: %s", i, string(out))
	}
	final, err := os.ReadFile(msgPath)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(final), "Co-authored-by: Alice <alice@example.com>"),
		"hook must short-circuit when the trailer is already present")
}

func TestRunGitBootstrap_NoHookWhenCoAuthorEmpty(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available on this runner")
	}

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir))
	t.Setenv("HOME", t.TempDir())

	t.Setenv(GitNameEnvVar, "Alice Hub")
	t.Setenv(GitEmailEnvVar, "alice@example.com")
	t.Setenv(CoAuthorEnvVar, "")

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--workdir=" + workdir}, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	_, err := os.Stat(filepath.Join(workdir, ".git", "hooks", "prepare-commit-msg"))
	require.True(t, os.IsNotExist(err), "no hook should be installed when no co-author trailer is set")

	_, err = os.Stat(filepath.Join(os.Getenv("HOME"), ".local", "bin", "gh"))
	require.NoError(t, err, "gh wrapper should still be installed when no co-author hook is needed")
}

func TestRunGitBootstrap_RejectsMissingWorkdir(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	code := runGitBootstrap(nil, &stderr)
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "--workdir is required")
}

func TestRunGitBootstrap_RejectsMissingIdentity(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available on this runner")
	}
	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir))
	t.Setenv("HOME", t.TempDir())

	t.Setenv(GitNameEnvVar, "")
	t.Setenv(GitEmailEnvVar, "")

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--workdir=" + workdir}, &stderr)
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), GitNameEnvVar)
}

func TestHandleSubcommand_FallthroughForUnknown(t *testing.T) {
	t.Parallel()
	handled, _ := HandleSubcommand([]string{"sentry_list_errors", "--limit=1"}, nil, io.Discard, io.Discard)
	require.False(t, handled, "unknown subcommands should fall through to the tool dispatcher")
}

func TestHandleSubcommand_NoArgsFallsThrough(t *testing.T) {
	t.Parallel()
	handled, _ := HandleSubcommand(nil, nil, io.Discard, io.Discard)
	require.False(t, handled)
}

// --- helpers ---

func gitInit(workdir string) error {
	return exec.Command("git", "-C", workdir, "init", "--quiet").Run()
}

func readGitConfig(t *testing.T, workdir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", workdir, "config", "--list", "--local").Output()
	require.NoError(t, err)
	return string(out)
}
