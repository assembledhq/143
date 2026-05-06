// sandboxcli.go: in-sandbox subcommands of `143-tools` that wire git/gh up
// to the per-session host socket. Lives here (not in cmd/tools) so the
// protocol struct, the client, and the CLI dispatchers stay in one
// importable package — the host-side server in PR 4 will reuse the same
// types.
//
// Subcommands:
//
//	143-tools git-credential
//	    Implements git's credential-helper protocol: reads a key=value blob
//	    on stdin, dials the host socket, prints "username=...\npassword=...\n".
//	    Configured by `git-bootstrap` as `git config credential.helper`.
//
//	143-tools auth-token [--action push|api]
//	    Prints a fresh GitHub token to stdout. Used by the `gh` wrapper to
//	    populate GH_TOKEN per invocation so no token sits at rest in
//	    ~/.config/gh.
//
//	143-tools git-bootstrap --workdir=/path/to/repo
//	    One-shot post-clone setup: writes git author config, installs the
//	    credential helper, and (when running under an App-token fallback)
//	    drops a prepare-commit-msg hook that appends a Co-authored-by trailer.

package sandboxauth

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Env vars that the orchestrator populates at container-create time. Kept
// next to SocketEnvVar so callers can find the full env-var contract in one
// place.
const (
	GitNameEnvVar  = "_143_GIT_NAME"
	GitEmailEnvVar = "_143_GIT_EMAIL"
	// WorkingBranchEnvVar pins the session's designated branch so
	// git-bootstrap can install a push guard.
	WorkingBranchEnvVar = "_143_WORKING_BRANCH"
	// CoAuthorEnvVar carries the full `Co-authored-by: NAME <email>` line.
	// Empty string means "user-identity resolution succeeded — don't append
	// a trailer" (the agent already commits as the user).
	CoAuthorEnvVar = "_143_COAUTHOR"
)

const ghBinaryPath = "/usr/local/bin/gh"

// HandleSubcommand dispatches a 143-tools invocation that targets one of the
// sandbox-side subcommands. Returns (handled, exitCode). When handled is
// false the caller should fall through to the existing tool dispatch.
//
// We intentionally keep this surface small and side-effect-only — no global
// state, no init() — so it can be unit-tested by passing in stdin/stdout/
// stderr explicitly.
func HandleSubcommand(args []string, stdin io.Reader, stdout, stderr io.Writer) (handled bool, exitCode int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "git-credential":
		return true, runGitCredential(args[1:], stdin, stdout, stderr)
	case "auth-token":
		return true, runAuthToken(args[1:], stdout, stderr)
	case "git-bootstrap":
		return true, runGitBootstrap(args[1:], stderr)
	}
	return false, 0
}

// runGitCredential implements the git-credential helper "get" verb. Git
// invokes us with stdin like:
//
//	protocol=https
//	host=github.com
//	<blank line>
//
// followed by an optional `username=...` for the second probe of an
// authentication round. We always reply with our resolved credential — git
// will use it for whichever request triggered this call.
//
// Per the protocol, only the "get" verb produces output. "store" and
// "erase" are no-ops: our tokens come from the host on every call, so
// caching them in git's helper graph would be both pointless and a leak.
func runGitCredential(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	verb := "get"
	if len(args) > 0 {
		verb = args[0]
	}
	switch verb {
	case "store", "erase":
		// Drain stdin so git doesn't see a SIGPIPE, then exit 0.
		_, _ = io.Copy(io.Discard, stdin)
		return 0
	case "get":
		// Drain stdin (we don't condition on protocol/host today — the
		// socket is per-session and per-repo, so any incoming probe gets
		// the same answer).
		_, _ = io.Copy(io.Discard, stdin)
	default:
		fmt.Fprintf(stderr, "143-tools git-credential: unknown verb %q\n", verb)
		return 2
	}

	resp, err := callHost(ActionPush)
	if err != nil {
		fmt.Fprintf(stderr, "143-tools git-credential: %s\n", err)
		// Returning a non-zero exit causes git to fall through to the next
		// helper (or prompt). Returning success with an empty payload would
		// make git think we have no credential, which is the same outcome
		// for our purposes but harder to debug.
		return 1
	}
	if resp.Error != "" {
		fmt.Fprintf(stderr, "143-tools git-credential: host: %s\n", resp.Error)
		return 1
	}
	username := resp.Username
	if username == "" {
		username = DefaultUsername
	}
	fmt.Fprintf(stdout, "username=%s\npassword=%s\n", username, resp.Token)
	return 0
}

// runAuthToken prints just the token to stdout for use by the `gh` wrapper:
//
//	GH_TOKEN=$(143-tools auth-token) gh pr comment ...
//
// Errors go to stderr and produce a non-zero exit so the wrapper aborts
// cleanly rather than running gh with an unset GH_TOKEN.
func runAuthToken(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("auth-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	action := fs.String("action", string(ActionAPI), "github action: push|api")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	resp, err := callHost(Action(*action))
	if err != nil {
		fmt.Fprintf(stderr, "143-tools auth-token: %s\n", err)
		return 1
	}
	if resp.Error != "" {
		fmt.Fprintf(stderr, "143-tools auth-token: host: %s\n", resp.Error)
		return 1
	}
	fmt.Fprintln(stdout, resp.Token)
	return 0
}

// runGitBootstrap configures git inside the cloned workspace so that
// subsequent commits, pushes, and gh calls use the host-issued credential
// transparently. Idempotent — safe to invoke more than once per session.
func runGitBootstrap(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("git-bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workdir := fs.String("workdir", "", "path to the cloned repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *workdir == "" {
		fmt.Fprintln(stderr, "143-tools git-bootstrap: --workdir is required")
		return 2
	}
	if _, err := os.Stat(filepath.Join(*workdir, ".git")); err != nil {
		fmt.Fprintf(stderr, "143-tools git-bootstrap: %s is not a git repo: %s\n", *workdir, err)
		return 1
	}

	name := os.Getenv(GitNameEnvVar)
	email := os.Getenv(GitEmailEnvVar)
	if name == "" || email == "" {
		fmt.Fprintf(stderr, "143-tools git-bootstrap: %s and %s must be set\n", GitNameEnvVar, GitEmailEnvVar)
		return 1
	}

	// git config in the repo (not --global) so a sandbox running multiple
	// repos in different directories can keep distinct identities, and so
	// the agent can override per-repo without our config fighting back.
	if err := runGit(*workdir, "config", "user.name", name); err != nil {
		fmt.Fprintf(stderr, "143-tools git-bootstrap: %s\n", err)
		return 1
	}
	if err := runGit(*workdir, "config", "user.email", email); err != nil {
		fmt.Fprintf(stderr, "143-tools git-bootstrap: %s\n", err)
		return 1
	}
	if err := runGit(*workdir, "config", "push.autoSetupRemote", "true"); err != nil {
		fmt.Fprintf(stderr, "143-tools git-bootstrap: %s\n", err)
		return 1
	}

	// Wire the credential helper. `--replace-all` collapses any prior
	// values for credential.helper into the single one we want — important
	// for idempotency: calling git-bootstrap twice (e.g. on session resume)
	// must not stack a second helper entry, and a plain `git config X Y`
	// would also fail loudly if a previous bootstrap left multiple values.
	// The leading "!" tells git to invoke the helper as a shell command,
	// which lets us use the binary name directly without an absolute path.
	if err := runGit(*workdir, "config", "--replace-all", "credential.helper", "!143-tools git-credential"); err != nil {
		fmt.Fprintf(stderr, "143-tools git-bootstrap: %s\n", err)
		return 1
	}
	if err := installGHWrapper(); err != nil {
		fmt.Fprintf(stderr, "143-tools git-bootstrap: install gh wrapper: %s\n", err)
		return 1
	}
	if branch := os.Getenv(WorkingBranchEnvVar); branch != "" {
		if err := installPushGuardHook(*workdir, branch); err != nil {
			fmt.Fprintf(stderr, "143-tools git-bootstrap: install push guard hook: %s\n", err)
			return 1
		}
	}

	if trailer := os.Getenv(CoAuthorEnvVar); trailer != "" {
		if err := installCoAuthorHook(*workdir, trailer); err != nil {
			fmt.Fprintf(stderr, "143-tools git-bootstrap: install co-author hook: %s\n", err)
			return 1
		}
	}

	return 0
}

// installCoAuthorHook drops a prepare-commit-msg hook that appends a single
// Co-authored-by trailer to every commit message that doesn't already
// contain it. Used when the resolved identity is the App fallback so the
// human triggerer still gets attribution on their PR.
//
// The hook is intentionally minimal and dependency-free: a POSIX shell
// script that uses only awk and grep, both already in the sandbox image.
// It deliberately does NOT modify amended commits' final newline pattern
// or merge commit messages — git invokes prepare-commit-msg for amends
// too, but the existing-trailer check prevents duplicates.
func installCoAuthorHook(workdir, trailer string) error {
	hooksDir := filepath.Join(workdir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		return err
	}

	// Quote the trailer for safe inclusion in a shell heredoc-less script.
	// The trailer contains no special chars by construction (it's
	// "Co-authored-by: NAME <email>") but treat it as untrusted anyway.
	escaped := strings.ReplaceAll(trailer, `'`, `'\''`)

	script := `#!/bin/sh
# Installed by 143-tools git-bootstrap.
# Appends a Co-authored-by trailer to commit messages when running under
# the App-token identity so the triggering user is still credited.
set -eu
COMMIT_MSG_FILE="$1"
COMMIT_SOURCE="${2:-}"
# Skip merge/squash messages — the trailer would land in confusing places.
case "$COMMIT_SOURCE" in
    merge|squash) exit 0 ;;
esac
TRAILER='` + escaped + `'
if grep -Fq "$TRAILER" "$COMMIT_MSG_FILE"; then
    exit 0
fi
# Ensure a blank line precedes the trailer block per Git's trailer
# convention — UNLESS the previous line is itself a trailer, in which case
# we extend the existing trailer block instead of splitting it (Git only
# recognizes contiguous trailers).
if [ -s "$COMMIT_MSG_FILE" ]; then
    last_byte=$(tail -c1 "$COMMIT_MSG_FILE")
    if [ -n "$last_byte" ]; then
        printf '\n' >> "$COMMIT_MSG_FILE"
    fi
    last_line=$(tail -n1 "$COMMIT_MSG_FILE")
    if [ -n "$last_line" ] && ! printf '%s' "$last_line" | grep -Eq '^[A-Za-z][A-Za-z0-9-]*: '; then
        printf '\n' >> "$COMMIT_MSG_FILE"
    fi
fi
printf '%s\n' "$TRAILER" >> "$COMMIT_MSG_FILE"
`
	return writeHookWithinDir(hooksDir, "prepare-commit-msg", []byte(script), 0o755)
}

func installPushGuardHook(workdir, branch string) error {
	hooksDir := filepath.Join(workdir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		return err
	}
	origHookPath := filepath.Join(hooksDir, "pre-push.143-orig")

	if existing, mode, err := readHookWithinDir(hooksDir, "pre-push"); err == nil {
		if !bytes.Contains(existing, []byte("Installed by 143-tools git-bootstrap.")) {
			if mode == 0 {
				mode = 0o755
			}
			if mode&0o100 == 0 {
				mode |= 0o100
			}
			if err := writeHookWithinDir(hooksDir, "pre-push.143-orig", existing, mode); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	script := fmt.Sprintf(`#!/bin/sh
# Installed by 143-tools git-bootstrap.
set -eu
expected_branch=%s
expected_ref="refs/heads/$expected_branch"
current_branch="$(git symbolic-ref --quiet --short HEAD 2>/dev/null || true)"
if [ "$current_branch" != "$expected_branch" ]; then
    echo "143-tools pre-push: refusing push from branch '$current_branch'; expected '$expected_branch'" >&2
    exit 1
fi
while IFS=' ' read -r local_ref local_sha remote_ref remote_sha; do
    if [ -n "${remote_ref:-}" ] && [ "$remote_ref" != "$expected_ref" ]; then
        echo "143-tools pre-push: refusing push to '$remote_ref'; expected '$expected_ref'" >&2
        exit 1
    fi
done
if [ -x %s ]; then
    exec %s "$@"
fi
`, shellQuote(branch), shellQuote(origHookPath), shellQuote(origHookPath))
	return writeHookWithinDir(hooksDir, "pre-push", []byte(script), 0o755)
}

func readHookWithinDir(dir, name string) ([]byte, os.FileMode, error) {
	f, err := os.OpenInRoot(dir, name)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, err
	}
	return data, info.Mode().Perm(), nil
}

func writeHookWithinDir(dir, name string, data []byte, perm os.FileMode) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.WriteFile(name, data, perm)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func installGHWrapper() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	binDir := filepath.Join(homeDir, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return err
	}
	wrapperPath := filepath.Join(binDir, "gh")
	// We honor a pre-set GH_TOKEN/GITHUB_TOKEN so an agent that explicitly
	// exports one (e.g. for cross-org work the host socket can't satisfy)
	// keeps control. The host socket and the legacy GITHUB_TOKEN env path
	// are mutually exclusive in the orchestrator (see
	// orchestrator.prepareSandboxGitHubAuth: it sets GITHUB_TOKEN only when
	// the resolver/socket path is *not* wired), so in normal operation only
	// one of these branches is ever populated by us.
	script := `#!/bin/sh
# Installed by 143-tools git-bootstrap.
set -eu
if [ -z "${GH_TOKEN:-}" ] && [ -z "${GITHUB_TOKEN:-}" ] && [ -n "${` + SocketEnvVar + `:-}" ]; then
    GH_TOKEN="$(143-tools auth-token --action=api)"
    export GH_TOKEN
fi
exec ` + ghBinaryPath + ` "$@"
`
	return os.WriteFile(wrapperPath, []byte(script), 0o755) // #nosec G306 -- wrapper must be executable
}

// callHost is the single entrypoint to the host socket from the sandbox-side
// CLI. Centralized so socket-path discovery and timeout handling stay
// consistent across subcommands.
func callHost(action Action) (*Response, error) {
	sockPath := os.Getenv(SocketEnvVar)
	if sockPath == "" {
		return nil, fmt.Errorf("%s is not set; was the container started by the orchestrator?", SocketEnvVar)
	}
	client := NewClient(sockPath)
	return client.Get(context.Background(), action)
}

// runGit is a thin wrapper around `git -C workdir <args...>`. Streams stderr
// to the caller's stderr so config errors (e.g. invalid value) are visible.
func runGit(workdir string, args ...string) error {
	full := append([]string{"-C", workdir}, args...)
	cmd := exec.Command("git", full...) // #nosec G204,G702 -- exec.Command does not invoke a shell; args come from fixed internal callsites and rooted repo paths
	cmd.Env = appendGitEnv(os.Environ())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w (%s)", strings.Join(full, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func appendGitEnv(base []string) []string {
	env := make([]string, 0, len(base)+2)
	for _, entry := range base {
		if strings.HasPrefix(entry, "GIT_CONFIG_GLOBAL=") || strings.HasPrefix(entry, "GIT_CONFIG_NOSYSTEM=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "GIT_CONFIG_GLOBAL="+os.DevNull)
	env = append(env, "GIT_CONFIG_NOSYSTEM=1")
	return env
}
