package agent

import (
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
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func sourceReviewConfig(t *testing.T, commands, verifiers []string, entries []repoconfig.ReviewEvidenceEntry) string {
	t.Helper()
	data, err := json.Marshal(repoconfig.Config{Bootstrap: repoconfig.CommandSection{Commands: []string{"exit 91"}}, Review: &repoconfig.ReviewSection{Commands: commands, VerifyCommands: verifiers, Evidence: entries}})
	require.NoError(t, err, "explicit review source fixture should serialize")
	return string(data)
}

func TestReviewDeclaredSourcesReplaceBootstrapAndRepairMixedCaches(t *testing.T) {
	t.Parallel()
	config := sourceReviewConfig(t,
		[]string{"mkdir -p node_modules/pkg .143/cache/go; printf types > node_modules/pkg/index.d.ts; printf 'package dep' > .143/cache/go/api.go; printf '.\\n' >> executions.log"},
		[]string{"test -f node_modules/pkg/index.d.ts && test -f .143/cache/go/api.go"},
		[]repoconfig.ReviewEvidenceEntry{
			{Path: "node_modules/pkg", Description: "TypeScript dependency declarations", Category: models.CodeReviewEvidenceJavaScriptDependencies},
			{Path: ".143/cache/go", Description: "Pinned Go implementations", Category: models.CodeReviewEvidenceGoDependencies},
		})
	f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
	o := f.orchestrator()
	ctx := withCodeReviewPreparationBudget(context.Background())
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "review-specific recipes should replace unrelated failing general bootstrap")
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "valid declared sources and verifiers should reuse the same prepared receipt")
	runs, err := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
	require.NoError(t, err, "recipe execution receipt should be readable")
	require.Equal(t, ".\n", string(runs), "healthy mixed-language sources should prepare once")
	require.NoError(t, os.RemoveAll(filepath.Join(f.workDir, ".143/cache/go")), "fixture should lose the Go sources while preserving npm outputs")
	require.FileExists(t, filepath.Join(f.workDir, "node_modules/pkg/index.d.ts"), "JavaScript cache should remain present while Go evidence is missing")
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "missing declared Go evidence must repair even when unrelated npm outputs remain")
	runs, err = os.ReadFile(filepath.Join(f.workDir, "executions.log"))
	require.NoError(t, err, "repair execution receipt should be readable")
	require.Equal(t, ".\n.\n", string(runs), "missing source roots must trigger one bounded repair")
}

func TestLegacyBootstrapCannotReuseUnrelatedOutputReceipt(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "mkdir -p node_modules/pkg; printf types > node_modules/pkg/index.d.ts; printf '.\\n' >> executions.log", false)
	o := f.orchestrator()
	ctx := withCodeReviewPreparationBudget(context.Background())
	for range 2 {
		require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "legacy bootstrap should remain executable within the original setup budget")
	}
	runs, err := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
	require.NoError(t, err, "legacy preparation execution evidence should be readable")
	require.Equal(t, ".\n.\n", string(runs), "undeclared npm outputs must not establish unrelated external Go/Python source completeness")
	expired := WithCodeReviewWorkspacePreparationDeadline(ctx, time.Now().Add(-time.Second))
	require.ErrorIs(t, o.ensureCodeReviewWorkspaceEvidence(expired, f.session, f.sandbox(), zerolog.Nop()), context.DeadlineExceeded, "legacy unverified artifact recipe must fail after budget exhaustion instead of obtaining a fresh setup budget")
	runs, err = os.ReadFile(filepath.Join(f.workDir, "executions.log"))
	require.NoError(t, err, "legacy execution evidence should survive expired verification")
	require.Equal(t, ".\n.\n", string(runs), "expired validation must not rerun general bootstrap")
}

func TestExpiredReviewVerificationNeverExecutesRepositoryVerifier(t *testing.T) {
	t.Parallel()
	config := sourceReviewConfig(t, []string{"mkdir -p .143/cache/go; printf 'package dep' > .143/cache/go/api.go"},
		[]string{"printf '.\\n' >> permit; test -f .143/cache/go/api.go"},
		[]repoconfig.ReviewEvidenceEntry{{Path: ".143/cache/go", Description: "Pinned Go source", Category: models.CodeReviewEvidenceGoDependencies}})
	f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
	o := f.orchestrator()
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "fresh preparation should execute its declared source verifier")
	expired := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Second))
	// Post-budget validation requires no installation token or repository fetch.
	o.github = nil
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(expired, f.session, f.sandbox(), zerolog.Nop()), "complete source receipt should validate using only platform-local checks after expiry")
	verifications, err := os.ReadFile(filepath.Join(f.workDir, "permit"))
	require.NoError(t, err, "verifier execution evidence should remain readable")
	require.Equal(t, ".\n", string(verifications), "a command named verify is arbitrary code and must never execute during expired validation")
	require.NoError(t, os.Remove(filepath.Join(f.workDir, ".143/cache/go/api.go")), "fixture should remove a recorded source file")
	require.ErrorIs(t, o.ensureCodeReviewWorkspaceEvidence(expired, f.session, f.sandbox(), zerolog.Nop()), context.DeadlineExceeded, "missing cached sources must fail rather than trigger a verifier that could repair them after expiry")
	verifications, err = os.ReadFile(filepath.Join(f.workDir, "permit"))
	require.NoError(t, err, "failed expired validation should preserve verifier evidence")
	require.Equal(t, ".\n", string(verifications), "expired source failure must not execute an arbitrary repair-capable verifier")
}

func TestReviewEvidenceRootSafetyAndTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, command string
		category      models.CodeReviewEvidenceCategory
		symlink       string
		success       bool
	}{
		{name: "missing root", command: "true", category: models.CodeReviewEvidenceGoDependencies},
		{name: "empty dependency directory", command: "mkdir -p .143/cache/source", category: models.CodeReviewEvidenceGoDependencies},
		{name: "dependency file has wrong type", command: "mkdir -p .143/cache; printf source > .143/cache/source", category: models.CodeReviewEvidenceGoDependencies},
		{name: "generated file", command: "mkdir -p .143/cache; printf generated > .143/cache/source", category: models.CodeReviewEvidenceGeneratedArtifacts, success: true},
		{name: "SDK fixture credential filenames", command: "mkdir -p .143/cache/source/test; printf fixture > .143/cache/source/test/.npmrc", category: models.CodeReviewEvidenceGoDependencies, success: true},
		{name: "escaping source symlink", command: "printf executed > permit", category: models.CodeReviewEvidenceGoDependencies, symlink: "outside"},
		{name: "escaping nested SDK symlink", command: "printf executed > permit", category: models.CodeReviewEvidenceGoDependencies, symlink: "nested-outside"},
		{name: "safe internal source symlink", command: "true", category: models.CodeReviewEvidenceGoDependencies, symlink: "inside", success: true},
		{name: "Git metadata symlink", command: "printf executed > permit", category: models.CodeReviewEvidenceGoDependencies, symlink: ".git"},
		{name: "workspace credential symlink", command: "printf executed > permit", category: models.CodeReviewEvidenceGeneratedArtifacts, symlink: ".env.production"},
		{name: "credential under aliased directory", command: "printf executed > permit", category: models.CodeReviewEvidenceGoDependencies, symlink: "credential-parent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := sourceReviewConfig(t, []string{tt.command}, []string{"true"}, []repoconfig.ReviewEvidenceEntry{{Path: ".143/cache/source", Description: "Declared source evidence", Category: tt.category}})
			f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
			if tt.symlink != "" {
				target := filepath.Join(f.workDir, tt.symlink)
				if tt.symlink == "outside" || tt.symlink == "nested-outside" {
					target = t.TempDir()
					require.NoError(t, os.WriteFile(filepath.Join(target, "private.go"), []byte("outside"), 0o600), "escape fixture should contain inaccessible source")
				}
				if tt.symlink == "inside" {
					target = filepath.Join(f.workDir, ".143/cache/actual")
					require.NoError(t, os.MkdirAll(target, 0o700), "safe in-repository target should exist")
					require.NoError(t, os.WriteFile(filepath.Join(target, "api.go"), []byte("package dep"), 0o600), "safe in-repository target should contain source")
				}
				if tt.symlink == ".env.production" {
					require.NoError(t, os.WriteFile(target, []byte("private"), 0o600), "credential target fixture should exist")
				}
				if tt.symlink == "credential-parent" {
					target = filepath.Join(f.workDir, ".config")
					require.NoError(t, os.Mkdir(target, 0o700), "aliased directory should expose its protected child location")
					require.NoError(t, os.WriteFile(filepath.Join(target, "gh"), []byte("private"), 0o600), "protected credential file should exist beneath the aliased directory")
				}
				require.NoError(t, os.MkdirAll(filepath.Join(f.workDir, ".143/cache"), 0o700), "evidence parent should exist for symlink fixture")
				link := filepath.Join(f.workDir, ".143/cache/source")
				if tt.symlink == "nested-outside" {
					require.NoError(t, os.Mkdir(link, 0o700), "nested escape should begin from an ordinary evidence root")
					link = filepath.Join(link, "api.go")
					target = filepath.Join(target, "private.go")
				}
				require.NoError(t, os.Symlink(target, link), "fixture should expose its configured evidence target")
			}
			err := f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop())
			if tt.success {
				require.NoError(t, err, "safe declared source/artifact root should prepare successfully; fixture cause: %v", errors.Unwrap(err))
			} else {
				require.Error(t, err, "missing, wrong-type, or unsafe evidence root must fail setup")
				_, receiptErr := os.Stat(filepath.Join(f.workDir, ".git/143-review-evidence.json"))
				require.ErrorIs(t, receiptErr, os.ErrNotExist, "unsafe or incomplete source roots must not publish readiness")
			}
			if tt.symlink != "" {
				_, markerErr := os.Stat(filepath.Join(f.workDir, "permit"))
				require.ErrorIs(t, markerErr, os.ErrNotExist, "unsafe existing roots must be rejected before repository commands execute")
			}
		})
	}
}

func TestIncompleteReviewArtifactRecipesCannotReuseReceipts(t *testing.T) {
	t.Parallel()
	entry := repoconfig.ReviewEvidenceEntry{Path: "node_modules/pkg", Description: "Dependency source", Category: models.CodeReviewEvidenceJavaScriptDependencies}
	tests := []struct {
		name      string
		verifiers []string
		evidence  []repoconfig.ReviewEvidenceEntry
	}{
		{name: "verifier without declared sources", verifiers: []string{"test -f node_modules/pkg/api.js"}},
		{name: "declared sources without verifier", evidence: []repoconfig.ReviewEvidenceEntry{entry}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := sourceReviewConfig(t, []string{"mkdir -p node_modules/pkg; printf api > node_modules/pkg/api.js; printf '.\\n' >> executions.log"}, tt.verifiers, tt.evidence)
			f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
			o := f.orchestrator()
			ctx := withCodeReviewPreparationBudget(context.Background())
			for range 2 {
				require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "incomplete review declarations should remain setup-compatible inside the original budget")
			}
			runs, err := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
			require.NoError(t, err, "incomplete recipe execution evidence should be readable")
			require.Equal(t, ".\n.\n", string(runs), "artifact receipt reuse requires both declared roots and verifier commands")
			expired := WithCodeReviewWorkspacePreparationDeadline(ctx, time.Now().Add(-time.Second))
			require.ErrorIs(t, o.ensureCodeReviewWorkspaceEvidence(expired, f.session, f.sandbox(), zerolog.Nop()), context.DeadlineExceeded, "incomplete artifact recipe must not gain a new preparation budget after expiry")
		})
	}
}

func TestIncompleteEvidenceCannotHideUnsafeLaterRoot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, firstRoot string
	}{
		{name: "missing first root"},
		{name: "empty first root", firstRoot: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := sourceReviewConfig(t, []string{"printf executed > permit"}, []string{"true"}, []repoconfig.ReviewEvidenceEntry{
				{Path: ".143/cache/first", Description: "First dependency source", Category: models.CodeReviewEvidenceGoDependencies},
				{Path: ".143/cache/second", Description: "Second dependency source", Category: models.CodeReviewEvidenceGoDependencies},
			})
			f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
			require.NoError(t, os.MkdirAll(filepath.Join(f.workDir, ".143/cache"), 0o700), "evidence roots should share an existing parent")
			if tt.firstRoot != "" {
				require.NoError(t, os.Mkdir(filepath.Join(f.workDir, ".143/cache/first"), 0o700), "first evidence root should be empty")
			}
			require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(f.workDir, ".143/cache/second")), "later evidence root should escape the workspace")
			require.Error(t, f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "all declared roots must be checked before setup despite incomplete earlier roots")
			_, err := os.Stat(filepath.Join(f.workDir, "permit"))
			require.ErrorIs(t, err, os.ErrNotExist, "incomplete earlier evidence must not allow setup against a later unsafe root")
		})
	}
}

func TestFailedReviewVerifierCannotRepairThroughUnsafeRoot(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	verifier := "if test -f permit; then ln -s '" + shellEscapeSingleQuote(outside) + "' .143/cache/go/escape; exit 1; fi; printf verified > permit"
	config := sourceReviewConfig(t,
		[]string{"mkdir -p .143/cache/go; printf source > .143/cache/go/api.go; printf '.\\n' >> executions.log"},
		[]string{verifier},
		[]repoconfig.ReviewEvidenceEntry{{Path: ".143/cache/go", Description: "Go source", Category: models.CodeReviewEvidenceGoDependencies}})
	f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
	o := f.orchestrator()
	ctx := withCodeReviewPreparationBudget(context.Background())
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "initial safe verifier should publish prepared evidence")
	require.Error(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "a failed verifier that exposes an unsafe root must fail before attempting repair")
	runs, err := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
	require.NoError(t, err, "preparation execution history should remain available")
	require.Equal(t, ".\n", string(runs), "unsafe verifier output must not trigger another repository setup command")
}

func TestReviewVerifierCannotPublishDirtyPinnedSource(t *testing.T) {
	t.Parallel()
	config := sourceReviewConfig(t, []string{"mkdir -p .143/cache/go; printf source > .143/cache/go/api.go"}, []string{"printf changed > changed.go"}, []repoconfig.ReviewEvidenceEntry{{Path: ".143/cache/go", Description: "Go source", Category: models.CodeReviewEvidenceGoDependencies}})
	f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
	require.Error(t, f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "repository verifier must preserve the pinned tracked tree before readiness")
	_, err := os.Stat(filepath.Join(f.workDir, ".git/143-review-evidence.json"))
	require.ErrorIs(t, err, os.ErrNotExist, "a verifier that dirties tracked source must not publish a receipt")
}

func TestReviewOnlySectionDoesNotChangeCodingBootstrap(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixtureWithConfig(t, `{"bootstrap":{"commands":["printf coding > permit"]},"review":{"commands":["exit 88"]}}`, false)
	require.NoError(t, PrepareSandboxRepository(context.Background(), localReviewEvidenceProvider{}, f.sandbox(), f.workDir, zerolog.Nop()), "ordinary coding should continue to execute only its general bootstrap")
	marker, err := os.ReadFile(filepath.Join(f.workDir, "permit"))
	require.NoError(t, err, "ordinary coding bootstrap marker should exist")
	require.Equal(t, "coding", strings.TrimSpace(string(marker)), "review-only recipe must not replace ordinary coding bootstrap")
}

func TestExpiredReviewVerificationDoesNotExecuteGitHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, key, attributes string
	}{
		{name: "clean filter", key: "filter.fixture.clean", attributes: "changed.go filter=fixture\n"},
		{name: "process filter", key: "filter.fixture.process", attributes: "changed.go filter=fixture\n"},
		{name: "fsmonitor", key: "core.fsmonitor"},
		{name: "external diff", key: "diff.external"},
		{name: "textconv", key: "diff.fixture.textconv", attributes: "changed.go diff=fixture\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			o := f.orchestrator()
			require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "fixture should have complete prepared source before configuring executable Git helpers")
			helper := filepath.Join(f.workDir, ".git/unsafe-helper.sh")
			require.NoError(t, os.WriteFile(helper, []byte("#!/bin/sh\nprintf executed > '"+shellEscapeSingleQuote(filepath.Join(f.workDir, "permit"))+"'\ncat\n"), 0o700), "configured Git helper should leave an observable execution marker")
			out, err := exec.Command("git", "-C", f.workDir, "config", tt.key, "'"+shellEscapeSingleQuote(helper)+"'").CombinedOutput()
			require.NoError(t, err, "fixture executable Git configuration should be installed: %s", out)
			if tt.attributes != "" {
				require.NoError(t, os.MkdirAll(filepath.Join(f.workDir, ".git/info"), 0o700), "fixture Git attributes directory should exist")
				require.NoError(t, os.WriteFile(filepath.Join(f.workDir, ".git/info/attributes"), []byte(tt.attributes), 0o600), "fixture should assign the executable helper to tracked source")
			}
			require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "changed.go"), []byte("source changed after preparation\n"), 0o600), "dirty source should require worktree comparison")
			expired := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Second))
			require.ErrorIs(t, o.ensureCodeReviewWorkspaceEvidence(expired, f.session, f.sandbox(), zerolog.Nop()), context.DeadlineExceeded, "post-budget platform validation must reject unsafe or dirty source without executing repository-provided Git helpers")
			_, err = os.Stat(filepath.Join(f.workDir, "permit"))
			require.ErrorIs(t, err, os.ErrNotExist, "Git filters, fsmonitor, external diff, and textconv must not execute during expired validation")
		})
	}
}

func TestReviewWorkspaceVerificationDoesNotExecuteIndexChangeHooks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		custom  bool
		expired bool
	}{
		{name: "active default hooks"},
		{name: "active custom hooks", custom: true},
		{name: "expired default hooks", expired: true},
		{name: "expired custom hooks", custom: true, expired: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			o := f.orchestrator()
			require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "hook fixture should first publish complete pinned evidence")
			hooks := filepath.Join(f.workDir, ".git/hooks")
			if tt.custom {
				hooks = filepath.Join(f.workDir, ".git/custom-hooks")
				out, err := exec.Command("git", "-C", f.workDir, "config", "core.hooksPath", hooks).CombinedOutput()
				require.NoError(t, err, "custom hook path should be configured for this isolated fixture: %s", out)
			}
			require.NoError(t, os.MkdirAll(hooks, 0o700), "configured hook directory should exist")
			hook := "#!/bin/sh\nprintf 'executed\\n' >> '" + shellEscapeSingleQuote(filepath.Join(f.workDir, "permit")) + "'\n"
			require.NoError(t, os.WriteFile(filepath.Join(hooks, "post-index-change"), []byte(hook), 0o700), "index-change hook should leave an execution marker")
			changed := filepath.Join(f.workDir, "changed.go")
			stamp := time.Now().Add(time.Minute)
			require.NoError(t, os.Chtimes(changed, stamp, stamp), "same-content tracked source should require index stat refresh")
			ctx := context.Background()
			if tt.expired {
				ctx = WithCodeReviewWorkspacePreparationDeadline(ctx, time.Now().Add(-time.Second))
			}
			err := o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop())
			if tt.custom {
				require.Error(t, err, "unowned custom hook configuration must invalidate preparation before any hook execution")
				require.NoError(t, o.execReviewEvidence(context.Background(), f.sandbox(), reviewPinnedCheckoutVerificationScript(f.workDir, f.base, f.head), ""), "separate local verification control must still suppress custom index-change hooks")
			} else {
				require.NoError(t, err, "platform verification should accept owned clean source without index-change hook execution; fixture cause: %v", errors.Unwrap(err))
			}
			_, err = os.Stat(filepath.Join(f.workDir, "permit"))
			require.ErrorIs(t, err, os.ErrNotExist, "active and expired platform checks must not invoke default or configured Git hooks")
			// Ordinary Git retains its configured behavior. A second stat change
			// proves this is an executable hook and the review-only override is
			// responsible for suppressing it, rather than a non-triggering fixture.
			stamp = stamp.Add(time.Minute)
			require.NoError(t, os.Chtimes(changed, stamp, stamp), "ordinary Git comparison should need a separate index stat refresh")
			out, err := exec.Command("git", "-C", f.workDir, "diff", "--quiet", "HEAD", "--").CombinedOutput()
			require.NoError(t, err, "ordinary Git should still accept unchanged source: %s", out)
			marker, err := os.ReadFile(filepath.Join(f.workDir, "permit"))
			require.NoError(t, err, "ordinary Git should still execute the configured index-change hook")
			require.Equal(t, "executed\n", string(marker), "review verification must leave ordinary Git hook behavior unchanged")
		})
	}
}

type olderGitLocalReviewEvidenceProvider struct{ localReviewEvidenceProvider }

func (p olderGitLocalReviewEvidenceProvider) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	// Exercise the --missing=print guard independently from the newer Git
	// environment variable, matching sandbox versions without its backport.
	return p.localReviewEvidenceProvider.Exec(ctx, sandbox, strings.ReplaceAll(command, "GIT_NO_LAZY_FETCH=1 ", ""), stdout, stderr)
}

func (p olderGitLocalReviewEvidenceProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return p.localReviewEvidenceProvider.ExecWithStdin(ctx, sandbox, strings.ReplaceAll(command, "GIT_NO_LAZY_FETCH=1 ", ""), stdin, stdout, stderr)
}

func TestExpiredReviewVerificationDoesNotRehydrateMissingGitBlobs(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	o := f.orchestrator()
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "fixture should hydrate all pinned trees before losing a local blob")
	out, err := exec.Command("git", "-C", f.workDir, "rev-parse", "HEAD:changed.go").CombinedOutput()
	require.NoError(t, err, "fixture should identify its current pinned source blob: %s", out)
	blob := strings.TrimSpace(string(out))
	indexes, err := filepath.Glob(filepath.Join(f.workDir, ".git/objects/pack/*.idx"))
	require.NoError(t, err, "partial clone pack indexes should be enumerable")
	removed := false
	for _, index := range indexes {
		out, err := exec.Command("git", "verify-pack", "-v", index).CombinedOutput()
		require.NoError(t, err, "fixture pack contents should be inspectable: %s", out)
		if strings.Contains(string(out), blob+" ") {
			for _, suffix := range []string{".pack", ".idx", ".promisor"} {
				name := strings.TrimSuffix(index, ".idx") + suffix
				if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
					require.NoError(t, err, "fixture source blob pack should be removable")
				}
			}
			removed = true
		}
	}
	require.True(t, removed, "fixture must remove an actual pinned source blob pack")
	require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "changed.go"), []byte("source changed after blob loss\n"), 0o600), "dirty worktree should expose accidental lazy-fetch behavior in an ordinary diff")
	before, err := filepath.Glob(filepath.Join(f.workDir, ".git/objects/pack/*"))
	require.NoError(t, err, "remaining local pack inventory should be readable")
	o.provider = olderGitLocalReviewEvidenceProvider{}
	expired := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Second))
	require.ErrorIs(t, o.ensureCodeReviewWorkspaceEvidence(expired, f.session, f.sandbox(), zerolog.Nop()), context.DeadlineExceeded, "expired verification must fail on missing pinned blobs before a diff can fetch them")
	after, err := filepath.Glob(filepath.Join(f.workDir, ".git/objects/pack/*"))
	require.NoError(t, err, "failed local verification should leave pack inventory available")
	require.Equal(t, before, after, "a reachable promisor remote must not be used to reconstruct source after the preparation budget expires")
}

func TestNonObjectReviewReceiptsAreRepairableOnlyWithinBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, receipt string
	}{
		{name: "null", receipt: "null"},
		{name: "list", receipt: "[]"},
		{name: "string", receipt: `"invalid receipt"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := sourceReviewConfig(t,
				[]string{"mkdir -p .143/cache/go; printf source > .143/cache/go/api.go; printf '.\\n' >> executions.log"},
				[]string{"test -f .143/cache/go/api.go"},
				[]repoconfig.ReviewEvidenceEntry{{Path: ".143/cache/go", Description: "Go source", Category: models.CodeReviewEvidenceGoDependencies}})
			f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
			o := f.orchestrator()
			ctx := withCodeReviewPreparationBudget(context.Background())
			require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "initial source preparation should publish its receipt")
			receipt := filepath.Join(f.workDir, ".git/143-review-evidence.json")
			require.NoError(t, os.WriteFile(receipt, []byte(tt.receipt), 0o600), "fixture should replace the readiness receipt with non-object JSON")
			require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "non-object receipt must be treated as missing readiness and repaired within the original budget")
			require.NoError(t, os.WriteFile(receipt, []byte(tt.receipt), 0o600), "fixture should corrupt the repaired receipt after source setup")
			expired := WithCodeReviewWorkspacePreparationDeadline(ctx, time.Now().Add(-time.Second))
			require.ErrorIs(t, o.ensureCodeReviewWorkspaceEvidence(expired, f.session, f.sandbox(), zerolog.Nop()), context.DeadlineExceeded, "expired validation must fail on non-object receipts without attempting another setup")
			runs, err := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
			require.NoError(t, err, "receipt recovery execution history should remain available")
			require.Equal(t, ".\n.\n", string(runs), "non-object receipt repair should run once within budget and never after expiry")
		})
	}
}

func TestReviewGitVerificationDoesNotExecuteNestedSubmoduleHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, key string
	}{
		{name: "nested clean filter", key: "filter.fixture.clean"},
		{name: "nested process filter", key: "filter.fixture.process"},
		{name: "nested fsmonitor", key: "core.fsmonitor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			parent, child := filepath.Join(root, "parent"), filepath.Join(root, "child")
			git := func(repo string, args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
				require.NoError(t, err, "nested Git verification fixture command should succeed: %s", out)
				return strings.TrimSpace(string(out))
			}
			for _, repo := range []string{parent, child} {
				out, err := exec.Command("git", "init", "-q", "-b", "main", repo).CombinedOutput()
				require.NoError(t, err, "isolated parent and child repositories should initialize: %s", out)
				git(repo, "config", "user.name", "Test")
				git(repo, "config", "user.email", "test@example.com")
			}
			require.NoError(t, os.WriteFile(filepath.Join(child, "source.txt"), []byte("first source\n"), 0o600), "child should contain its initial source")
			git(child, "add", ".")
			git(child, "commit", "-qm", "first child source")
			previous := git(child, "rev-parse", "HEAD")
			require.NoError(t, os.WriteFile(filepath.Join(child, "source.txt"), []byte("second source\n"), 0o600), "child should contain a distinct pinned source revision")
			git(child, "commit", "-qam", "pinned child source")
			git(parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", child, "sdk")
			git(parent, "commit", "-qm", "pinned submodule")
			pinned := git(parent, "rev-parse", "HEAD")
			git(parent, "config", "diff.ignoreSubmodules", "all")
			helper := filepath.Join(root, "unsafe-helper.sh")
			marker := filepath.Join(root, "executed")
			require.NoError(t, os.WriteFile(helper, []byte("#!/bin/sh\nprintf executed > '"+shellEscapeSingleQuote(marker)+"'\ncat\n"), 0o700), "nested Git helper should expose any execution")
			sdk := filepath.Join(parent, "sdk")
			git(sdk, "config", tt.key, "'"+shellEscapeSingleQuote(helper)+"'")
			info := git(sdk, "rev-parse", "--git-path", "info")
			if !filepath.IsAbs(info) {
				info = filepath.Join(sdk, info)
			}
			require.NoError(t, os.MkdirAll(info, 0o700), "nested repository attributes directory should exist")
			require.NoError(t, os.WriteFile(filepath.Join(info, "attributes"), []byte("source.txt filter=fixture\n"), 0o600), "nested source should have its executable filter assignment")
			require.NoError(t, os.WriteFile(filepath.Join(sdk, "source.txt"), []byte("dirty nested source\n"), 0o600), "nested worktree should need a dirty-source comparison")
			o := &Orchestrator{provider: localReviewEvidenceProvider{}}
			sandbox := &Sandbox{WorkDir: parent}
			verification := reviewPinnedCheckoutVerificationScript(parent, pinned, pinned)
			require.NoError(t, o.execReviewEvidence(context.Background(), sandbox, verification, ""), "platform Git verification should preserve the pinned gitlink without inspecting a child worktree through arbitrary helpers")
			_, err := os.Stat(marker)
			require.ErrorIs(t, err, os.ErrNotExist, "dirty nested source must not execute child clean/process filters or fsmonitor")
			git(sdk, "update-ref", "HEAD", previous)
			require.Error(t, o.execReviewEvidence(context.Background(), sandbox, verification, ""), "an actual child HEAD change must fail pinned gitlink verification despite diff.ignoreSubmodules=all")
			_, err = os.Stat(marker)
			require.ErrorIs(t, err, os.ErrNotExist, "changed gitlink validation must still avoid executable nested helpers")
		})
	}
}

func TestReviewEvidenceManifestRetainsFiniteCapacity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		files    int
		width    int
		maxRead  int
		accepted bool
	}{
		{name: "maximum visited entries", files: 299999, width: 6, accepted: true},
		{name: "entry capacity exceeded", files: 300000, width: 6},
		{name: "path bytes capacity exceeded", files: 140000, width: 500},
		{name: "huge directory stops at first over-cap child", files: 1000000, width: 6, maxRead: 300000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(map[string]any{
				"work_dir": t.TempDir(), "timeout_seconds": 60,
				"evidence":      []repoconfig.ReviewEvidenceEntry{{Path: "sources", Description: "Bounded source inventory", Category: models.CodeReviewEvidenceGoDependencies}},
				"fixture_files": tt.files, "fixture_width": tt.width, "maximum_enumerated": tt.maxRead,
			})
			require.NoError(t, err, "bounded manifest fixture should serialize")
			// Use synthetic filesystem metadata to exercise the real traversal
			// boundary without creating hundreds of thousands of test files.
			script := reviewWorkspacePreparationPython[:strings.LastIndex(reviewWorkspacePreparationPython, "\ntry:\n")] + `
from types import SimpleNamespace
from contextlib import nullcontext
safe_evidence_path = lambda relative: (relative, os.path.join(real_workdir, relative))
os.path.exists = lambda target: True
os.path.isdir = lambda target: target == "sources"
os.access = lambda target, mode: True
os.lstat = lambda target: SimpleNamespace(st_mode=stat.S_IFDIR if target == "sources" else stat.S_IFREG, st_size=1)
def synthetic_children(target):
    for index in range(p["fixture_files"]):
        if p["maximum_enumerated"] and index >= p["maximum_enumerated"]:
            raise RuntimeError("fixture enumerated beyond the first over-cap child")
        name = str(index).zfill(6) + "a" * (p["fixture_width"] - 6)
        yield SimpleNamespace(name=name, path="sources/"+name, stat=lambda follow_symlinks: SimpleNamespace(st_mode=stat.S_IFREG, st_size=1))
os.scandir = lambda target: nullcontext(synthetic_children(target))
evidence_manifest(required=True)
print("accepted")
`
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			out, err := exec.CommandContext(ctx, "python3", "-c", script, string(payload)).CombinedOutput()
			if tt.accepted {
				require.NoError(t, err, "manifest should support the finite documented maximum visited entries: %s", out)
				require.Equal(t, "accepted\n", string(out), "inventory at the entry bound should complete successfully")
			} else {
				require.Error(t, err, "manifest must reject inventories above entry or path-byte capacity")
				require.Contains(t, string(out), "declared review evidence exceeds bounded source manifest capacity", "oversized inventories must fail through the explicit platform capacity check")
			}
		})
	}
}

func TestReviewEvidenceManifestPreservesPathsTypesAndLinkTargets(t *testing.T) {
	t.Parallel()
	root := ".143/cache/source"
	config := sourceReviewConfig(t,
		[]string{"mkdir -p .143/cache/source/sdk; printf abc > .143/cache/source/direct.go; printf x > .143/cache/source/sdk/types.d.ts; ln -s direct.go .143/cache/source/link.go"},
		[]string{"test -f .143/cache/source/sdk/types.d.ts"},
		[]repoconfig.ReviewEvidenceEntry{{Path: root, Description: "Stable source and type inventory", Category: models.CodeReviewEvidenceGoDependencies}})
	f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
	require.NoError(t, f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "source inventory should record regular files and safe symlinks")
	raw, err := os.ReadFile(filepath.Join(f.workDir, ".git/143-review-evidence.json"))
	require.NoError(t, err, "prepared inventory receipt should be readable")
	var receipt struct {
		Evidence []struct {
			Path  string  `json:"path"`
			Files [][]any `json:"files"`
		} `json:"evidence"`
	}
	require.NoError(t, json.Unmarshal(raw, &receipt), "prepared source inventory should decode")
	expected := []struct {
		Path  string  `json:"path"`
		Files [][]any `json:"files"`
	}{{Path: root, Files: [][]any{
		{root, "directory"},
		{root + "/direct.go", "file", float64(3)},
		{root + "/link.go", "symlink", "direct.go"},
		{root + "/link.go", "resolved_file", float64(3)},
		{root + "/sdk", "directory"},
		{root + "/sdk/types.d.ts", "file", float64(1)},
	}}}
	require.Equal(t, expected, receipt.Evidence, "optimized directory traversal must preserve exact source paths, types, sizes, deterministic order, and safe link targets")
}

func TestReviewVerifierSourceChangesCannotReuseReceipt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mutation string
		unsafe         bool
	}{
		{name: "deleted source", mutation: "rm .143/cache/go/api.go"},
		{name: "changed source size", mutation: "printf modified > .143/cache/go/api.go"},
		{name: "added source", mutation: "printf added > .143/cache/go/new.go"},
		{name: "changed source type", mutation: "rm .143/cache/go/api.go; mkdir .143/cache/go/api.go"},
		{name: "changed source symlink", mutation: "rm .143/cache/go/api.go; ln -s other.go .143/cache/go/api.go"},
		{name: "introduced unsafe link", unsafe: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mutation := tt.mutation
			if tt.unsafe {
				mutation = "ln -s '" + shellEscapeSingleQuote(t.TempDir()) + "' .143/cache/go/escape"
			}
			verifier := "if test -f permit; then " + mutation + "; rm permit; else printf verified > permit; fi"
			config := sourceReviewConfig(t,
				[]string{"rm -rf .143/cache/go; mkdir -p .143/cache/go; printf abc > .143/cache/go/api.go; printf abc > .143/cache/go/other.go; printf '.\\n' >> executions.log"},
				[]string{verifier},
				[]repoconfig.ReviewEvidenceEntry{{Path: ".143/cache/go", Description: "Pinned source inventory", Category: models.CodeReviewEvidenceGoDependencies}})
			f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
			o := f.orchestrator()
			ctx := withCodeReviewPreparationBudget(context.Background())
			require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "fixture should publish the original complete source receipt")
			err := o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop())
			expectedRuns := ".\n.\n"
			if tt.unsafe {
				require.Error(t, err, "a successful verifier that exposes an unsafe source graph must fail before repair")
				expectedRuns = ".\n"
			} else {
				require.NoError(t, err, "changed source inventory must undergo bounded preparation instead of reusing an old receipt")
			}
			runs, readErr := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
			require.NoError(t, readErr, "source preparation history should remain readable")
			require.Equal(t, expectedRuns, string(runs), "post-verifier source additions, deletions, type, size, or symlink changes must invalidate reuse; unsafe changes must fail before setup")
		})
	}
}

func TestReviewEvidenceSafetyRejectsSpecialObjectsBeforeRecipe(t *testing.T) {
	t.Parallel()
	config := sourceReviewConfig(t, []string{"printf executed > permit"}, []string{"true"}, []repoconfig.ReviewEvidenceEntry{{Path: ".143/cache/go", Description: "Go source", Category: models.CodeReviewEvidenceGoDependencies}})
	f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
	root := filepath.Join(f.workDir, ".143/cache/go")
	require.NoError(t, os.MkdirAll(root, 0o700), "existing source root should be present")
	require.NoError(t, os.WriteFile(filepath.Join(root, "api.go"), []byte("source"), 0o600), "existing source graph should contain readable regular files")
	out, err := exec.Command("mkfifo", filepath.Join(root, "unsafe")).CombinedOutput()
	require.NoError(t, err, "fixture should contain a special filesystem object: %s", out)
	require.Error(t, f.orchestrator().ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "cheap pre-command safety traversal must reject special objects alongside otherwise complete sources")
	_, err = os.Stat(filepath.Join(f.workDir, "permit"))
	require.ErrorIs(t, err, os.ErrNotExist, "unsafe source graphs must not execute repository preparation commands")
}
