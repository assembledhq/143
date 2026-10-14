package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/reviewpreparation"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type recordingReviewCredentialProvider struct {
	localReviewEvidenceProvider
	mu       sync.Mutex
	commands []string
	inputs   int
}

func (p *recordingReviewCredentialProvider) record(command string, private bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commands = append(p.commands, command)
	if private {
		p.inputs++
	}
}

func (p *recordingReviewCredentialProvider) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	p.record(command, false)
	return p.localReviewEvidenceProvider.Exec(ctx, sandbox, command, stdout, stderr)
}

func (p *recordingReviewCredentialProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	input, err := io.ReadAll(stdin)
	if err != nil {
		return -1, err
	}
	var message struct {
		Authentication json.RawMessage `json:"authentication"`
	}
	if err := json.Unmarshal(input, &message); err != nil {
		return -1, err
	}
	p.record(command, len(message.Authentication) > 0)
	return p.localReviewEvidenceProvider.ExecWithStdin(ctx, sandbox, command, bytes.NewReader(input), stdout, stderr)
}

// Inspection compares only the test secret, and records no host environment
// values. Linux checks real /proc argv/environ/fds; macOS checks ps argv/env.
const reviewCredentialInspectionPython = `
import base64, os, pathlib, subprocess, sys
secret = pathlib.Path(os.environ["REVIEW_TEST_SECRET_PATH"]).read_text()
forbidden = [secret.encode(), base64.b64encode(("x-access-token:" + secret).encode())]
pid = os.getpid()
seen = 0
while pid > 1 and seen < 5:
    proc = pathlib.Path("/proc") / str(pid)
    if proc.exists():
        values = [(proc / "cmdline").read_bytes(), (proc / "environ").read_bytes()]
        for fd in (proc / "fd").iterdir():
            try:
                target = fd.resolve()
                if str(target).startswith(os.environ["REVIEW_TEST_ROOT"]) and target.is_file() and target.stat().st_size < 65536:
                    values.append(target.read_bytes())
            except (OSError, RuntimeError):
                pass
        pid = int((proc / "stat").read_text().rsplit(")", 1)[1].split()[1])
    else:
        values = [subprocess.check_output(["ps", "eww", "-p", str(pid), "-o", "command="])]
        pid = int(subprocess.check_output(["ps", "-p", str(pid), "-o", "ppid="]).strip())
    if any(token in value for token in forbidden for value in values):
        raise RuntimeError("private installation authentication reached repository process ancestry")
    seen += 1
if sys.stdin.buffer.read() != b"":
    raise RuntimeError("repository child inherited private stdin input")
pathlib.Path(".143/cache/source").mkdir(parents=True, exist_ok=True)
pathlib.Path(".143/cache/source/api.txt").write_text("local source")
with open("executions.log", "a") as out:
    out.write("checked\n")
`

func TestReviewCredentialInputIsPrivateFromSetupAndWarmVerifier(t *testing.T) {
	t.Parallel()
	command := "python3 -c '" + shellEscapeSingleQuote(reviewCredentialInspectionPython) + "'"
	config, err := json.Marshal(map[string]any{"review": map[string]any{
		"commands": []string{command}, "verify_commands": []string{command},
		"evidence": []map[string]string{{"path": ".143/cache/source", "description": "local pinned sources", "category": "go_dependencies"}},
	}})
	require.NoError(t, err, "credential-inspection recipe should serialize without containing its secret")
	f := newReviewEvidenceGitFixtureWithConfig(t, string(config), false)
	secret := "fake-installation-credential-review-only"
	secretPath := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(secretPath, []byte(secret), 0o600), "fake secret should be outside repository and command arguments")
	// Use a HTTPS repository identity (the credential-bearing production path)
	// with an isolated trusted test transport shim to the real local Git remote.
	// The shim only replaces transport and emits no credentials or environment.
	remote, err := json.Marshal(f.remote)
	require.NoError(t, err, "isolated transport URL should serialize")
	shimRoot := t.TempDir()
	shim := filepath.Join(shimRoot, "git-fixture")
	shimPython := filepath.Join(shimRoot, "git-fixture.py")
	diagnosticPath := filepath.Join(shimRoot, "fsck-failure")
	diagnosticJSON, err := json.Marshal(diagnosticPath)
	require.NoError(t, err, "fixture diagnostic path should serialize")
	shimSource := `import os, pathlib, subprocess, sys
remote = ` + string(remote) + `
args = [remote if value.startswith('https://') else value for value in sys.argv[1:]]
if 'fetch' in args:
    args = ['-c', 'url.' + remote + '.insteadOf=https://example.com/repo.git'] + args
env = dict(os.environ)
env['GIT_ALLOW_PROTOCOL'] = 'file'
if 'fsck' not in args:
    sys.exit(subprocess.call(['/usr/bin/git'] + args, env=env))
# Production intentionally hides fsck output from public setup errors. Retain
# bounded diagnostics for this isolated fake-credential fixture, without
# recording environment values, command arguments, or successful fsck output.
child = subprocess.Popen(['/usr/bin/git'] + args, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = bytearray()
while True:
    chunk = child.stdout.read(4096)
    if not chunk:
        break
    output.extend(chunk[:max(0, 4096 - len(output))])
code = child.wait()
if code:
    pathlib.Path(` + string(diagnosticJSON) + `).write_bytes(output)
sys.exit(code)
`
	require.NoError(t, os.WriteFile(shimPython, []byte(shimSource), 0o600), "trusted fixture transport should preserve real Git behavior without network access")
	// Linux passes a shebang's entire optional argument as one argv value.
	// An owned shell launcher keeps both Python isolation flags separate and
	// avoids an interpreter-startup failure appearing as corrupt Git objects.
	launcher := "#!/bin/sh\nexec /usr/bin/python3 -I -S '" + shellEscapeSingleQuote(shimPython) + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(shim, []byte(launcher), 0o700), "trusted fixture launcher should preserve isolated Python startup on Linux and macOS")
	out, err := exec.Command(shim, "--version").CombinedOutput()
	require.NoError(t, err, "direct fixture launcher should execute before integrity diagnostics are suppressed: %s", out)
	require.Contains(t, string(out), "git version ", "fixture launcher should forward the requested Git argument")
	logFailure := func(format string, args ...any) {
		t.Logf(format, args...)
		if output, readErr := os.ReadFile(diagnosticPath); readErr == nil {
			safe := strings.ReplaceAll(string(output), secret, "[fake credential redacted]")
			safe = strings.ReplaceAll(safe, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+secret)), "[fake credential redacted]")
			t.Logf("isolated credential fixture fsck diagnostic: %s", safe)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			t.Logf("could not read isolated fixture fsck diagnostic: %v", readErr)
		}
	}
	provider := &recordingReviewCredentialProvider{localReviewEvidenceProvider: localReviewEvidenceProvider{diagnoseFailures: true, logFailure: logFailure, rewriteTrustedGit: shim}}
	o := f.orchestrator()
	o.provider, o.github = provider, testInternalGitHubTokens{token: secret}
	o.repositories = testInternalRepoStore{repo: models.Repository{CloneURL: "https://example.com/repo.git"}}
	out, err = exec.Command("git", "-C", f.workDir, "remote", "set-url", "origin", "https://example.com/repo.git").CombinedOutput()
	require.NoError(t, err, "HTTPS identity fixture should bind its local origin to trusted repository metadata: %s", out)
	sandbox := f.sandbox()
	sandbox.Env = map[string]string{"REVIEW_TEST_SECRET_PATH": secretPath, "REVIEW_TEST_ROOT": f.workDir, "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "http.extraHeader", "GIT_CONFIG_VALUE_0": "stale-fake-header", "GIT_TRACE": "1"}
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, sandbox, zerolog.Nop()), "cold missing-tree hydration and repository setup must hide private authentication from ancestry/stdin")
	require.Equal(t, 1, provider.inputs, "only missing pinned trees should request private authentication transport")
	o.github = nil
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, sandbox, zerolog.Nop()), "healthy warm verifier must bind scoped repository metadata without any token lookup")
	require.NoError(t, os.Remove(filepath.Join(f.workDir, ".143/cache/source/api.txt")), "fixture should remove dependency evidence while leaving Git trees complete")
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, sandbox, zerolog.Nop()), "local dependency repair must run without installation authentication")
	expired := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Minute))
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(expired, f.session, sandbox, zerolog.Nop()), "complete expired evidence must verify without private input or arbitrary recipe execution")
	require.Equal(t, 1, provider.inputs, "warm repair and expired reuse must not use private authentication transport")
	for _, value := range []string{strings.Join(provider.commands, "\n"), readCredentialFixtureFile(t, filepath.Join(f.workDir, ".git/config")), readCredentialFixtureFile(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"))} {
		require.NotContains(t, value, secret, "provider command arguments and persisted evidence must exclude the fake raw installation token")
		require.NotContains(t, value, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+secret)), "provider arguments and persisted evidence must exclude encoded HTTP authentication")
	}
	checks := readCredentialFixtureFile(t, filepath.Join(f.workDir, "executions.log"))
	require.Equal(t, "checked\nchecked\nchecked\nchecked\nchecked\n", checks, "cold setup/verify, warm verify, and dependency repair setup/verify should inspect real process ancestry; expired verification must execute no recipe")
}

func readCredentialFixtureFile(t *testing.T, path string) string {
	t.Helper()
	value, err := os.ReadFile(path)
	require.NoError(t, err, "credential regression fixture should be readable")
	return string(value)
}

func TestReviewGitPrivateHeaderIsScopedAndNotInArguments(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700), "fake Git fixture directory should exist")
	secret := "fake-header-child-only"
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + secret))
	git := `#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys
data = {"argv": sys.argv[1:], "auth": {k:v for k,v in os.environ.items() if k.startswith("GIT_CONFIG_")}, "stdin": sys.stdin.read()}
pathlib.Path(os.environ["REVIEW_TEST_GIT_OUTPUT"], "git-observed.json").write_text(json.dumps(data))
subprocess.check_call(["__HELPER__", "origin", "https://example.com/repo.git"])
destination = sys.argv[-1]
subprocess.check_call(["/usr/bin/git", "init", "--quiet", destination])
subprocess.check_call(["/usr/bin/git", "-C", destination, "config", "remote.origin.url", "https://example.com/repo.git"])
`
	helper := `#!/usr/bin/env python3
import json, os, pathlib, sys
pathlib.Path(os.environ["REVIEW_TEST_GIT_OUTPUT"], "remote-observed.json").write_text(json.dumps(sys.argv[1:]))
`
	git = strings.ReplaceAll(git, "__HELPER__", filepath.Join(bin, "git-remote-https"))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o700), "trusted Git test executable should record only controlled authentication namespaces")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git-remote-https"), []byte(helper), 0o700), "remote helper fixture should record its clean arguments")
	provider := &recordingReviewCredentialProvider{localReviewEvidenceProvider: localReviewEvidenceProvider{rewriteTrustedGit: filepath.Join(bin, "git")}}
	o := &Orchestrator{provider: provider, logger: zerolog.Nop()}
	sandbox := &Sandbox{WorkDir: filepath.Join(root, "checkout"), Env: map[string]string{"PATH": bin + ":" + os.Getenv("PATH"), "REVIEW_TEST_GIT_OUTPUT": root}}
	require.NoError(t, o.cloneRepositoryForReview(context.Background(), sandbox, "https://example.com/repo.git", "main", secret), "clone should send private authentication to its trusted Git child")
	var observed struct {
		Argv  []string          `json:"argv"`
		Auth  map[string]string `json:"auth"`
		Stdin string            `json:"stdin"`
	}
	require.NoError(t, json.Unmarshal([]byte(readCredentialFixtureFile(t, filepath.Join(root, "git-observed.json"))), &observed), "fake Git observation should decode")
	require.Equal(t, map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null", "GIT_CONFIG_COUNT": "4", "GIT_CONFIG_KEY_0": "http.extraHeader", "GIT_CONFIG_VALUE_0": "", "GIT_CONFIG_KEY_1": "http.https://example.com/repo.git.extraHeader", "GIT_CONFIG_VALUE_1": "AUTHORIZATION: basic " + encoded, "GIT_CONFIG_KEY_2": "http.proxy", "GIT_CONFIG_VALUE_2": "", "GIT_CONFIG_KEY_3": "http.sslVerify", "GIT_CONFIG_VALUE_3": "true"}, observed.Auth, "Git child alone should receive a transient scoped header and explicit direct/TLS verification defaults")
	require.Empty(t, observed.Stdin, "trusted Git child must not inherit private input")
	require.Contains(t, observed.Argv, "--no-checkout", "authenticated clone must defer checkout filters until authentication is gone")
	for _, value := range []string{strings.Join(provider.commands, "\n"), strings.Join(observed.Argv, "\n"), readCredentialFixtureFile(t, filepath.Join(root, "remote-observed.json"))} {
		require.NotContains(t, value, secret, "provider, Git, and remote helper arguments must exclude raw authentication")
		require.NotContains(t, value, encoded, "provider, Git, and remote helper arguments must exclude encoded authentication")
	}
}

func TestReviewCloneAndCheckoutFiltersCannotInheritAuthentication(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	root := t.TempDir()
	destination := filepath.Join(root, "checkout")
	secret := "fake-filter-installation-secret"
	provider := &recordingReviewCredentialProvider{}
	o := &Orchestrator{provider: provider, logger: zerolog.Nop()}
	sandbox := &Sandbox{WorkDir: destination}
	require.NoError(t, o.cloneRepositoryForReview(context.Background(), sandbox, f.remote, "review", secret), "authenticated clone must publish only a local Git database")
	require.NoFileExists(t, filepath.Join(destination, "changed.go"), "authenticated clone must not run checkout filters while holding its private header")
	require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, sandbox, f.remote, secret, zerolog.Nop()), "owned clone profile must hydrate before untrusted local filter metadata is planted")
	marker := filepath.Join(root, "filter-result")
	filter := filepath.Join(root, "filter.py")
	code := `import os, pathlib, sys
for name in os.environ:
    if name in {"GITHUB_TOKEN", "GH_TOKEN"} and os.environ[name] or name.startswith("GIT_CONFIG_VALUE_") and "AUTHORIZATION:" in os.environ[name]:
        raise RuntimeError("checkout filter inherited Git authentication")
pathlib.Path(sys.argv[1]).write_text("clean")
sys.stdout.buffer.write(sys.stdin.buffer.read())
`
	require.NoError(t, os.WriteFile(filter, []byte(code), 0o600), "controlled smudge filter should inspect only authentication namespaces")
	require.NoError(t, os.MkdirAll(filepath.Join(destination, ".git/info"), 0o700), "template-free clone fixture should explicitly create its local attributes directory")
	require.NoError(t, os.WriteFile(filepath.Join(destination, ".git/info/attributes"), []byte("changed.go filter=fixture\n"), 0o600), "fixture should require an actual smudge filter during checkout")
	out, err := exec.Command("git", "-C", destination, "config", "filter.fixture.smudge", "/usr/bin/python3 -I -S '"+shellEscapeSingleQuote(filter)+"' '"+shellEscapeSingleQuote(marker)+"'").CombinedOutput()
	require.NoError(t, err, "fixture smudge command should be configured: %s", out)
	out, err = exec.Command("git", "-C", strings.TrimPrefix(f.remote, "file://"), "update-ref", "refs/pull/42/head", f.head).CombinedOutput()
	require.NoError(t, err, "fixture should expose the pinned PR ref: %s", out)
	require.NoFileExists(t, marker, "hydration must never run the configured smudge filter")
	require.Error(t, o.checkoutReviewPullRequestHead(context.Background(), sandbox, f.remote, secret, 42, "review", f.head), "mutable local attributes/filter configuration must fail before any authenticated fetch")
	require.NoFileExists(t, marker, "unsafe configuration must never execute its smudge helper with authentication")
	require.NoError(t, o.runBoundedReviewCommand(context.Background(), sandbox, "GIT_NO_LAZY_FETCH=1 /usr/bin/git -c core.hooksPath=/dev/null checkout -B review '"+f.head+"'", ""), "separate credential-free checkout control should exercise the smudge filter without private header inheritance")
	require.Equal(t, "clean", readCredentialFixtureFile(t, marker), "actual checkout filter must observe no inherited installation header/token")
	for _, value := range []string{strings.Join(provider.commands, "\n"), readCredentialFixtureFile(t, filepath.Join(destination, ".git/config"))} {
		require.NotContains(t, value, secret, "clone, direct hydration and PR fetch command/config must exclude installation tokens")
		require.NotContains(t, value, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+secret)), "direct authenticated paths must never persist or expose encoded headers in arguments")
	}
}

func TestReviewTrustedWrapperIgnoresWritablePathAndPythonStartup(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700), "poisoned executable fixture should be isolated")
	marker := filepath.Join(root, "intercepted")
	for _, name := range []string{"python3", "git", "sh", "git-remote-https"} {
		body := "#!/bin/sh\nprintf intercepted > '" + shellEscapeSingleQuote(marker) + "'\ncat >/dev/null\nexit 126\n"
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700), "writable executable must expose any trusted transport interception")
	}
	startup := "import pathlib; pathlib.Path('" + marker + "').write_text('startup interception')\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "sitecustomize.py"), []byte(startup), 0o600), "PYTHONPATH startup fixture should expose nonisolated Python")
	query := exec.Command("/usr/bin/python3", "-c", "import site; print(site.getusersitepackages())")
	query.Env = append(os.Environ(), "HOME="+root)
	out, err := query.CombinedOutput()
	require.NoError(t, err, "fixture should locate the isolated user Python startup directory: %s", out)
	userSite := strings.TrimSpace(string(out))
	require.NoError(t, os.MkdirAll(userSite, 0o700), "user site startup fixture directory should exist")
	require.NoError(t, os.WriteFile(filepath.Join(userSite, "sitecustomize.py"), []byte(startup), 0o600), "user-site startup fixture should expose unisolated transport")
	sandbox := f.sandbox()
	sandbox.Env = map[string]string{"PATH": bin + ":" + os.Getenv("PATH"), "PYTHONPATH": root, "HOME": root, "GIT_EXEC_PATH": bin}
	o := f.orchestrator()
	o.github = testInternalGitHubTokens{token: "fake-startup-installation-secret"}
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, sandbox, zerolog.Nop()), "trusted no-auth probe and authenticated retry must bypass writable interpreter/Git/startup hooks")
	require.NoFileExists(t, marker, "writable Python, Git, shell, remote helper, and startup modules must never intercept private input")
}

func TestReviewPrivateAuthenticationFailsClosedAndRedactsDiagnostics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		provider       SandboxProvider
		env            map[string]string
		want           string
		classification error
	}{
		{name: "unsupported transport", provider: struct{ SandboxProvider }{}, want: "private Git authentication input"},
		{name: "legacy container token", provider: &testInternalSandboxProvider{}, env: map[string]string{"GITHUB_TOKEN": "legacy-fake-token"}, want: "must be recreated"},
		{name: "private provider error", provider: &testInternalSandboxProvider{execErr: errors.New("fake-only " + base64.StdEncoding.EncodeToString([]byte("x-access-token:fake-only")))}, want: "review preparation transport unavailable", classification: ErrCodeReviewPreparationTransport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &Orchestrator{provider: tt.provider, logger: zerolog.Nop()}
			err := o.execReviewPreparationPayload(context.Background(), &Sandbox{WorkDir: t.TempDir(), Env: tt.env}, reviewWorkspacePreparationPayload{WorkDir: t.TempDir(), Standalone: true, Prepare: "true", CancelPath: filepath.Join(t.TempDir(), "cancel"), TimeoutSeconds: 1}, reviewGitAuthentication{"https://example.com/repo.git", "fake-only"}, zerolog.Nop())
			require.ErrorContains(t, err, tt.want, "unsafe transport/container and private diagnostics must fail without exposing authentication")
			if tt.classification != nil {
				require.ErrorIs(t, err, tt.classification, "private provider failure must retain the retry classification without raw diagnostic details")
			}
			require.NotContains(t, err.Error(), "fake-only", "safe failure must redact raw fake authentication")
			require.NotContains(t, err.Error(), base64.StdEncoding.EncodeToString([]byte("x-access-token:fake-only")), "safe failure must redact encoded fake authentication")
		})
	}
}

func TestReviewPrivateInputReadUsesOriginalDeadline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cancel   bool
		wantCode int
	}{
		{name: "stalled EOF expires original budget", wantCode: 124},
		{name: "cancelled incomplete JSON", cancel: true, wantCode: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			seconds := 0.3
			if tt.cancel {
				seconds = 5
			}
			payload, err := json.Marshal(reviewWorkspacePreparationControl{WorkDir: root, TimeoutSeconds: seconds, CancelPath: filepath.Join(root, "cancel")})
			require.NoError(t, err, "private-input control fixture should serialize")
			cmd := exec.Command("/usr/bin/python3", "-I", "-S", "-c", reviewWorkspacePreparationPython, string(payload))
			stdin, err := cmd.StdinPipe()
			require.NoError(t, err, "fixture should retain an open incomplete authentication stream")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			require.NoError(t, cmd.Start(), "private-input fixture should start before receiving authentication")
			_, err = io.WriteString(stdin, `{"payload":`)
			require.NoError(t, err, "fixture should leave a partial message without EOF")
			if tt.cancel {
				require.NoError(t, os.WriteFile(filepath.Join(root, "cancel"), []byte("cancelled"), 0o600), "cancellation must be observable before stdin decoding completes")
			}
			started := time.Now()
			err = cmd.Wait()
			var exited *exec.ExitError
			require.ErrorAs(t, err, &exited, "stalled authentication input must terminate within the shared budget or cancellation")
			require.Equal(t, tt.wantCode, exited.ExitCode(), "private-input failure must preserve timeout versus cancellation classification")
			require.Less(t, time.Since(started), 2*time.Second, "open stdin cannot escape the preparation budget")
			closeErr := stdin.Close()
			require.True(t, closeErr == nil || errors.Is(closeErr, os.ErrClosed), "process wait must close the private input pipe even when a sender held it open")
			require.FileExists(t, filepath.Join(root, "cancel.done"), "private-input failure must publish cleanup completion before returning")
			require.NoFileExists(t, filepath.Join(root, "entered"), "incomplete authentication must prevent any Git or recipe child execution")
		})
	}
}

func TestReviewPreparationKeepsLargeRecipeOffArguments(t *testing.T) {
	t.Parallel()
	large := strings.Repeat("printf 'quoted data'\\n", 3000)
	payload := reviewWorkspacePreparationPayload{WorkDir: "/checkout", CancelPath: "/checkout/.git/cancel", TimeoutSeconds: 1, Prepare: large}
	command, input, err := reviewPreparationCommand(payload, reviewGitAuthentication{})
	require.NoError(t, err, "valid large declared recipe must fit the bounded private input")
	require.NotContains(t, command, "quoted data", "commands must be transported through stdin rather than expanded/quoted argv")
	data, err := io.ReadAll(input)
	require.NoError(t, err, "bounded preparation input should be readable")
	var message struct {
		Payload reviewWorkspacePreparationPayload `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(data, &message), "stdin must carry the exact general payload")
	require.Equal(t, large, message.Payload.Prepare, "stdin transport must preserve large quoted recipe bytes exactly")
}

func TestActivatedCodeReviewAuthDoesNotPersistInstallationToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		origin models.SessionOrigin
		want   string
	}{
		{name: "review", origin: models.SessionOriginCodeReview, want: ""},
		{name: "ordinary coding", origin: models.SessionOriginManual, want: "fake-fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := SandboxConfig{Env: map[string]string{}}
			orgID, repoID := uuid.New(), uuid.New()
			policy, err := reviewpreparation.NewPolicy(true, []string{"org/repo"})
			require.NoError(t, err, "activated auth test policy must construct")
			o := &Orchestrator{logger: zerolog.Nop(), reviewPreparationPolicy: policy}
			_, err = o.prepareSandboxGitHubAuth(context.Background(), &models.Session{OrgID: orgID, RepositoryID: &repoID, Origin: tt.origin, RevisionContext: json.RawMessage(`{"github_repo":"org/repo"}`)}, &models.Repository{ID: repoID, OrgID: orgID, FullName: "org/repo"}, "fake-fallback", &cfg, zerolog.Nop())
			require.NoError(t, err, "legacy Git identity setup should preserve ordinary coding compatibility")
			require.Equal(t, tt.want, cfg.Env["GITHUB_TOKEN"], "activated review must suppress the token fallback while ordinary coding retains it")
		})
	}
}
