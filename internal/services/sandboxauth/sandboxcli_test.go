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
	"time"

	"github.com/stretchr/testify/require"
)

var resolvedGitPath = func() string {
	path, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	return path
}()

// gitAvailable reports whether `git` is on PATH. Bootstrap tests skip when
// it's not — sandboxauth runs inside the sandbox image where git is always
// present, so the tests are integration-grade by design.
func gitAvailable() bool {
	return resolvedGitPath != ""
}

// startSocketServer spins up a Unix-domain socket server in a goroutine that
// runs handler once per accepted connection. Returns the socket path; the
// listener and goroutine are cleaned up via t.Cleanup so each test can
// share-nothing.
//
// The helper owns connection teardown rather than the handler. After the
// handler completes its request/response exchange, we half-close the write
// side and drain any remaining bytes from the client before fully closing.
// Without this, a fast handler exit could race the client's request flush:
// json.Decode returns as soon as the JSON value is parsed, but the kernel
// may not yet have delivered the trailing newline, and the immediate
// Close() then surfaces as "write: broken pipe" on whichever side still has
// bytes in flight. Handlers must therefore NOT defer conn.Close themselves
// — closing the conn from inside the handler defeats the half-close
// handshake.
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
			go func(c net.Conn) {
				defer func() {
					if uc, ok := c.(*net.UnixConn); ok {
						_ = uc.CloseWrite()
					}
					// Bound the drain so a misbehaving client can't leak
					// this goroutine across tests in the same process.
					_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
					_, _ = io.Copy(io.Discard, c)
					_ = c.Close()
				}()
				handler(c)
			}(conn)
		}
	}()
	return sock
}

func TestClient_GetSuccess(t *testing.T) {
	t.Parallel()

	sock := startSocketServer(t, func(conn net.Conn) {
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
	t.Setenv(SocketEnvVar, "")

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
			if tt.socketPath == "" {
				t.Setenv(SocketEnvVar, "")
			}
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
	t.Setenv(WorkingBranchEnvVar, "143/abc123/fix-typo")
	t.Setenv(CoAuthorEnvVar, "Co-authored-by: Alice <alice@example.com>")

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--workdir=" + workdir}, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	cfg := readGitConfig(t, workdir)
	require.Contains(t, cfg, "user.name=Alice Hub")
	require.Contains(t, cfg, "user.email=1+alicehub@users.noreply.github.com")
	require.Contains(t, cfg, "push.autosetupremote=true")
	require.Contains(t, cfg, "credential.helper=!143-tools git-credential")

	hook, err := os.ReadFile(filepath.Join(workdir, ".git", "hooks", "prepare-commit-msg"))
	require.NoError(t, err)
	require.Contains(t, string(hook), "Co-authored-by: Alice <alice@example.com>")
	stat, err := os.Stat(filepath.Join(workdir, ".git", "hooks", "prepare-commit-msg"))
	require.NoError(t, err)
	require.NotZero(t, stat.Mode()&0o100, "hook must be executable")
	pushHook, err := os.ReadFile(filepath.Join(workdir, ".git", "hooks", "pre-push"))
	require.NoError(t, err)
	require.Contains(t, string(pushHook), "expected_branch='143/abc123/fix-typo'")
	pushStat, err := os.Stat(filepath.Join(workdir, ".git", "hooks", "pre-push"))
	require.NoError(t, err)
	require.NotZero(t, pushStat.Mode()&0o100, "push hook must be executable")

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
	t.Setenv(WorkingBranchEnvVar, "143/abc123/fix-typo")
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
	out, err := testGitCommand(workdir, "config", "--local", "--get-all", "credential.helper").Output()
	require.NoError(t, err)
	helpers := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.Len(t, helpers, 1, "credential.helper must have exactly one value after repeated bootstraps, got %v", helpers)
	require.Equal(t, "!143-tools git-credential", helpers[0])

	// User identity stays a single value.
	cfg := readGitConfig(t, workdir)
	require.Equal(t, 1, strings.Count(cfg, "user.name="), "user.name must not stack")
	require.Equal(t, 1, strings.Count(cfg, "user.email="), "user.email must not stack")
	require.Equal(t, 1, strings.Count(cfg, "push.autosetupremote=true"), "push.autoSetupRemote must not stack")

	// Hook content is overwritten in place — three runs of the bootstrap
	// must leave a single hook with a single trailer line.
	hookBytes, err := os.ReadFile(filepath.Join(workdir, ".git", "hooks", "prepare-commit-msg"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(hookBytes), "Co-authored-by: Alice <alice@example.com>"),
		"prepare-commit-msg must contain the trailer literal exactly once")
	pushHookBytes, err := os.ReadFile(filepath.Join(workdir, ".git", "hooks", "pre-push"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(pushHookBytes), "expected_branch='143/abc123/fix-typo'"),
		"pre-push must contain the expected branch literal exactly once")

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

func TestRunGitBootstrap_PrePushHookRejectsWrongBranchOrRemote(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available on this runner")
	}

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir))
	t.Setenv("HOME", t.TempDir())
	t.Setenv(GitNameEnvVar, "Alice Hub")
	t.Setenv(GitEmailEnvVar, "alice@example.com")
	t.Setenv(WorkingBranchEnvVar, "143/abc123/fix-typo")

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--workdir=" + workdir}, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	require.NoError(t, testGitCommand(workdir, "commit", "--allow-empty", "-m", "init").Run(), "test repo should create an initial commit before branch switching")
	require.NoError(t, testGitCommand(workdir, "checkout", "-b", "143/abc123/fix-typo").Run(), "test repo should create the designated branch")
	hookPath := filepath.Join(workdir, ".git", "hooks", "pre-push")

	cmd := exec.Command("sh", hookPath, "origin", "https://github.com/owner/repo.git")
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader("refs/heads/143/abc123/fix-typo abc refs/heads/143/abc123/fix-typo def\n")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "pre-push should allow pushes from the designated branch to the designated remote ref: %s", string(out))

	require.NoError(t, testGitCommand(workdir, "checkout", "-B", "main").Run(), "test repo should switch to a non-designated branch")
	cmd = exec.Command("sh", hookPath, "origin", "https://github.com/owner/repo.git")
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader("refs/heads/main abc refs/heads/143/abc123/fix-typo def\n")
	out, err = cmd.CombinedOutput()
	require.Error(t, err, "pre-push should reject pushes from the wrong local branch")
	require.Contains(t, string(out), "refusing push from branch 'main'; expected '143/abc123/fix-typo'")

	require.NoError(t, testGitCommand(workdir, "checkout", "143/abc123/fix-typo").Run(), "test repo should switch back to the designated branch")
	cmd = exec.Command("sh", hookPath, "origin", "https://github.com/owner/repo.git")
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader("refs/heads/143/abc123/fix-typo abc refs/heads/main def\n")
	out, err = cmd.CombinedOutput()
	require.Error(t, err, "pre-push should reject pushes to the wrong remote ref")
	require.Contains(t, string(out), "refusing push to 'refs/heads/main'; expected 'refs/heads/143/abc123/fix-typo'")
}

func TestRunGitBootstrap_PreservesExistingPrePushHook(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available on this runner")
	}

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir))
	t.Setenv("HOME", t.TempDir())
	t.Setenv(GitNameEnvVar, "Alice Hub")
	t.Setenv(GitEmailEnvVar, "alice@example.com")
	t.Setenv(WorkingBranchEnvVar, "143/abc123/fix-typo")

	hookPath := filepath.Join(workdir, ".git", "hooks", "pre-push")
	originalHook := "#!/bin/sh\necho original-hook-ran\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(originalHook), 0o755), "test repo should start with an existing pre-push hook")

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--workdir=" + workdir}, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	preservedPath := filepath.Join(workdir, ".git", "hooks", "pre-push.143-orig")
	preserved, err := os.ReadFile(preservedPath)
	require.NoError(t, err)
	require.Equal(t, originalHook, string(preserved), "git-bootstrap should preserve the original pre-push hook")

	require.NoError(t, testGitCommand(workdir, "commit", "--allow-empty", "-m", "init").Run(), "test repo should create an initial commit before branch switching")
	require.NoError(t, testGitCommand(workdir, "checkout", "-b", "143/abc123/fix-typo").Run(), "test repo should create the designated branch")

	cmd := exec.Command("sh", hookPath, "origin", "https://github.com/owner/repo.git")
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader("refs/heads/143/abc123/fix-typo abc refs/heads/143/abc123/fix-typo def\n")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "wrapped pre-push should still allow the designated branch: %s", string(out))
	require.Contains(t, string(out), "original-hook-ran", "wrapped pre-push should chain to the preserved original hook")
}

func TestRunGitBootstrap_RejectsPrePushSymlinkOutsideHooksDir(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir), "git init should prepare a repo for hook installation")

	outsideDir := t.TempDir()
	outsideHook := filepath.Join(outsideDir, "pre-push")
	original := "#!/bin/sh\necho outside-hook\n"
	require.NoError(t, os.WriteFile(outsideHook, []byte(original), 0o755), "test should create a hook outside the repo")

	hookPath := filepath.Join(workdir, ".git", "hooks", "pre-push")
	err := os.Remove(hookPath)
	if err != nil {
		require.True(t, os.IsNotExist(err), "test should only tolerate a missing default pre-push hook, got %v", err)
	}
	require.NoError(t, os.Symlink(outsideHook, hookPath), "test should replace pre-push with a symlink escaping the hooks dir")

	err = installPushGuardHook(workdir, "143/abc123/fix-typo")
	require.Error(t, err, "installPushGuardHook should reject a pre-push symlink that escapes the hooks directory")

	after, readErr := os.ReadFile(outsideHook)
	require.NoError(t, readErr, "the outside hook should remain readable after rejection")
	require.Equal(t, original, string(after), "installPushGuardHook should not rewrite a hook outside the repo")
}

func TestRunGitBootstrap_ReportsPushAutoSetupRemoteFailure(t *testing.T) {
	workdir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workdir, ".git"), 0o755), "test should create a minimal git directory for bootstrap validation")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", fakeGitPath(t, `#!/bin/sh
for arg in "$@"; do
	if [ "$arg" = "push.autoSetupRemote" ]; then
		echo "push.autosetupremote failed" >&2
		exit 17
	fi
done
exit 0
`)+":"+os.Getenv("PATH"))
	t.Setenv(GitNameEnvVar, "Alice Hub")
	t.Setenv(GitEmailEnvVar, "alice@example.com")

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--workdir=" + workdir}, &stderr)
	require.Equal(t, 1, code, "git-bootstrap should fail when configuring push.autoSetupRemote fails")
	require.Contains(t, stderr.String(), "push.autosetupremote failed", "git-bootstrap should surface the push.autoSetupRemote git failure")
}

func TestRunGitBootstrap_ReportsPushGuardInstallFailure(t *testing.T) {
	workdir := t.TempDir()
	gitDir := filepath.Join(workdir, ".git")
	require.NoError(t, os.Mkdir(gitDir, 0o755), "test should create a minimal git directory for bootstrap validation")
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "hooks"), []byte("not-a-dir"), 0o600), "test should block hook directory creation with a file")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", fakeGitPath(t, "#!/bin/sh\nexit 0\n")+":"+os.Getenv("PATH"))
	t.Setenv(GitNameEnvVar, "Alice Hub")
	t.Setenv(GitEmailEnvVar, "alice@example.com")
	t.Setenv(WorkingBranchEnvVar, "143/abc123/fix-typo")

	var stderr bytes.Buffer
	code := runGitBootstrap([]string{"--workdir=" + workdir}, &stderr)
	require.Equal(t, 1, code, "git-bootstrap should fail when the push guard hook cannot be installed")
	require.Contains(t, stderr.String(), "install push guard hook", "git-bootstrap should explain push guard installation failures")
}

func TestInstallPushGuardHook_PreservedBackupAddsExecBit(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir), "git init should prepare a repo for hook installation")

	hookPath := filepath.Join(workdir, ".git", "hooks", "pre-push")
	originalHook := "#!/bin/sh\necho original-hook-ran\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(originalHook), 0o644), "test should create a non-executable original hook")

	err := installPushGuardHook(workdir, "143/abc123/fix-typo")
	require.NoError(t, err, "installPushGuardHook should succeed when wrapping an existing hook")

	preservedPath := filepath.Join(workdir, ".git", "hooks", "pre-push.143-orig")
	info, statErr := os.Stat(preservedPath)
	require.NoError(t, statErr, "installPushGuardHook should preserve the original hook")
	require.NotZero(t, info.Mode()&0o100, "installPushGuardHook should add the owner execute bit to the preserved hook")
}

func TestInstallPushGuardHook_ErrorsWhenPreservedHookCannotBeWritten(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir), "git init should prepare a repo for hook installation")

	hooksDir := filepath.Join(workdir, ".git", "hooks")
	hookPath := filepath.Join(hooksDir, "pre-push")
	require.NoError(t, os.WriteFile(hookPath, []byte("#!/bin/sh\necho original-hook-ran\n"), 0o755), "test should create an original hook")
	require.NoError(t, os.Mkdir(filepath.Join(hooksDir, "pre-push.143-orig"), 0o755), "test should block preserved-hook creation with a directory")

	err := installPushGuardHook(workdir, "143/abc123/fix-typo")
	require.Error(t, err, "installPushGuardHook should fail when it cannot preserve the existing hook")
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
	t.Setenv(SocketEnvVar, "")
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

func TestRunGit_IgnoresBrokenAmbientGitConfig(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available on this runner")
	}

	workdir := t.TempDir()
	require.NoError(t, gitInit(workdir), "git init should prepare a repo for local config writes")

	badGlobal := filepath.Join(t.TempDir(), "bad.gitconfig")
	require.NoError(t, os.WriteFile(badGlobal, []byte("this is not valid git config\n"), 0o600), "test should create an invalid global git config")
	t.Setenv("GIT_CONFIG_GLOBAL", badGlobal)

	err := runGit(workdir, "config", "user.name", "Alice Example")
	require.NoError(t, err, "runGit should ignore invalid ambient global git config when writing repo-local settings")

	configBytes, readErr := os.ReadFile(filepath.Join(workdir, ".git", "config"))
	require.NoError(t, readErr, "test should read the repo-local git config after runGit succeeds")
	require.Contains(t, string(configBytes), "Alice Example", "runGit should still write the requested repo-local config entry")
}

func TestTestGitCommand_UsesResolvedSystemGitPath(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available on this runner")
	}

	workdir := t.TempDir()
	t.Setenv("PATH", fakeGitPath(t, "#!/bin/sh\nexit 17\n")+":"+os.Getenv("PATH"))

	err := testGitCommand(workdir, "init", "--quiet").Run()
	require.NoError(t, err, "testGitCommand should keep using the real git binary even when PATH is overridden")
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
	return testGitCommand(workdir, "init", "--quiet").Run()
}

func readGitConfig(t *testing.T, workdir string) string {
	t.Helper()
	out, err := testGitCommand(workdir, "config", "--list", "--local").Output()
	require.NoError(t, err)
	return string(out)
}

func testGitCommand(workdir string, args ...string) *exec.Cmd {
	full := append([]string{"-C", workdir}, args...)
	cmd := exec.Command(resolvedGitPath, full...) // #nosec G204,G702 -- test helper executes fixed git invocations against temp repos
	cmd.Env = appendGitEnv(os.Environ())
	return cmd
}

func fakeGitPath(t *testing.T, script string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "git")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755), "test should install a fake git binary")
	return dir
}
