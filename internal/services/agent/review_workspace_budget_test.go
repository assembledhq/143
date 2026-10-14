package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/reviewpreparation"
	"github.com/google/uuid"
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
	// Each host fixture models a separate container namespace, including quoted paths.
	command = strings.ReplaceAll(command, reviewWorkspaceCleanupQuarantinePath, shellEscapeSingleQuote(filepath.Join(sandbox.WorkDir, ".143-test-cleanup-quarantine")))
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
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		ctx := withCodeReviewPreparationBudget(jobctx.WithJobCreatedAt(context.Background(), started.Add(-time.Hour)))
		first, cancelFirst := codeReviewPreparationContext(ctx)
		defer cancelFirst()
		time.Sleep(3 * time.Minute)
		second, cancelSecond := codeReviewPreparationContext(withCodeReviewPreparationBudget(ctx))
		defer cancelSecond()
		firstDeadline, _ := first.Deadline()
		secondDeadline, _ := second.Deadline()
		require.Equal(t, started.Add(CodeReviewWorkspacePreparationTimeout), firstDeadline, "preparation must begin at actual setup rather than durable job creation")
		require.Equal(t, firstDeadline, secondDeadline, "later preparation stages must retain the first absolute deadline")
		require.Equal(t, 5*time.Minute, time.Until(secondDeadline), "setup elapsed time must consume the original total budget")
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		require.ErrorIs(t, first.Err(), context.DeadlineExceeded, "the original preparation must end after eight minutes")
		require.ErrorIs(t, second.Err(), context.DeadlineExceeded, "later stages must stop at the same absolute deadline")
		require.NoError(t, ctx.Err(), "preparation deadline marker must not cancel later reviewer execution")
	})
}

func TestReviewPreparationBudgetPreservesEarlierDeadlines(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		elapsed time.Duration
		caller  bool
	}{
		{name: "earlier preparation", elapsed: time.Minute},
		{name: "expired preparation", elapsed: -time.Minute},
		{name: "earlier caller", elapsed: time.Minute, caller: true},
		{name: "expired caller", elapsed: -time.Minute, caller: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				deadline := time.Now().Add(test.elapsed)
				ctx := jobctx.WithJobCreatedAt(context.Background(), time.Now().Add(-time.Hour))
				if test.caller {
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, deadline)
					defer cancel()
				} else {
					ctx = WithCodeReviewWorkspacePreparationDeadline(ctx, deadline)
				}
				prepared, cancel := codeReviewPreparationContext(withCodeReviewPreparationBudget(ctx))
				defer cancel()
				actual, _ := prepared.Deadline()
				require.Equal(t, deadline, actual, "fresh setup must preserve an explicitly supplied earlier deadline")
				if test.elapsed < 0 {
					require.ErrorIs(t, prepared.Err(), context.DeadlineExceeded, "expired explicit budgets must never authorize renewed preparation")
				} else {
					require.NoError(t, prepared.Err(), "a live earlier deadline must permit preparation only for its remaining time")
				}
			})
		})
	}
}

type reviewSetupBudgetProvider struct {
	reviewWorkspaceProvider
	deadline time.Time
	prepared int
}

func (p *reviewSetupBudgetProvider) Create(ctx context.Context, cfg SandboxConfig) (*Sandbox, error) {
	bounded, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	p.deadline, _ = bounded.Deadline()
	time.Sleep(time.Minute)
	return p.reviewWorkspaceProvider.Create(ctx, cfg)
}

func (p *reviewSetupBudgetProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	input, err := io.ReadAll(stdin)
	if err != nil {
		return -1, err
	}
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return -1, err
	}
	command += " " + string(envelope.Payload)
	if strings.Contains(command, "clone --template=/dev/null --no-checkout") {
		p.cloneCount++
		time.Sleep(2 * time.Minute)
	}
	return p.Exec(ctx, sandbox, command, stdout, stderr)
}

func (p *reviewSetupBudgetProvider) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	if strings.Contains(command, "identity = json.loads(sys.argv[1])") {
		p.prepared++
		deadline, _ := ctx.Deadline()
		if deadline != p.deadline || time.Until(deadline) != 5*time.Minute {
			return -1, errors.New("input preparation renewed the setup budget")
		}
	}
	return p.reviewWorkspaceProvider.Exec(ctx, sandbox, command, stdout, stderr)
}

func TestStandaloneActivatedReviewWorkspaceUsesAdmittedPreparationDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		head := strings.Repeat("a", 40)
		repository, organization := uuid.New(), uuid.New()
		policy, err := reviewpreparation.NewPolicy(true, []string{"test/repo"})
		require.NoError(t, err, "the admitted preparation fixture must explicitly activate its repository")
		provider := &reviewSetupBudgetProvider{reviewWorkspaceProvider: reviewWorkspaceProvider{actualHead: head}}
		orchestrator := &Orchestrator{
			provider:                provider,
			reviewPreparationPolicy: policy,
			sandboxCapacity: NewSandboxCapacityGate(SandboxCapacityGateConfig{
				Counter: reviewWorkspaceCapacityCounter{}, MaxActive: 2, Logger: zerolog.Nop(),
			}),
			env: NewAgentEnv(AgentEnvDeps{}),
			repositories: testInternalRepoStore{repo: models.Repository{
				ID: repository, OrgID: organization, FullName: "test/repo", CloneURL: "https://github.com/test/repo.git", DefaultBranch: "main",
			}},
			github: testInternalGitHubTokens{token: "synthetic-token"},
			logger: zerolog.Nop(),
		}
		branch := "143/review/admitted-budget"
		session := &models.Session{
			ID: uuid.New(), OrgID: organization, WorkingBranch: &branch, Origin: models.SessionOriginCodeReview,
			AgentType: models.AgentTypeCodex, RepositoryID: &repository,
			RevisionContext: []byte(`{"kind":"code_review","github_repo":"test/repo","base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","github_pr_number":42,"head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		}
		ctx := jobctx.WithJobCreatedAt(context.Background(), started.Add(-time.Hour))
		// The initial preparation handler supplies the durable admitted checkpoint,
		// which takes precedence over the intake job's earlier capacity wait.
		ctx = WithCodeReviewWorkspacePreparationDeadline(ctx, started.Add(CodeReviewWorkspacePreparationTimeout))
		sandbox, err := orchestrator.PrepareCodeReviewWorkspace(ctx, session, head)
		require.NoError(t, err, "activated cold preparation must retain the handler checkpoint after an earlier capacity wait")
		require.Equal(t, "prepared-container", sandbox.ID, "successful preparation must transfer its owned container")
		require.Equal(t, started.Add(CodeReviewWorkspacePreparationTimeout), provider.deadline, "the handler checkpoint must bound setup before container creation")
		require.Equal(t, 1, provider.prepared, "input generation must consume the same budget after create and clone")
	})
}

type reviewBudgetStageObservation struct {
	contextDeadline time.Time
	payloadDeadline float64
	script          string
}

type coordinatedReviewBudgetProvider struct {
	SandboxProvider
	mu     sync.Mutex
	stages []reviewBudgetStageObservation
}

func (p *coordinatedReviewBudgetProvider) ExecForReviewPreparation(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error) {
	// Deadline propagation is verified separately from real process cleanup.
	// Cleanup transport must not advance the virtual preparation clock.
	return 0, nil
}

func (p *coordinatedReviewBudgetProvider) ExecWithStdin(ctx context.Context, _ *Sandbox, _ string, stdin io.Reader, _, _ io.Writer) (int, error) {
	var message struct {
		Payload reviewWorkspacePreparationPayload `json:"payload"`
	}
	if err := json.NewDecoder(stdin).Decode(&message); err != nil {
		return -1, err
	}
	deadline, _ := ctx.Deadline()
	p.mu.Lock()
	p.stages = append(p.stages, reviewBudgetStageObservation{deadline, message.Payload.DeadlineUnix, message.Payload.Prepare})
	stage := len(p.stages)
	p.mu.Unlock()
	if stage == 1 {
		time.Sleep(time.Minute)
		return 0, nil
	}
	if stage == 2 {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	return -1, errors.New("exhausted preparation budget unexpectedly started another stage")
}

func (p *coordinatedReviewBudgetProvider) observations() []reviewBudgetStageObservation {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]reviewBudgetStageObservation(nil), p.stages...)
}

func TestReviewPreparationCombinedStagesExhaustBudget(t *testing.T) {
	t.Parallel()
	// synctest forbids t.Parallel inside its bubble; the outer test is parallel.
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		deadline := started.Add(2 * time.Minute)
		provider := &coordinatedReviewBudgetProvider{}
		o := &Orchestrator{provider: provider, logger: zerolog.Nop()}
		sandbox := &Sandbox{WorkDir: "/budget-fixture"}
		ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), deadline)

		require.NoError(t, o.runBoundedReviewCommand(ctx, sandbox, "printf first-stage", ""), "coordinated first stage should consume only part of the original preparation budget")
		require.Equal(t, time.Minute, time.Since(started), "first stage should finish after consuming half of the shared virtual budget")
		err := o.runBoundedReviewCommand(ctx, sandbox, "printf second-stage", "")
		require.ErrorIs(t, err, context.DeadlineExceeded, "second stage must expire at the original deadline rather than renew its preparation budget")
		require.Equal(t, 2*time.Minute, time.Since(started), "combined stages must stop when the original total budget is exhausted")
		require.ErrorIs(t, o.runBoundedReviewCommand(ctx, sandbox, "printf forbidden-stage", ""), context.DeadlineExceeded, "exhausted preparation budget must reject a third stage before transport execution")
		unixDeadline := float64(deadline.UnixNano()) / float64(time.Second)
		require.Equal(t, []reviewBudgetStageObservation{
			{contextDeadline: deadline, payloadDeadline: unixDeadline, script: "set -eu\nprintf first-stage"},
			{contextDeadline: deadline, payloadDeadline: unixDeadline, script: "set -eu\nprintf second-stage"},
		}, provider.observations(), "both stages must receive the same original context and serialized deadline, and no third stage may execute")
	})
}

func TestReviewPreparationCancellationTerminatesDetachedStage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o700), "bounded command markers should have an existing directory")
	o := &Orchestrator{provider: disconnectedReviewEvidenceProvider{}, logger: zerolog.Nop()}
	sandbox := &Sandbox{WorkDir: dir}
	ctx, cancel := context.WithCancel(WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(15*time.Second)))
	defer cancel()
	// Exec replaces the stage shell with this observable real child. Waiting
	// for its PID avoids relying on process startup or a late-write timer.
	child := "import os,pathlib,signal; pathlib.Path('stage.pid').write_text(str(os.getpid())); signal.pause(); pathlib.Path('escaped-stage').write_text('escaped')"
	command := "/usr/bin/python3 -I -S -c '" + shellEscapeSingleQuote(child) + "'"
	completed := make(chan error, 1)
	go func() { completed <- o.runBoundedReviewCommand(ctx, sandbox, "exec "+command, "") }()
	pidPath := filepath.Join(dir, "stage.pid")
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidPath)
		if err != nil {
			return false
		}
		_, err = strconv.Atoi(string(data))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "cancellation proof must observe a running real stage before disconnecting its attachment")
	data, err := os.ReadFile(pidPath)
	require.NoError(t, err, "observed stage should publish a readable PID")
	pid, err := strconv.Atoi(string(data))
	require.NoError(t, err, "observed stage should publish its actual process ID")
	needsCleanup := true
	t.Cleanup(func() {
		if !needsCleanup {
			return
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("could not terminate the owned stage fixture during cleanup: %v", err)
		}
	})
	require.NoError(t, syscall.Kill(pid, 0), "the observed real stage must still be alive before attachment cancellation")
	cancel()
	select {
	case err := <-completed:
		require.ErrorIs(t, err, context.Canceled, "disconnected output attachment must preserve caller cancellation")
	case <-time.After(10 * time.Second):
		t.Fatal("preparation must return after cancelling its observed real stage")
	}
	terminationErr := syscall.Kill(pid, 0)
	needsCleanup = !errors.Is(terminationErr, syscall.ESRCH)
	require.ErrorIs(t, terminationErr, syscall.ESRCH, "the observed real stage must be terminated and reaped before cancelled preparation returns")
	_, err = os.Stat(filepath.Join(dir, "escaped-stage"))
	require.ErrorIs(t, err, os.ErrNotExist, "cancelled detached stage must never continue to its next instruction")
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
	require.Equal(t, "/usr/bin/git -c advice.graftFileDeprecated=false -c core.hooksPath=/dev/null -c credential.helper= -c maintenance.auto=false -c gc.auto=0 clone --template=/dev/null --no-checkout --filter=blob:none --branch 'main' 'https://example.com/repo.git' '/checkout'", reviewCloneScript("https://example.com/repo.git", "main", "/checkout"), "clone arguments must remain credential-free and skip unauthenticated repository code")
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

func TestReviewPreparationCleanupErrorPreservesSafeCausePriority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause error
	}{
		{name: "deadline", cause: context.DeadlineExceeded},
		{name: "cancellation", cause: context.Canceled},
		{name: "transport diagnostics", cause: errors.New("git https://x-access-token:secret@example.com/repo.git failed")},
		{name: "moved ref", cause: ErrStalePullRequestHead},
		{name: "source integrity", cause: ErrCodeReviewSourceIntegrity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageHydration, errors.Join(ErrCodeReviewPreparationCleanup, tt.cause))
			require.Equal(t, "Review workspace preparation could not confirm process cleanup. Recreate the review workspace and retry. Review did not run.", err.Error(), "cleanup uncertainty must require workspace recreation without exposing other diagnostics")
			require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "cleanup uncertainty must retain its terminal identity")
			require.ErrorIs(t, err, tt.cause, "internal callers must retain the joined original cause")
		})
	}
}

func TestReviewPreparationCleanupNormalizesOuterTypedCauses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		innerCause error
		outerCause error
		wrapped    bool
	}{
		{name: "typed hydration joined with cleanup and deadline", innerCause: io.ErrUnexpectedEOF, outerCause: errors.Join(ErrCodeReviewPreparationCleanup, context.DeadlineExceeded)},
		{name: "typed hydration deadline joined with cleanup and EOF", innerCause: context.DeadlineExceeded, outerCause: errors.Join(ErrCodeReviewPreparationCleanup, io.ErrUnexpectedEOF)},
		{name: "typed cleanup joined with transport EOF", innerCause: ErrCodeReviewPreparationCleanup, outerCause: io.ErrUnexpectedEOF},
		{name: "typed cleanup wrapped in private diagnostics", innerCause: ErrCodeReviewPreparationCleanup, wrapped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inner := NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageHydration, tt.innerCause)
			var cause error
			if tt.wrapped {
				cause = fmt.Errorf("provider token=synthetic-secret: %w", inner)
			} else {
				cause = errors.Join(inner, tt.outerCause)
			}
			actual := NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageReadiness, cause)
			var typed *CodeReviewWorkspacePreparationError
			require.ErrorAs(t, actual, &typed, "outer cleanup uncertainty must normalize to a typed preparation failure")
			require.Equal(t, CodeReviewPreparationStageHydration, typed.Stage, "normalization must preserve the original concrete preparation stage")
			require.Equal(t, "Review workspace preparation could not confirm process cleanup. Recreate the review workspace and retry. Review did not run.", actual.Error(), "outer cleanup must render only the finite safe recreation message")
			require.ErrorIs(t, actual, ErrCodeReviewPreparationCleanup, "the normalized error must expose cleanup uncertainty")
			require.ErrorIs(t, actual, cause, "normalization must preserve the complete original error tree")
			require.ErrorIs(t, actual, inner, "original typed stage error must remain available for internal diagnosis")
			require.ErrorIs(t, actual, tt.innerCause, "original typed cause must survive normalization")
			if tt.outerCause != nil {
				require.ErrorIs(t, actual, tt.outerCause, "all causes joined outside the typed error must survive normalization")
			}
		})
	}
}

func TestReviewPreparationErrorPreservesExistingIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cleanup bool
		wrapped bool
	}{
		{name: "ordinary typed error"},
		{name: "ordinary wrapped typed error", wrapped: true},
		{name: "direct typed cleanup error", cleanup: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cause := error(io.ErrUnexpectedEOF)
			if tt.cleanup {
				cause = ErrCodeReviewPreparationCleanup
			}
			existing := NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageHydration, cause)
			if tt.wrapped {
				existing = fmt.Errorf("preserved wrapper: %w", existing)
			}
			require.Same(t, existing, NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageReadiness, existing), "ordinary existing errors and direct safe cleanup errors must remain idempotent")
		})
	}
}

func TestReviewPreparationBudgetSeparatesAdmissionFromActivatedExecution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		active    bool
		remaining time.Duration
	}{
		{name: "inactive shared input setup starts after admission", remaining: CodeReviewWorkspacePreparationTimeout},
		{name: "activated execution retains durable attempt age", active: true, remaining: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				ctx := withReviewPreparationActivation(jobctx.WithJobCreatedAt(context.Background(), started.Add(-3*time.Minute)), tt.active)
				ctx = withCodeReviewPreparationBudget(ctx)
				first, cancelFirst := codeReviewPreparationContext(ctx)
				defer cancelFirst()
				deadline, ok := first.Deadline()
				require.True(t, ok, "both setup modes must carry an absolute preparation boundary")
				require.Equal(t, started.Add(tt.remaining), deadline, "only activated recipe execution must consume durable attempt age")
				time.Sleep(time.Minute)
				second, cancelSecond := codeReviewPreparationContext(ctx)
				defer cancelSecond()
				actual, ok := second.Deadline()
				require.True(t, ok, "later stages must retain a concrete preparation boundary")
				require.Equal(t, deadline, actual, "later stage entry must not renew either preparation mode")
				require.Equal(t, tt.remaining-time.Minute, time.Until(actual), "setup time must consume the same absolute boundary")
			})
		})
	}
}

func TestWarmReviewSetupWindowStartsLazilyAndNeverRenews(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		parent := context.Background()
		originalDeadline := time.Now().Add(time.Minute)
		preparation, cancel := context.WithDeadline(WithCodeReviewWorkspacePreparationDeadline(parent, originalDeadline), originalDeadline)
		defer cancel()
		window := codeReviewSetupWindow{parent: parent, preparation: preparation, reusable: true}
		defer window.Close()
		require.Same(t, preparation, window.Context(), "a live setup must retain its original preparation context")
		time.Sleep(time.Minute)
		synctest.Wait()
		first := window.Context()
		deadline, ok := first.Deadline()
		require.True(t, ok, "a reusable expired setup must receive a bounded verification context")
		require.Equal(t, originalDeadline.Add(CodeReviewWorkspaceVerificationTimeout), deadline, "the existing one-minute allowance must begin only at its first post-expiry check")
		time.Sleep(40 * time.Second)
		require.Same(t, first, window.Context(), "a later verification stage must consume the same allowance")
		require.Equal(t, 20*time.Second, time.Until(deadline), "the second verification must receive only the unused allowance")
		time.Sleep(20 * time.Second)
		synctest.Wait()
		require.ErrorIs(t, window.Context().Err(), context.DeadlineExceeded, "an exhausted verification window must never renew")
		window.Close()
		require.Same(t, first, window.Context(), "closing a consumed window must not permit a replacement")
	})
}

func TestWarmReviewSetupWindowPreservesCallerAuthority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		reusable      bool
		cancelCaller  bool
		outerDeadline bool
	}{
		{name: "cold setup cannot extend preparation"},
		{name: "cancelled caller cannot open verification", reusable: true, cancelCaller: true},
		{name: "earlier outer deadline bounds verification", reusable: true, outerDeadline: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				parent, cancelParent := context.WithCancel(context.Background())
				defer cancelParent()
				var cancelOuter context.CancelFunc
				if tt.outerDeadline {
					parent, cancelOuter = context.WithDeadline(parent, started.Add(80*time.Second))
					defer cancelOuter()
				}
				preparation, cancel := context.WithDeadline(parent, started.Add(time.Minute))
				defer cancel()
				window := codeReviewSetupWindow{parent: parent, preparation: preparation, reusable: tt.reusable}
				defer window.Close()
				time.Sleep(time.Minute)
				synctest.Wait()
				if tt.cancelCaller {
					cancelParent()
				}
				actual := window.Context()
				if !tt.reusable || tt.cancelCaller {
					require.Same(t, preparation, actual, "a cold or cancelled setup must retain the expired preparation context")
					require.ErrorIs(t, actual.Err(), context.DeadlineExceeded, "local verification must not bypass a closed setup boundary")
					return
				}
				deadline, ok := actual.Deadline()
				require.True(t, ok, "warm verification must remain deadline bounded")
				require.Equal(t, started.Add(80*time.Second), deadline, "an earlier outer deadline must cap the existing verification allowance")
				cancelParent()
				require.ErrorIs(t, actual.Err(), context.Canceled, "caller cancellation must immediately cancel an already-open verification window")
			})
		})
	}
}

type sharedWarmVerificationProvider struct {
	SandboxProvider
	stages          []reviewWorkspacePreparationPayload
	deadlines       []time.Time
	authentications []*reviewGitAuthentication
}

func (*sharedWarmVerificationProvider) ReadFile(context.Context, *Sandbox, string) ([]byte, error) {
	return nil, nil
}

func (*sharedWarmVerificationProvider) Exec(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error) {
	return 0, nil
}

func (p *sharedWarmVerificationProvider) ExecWithStdin(ctx context.Context, _ *Sandbox, _ string, input io.Reader, _, _ io.Writer) (int, error) {
	var message struct {
		Payload        reviewWorkspacePreparationPayload `json:"payload"`
		Authentication *reviewGitAuthentication          `json:"authentication,omitempty"`
	}
	if err := json.NewDecoder(input).Decode(&message); err != nil {
		return -1, err
	}
	deadline, _ := ctx.Deadline()
	p.stages = append(p.stages, message.Payload)
	p.deadlines = append(p.deadlines, deadline)
	p.authentications = append(p.authentications, message.Authentication)
	if len(p.stages) == 1 {
		time.Sleep(40 * time.Second)
		return 0, nil
	}
	<-ctx.Done()
	return -1, ctx.Err()
}

func TestWarmRecipeAndCapturedInputVerificationShareRemainingAllowance(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		originalDeadline := started.Add(time.Minute)
		parent := WithCodeReviewWorkspacePreparationDeadline(context.Background(), originalDeadline)
		preparation, cancel := context.WithDeadline(parent, originalDeadline)
		defer cancel()
		window := codeReviewSetupWindow{parent: parent, preparation: preparation, reusable: true}
		defer window.Close()
		require.Same(t, preparation, window.Context(), "setup begins with its original preparation boundary")
		time.Sleep(time.Minute)
		synctest.Wait()
		provider := &sharedWarmVerificationProvider{}
		tokens := &reviewInputsTokenSpy{}
		orchestrator := &Orchestrator{provider: provider, repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo", CloneURL: "https://github.com/example/repo.git"}}, github: tokens, logger: zerolog.Nop()}
		session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
		sandbox := &Sandbox{ID: "shared-warm-window", WorkDir: "/workspace"}
		require.NoError(t, orchestrator.ensureCodeReviewWorkspaceEvidence(window.Context(), session, sandbox, zerolog.Nop()), "a complete warm recipe receipt may use local verification after mid-setup expiry")
		err := orchestrator.ensureCodeReviewInputs(window.Context(), session, sandbox)
		synctest.Wait()
		require.ErrorIs(t, err, context.DeadlineExceeded, "captured inputs must exhaust only the remaining verification allowance")
		require.Equal(t, started.Add(2*time.Minute), time.Now(), "sequential verifiers may consume one existing minute after setup expiry")
		require.Equal(t, []time.Time{started.Add(2 * time.Minute), started.Add(2 * time.Minute)}, provider.deadlines, "recipe receipt and captured input checks must receive exactly one shared absolute deadline")
		require.Len(t, provider.stages, 2, "only the two local verification commands may execute")
		require.True(t, provider.stages[0].VerifyOnly, "expired recipe evidence may verify existing receipts without repair")
		require.True(t, provider.stages[1].Standalone, "the second command must be the actual shared captured-input verifier")
		require.Contains(t, provider.stages[1].Prepare, "'true' 'false' 'true'", "captured-input platform command must be verify-only with private-input support")
		for _, stage := range provider.stages {
			require.False(t, stage.AuthenticateGit, "expired verification must never request Git authentication")
			require.InDelta(t, float64(started.Add(2*time.Minute).UnixNano())/float64(time.Second), stage.DeadlineUnix, 0.001, "managed verification payloads must share the same deadline")
		}
		require.Equal(t, []*reviewGitAuthentication{nil, nil}, provider.authentications, "expired verification must never carry credentials")
		require.Zero(t, tokens.calls.Load(), "expired verification must never mint a GitHub installation token")
	})
}

func TestReviewPreparationIntegrityNormalizesOuterTypedCauses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		wrapped bool
	}{
		{name: "integrity joined outside typed transport failure"},
		{name: "typed integrity wrapped in private diagnostics", wrapped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			innerCause := error(io.ErrUnexpectedEOF)
			if tt.wrapped {
				innerCause = ErrCodeReviewSourceIntegrity
			}
			inner := NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageHydration, innerCause)
			cause := errors.Join(inner, ErrCodeReviewSourceIntegrity)
			if tt.wrapped {
				cause = fmt.Errorf("provider token=synthetic-secret: %w", inner)
			}
			actual := NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageReadiness, cause)
			var typed *CodeReviewWorkspacePreparationError
			require.ErrorAs(t, actual, &typed, "joined integrity must retain a typed finite preparation failure")
			require.Equal(t, CodeReviewPreparationStageHydration, typed.Stage, "normalization must retain the concrete failed stage")
			require.Equal(t, "Review workspace preparation could not verify pinned Git source object integrity. Recreate the review workspace and retry. Review did not run.", actual.Error(), "outer integrity must not expose private provider diagnostics")
			require.ErrorIs(t, actual, ErrCodeReviewSourceIntegrity, "the finite error must retain terminal source integrity")
			require.ErrorIs(t, actual, cause, "the full original tree must remain available for internal classification")
		})
	}
}
