package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type localReviewEvidenceProvider struct {
	SandboxProvider
	diagnoseFailures  bool
	logFailure        func(string, ...any)
	rewriteTrustedGit string // Controlled executable fixture; production always uses system Git.
}

type uncancelledLocalReviewEvidenceProvider struct{ localReviewEvidenceProvider }

func (p uncancelledLocalReviewEvidenceProvider) Exec(_ context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	return p.localReviewEvidenceProvider.Exec(context.Background(), sandbox, command, stdout, stderr)
}

func (p uncancelledLocalReviewEvidenceProvider) ExecWithStdin(_ context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return p.localReviewEvidenceProvider.ExecWithStdin(context.Background(), sandbox, command, stdin, stdout, stderr)
}

func (p localReviewEvidenceProvider) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	return p.ExecWithStdin(ctx, sandbox, command, nil, stdout, stderr)
}

func (p localReviewEvidenceProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if stdin != nil && p.rewriteTrustedGit != "" {
		input, err := io.ReadAll(stdin)
		if err != nil {
			return -1, err
		}
		stdin = bytes.NewReader(bytes.ReplaceAll(input, []byte("/usr/bin/git"), []byte(p.rewriteTrustedGit)))
	}
	if p.diagnoseFailures {
		// Fixtures use local remotes and fake credentials. Include the exact
		// failed child command in their underlying error so unexpected setup
		// failures cannot disappear behind the public-safe stage summary.
		diagnostic := strings.Replace(reviewWorkspacePreparationPython,
			`raise RuntimeError("review workspace preparation command failed (exit=%d)" % code)`,
			`raise RuntimeError("review workspace preparation command failed (exit=%d), command=%r" % (code, script))`, 1)
		diagnostic = strings.Replace(diagnostic, `raise RuntimeError("review Git configuration is unsafe for authenticated preparation; workspace must be recreated")`, `raise RuntimeError("review fixture configuration rejected key=%r" % globals().get("key", "topology"))`, 1)
		command = strings.Replace(command, shellEscapeSingleQuote(reviewWorkspacePreparationPython), shellEscapeSingleQuote(diagnostic), 1)
	}
	// Each host fixture models a separate container namespace, including quoted paths.
	command = strings.ReplaceAll(command, reviewWorkspaceCleanupQuarantinePath, shellEscapeSingleQuote(filepath.Join(sandbox.WorkDir, ".143-test-cleanup-quarantine")))
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = sandbox.WorkDir
	cmd.Stdin = stdin
	cmd.Env = os.Environ()
	for name, value := range sandbox.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	var diagnostics bytes.Buffer
	if p.diagnoseFailures {
		cmd.Stderr = &diagnostics
		if stderr != nil {
			cmd.Stderr = io.MultiWriter(stderr, &diagnostics)
		}
	}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if p.logFailure != nil {
				p.logFailure("local review fixture command failed (exit=%d): %s", exit.ExitCode(), diagnostics.String())
			}
			return exit.ExitCode(), nil
		}
		if p.logFailure != nil {
			p.logFailure("local review fixture could not execute command (%v): %s", err, diagnostics.String())
		}
		return -1, err
	}
	return 0, nil
}

func (localReviewEvidenceProvider) ReadFile(_ context.Context, _ *Sandbox, name string) ([]byte, error) {
	return os.ReadFile(name)
}

type reviewEvidenceGitFixture struct {
	workDir, remote, base, head, historyBlob, source string
	session                                          *models.Session
	t                                                *testing.T
}

func newReviewEvidenceGitFixture(t *testing.T, bootstrap string, noCheckout bool) reviewEvidenceGitFixture {
	t.Helper()
	config := ""
	if bootstrap != "" {
		encoded, err := json.Marshal(map[string]any{"bootstrap": map[string]any{"commands": []string{bootstrap}}})
		require.NoError(t, err, "fixture bootstrap should serialize")
		config = string(encoded)
	}
	return newReviewEvidenceGitFixtureWithConfig(t, config, noCheckout)
}

func newReviewEvidenceGitFixtureWithConfig(t *testing.T, config string, noCheckout bool) reviewEvidenceGitFixture {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(repo string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, "Git evidence fixture command should succeed (args=%v repo=%q): %s", args, repo, out)
		return strings.TrimSpace(string(out))
	}
	out, err := exec.Command("git", "init", "-q", "-b", "main", source).CombinedOutput()
	require.NoError(t, err, "initialize isolated evidence repository: %s", out)
	git(source, "config", "user.name", "Test")
	git(source, "config", "user.email", "test@example.com")
	write := func(name, value string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(source, name)), 0o700), "fixture directories should exist")
		require.NoError(t, os.WriteFile(filepath.Join(source, name), []byte(value), 0o600), "fixture evidence should be written")
	}
	write("history-only.txt", "old unrelated history\n")
	git(source, "add", ".")
	git(source, "commit", "-qm", "history before review")
	historyBlob := git(source, "rev-parse", "HEAD:history-only.txt")
	git(source, "rm", "history-only.txt")
	write("removed.go", "base implementation removed by the PR\n")
	write("surrounding.go", "surrounding implementation\n")
	write("changed.go", "common implementation\n")
	write(".gitignore", "node_modules/\n.143/cache/\nexecutions.log\npermit\nlate-child\n")
	if config != "" {
		write(".143/config.json", config)
	}
	git(source, "add", ".")
	git(source, "commit", "-qm", "comparison merge base")
	git(source, "checkout", "-qb", "review")
	git(source, "rm", "removed.go")
	git(source, "mv", "surrounding.go", "renamed.go")
	write("changed.go", "PR head implementation\n")
	git(source, "add", ".")
	git(source, "commit", "-qm", "review head")
	head := git(source, "rev-parse", "HEAD")
	git(source, "checkout", "-q", "main")
	write("changed.go", "pinned target implementation\n")
	write("target-only.go", "target branch surrounding implementation\n")
	git(source, "add", ".")
	git(source, "commit", "-qm", "pinned base")
	base := git(source, "rev-parse", "HEAD")
	remote := filepath.Join(root, "remote.git")
	out, err = exec.Command("git", "clone", "-q", "--bare", source, remote).CombinedOutput()
	require.NoError(t, err, "create isolated filtered-clone remote: %s", out)
	git(remote, "config", "uploadpack.allowFilter", "true")
	git(remote, "config", "uploadpack.allowAnySHA1InWant", "true")
	workDir := filepath.Join(root, "reviewer's checkout")
	remoteURL := "file://" + remote
	args := []string{"clone", "-q", "--filter=blob:none", "--branch", "review"}
	if noCheckout {
		args = append(args, "--no-checkout")
	}
	args = append(args, remoteURL, workDir)
	clone := exec.Command("git", args...)
	// Match review clone's isolated configuration rather than inheriting host
	// branch.autoSetupRebase/templates into the server-owned local profile.
	clone.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err = clone.CombinedOutput()
	require.NoError(t, err, "create actual partial-clone fixture: %s", out)
	repoID := uuid.New()
	revision, err := json.Marshal(codeReviewCheckoutContext{Kind: "code_review", GitHubPRNumber: 42, BaseSHA: base, HeadSHA: head})
	require.NoError(t, err, "pinned review revision should serialize")
	return reviewEvidenceGitFixture{
		workDir: workDir, remote: remoteURL, base: base, head: head, historyBlob: historyBlob, source: source,
		session: &models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: models.SessionOriginCodeReview, RepositoryID: &repoID, RevisionContext: revision}, t: t,
	}
}

func (f reviewEvidenceGitFixture) orchestrator() *Orchestrator {
	return &Orchestrator{
		provider: localReviewEvidenceProvider{diagnoseFailures: true, logFailure: f.t.Logf}, repositories: testInternalRepoStore{repo: models.Repository{CloneURL: f.remote}},
		github: testInternalGitHubTokens{}, logger: zerolog.Nop(),
	}
}

func (f reviewEvidenceGitFixture) sandbox() *Sandbox {
	return &Sandbox{ID: "shared", WorkDir: f.workDir}
}

func TestReviewEvidenceHydratesPinnedTreesForOfflineReading(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", true)
	// Move the remote target after capturing the review revision. Evidence
	// must still resolve the captured base, never the new main branch tip.
	require.NoError(t, os.WriteFile(filepath.Join(f.source, "target-only.go"), []byte("new target tip\n"), 0o600), "fixture should advance target source after capture")
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "target advanced after review capture"}, {"push", "-q", f.remote, "main"}} {
		out, err := exec.Command("git", append([]string{"-C", f.source}, args...)...).CombinedOutput()
		require.NoError(t, err, "advance remote target fixture: %s", out)
	}
	orchestrator := f.orchestrator()
	// --missing=print must fail on missing blobs without fetching them even if
	// the sandbox Git version ignores the newer GIT_NO_LAZY_FETCH variable.
	olderGitProbe := strings.Replace(reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), "GIT_NO_LAZY_FETCH=1 ", "", 1)
	require.Error(t, orchestrator.execReviewEvidence(context.Background(), f.sandbox(), olderGitProbe, ""), "offline verification should detect missing evidence without relying on the no-lazy-fetch environment variable")
	require.NoError(t, orchestrator.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", zerolog.Nop()), "server preparation should materialize all pinned review trees")
	require.NoError(t, os.Rename(strings.TrimPrefix(f.remote, "file://"), strings.TrimPrefix(f.remote, "file://")+"-offline"), "remote should be unavailable for offline verification")
	require.NoError(t, orchestrator.execReviewEvidence(context.Background(), f.sandbox(), reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), ""), "pinned evidence should verify with no remote access")
	tests := []struct{ revision, file, expected string }{
		{revision: f.base, file: "removed.go", expected: "base implementation removed by the PR\n"},
		{revision: f.base, file: "target-only.go", expected: "target branch surrounding implementation\n"},
		{revision: f.head, file: "changed.go", expected: "PR head implementation\n"},
		{revision: f.base, file: "surrounding.go", expected: "surrounding implementation\n"},
		{revision: f.head, file: "renamed.go", expected: "surrounding implementation\n"},
	}
	for _, tt := range tests {
		t.Run(tt.file+tt.revision[:8], func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("git", "-C", f.workDir, "-c", "remote.origin.promisor=false", "show", tt.revision+":"+tt.file)
			cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "base/head source should be readable offline: %s", out)
			require.Equal(t, tt.expected, string(out), "offline review content should match its exact pinned revision")
		})
	}
	cmd := exec.Command("git", "-C", f.workDir, "-c", "remote.origin.promisor=false", "cat-file", "-e", f.historyBlob)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	require.Error(t, cmd.Run(), "hydration must not download unrelated history blobs")
}

func TestReviewEvidencePreparationAcrossSeparateExecutors(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixtureWithConfig(t, `{"review":{"commands":["mkdir -p node_modules/pkg; printf types > node_modules/pkg/index.d.ts; printf '.\\n' >> executions.log"],"verify_commands":["test -f node_modules/pkg/index.d.ts"],"evidence":[{"path":"node_modules/pkg","description":"Dependency type declarations","category":"javascript_dependencies"}]}}`, false)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop())
		}()
	}
	for range 2 {
		err := <-results
		require.NoError(t, err, "separate executors should join shared evidence preparation; fixture cause: %v", errors.Unwrap(err))
	}
	count, err := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
	require.NoError(t, err, "bootstrap run evidence should be readable")
	require.Equal(t, ".\n", string(count), "two concurrent executors should run dependency bootstrap once")
	// Reusing after an orchestrator restart verifies actual output presence.
	require.NoError(t, os.Remove(filepath.Join(f.workDir, "node_modules/pkg/index.d.ts")), "remove an installed dependency type declaration")
	require.NoError(t, f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "a stale receipt should repair missing dependency evidence")
	count, err = os.ReadFile(filepath.Join(f.workDir, "executions.log"))
	require.NoError(t, err, "bootstrap retry evidence should be readable")
	require.Equal(t, ".\n.\n", string(count), "missing outputs should trigger exactly one new preparation")
}

func TestReviewPullRequestCheckoutSuppressesRepositoryHooks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		custom bool
	}{
		{name: "default hooks"},
		{name: "custom hooks", custom: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			out, err := exec.Command("git", "-C", strings.TrimPrefix(f.remote, "file://"), "update-ref", "refs/pull/42/head", f.head).CombinedOutput()
			require.NoError(t, err, "isolated remote should expose the captured pull request ref: %s", out)
			hooks := filepath.Join(f.workDir, ".git/hooks")
			if tt.custom {
				hooks = filepath.Join(f.workDir, ".git/custom-hooks")
				out, err = exec.Command("git", "-C", f.workDir, "config", "core.hooksPath", hooks).CombinedOutput()
				require.NoError(t, err, "fixture should use its custom hook directory: %s", out)
			}
			require.NoError(t, os.MkdirAll(hooks, 0o700), "checkout hook directory should exist")
			marker := filepath.Join(f.workDir, "permit")
			hook := "#!/bin/sh\nprintf 'checkout\\n' >> '" + shellEscapeSingleQuote(marker) + "'\n"
			require.NoError(t, os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte(hook), 0o700), "checkout hook should expose execution")
			checkoutErr := f.orchestrator().checkoutReviewPullRequestHead(context.Background(), f.sandbox(), f.remote, "", 42, "review", f.head)
			if tt.custom {
				require.Error(t, checkoutErr, "unowned custom hooks configuration must be rejected before authentication")
				require.NoError(t, f.orchestrator().runBoundedReviewCommand(context.Background(), f.sandbox(), "/usr/bin/git -c core.hooksPath=/dev/null checkout -B review '"+f.head+"'", ""), "credential-free checkout control must still suppress configured hooks")
			} else {
				require.NoError(t, checkoutErr, "owned default-hook layout should preserve pinned checkout without executing hooks")
			}
			_, err = os.Stat(marker)
			require.ErrorIs(t, err, os.ErrNotExist, "review pull request checkout must suppress default and configured post-checkout hooks")
			out, err = exec.Command("git", "-C", f.workDir, "checkout", "-B", "ordinary", f.head).CombinedOutput()
			require.NoError(t, err, "ordinary Git checkout should keep its configured behavior: %s", out)
			actual, err := os.ReadFile(marker)
			require.NoError(t, err, "ordinary checkout should invoke the same hook as a control")
			require.Equal(t, "checkout\n", string(actual), "transient review override must leave ordinary checkout hooks enabled")
		})
	}
}

func TestReviewCloneSuppressesTemplateHooksWithoutPersistingOverride(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	root := t.TempDir()
	template := filepath.Join(root, "template")
	require.NoError(t, os.MkdirAll(filepath.Join(template, "hooks"), 0o700), "Git clone template should contain its hook directory")
	marker := filepath.Join(root, "executed")
	hook := "#!/bin/sh\nprintf 'checkout\\n' >> '" + shellEscapeSingleQuote(marker) + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(template, "hooks/post-checkout"), []byte(hook), 0o700), "clone template should contain an executable checkout hook")
	env := append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=init.templateDir", "GIT_CONFIG_VALUE_0="+template)
	destination := filepath.Join(root, "review checkout")
	cmd := exec.Command("sh", "-c", reviewCloneScript(f.remote, "review", destination))
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "review clone should still initialize and check out the pinned branch: %s", out)
	require.NoFileExists(t, filepath.Join(destination, ".git/hooks/post-checkout"), "authenticated review clone must exclude user templates entirely")
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "review-only clone must suppress checkout hooks from configured templates")
	config, err := os.ReadFile(filepath.Join(destination, ".git/config"))
	require.NoError(t, err, "review clone configuration should remain readable")
	require.NotContains(t, string(config), "hooksPath", "review hook override must remain transient rather than changing ordinary repository behavior")
	ordinary := exec.Command("git", "clone", "-q", "--branch", "review", f.remote, filepath.Join(root, "ordinary"))
	ordinary.Env = env
	out, err = ordinary.CombinedOutput()
	require.NoError(t, err, "ordinary clone should retain configured template behavior: %s", out)
	actual, err := os.ReadFile(marker)
	require.NoError(t, err, "ordinary clone should execute its template checkout hook as a control")
	require.Equal(t, "checkout\n", string(actual), "review hook suppression must not alter ordinary clone behavior")
}

func TestReviewCheckoutDetectsMovedHeadBeforeCheckout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		stale bool
	}{
		{name: "captured PR head"},
		{name: "PR ref changed after capture", stale: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", true)
			ref := f.head
			if tt.stale {
				ref = f.base
			}
			out, err := exec.Command("git", "-C", strings.TrimPrefix(f.remote, "file://"), "update-ref", "refs/pull/42/head", ref).CombinedOutput()
			require.NoError(t, err, "fixture should expose the current PR head ref: %s", out)
			orchestrator := f.orchestrator()
			require.NoError(t, orchestrator.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", zerolog.Nop()), "review trees should be local before bounded checkout")
			err = orchestrator.checkoutReviewPullRequestHead(context.Background(), f.sandbox(), f.remote, "", 42, "143/review/test", f.head)
			if tt.stale {
				require.ErrorIs(t, err, ErrStalePullRequestHead, "a moved PR ref should retain stale-head identity for the review controller")
				err = exec.Command("git", "-C", f.workDir, "show-ref", "--verify", "--quiet", "refs/heads/143/review/test").Run()
				require.Error(t, err, "mismatched mutable PR refs must not prepare a working branch")
			} else {
				require.NoError(t, err, "captured PR head should check out its hydrated source")
				out, err = exec.Command("git", "-C", f.workDir, "rev-parse", "HEAD").CombinedOutput()
				require.NoError(t, err, "prepared checkout should identify its exact head")
				require.Equal(t, f.head, strings.TrimSpace(string(out)), "bounded checkout should preserve the captured head")
			}
			cmd := exec.Command("git", "-C", f.workDir, "-c", "remote.origin.promisor=false", "cat-file", "-e", f.historyBlob)
			cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
			require.Error(t, cmd.Run(), "bounded PR fetch must retain filtered history rather than downloading unrelated blobs")
		})
	}
}

func TestReviewEvidencePreparationFailsWithoutPublishingReadiness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, bootstrap, mutate, config string
	}{
		{name: "bootstrap fails", bootstrap: "exit 23"},
		{name: "bootstrap changes pinned source", bootstrap: "printf changed > changed.go"},
		{name: "dirty tracked source before bootstrap", bootstrap: "mkdir node_modules", mutate: "changed.go"},
		{name: "malformed configuration", config: "{"},
		{name: "unknown declared tool", config: `{"dependencies":{"unsupported-tool":"1.0.0"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, tt.bootstrap, false)
			if tt.mutate != "" {
				require.NoError(t, os.WriteFile(filepath.Join(f.workDir, tt.mutate), []byte("dirty"), 0o600), "fixture should modify pinned source")
			}
			if tt.config != "" {
				require.NoError(t, os.MkdirAll(filepath.Join(f.workDir, ".143"), 0o700), "fixture config directory should exist")
				require.NoError(t, os.WriteFile(filepath.Join(f.workDir, ".143/config.json"), []byte(tt.config), 0o600), "fixture should expose invalid setup instructions")
			}
			require.Error(t, f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "incomplete or inconsistent review evidence must fail setup")
			_, err := os.Stat(filepath.Join(f.workDir, ".git/143-review-evidence.json"))
			require.True(t, os.IsNotExist(err), "failed setup must never publish a readiness receipt")
		})
	}
}

func TestReviewEvidencePreparationRetriesFailedBootstrap(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "test -f permit && mkdir -p node_modules/pkg && printf 'types' > node_modules/pkg/index.d.ts", false)
	require.Error(t, f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "failed bootstrap must not be cached")
	require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "permit"), nil, 0o600), "fixture should permit the next bootstrap attempt")
	require.NoError(t, f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "retry should prepare legacy workspace after an earlier setup failure")
}

func TestReviewEvidenceErrorsRedactAuthentication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		execErr error
		exit    int
	}{
		{name: "provider command error", execErr: errors.New("git fetch https://x-access-token:secret-token@github.com/repo")},
		{name: "git command error", exit: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &testInternalSandboxProvider{execErr: tt.execErr, execExit: tt.exit, execStderr: "https://x-access-token:secret-token@github.com/repo"}
			err := (&Orchestrator{provider: provider}).execReviewEvidence(context.Background(), &Sandbox{}, "git fetch", "secret-token")
			require.Error(t, err, "failed authenticated hydration should fail setup")
			require.NotContains(t, err.Error(), "secret-token", "provider and Git errors must not reveal installation tokens")
		})
	}
}

func TestReviewPreparationTimeoutKillsChildrenBeforeUnlocking(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	payload := reviewWorkspacePreparationPayload{
		WorkDir: f.workDir, Receipt: json.RawMessage(`{"version":1}`), VerifyGit: "true", VerifyTrees: "true", VerifyTools: "true",
		Prepare: `python3 -c "import signal,time,pathlib; signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(2); pathlib.Path('late-child').write_text('escaped')" & wait`, BootstrapDeclared: true, TimeoutSeconds: 1,
	}
	command, input, err := reviewPreparationCommand(payload, reviewGitAuthentication{})
	require.NoError(t, err, "bounded preparation fixture should serialize")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.Error(t, f.orchestrator().execReviewEvidenceInput(ctx, f.sandbox(), command, "", input), "expired preparation must fail before publishing readiness")
	// Wait past the child's attempted write to prove no detached mutation survives.
	time.Sleep(1500 * time.Millisecond)
	_, err = os.Stat(filepath.Join(f.workDir, "late-child"))
	require.True(t, os.IsNotExist(err), "timed-out setup must kill its child process group before releasing the shared lock")
}

func TestReviewPreparationCancellationStopsDisconnectedExec(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	cancelPath := filepath.Join(f.workDir, ".git", "test-cancel")
	payload := reviewWorkspacePreparationPayload{
		WorkDir: f.workDir, Standalone: true, CancelPath: cancelPath,
		Prepare: `python3 -c "import signal,time,pathlib; signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(2); pathlib.Path('late-child').write_text('escaped')" & wait`, TimeoutSeconds: 60,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	// This provider ignores context cancellation while reading command output,
	// matching Docker Exec's attach behavior. Cleanup must use a second command.
	provider := uncancelledLocalReviewEvidenceProvider{}
	err := (&Orchestrator{provider: provider}).execReviewPreparationPayload(ctx, f.sandbox(), payload, reviewGitAuthentication{}, zerolog.Nop())
	require.ErrorIs(t, err, context.DeadlineExceeded, "context cancellation should stop the attached preparation before its independent deadline")
	time.Sleep(2 * time.Second)
	_, err = os.Stat(filepath.Join(f.workDir, "late-child"))
	require.True(t, os.IsNotExist(err), "cancelled preparation must stop TERM-ignoring descendants before returning")
}
