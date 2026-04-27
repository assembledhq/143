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

func TestRunGitCredential_DefaultsUsernameWhenHostOmitsIt(t *testing.T) {
	sock := startSocketServer(t, func(conn net.Conn) {
		defer conn.Close()
		// Drain the request before responding so the deferred Close doesn't
		// race the client's write and surface as a "broken pipe".
		var req Request
		require.NoError(t, json.NewDecoder(conn).Decode(&req), "host should read the request first")
		require.NoError(t, json.NewEncoder(conn).Encode(&Response{Token: "tok"}), "host should encode a response")
	})
	t.Setenv(SocketEnvVar, sock)

	var stdout, stderr bytes.Buffer
	code := runGitCredential([]string{"get"}, strings.NewReader("\n"), &stdout, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	require.Equal(t, "username="+DefaultUsername+"\npassword=tok\n", stdout.String(), "git-credential should fall back to the canonical GitHub token username")
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

func TestRunGitCredential_RejectsUnknownVerb(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := runGitCredential([]string{"approve"}, strings.NewReader("protocol=https\n\n"), &stdout, &stderr)
	require.Equal(t, 2, code, "unknown credential verbs should fail with the git helper convention exit code")
	require.Empty(t, stdout.String(), "unknown credential verbs should not print credentials")
	require.Contains(t, stderr.String(), `unknown verb "approve"`, "git-credential should explain the invalid verb")
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

func TestRunAuthToken_ErrorPaths(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		socketPath string
		handler    func(conn net.Conn)
		wantCode   int
		wantErr    string
	}{
		{
			name:     "flag parse error",
			args:     []string{"--nope"},
			wantCode: 2,
			wantErr:  "flag provided but not defined",
		},
		{
			name:     "host socket missing",
			wantCode: 1,
			wantErr:  SocketEnvVar + " is not set",
		},
		{
			name: "host returns structured error",
			handler: func(conn net.Conn) {
				defer conn.Close()
				var req Request
				require.NoError(t, json.NewDecoder(conn).Decode(&req), "host should receive the auth-token request before replying with an error")
				require.NoError(t, json.NewEncoder(conn).Encode(&Response{Error: "revoked"}), "host should encode an error response")
			},
			wantCode: 1,
			wantErr:  "host: revoked",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if tt.handler != nil {
				tt.socketPath = startSocketServer(t, tt.handler)
				t.Setenv(SocketEnvVar, tt.socketPath)
			}

			var stdout, stderr bytes.Buffer
			code := runAuthToken(tt.args, &stdout, &stderr)
			require.Equal(t, tt.wantCode, code, "runAuthToken should return the expected exit code")
			require.Empty(t, stdout.String(), "runAuthToken should not print a token on failure")
			require.Contains(t, stderr.String(), tt.wantErr, "runAuthToken should explain the failure")
		})
	}
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

func TestInstallCoAuthorHook_CreatesHooksDirWithStrictPerms(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir), "git init should prepare a repo for hook installation")

	hooksDir := filepath.Join(workdir, ".git", "hooks")
	require.NoError(t, os.RemoveAll(hooksDir), "test should remove the default hooks dir so installCoAuthorHook recreates it")

	err := installCoAuthorHook(workdir, "Co-authored-by: Alice <alice@example.com>")
	require.NoError(t, err, "installCoAuthorHook should succeed")

	info, err := os.Stat(hooksDir)
	require.NoError(t, err, "installCoAuthorHook should recreate the hooks directory")
	require.Equal(t, os.FileMode(0o750), info.Mode().Perm(), "hooks directory should be created with 0750 permissions")
}

func TestInstallGHWrapper_CreatesBinDirWithStrictPerms(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	err := installGHWrapper()
	require.NoError(t, err, "installGHWrapper should succeed")

	info, err := os.Stat(filepath.Join(homeDir, ".local", "bin"))
	require.NoError(t, err, "installGHWrapper should create ~/.local/bin")
	require.Equal(t, os.FileMode(0o750), info.Mode().Perm(), "gh wrapper bin directory should be created with 0750 permissions")
}

func TestRunGitBootstrap_RejectsMissingWorkdir(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	code := runGitBootstrap(nil, &stderr)
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "--workdir is required")
}

func TestRunGitBootstrap_RejectsUnknownFlag(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--unknown"}, &stderr)
	require.Equal(t, 2, code, "invalid bootstrap flags should use the standard flag parser exit code")
	require.Contains(t, stderr.String(), "flag provided but not defined", "git-bootstrap should report the invalid flag")
}

func TestRunGitBootstrap_RejectsNonGitDirectory(t *testing.T) {
	t.Setenv(GitNameEnvVar, "Alice")
	t.Setenv(GitEmailEnvVar, "alice@example.com")

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--workdir=" + t.TempDir()}, &stderr)
	require.Equal(t, 1, code, "git-bootstrap should reject directories that are not git repos")
	require.Contains(t, stderr.String(), "is not a git repo", "git-bootstrap should explain why bootstrap failed")
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

func TestInstallCoAuthorHook_ErrorsWhenHooksPathIsNotDirectory(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir), "git init should prepare a repo")

	hooksPath := filepath.Join(workdir, ".git", "hooks")
	require.NoError(t, os.RemoveAll(hooksPath), "test should remove the default hooks directory")
	require.NoError(t, os.WriteFile(hooksPath, []byte("not-a-dir"), 0o600), "test should replace hooks with a file")

	err := installCoAuthorHook(workdir, "Co-authored-by: Alice <alice@example.com>")
	require.Error(t, err, "installCoAuthorHook should fail when the hooks path is blocked by a file")
}

func TestInstallGHWrapper_ErrorsWhenBinPathCannotBeCreated(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".local"), []byte("not-a-dir"), 0o600), "test should block ~/.local/bin creation with a file")

	err := installGHWrapper()
	require.Error(t, err, "installGHWrapper should fail when ~/.local/bin cannot be created")
}

func TestCallHost_RequiresSocketEnv(t *testing.T) {
	t.Parallel()

	resp, err := callHost(ActionPush)
	require.Nil(t, resp, "callHost should not return a response without a socket path")
	require.Error(t, err, "callHost should fail when the socket env var is missing")
	require.Contains(t, err.Error(), SocketEnvVar, "callHost should mention the missing env var")
}

func TestRunGit_ReturnsCommandError(t *testing.T) {
	t.Parallel()

	err := runGit(filepath.Join(t.TempDir(), "missing"), "status")
	require.Error(t, err, "runGit should surface git execution failures")
	require.Contains(t, err.Error(), "git -C", "runGit should include the failing git invocation in the error")
}

func TestHandleSubcommand_GitBootstrapDispatches(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	handled, code := HandleSubcommand([]string{"git-bootstrap"}, nil, io.Discard, &stderr)
	require.True(t, handled, "git-bootstrap should be handled by the sandboxauth subcommand dispatcher")
	require.Equal(t, 2, code, "git-bootstrap without a workdir should return the validation exit code")
	require.Contains(t, stderr.String(), "--workdir is required", "dispatcher should surface git-bootstrap validation errors")
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
