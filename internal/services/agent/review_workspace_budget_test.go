package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// This transport returns as soon as its attachment is cancelled while the
// command itself continues, matching Docker's independent exec lifecycle.
type disconnectedReviewEvidenceProvider struct{ localReviewEvidenceProvider }

func (p disconnectedReviewEvidenceProvider) ExecForReviewPreparation(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	return p.ExecWithStdin(ctx, sandbox, command, nil, stdout, stderr)
}

func (p disconnectedReviewEvidenceProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if stdin != nil && p.rewriteTrustedGit != "" {
		input, err := io.ReadAll(stdin)
		if err != nil {
			return -1, err
		}
		stdin = bytes.NewReader(bytes.ReplaceAll(input, []byte("/usr/bin/git"), []byte(p.rewriteTrustedGit)))
	}
	type result struct {
		code            int
		err             error
		out, diagnostic bytes.Buffer
	}
	completed := make(chan result, 1)
	go func() {
		r := result{}
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = sandbox.WorkDir
		cmd.Stdin = stdin
		cmd.Env = os.Environ()
		for name, value := range sandbox.Env {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
		cmd.Stdout, cmd.Stderr = &r.out, &r.diagnostic
		r.err = cmd.Run()
		var exit *exec.ExitError
		if errors.As(r.err, &exit) {
			r.code, r.err = exit.ExitCode(), nil
		}
		completed <- r
	}()
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case r := <-completed:
		if _, err := io.Copy(stdout, &r.out); err != nil {
			return -1, err
		}
		if _, err := io.Copy(stderr, &r.diagnostic); err != nil {
			return -1, err
		}
		return r.code, r.err
	}
}

func TestReviewPreparationUsesOneAbsoluteBudget(t *testing.T) {
	t.Parallel()
	started := time.Now().Add(-3 * time.Minute)
	ctx := withCodeReviewPreparationBudget(jobctx.WithJobCreatedAt(context.Background(), started))
	first, cancelFirst := codeReviewPreparationContext(ctx)
	defer cancelFirst()
	second, cancelSecond := codeReviewPreparationContext(withCodeReviewPreparationBudget(ctx))
	defer cancelSecond()
	firstDeadline, _ := first.Deadline()
	secondDeadline, _ := second.Deadline()
	require.Equal(t, started.Add(CodeReviewWorkspacePreparationTimeout), firstDeadline, "preparation must include the durable job's elapsed queue and retry time")
	require.Equal(t, firstDeadline, secondDeadline, "later preparation stages must retain the first absolute deadline")
	require.NoError(t, ctx.Err(), "preparation deadline marker must not cancel the later reviewer execution")
}

func TestReviewPreparationCombinedStagesExhaustBudget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o700), "bounded command markers should have an existing directory")
	o := &Orchestrator{provider: disconnectedReviewEvidenceProvider{}, logger: zerolog.Nop()}
	sandbox := &Sandbox{WorkDir: dir}
	ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(400*time.Millisecond))
	require.NoError(t, o.runBoundedReviewCommand(ctx, sandbox, "sleep 0.2", ""), "first stage should consume part of the total budget")
	err := o.runBoundedReviewCommand(ctx, sandbox, "sleep 2; printf escaped > late-stage", "")
	require.ErrorIs(t, err, context.DeadlineExceeded, "second stage must use the remaining budget instead of starting a fresh timeout")
	require.ErrorIs(t, o.runBoundedReviewCommand(ctx, sandbox, "printf started > forbidden-stage", ""), context.DeadlineExceeded, "an exhausted budget must reject new stages before command execution")
	time.Sleep(250 * time.Millisecond)
	_, err = os.Stat(filepath.Join(dir, "late-stage"))
	require.ErrorIs(t, err, os.ErrNotExist, "cancelled attachment must still terminate the underlying second-stage process")
	_, err = os.Stat(filepath.Join(dir, "forbidden-stage"))
	require.ErrorIs(t, err, os.ErrNotExist, "deadline exhaustion must not start a new command")
}

func TestReviewPreparationExpiredBudgetVerifiesOnlyHealthyEvidence(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixtureWithConfig(t, `{"review":{"commands":["mkdir -p node_modules/pkg; printf types > node_modules/pkg/index.d.ts; printf '.\\n' >> executions.log"],"verify_commands":["test -f node_modules/pkg/index.d.ts"],"evidence":[{"path":"node_modules/pkg","description":"Dependency type declarations","category":"javascript_dependencies"}]}}`, false)
	o := f.orchestrator()
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "fixture should prepare complete source and dependency evidence")
	ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Second))
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "complete cached evidence should permit later synthesis after the initial preparation deadline")
	require.NoError(t, os.Remove(filepath.Join(f.workDir, "node_modules/pkg/index.d.ts")), "fixture should remove dependency evidence after preparation")
	err := o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop())
	require.ErrorIs(t, err, context.DeadlineExceeded, "expired preparation may verify evidence but must not repair missing outputs")
	runs, err := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
	require.NoError(t, err, "bootstrap evidence should remain readable")
	require.Equal(t, ".\n", string(runs), "expired healthy verification and failed repair must not rerun bootstrap")
}

func TestReviewCloneIsBoundedBeforeGitDirectoryExists(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700), "fixture Git executable should have its own directory")
	script := "#!/bin/sh\nprintf entered > '" + shellEscapeSingleQuote(filepath.Join(root, "clone-started")) + "'\npython3 -c \"import signal,time,pathlib; signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(1); pathlib.Path('" + filepath.Join(root, "escaped-clone") + "').write_text('escaped')\" & wait\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700), "fixture clone should simulate a Git descendant surviving attachment cancellation")
	o := &Orchestrator{provider: disconnectedReviewEvidenceProvider{localReviewEvidenceProvider{rewriteTrustedGit: filepath.Join(bin, "git")}}, logger: zerolog.Nop()}
	sandbox := &Sandbox{WorkDir: filepath.Join(root, "checkout")}
	ctx, cancel := context.WithCancel(WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(5*time.Second)))
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		completed <- o.cloneRepositoryForReview(ctx, sandbox, "https://github.com/example/repo.git", "main", "secret-token")
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(root, "clone-started")); return err == nil }, 3*time.Second, 10*time.Millisecond, "clone cancellation should exercise an active Git subprocess before closing its output attachment")
	cancel()
	err := <-completed
	require.ErrorIs(t, err, context.Canceled, "initial clone should stop when the shared preparation context is cancelled")
	var preparationErr *CodeReviewWorkspacePreparationError
	require.ErrorAs(t, err, &preparationErr, "clone cancellation should expose its safe preparation stage")
	require.Equal(t, CodeReviewPreparationStageClone, preparationErr.Stage, "author-facing failure should identify repository cloning")
	time.Sleep(1100 * time.Millisecond)
	_, err = os.Stat(filepath.Join(root, "escaped-clone"))
	require.ErrorIs(t, err, os.ErrNotExist, "cancelled clone must kill descendants despite closing output before process completion")
	markers, err := filepath.Glob(filepath.Join(root, "143-review-cancel-*"))
	require.NoError(t, err, "clone cancellation markers should be inspectable")
	require.Empty(t, markers, "clone cleanup must work before the destination .git directory exists")
}

func TestReviewCloneKeepsBareOrigin(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	destination := filepath.Join(t.TempDir(), "fresh checkout")
	o := &Orchestrator{provider: disconnectedReviewEvidenceProvider{}, logger: zerolog.Nop()}
	require.NoError(t, o.cloneRepositoryForReview(context.Background(), &Sandbox{WorkDir: destination}, f.remote, "review", "short-lived-token"), "bounded clone should support quoted destinations and partial transport")
	out, err := exec.Command("git", "-C", destination, "remote", "get-url", "origin").CombinedOutput()
	require.NoError(t, err, "bounded clone should preserve origin configuration")
	require.Equal(t, f.remote, strings.TrimSpace(string(out)), "transient authenticated URL rewrite must leave only the bare URL in origin")
}

func TestReviewCloneDoesNotPersistPrivateAuthentication(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	root := t.TempDir()
	origin := f.remote
	destination := filepath.Join(root, "fresh checkout")
	o := &Orchestrator{provider: disconnectedReviewEvidenceProvider{}, logger: zerolog.Nop()}
	require.NoError(t, o.cloneRepositoryForReview(context.Background(), &Sandbox{WorkDir: destination}, origin, "review", "temporary"), "private clone authentication should preserve the durable origin identity")
	config, err := os.ReadFile(filepath.Join(destination, ".git", "config"))
	require.NoError(t, err, "successful clone should write a readable Git configuration")
	require.Contains(t, string(config), origin, "clone should persist only its original bare URL")
	require.NotContains(t, string(config), "temporary", "clone must not persist private credentials")
	require.Equal(t, "/usr/bin/git -c core.hooksPath=/dev/null -c credential.helper= -c maintenance.auto=false -c gc.auto=0 clone --template=/dev/null --no-checkout --filter=blob:none --branch 'main' 'https://example.com/repo.git' '/checkout'", reviewCloneScript("https://example.com/repo.git", "main", "/checkout"), "clone arguments must remain credential-free and skip unauthenticated repository code")
}

func TestReviewPreparationErrorHidesProviderDiagnostics(t *testing.T) {
	t.Parallel()
	cause := errors.New("git https://x-access-token:secret@example.com/repo.git failed")
	err := NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageHydration, cause)
	require.Equal(t, "Review workspace preparation failed during pinned source hydration. Review did not run.", err.Error(), "author-facing setup failure should expose a concrete safe stage")
	require.ErrorIs(t, err, cause, "internal callers should retain original cause classification")
}

type reviewBudgetSnapshotStore struct{}

func (reviewBudgetSnapshotStore) Save(context.Context, string, io.Reader) error { return nil }
func (reviewBudgetSnapshotStore) Delete(context.Context, string) error          { return nil }
func (reviewBudgetSnapshotStore) Load(_ context.Context, _ string, writer io.Writer) error {
	_, err := io.WriteString(writer, "snapshot bytes")
	return err
}

type reviewBudgetSnapshotProvider struct {
	SandboxProvider
	deadline                     time.Time
	created, restored, destroyed int
}

func (p *reviewBudgetSnapshotProvider) Create(ctx context.Context, _ SandboxConfig) (*Sandbox, error) {
	p.created++
	p.deadline, _ = ctx.Deadline()
	return &Sandbox{ID: "partial"}, nil
}
func (p *reviewBudgetSnapshotProvider) Restore(context.Context, *Sandbox, io.Reader) error {
	return errors.New("ordinary restore must not run for review preparation")
}
func (p *reviewBudgetSnapshotProvider) RestoreForReviewPreparation(ctx context.Context, _ *Sandbox, reader io.Reader) error {
	p.restored++
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
func (p *reviewBudgetSnapshotProvider) Destroy(context.Context, *Sandbox) error {
	p.destroyed++
	return nil
}

func TestReviewSnapshotRestorationRetainsBudgetAndDestroysPartialSandbox(t *testing.T) {
	t.Parallel()
	deadline := time.Now().Add(100 * time.Millisecond)
	ctx, cancel := codeReviewPreparationContext(WithCodeReviewWorkspacePreparationDeadline(context.Background(), deadline))
	defer cancel()
	provider := &reviewBudgetSnapshotProvider{}
	_, err := HydrateSandboxFromSnapshot(ctx, sandboxProviderForPreparation(ctx, provider), reviewBudgetSnapshotStore{}, "checkpoint", SandboxConfig{})
	require.ErrorIs(t, err, context.DeadlineExceeded, "snapshot cancellation must return the shared preparation deadline failure")
	require.Equal(t, deadline, provider.deadline, "snapshot creation and restoration must share the original remaining budget")
	require.Equal(t, 1, provider.created, "an expired snapshot attempt must not reset the budget and create another sandbox")
	require.Equal(t, 1, provider.restored, "snapshot preparation must use the cancellation-aware restore transport")
	require.Equal(t, 1, provider.destroyed, "failed snapshot preparation must destroy its partial sandbox")
}

func TestReviewPreparationMovedHeadSignal(t *testing.T) {
	t.Parallel()
	cause := fmt.Errorf("%w: expected private-old, got private-new", ErrStalePullRequestHead)
	err := NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageCheckout, cause)
	require.Equal(t, "Review workspace preparation observed a moved pull request head. Review did not run.", err.Error(), "durable moved-head signal must contain no raw ref or provider diagnostics")
	require.ErrorIs(t, err, ErrStalePullRequestHead, "controller must distinguish supersession from setup failure")
}
