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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type managedReviewInputsProvider struct {
	localReviewEvidenceProvider
	mu                 sync.Mutex
	preparations       []reviewWorkspacePreparationPayload
	authentications    []reviewGitAuthentication
	marker             string
	cleanupFailure     bool
	acknowledged       atomic.Bool
	clearedBeforeProof atomic.Bool
}

func (p *managedReviewInputsProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, input io.Reader, stdout, stderr io.Writer) (int, error) {
	encoded, err := io.ReadAll(input)
	if err != nil {
		return -1, err
	}
	var message struct {
		Payload        reviewWorkspacePreparationPayload `json:"payload"`
		Authentication reviewGitAuthentication           `json:"authentication"`
	}
	if err := json.Unmarshal(encoded, &message); err != nil {
		return -1, err
	}
	p.mu.Lock()
	p.preparations = append(p.preparations, message.Payload)
	p.authentications = append(p.authentications, message.Authentication)
	p.mu.Unlock()
	if strings.Contains(command, message.Authentication.Token) && message.Authentication.Token != "" {
		return -1, errors.New("private authentication appeared in managed command")
	}
	if sandbox.Env["GH143_REVIEW_FETCH_TOKEN"] != "" {
		return -1, errors.New("managed command inherited fetch authentication")
	}
	return p.localReviewEvidenceProvider.ExecWithStdin(ctx, sandbox, command, bytes.NewReader(encoded), stdout, stderr)
}

func (p *managedReviewInputsProvider) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	if strings.Contains(command, "valid = False") {
		if p.marker != "" {
			if _, err := os.Stat(p.marker); err != nil {
				p.clearedBeforeProof.Store(true)
			}
		}
		if p.cleanupFailure {
			return 1, nil
		}
		code, err := p.localReviewEvidenceProvider.Exec(ctx, sandbox, command, stdout, stderr)
		if code == 0 && err == nil {
			p.acknowledged.Store(true)
		}
		return code, err
	}
	if strings.Contains(command, "marker, owner, mode = ") && strings.HasSuffix(command, "'clear'") && !p.acknowledged.Load() {
		p.clearedBeforeProof.Store(true)
	}
	return p.localReviewEvidenceProvider.Exec(ctx, sandbox, command, stdout, stderr)
}

func TestManagedSharedReviewInputsFullShallowAndPartialClones(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"full", "shallow", "partial"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			reviewInputsGit(t, f.source, "checkout", "-q", "review")
			require.NoError(t, os.WriteFile(filepath.Join(f.source, "binary.dat"), []byte{0, 1, 2, 255}, 0o600), "review should include binary evidence")
			reviewInputsGit(t, f.source, "add", "binary.dat")
			reviewInputsGit(t, f.source, "commit", "-qm", "binary evidence")
			f.head = reviewInputsGit(t, f.source, "rev-parse", "HEAD")
			reviewInputsGit(t, f.source, "push", "-q", f.remote, "review")
			if mode == "partial" {
				reviewInputsGit(t, f.workDir, "fetch", "-q", "origin", "review")
				reviewInputsGit(t, f.workDir, "checkout", "-qB", "review", f.head)
			} else {
				f.workDir = filepath.Join(t.TempDir(), mode)
				args := []string{"clone", "-q", "--branch=review", "--no-single-branch"}
				if mode == "shallow" {
					args = append(args, "--depth=1")
				}
				clone := exec.Command("git", append(args, f.remote, f.workDir)...)
				clone.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
				output, err := clone.CombinedOutput()
				require.NoError(t, err, "fixture clone should succeed: %s", output)
			}
			reviewInputsWritableCleanup(t, f.workDir)
			session := reviewInputsSession(t, f.base, f.head)
			sandbox := f.sandbox()
			provider := &managedReviewInputsProvider{localReviewEvidenceProvider: localReviewEvidenceProvider{diagnoseFailures: true, logFailure: t.Logf}}
			tokens := &reviewInputsTokenSpy{}
			orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.Nop()}
			identity, err := codeReviewInputIdentityFromSession(session)
			require.NoError(t, err, "captured source should validate")
			if mode == "partial" {
				remote := strings.TrimPrefix(f.remote, "file://")
				require.NoError(t, os.Rename(remote, remote+".offline"), "offline proof should make implicit promisor fetch impossible")
				code, err := orchestrator.runCodeReviewInputPreparation(context.Background(), sandbox, identity, f.remote, "", false, false)
				require.NoError(t, err, "missing local tree objects should request typed hydration without attempting an implicit fetch")
				require.Equal(t, 42, code, "partial pinned trees should request authenticated hydration")
				require.NoError(t, os.Rename(remote+".offline", remote), "coordinator should regain its explicit hydration remote")
			}
			ctx := reviewPreparationContext(uuid.New(), uuid.New())
			provider.marker = codeReviewInputPreparationMarker(sandbox, identity)
			require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ctx, session, sandbox), "owner should mark shared input preparation")
			require.NoError(t, orchestrator.ensureCodeReviewInputs(ctx, session, sandbox), "managed coordinator should publish complete captured evidence")
			require.True(t, provider.acknowledged.Load(), "managed preparation must consume a successful cleanup acknowledgement")
			require.False(t, provider.clearedBeforeProof.Load(), "owner marker must survive until managed cleanup is confirmed")
			require.NoFileExists(t, provider.marker, "successful managed completion should release the owner marker")
			_, patchPath := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
			patch, err := os.ReadFile(filepath.Join(f.workDir, patchPath))
			require.NoError(t, err, "complete patch should be local")
			require.Contains(t, string(patch), "GIT binary patch", "shared inputs must retain complete binary changes")
			require.Contains(t, string(patch), "removed.go", "merge-base patch must retain deleted-file evidence")
			before := tokens.calls.Load()
			require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "changed.go"), []byte("reviewer edit\n"), 0o600), "warm reviewer should leave a workspace edit")
			require.NoError(t, orchestrator.ensureCodeReviewInputs(ctx, session, sandbox), "dirty warm reuse should retain captured evidence")
			require.Equal(t, before, tokens.calls.Load(), "warm reviewers must not request another token")
			if mode == "full" {
				require.Zero(t, before, "complete local source should never authenticate")
			} else {
				require.Equal(t, int64(1), before, "one coordinator authentication should hydrate missing captured source")
			}
			provider.mu.Lock()
			for _, payload := range provider.preparations {
				require.True(t, payload.Standalone && payload.PlatformCommand, "shared inputs must use the trusted standalone platform contract")
				if payload.VerifyIntegrity != "" {
					require.True(t, payload.GuardConfiguration, "creation and hydration must retain the strict configuration guard")
					require.Equal(t, f.remote, payload.ExpectedRepoURL, "creation must guard the same literal clone URL as hydration")
				}
			}
			warm := provider.preparations[len(provider.preparations)-1]
			require.Empty(t, warm.VerifyIntegrity, "warm reuse must never run a full object scan")
			require.NotContains(t, warm.VerifyCapturedIntegrity, "fsck", "warm reuse must never repeat a full object scan")
			require.False(t, warm.GuardConfiguration || warm.AuthenticateGit, "local warm verification must not depend on authentication configuration")
			provider.mu.Unlock()
		})
	}
}

func TestManagedSharedReviewInputsRetainOwnerOnCleanupUncertainty(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	provider := &managedReviewInputsProvider{cleanupFailure: true}
	session := reviewInputsSession(t, f.base, f.head)
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "captured input should validate")
	sandbox := f.sandbox()
	provider.marker = codeReviewInputPreparationMarker(sandbox, identity)
	tokens := &reviewInputsTokenSpy{}
	orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.Nop()}
	ctx := reviewPreparationContext(uuid.New(), uuid.New())
	require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ctx, session, sandbox), "owner marker should exist before managed preparation")
	err = orchestrator.ensureCodeReviewInputs(ctx, session, sandbox)
	require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "missing cleanup proof must remain terminal even when hydration is requested")
	require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "uncertain processes cannot become an ordinary availability retry")
	require.FileExists(t, provider.marker, "uncertain managed work must retain the preparation owner")
	require.False(t, provider.acknowledged.Load(), "failed cleanup must not count as acknowledgement")
	require.False(t, provider.clearedBeforeProof.Load(), "uncertain preparation must never attempt marker completion")
	require.Zero(t, tokens.calls.Load(), "cleanup uncertainty must forbid authenticated hydration")
}

type sharedInputsBudgetProvider struct {
	SandboxProvider
	stages []reviewBudgetStageObservation
	expire bool
}

func (*sharedInputsBudgetProvider) Exec(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error) {
	return 0, nil
}

func (p *sharedInputsBudgetProvider) ExecWithStdin(ctx context.Context, _ *Sandbox, _ string, input io.Reader, _, _ io.Writer) (int, error) {
	var message struct {
		Payload reviewWorkspacePreparationPayload `json:"payload"`
	}
	if err := json.NewDecoder(input).Decode(&message); err != nil {
		return -1, err
	}
	deadline, _ := ctx.Deadline()
	p.stages = append(p.stages, reviewBudgetStageObservation{deadline, message.Payload.DeadlineUnix, message.Payload.Prepare})
	if len(p.stages) == 1 {
		return 126, nil
	}
	if len(p.stages) == 2 {
		time.Sleep(time.Minute)
		return 126, nil
	}
	if p.expire {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	time.Sleep(time.Minute)
	return 0, nil
}

func TestSharedReviewInputsRetainOneAbsolutePreparationBudget(t *testing.T) {
	t.Parallel()
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "no fixed command cap", true: "earlier caller deadline"}[expire], func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				deadline := started.Add(5 * time.Minute)
				ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), deadline)
				if expire {
					deadline = started.Add(90 * time.Second)
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, deadline)
					defer cancel()
				}
				provider := &sharedInputsBudgetProvider{expire: expire}
				orchestrator := &Orchestrator{provider: provider, repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo", CloneURL: "https://github.com/example/repo.git"}}, github: &reviewInputsTokenSpy{}, logger: zerolog.Nop()}
				err := orchestrator.ensureCodeReviewInputs(ctx, reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40)), &Sandbox{ID: "shared", WorkDir: "/workspace"})
				if expire {
					require.ErrorIs(t, err, context.DeadlineExceeded, "an earlier caller deadline must survive shared coordination")
				} else {
					require.NoError(t, err, "two one-minute stages must not renew or hit an obsolete per-command cap")
				}
				synctest.Wait()
				require.Len(t, provider.stages, 3, "warm probe, creation proof and authenticated hydration should share the original deadline")
				for _, stage := range provider.stages {
					require.Equal(t, deadline, stage.contextDeadline, "every stage context must carry the earliest absolute deadline")
					require.InDelta(t, float64(deadline.UnixNano())/float64(time.Second), stage.payloadDeadline, 0.001, "managed payload must carry the same deadline")
					require.Contains(t, stage.script, "timeout=remaining()", "Git subprocesses must consume the remaining total budget")
					require.NotContains(t, stage.script, "min(35", "shared input commands must not impose the removed fixed cap")
				}
			})
		})
	}
}

func TestManagedSharedReviewInputsCancellationReapsDetachedGit(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	bin := filepath.Join(t.TempDir(), "git")
	child := "import os,pathlib,signal; pathlib.Path('shared-git.pid').write_text(str(os.getpid())); signal.pause(); pathlib.Path('escaped-git').write_text('escaped')"
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexec /usr/bin/python3 -I -S -c '"+shellEscapeSingleQuote(child)+"'\n"), 0o700), "controlled Git fixture should expose an owned live process")
	provider := disconnectedReviewEvidenceProvider{localReviewEvidenceProvider{rewriteTrustedGit: bin}}
	orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, logger: zerolog.Nop()}
	session := reviewInputsSession(t, f.base, f.head)
	ctx, cancel := context.WithCancel(reviewPreparationContext(uuid.New(), uuid.New()))
	defer cancel()
	sandbox := f.sandbox()
	require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ctx, session, sandbox), "cancellable owner should have a marker")
	completed := make(chan error, 1)
	var finished atomic.Bool
	go func() {
		completed <- orchestrator.ensureCodeReviewInputs(ctx, session, sandbox)
		finished.Store(true)
	}()
	t.Cleanup(func() {
		cancel()
		require.Eventually(t, finished.Load, 8*time.Second, 10*time.Millisecond, "controlled preparation must finish cleanup before its temporary files are removed")
	})
	pidPath := filepath.Join(f.workDir, "shared-git.pid")
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidPath)
		if err != nil {
			return false
		}
		pid, err := strconv.Atoi(string(data))
		return err == nil && pid > 0
	}, 5*time.Second, 10*time.Millisecond, "cancellation must observe a complete real Git PID before closing its output attachment")
	data, err := os.ReadFile(pidPath)
	require.NoError(t, err, "owned child should expose its PID")
	pid, err := strconv.Atoi(string(data))
	require.NoError(t, err, "owned PID should parse")
	needsCleanup := true
	t.Cleanup(func() {
		if needsCleanup {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Errorf("failed to clean controlled Git fixture: %v", err)
			}
		}
	})
	cancel()
	select {
	case err = <-completed:
		require.ErrorIs(t, err, context.Canceled, "managed input cancellation must preserve the owner cause")
		require.NotErrorIs(t, err, ErrCodeReviewPreparationCleanup, "observed successful reap should confirm managed cleanup")
	case <-time.After(8 * time.Second):
		t.Fatal("canceled shared input owner must join bounded managed cleanup")
	}
	termination := syscall.Kill(pid, 0)
	needsCleanup = !errors.Is(termination, syscall.ESRCH)
	require.ErrorIs(t, termination, syscall.ESRCH, "the observed Git child must be terminated and reaped before ensure returns")
	require.NoFileExists(t, filepath.Join(f.workDir, "escaped-git"), "canceled Git must not execute its next instruction")
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "captured identity should remain valid")
	require.FileExists(t, codeReviewInputPreparationMarker(sandbox, identity), "canceled owners must retain their preparation marker for exact lease recovery")
}

type sharedInputsFlightProvider struct {
	SandboxProvider
	entered        chan struct{}
	release        chan struct{}
	cleanupEntered chan struct{}
	cleanupRelease chan struct{}
	cleanupFailure bool
	started        atomic.Int64
}

func (p *sharedInputsFlightProvider) ExecWithStdin(ctx context.Context, _ *Sandbox, _ string, _ io.Reader, _, _ io.Writer) (int, error) {
	if p.started.Add(1) == 1 {
		close(p.entered)
	}
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-p.release:
		return 0, nil
	}
}

func (p *sharedInputsFlightProvider) Exec(ctx context.Context, _ *Sandbox, command string, _, _ io.Writer) (int, error) {
	if strings.Contains(command, "valid = False") {
		close(p.cleanupEntered)
		select {
		case <-p.cleanupRelease:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
		if p.cleanupFailure {
			return 1, nil
		}
	}
	return 0, nil
}

func TestSharedReviewInputFlightsSeparateLeaderAndFollowerCancellation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"cancel follower", "cancel owner", "uncertain owner cleanup", "expired owner cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				provider := &sharedInputsFlightProvider{entered: make(chan struct{}), release: make(chan struct{}), cleanupEntered: make(chan struct{}), cleanupRelease: make(chan struct{}), cleanupFailure: scenario == "uncertain owner cleanup"}
				orchestrator := &Orchestrator{provider: provider, repositories: testInternalRepoStore{}, logger: zerolog.Nop()}
				session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
				sandbox := &Sandbox{ID: "shared", WorkDir: "/workspace"}
				ownerCtx, cancelOwner := context.WithCancel(context.Background())
				defer cancelOwner()
				if scenario == "expired owner cancellation" {
					ownerCtx = WithCodeReviewWorkspacePreparationDeadline(ownerCtx, time.Now().Add(-time.Second))
				}
				followerCtx, cancelFollower := context.WithCancel(context.Background())
				defer cancelFollower()
				ownerResult, followerResult := make(chan error, 1), make(chan error, 1)
				go func() { ownerResult <- orchestrator.ensureCodeReviewInputs(ownerCtx, session, sandbox) }()
				<-provider.entered
				go func() { followerResult <- orchestrator.ensureCodeReviewInputs(followerCtx, session, sandbox) }()
				synctest.Wait()
				if scenario == "cancel follower" {
					cancelFollower()
					require.ErrorIs(t, <-followerResult, context.Canceled, "a canceled follower must return its own cancellation promptly")
					select {
					case <-ownerResult:
						t.Fatal("canceling a follower must not stop the preparation owner")
					default:
					}
					close(provider.release)
				} else {
					cancelOwner()
				}
				<-provider.cleanupEntered
				select {
				case <-ownerResult:
					t.Fatal("the preparation owner must join managed cleanup before returning")
				default:
				}
				close(provider.cleanupRelease)
				ownerErr := <-ownerResult
				if scenario == "cancel follower" {
					require.NoError(t, ownerErr, "a canceled follower must leave the owner's successful result intact")
				} else {
					require.ErrorIs(t, ownerErr, context.Canceled, "the canceled owner must retain its own cause")
					followerErr := <-followerResult
					if provider.cleanupFailure {
						require.ErrorIs(t, ownerErr, ErrCodeReviewPreparationCleanup, "cleanup uncertainty must survive owner cancellation")
						require.ErrorIs(t, followerErr, ErrCodeReviewPreparationCleanup, "live followers must reject uncertain owner cleanup")
						require.NotErrorIs(t, followerErr, ErrSandboxWorkspaceNotReady, "uncertain work must never become an ordinary retry")
					} else if scenario == "expired owner cancellation" {
						require.ErrorIs(t, ownerErr, context.DeadlineExceeded, "expired verification must retain its deadline even if its owner later cancels")
						require.ErrorIs(t, followerErr, ErrSandboxWorkspaceNotReady, "a live follower must retry when its owner's verification allowance ends")
						require.NotErrorIs(t, followerErr, context.DeadlineExceeded, "the owner's deadline must not become a live follower's deadline")
					} else {
						require.NotErrorIs(t, ownerErr, ErrCodeReviewPreparationCleanup, "acknowledged owner cleanup should preserve plain cancellation")
						require.ErrorIs(t, followerErr, ErrSandboxWorkspaceNotReady, "a live follower must retry when its preparation owner cancels")
					}
				}
				require.Equal(t, int64(1), provider.started.Load(), "siblings must share one preparation executor")
			})
		})
	}
}

func TestSharedReviewInputFollowerUsesItsOwnPreparationDeadline(t *testing.T) {
	t.Parallel()
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "shorter preparation budget", true: "expired budget verification allowance"}[expired], func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				provider := &sharedInputsFlightProvider{entered: make(chan struct{}), release: make(chan struct{}), cleanupEntered: make(chan struct{}), cleanupRelease: make(chan struct{})}
				orchestrator := &Orchestrator{provider: provider, repositories: testInternalRepoStore{}, logger: zerolog.Nop()}
				session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
				sandbox := &Sandbox{ID: "shared", WorkDir: "/workspace"}
				ownerResult := make(chan error, 1)
				go func() { ownerResult <- orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox) }()
				<-provider.entered
				started := time.Now()
				duration := 2 * time.Minute
				deadline := started.Add(duration)
				if expired {
					deadline = started.Add(-time.Second)
					duration = CodeReviewWorkspaceVerificationTimeout
				}
				ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), deadline)
				err := orchestrator.ensureCodeReviewInputs(ctx, session, sandbox)
				require.ErrorIs(t, err, context.DeadlineExceeded, "a follower's own preparation deadline must bound its shared wait")
				require.Equal(t, duration, time.Since(started), "the follower must stop at its own allowance rather than the owner's later deadline")
				select {
				case <-ownerResult:
					t.Fatal("an expired follower must not cancel the live preparation owner")
				default:
				}
				close(provider.release)
				<-provider.cleanupEntered
				close(provider.cleanupRelease)
				require.NoError(t, <-ownerResult, "a live owner must retain its successful result after a follower expires")
			})
		})
	}
}

func TestSharedReviewInputsExpiredBudgetVerifiesExistingBundleOnly(t *testing.T) {
	t.Parallel()
	for _, bundle := range []string{"cached", "missing", "invalid"} {
		t.Run(bundle, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			reviewInputsWritableCleanup(t, f.workDir)
			session := reviewInputsSession(t, f.base, f.head)
			provider := &managedReviewInputsProvider{}
			tokens := &reviewInputsTokenSpy{}
			orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.Nop()}
			sandbox := f.sandbox()
			require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "initial preparation should publish the complete bundle")
			manifest, patch := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
			if bundle == "missing" {
				require.NoError(t, os.Chmod(filepath.Dir(filepath.Join(f.workDir, manifest)), 0o700), "missing-bundle fixture should permit deletion")
				require.NoError(t, os.RemoveAll(filepath.Dir(filepath.Join(f.workDir, manifest))), "expired preparation must observe an absent artifact")
			} else if bundle == "invalid" {
				require.NoError(t, os.Chmod(filepath.Join(f.workDir, patch), 0o600), "invalid-bundle fixture should permit corruption")
				require.NoError(t, os.WriteFile(filepath.Join(f.workDir, patch), []byte("corrupted"), 0o600), "expired verification should detect changed evidence")
			}
			ownerCtx := reviewPreparationContext(uuid.New(), uuid.New())
			require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ownerCtx, session, sandbox), "verification must not change an existing preparation owner")
			identity, err := codeReviewInputIdentityFromSession(session)
			require.NoError(t, err, "verification input should remain pinned")
			marker := codeReviewInputPreparationMarker(sandbox, identity)
			before, err := os.ReadFile(marker)
			require.NoError(t, err, "owner marker should be readable")
			calls := tokens.calls.Load()
			expired := WithCodeReviewWorkspacePreparationDeadline(ownerCtx, time.Now().Add(-time.Second))
			err = orchestrator.ensureCodeReviewInputs(expired, session, sandbox)
			if bundle == "cached" {
				require.NoError(t, err, "complete local cached evidence may validate after the setup budget")
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded, "expired setup must not repair missing or invalid evidence")
			}
			require.Equal(t, calls, tokens.calls.Load(), "verification-only work must never acquire authentication")
			after, err := os.ReadFile(marker)
			require.NoError(t, err, "verification must preserve preparation ownership")
			require.Equal(t, before, after, "verification-only work must never clear or replace the owner marker")
			if bundle == "missing" {
				require.NoDirExists(t, filepath.Dir(filepath.Join(f.workDir, manifest)), "expired verification must never generate an artifact")
			}
			ctx, cancel := context.WithCancel(expired)
			cancel()
			require.ErrorIs(t, orchestrator.ensureCodeReviewInputs(ctx, session, sandbox), context.Canceled, "caller cancellation must override the warm verification allowance")
			deadlineCtx, deadlineCancel := context.WithDeadline(expired, time.Now().Add(-time.Second))
			defer deadlineCancel()
			require.ErrorIs(t, orchestrator.ensureCodeReviewInputs(deadlineCtx, session, sandbox), context.DeadlineExceeded, "an expired caller deadline must override the verification allowance")
		})
	}
}

type managedReviewInputsWithoutStdin struct{ SandboxProvider }

func (managedReviewInputsWithoutStdin) ExecForReviewPreparation(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("unsupported provider must never execute a shared preparation command")
}

func TestSharedReviewInputsRejectManagedProviderWithoutPrivateInput(t *testing.T) {
	t.Parallel()
	orchestrator := &Orchestrator{provider: managedReviewInputsWithoutStdin{}, repositories: testInternalRepoStore{}}
	err := orchestrator.ensureCodeReviewInputs(context.Background(), reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40)), &Sandbox{ID: "shared", WorkDir: "/workspace"})
	require.ErrorContains(t, err, "without private input support", "managed providers must not fall back to an unaudited Exec path")
	require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "an unsupported managed contract is a fatal setup failure")
}

type sharedInputsTransportFailureProvider struct {
	sharedInputsBudgetProvider
	cause error
}

func (p *sharedInputsTransportFailureProvider) ExecWithStdin(_ context.Context, _ *Sandbox, _ string, input io.Reader, _, _ io.Writer) (int, error) {
	if p.cause != nil {
		return -1, p.cause
	}
	var message struct {
		Authentication reviewGitAuthentication `json:"authentication"`
	}
	if err := json.NewDecoder(input).Decode(&message); err != nil {
		return -1, err
	}
	token := message.Authentication.Token
	return -1, errors.New("executor disconnected with token=" + token + " header=" + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
}

func TestManagedSharedReviewInputTransportRetryKeepsCredentialsRedacted(t *testing.T) {
	t.Parallel()
	const token = "synthetic-private-token"
	provider := &sharedInputsTransportFailureProvider{}
	orchestrator := &Orchestrator{provider: provider, logger: zerolog.Nop()}
	session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "captured inputs must validate before managed dispatch")
	_, err = orchestrator.runCodeReviewInputPreparation(context.Background(), &Sandbox{ID: "shared", WorkDir: "/workspace"}, identity, "https://github.com/example/repo.git", token, true, false)
	require.ErrorIs(t, err, ErrSandboxWorkspaceNotReady, "acknowledged transport loss should preserve the ordinary waiting retry")
	require.ErrorIs(t, err, errReviewPreparationTransport, "sanitized retry must preserve its finite transport identity")
	require.NotContains(t, err.Error(), token, "raw authentication must not enter error output")
	require.NotContains(t, err.Error(), base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)), "HTTP authentication headers must not enter error output")
	_, err = orchestrator.runCodeReviewInputPreparation(context.Background(), &Sandbox{ID: "legacy", WorkDir: "/workspace", Env: map[string]string{"GITHUB_TOKEN": token}}, identity, "https://github.com/example/repo.git", token, true, false)
	require.Error(t, err, "unsafe legacy authentication must fail setup")
	require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "trust rejection must never be classified as transport availability")
	require.NotContains(t, err.Error(), token, "trust rejection must not reveal legacy authentication")
	provider.cause = ErrCodeReviewSourceIntegrity
	_, err = orchestrator.runCodeReviewInputPreparation(context.Background(), &Sandbox{ID: "integrity", WorkDir: "/workspace"}, identity, "https://github.com/example/repo.git", token, true, false)
	require.ErrorIs(t, err, ErrCodeReviewSourceIntegrity, "typed source integrity failures must survive provider dispatch")
	require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "source integrity must never be classified as transport availability")
}

func TestManagedSharedReviewInputsCompletionUsesTrustedPython(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	reviewInputsWritableCleanup(t, f.workDir)
	bin := filepath.Join(t.TempDir(), "bin")
	require.NoError(t, os.Mkdir(bin, 0o700), "controlled PATH shadow should have an isolated directory")
	shadowed := filepath.Join(f.workDir, "shadowed-python")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "python3"), []byte("#!/bin/sh\nprintf unsafe > '"+shellEscapeSingleQuote(shadowed)+"'\nexit 0\n"), 0o700), "writable PATH should expose an unsafe Python shim")
	sandbox := f.sandbox()
	sandbox.Env = map[string]string{"PATH": bin + ":/usr/bin:/bin"}
	session := reviewInputsSession(t, f.base, f.head)
	orchestrator := &Orchestrator{provider: localReviewEvidenceProvider{}, repositories: f.orchestrator().repositories, github: &reviewInputsTokenSpy{}, logger: zerolog.Nop()}
	ctx := reviewPreparationContext(uuid.New(), uuid.New())
	require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ctx, session, sandbox), "trusted marker creation must ignore writable PATH")
	require.NoError(t, orchestrator.ensureCodeReviewInputs(ctx, session, sandbox), "trusted preparation and completion must ignore writable PATH")
	require.NoFileExists(t, shadowed, "owner completion must never execute repository-shadowed Python")
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "captured source should remain valid")
	require.NoFileExists(t, codeReviewInputPreparationMarker(sandbox, identity), "only the trusted owner-completion command may release readiness")
}

func TestGenericSharedReviewInputsExpiredVerificationRequiresLocalCapturedTrees(t *testing.T) {
	t.Parallel()
	directory, base, head, mergeBase := newReviewInputsGitFixture(t)
	session := reviewInputsSession(t, base, head)
	tokens := &reviewInputsTokenSpy{}
	orchestrator := &Orchestrator{provider: &reviewInputsProvider{}, repositories: testInternalRepoStore{repo: models.Repository{CloneURL: directory}}, github: tokens}
	sandbox := &Sandbox{ID: "shared", WorkDir: directory}
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "complete local source should publish its bundle")
	expired := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Second))
	require.NoError(t, orchestrator.ensureCodeReviewInputs(expired, session, sandbox), "the optional generic contract may verify healthy local evidence after expiry")
	blob := reviewInputsGit(t, directory, "rev-parse", mergeBase+":change.txt")
	require.NoError(t, os.Remove(filepath.Join(directory, ".git", "objects", blob[:2], blob[2:])), "fixture should remove a required local captured blob")
	err := orchestrator.ensureCodeReviewInputs(expired, session, sandbox)
	require.ErrorIs(t, err, context.DeadlineExceeded, "cached patch alone must not satisfy expired verification when captured trees are incomplete")
	require.Zero(t, tokens.calls.Load(), "expired generic verification must never hydrate or acquire authentication")
}

type markerReadinessFailureProvider struct {
	SandboxProvider
	validationError error
	executionError  error
	exit            int
	executions      atomic.Int64
	cancel          context.CancelFunc
}

func (p *markerReadinessFailureProvider) ValidateReviewPreparationEnvironment(context.Context, *Sandbox) error {
	return p.validationError
}

func (p *markerReadinessFailureProvider) Exec(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error) {
	p.executions.Add(1)
	if p.cancel != nil {
		p.cancel()
	}
	return p.exit, p.executionError
}

func TestSharedReviewInputMarkerReadinessRetriesOnlyExecutionTransport(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		execution  error
		validation error
		legacy     bool
		waiting    bool
		exit       int
		cancel     bool
	}{
		{name: "transport unavailable", execution: errors.New("executor disconnected"), waiting: true},
		{name: "legacy credentials", legacy: true},
		{name: "unsafe environment", validation: errors.New("unsafe preparation environment")},
		{name: "cleanup uncertainty", execution: ErrCodeReviewPreparationCleanup},
		{name: "cleanup during cancellation", execution: ErrCodeReviewPreparationCleanup, cancel: true},
		{name: "source integrity", execution: ErrCodeReviewSourceIntegrity},
		{name: "marker command failed", exit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := &markerReadinessFailureProvider{validationError: test.validation, executionError: test.execution, exit: test.exit}
			orchestrator := &Orchestrator{provider: provider}
			sandbox := &Sandbox{ID: "shared", WorkDir: "/workspace"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				provider.cancel = cancel
			}
			if test.legacy {
				sandbox.Env = map[string]string{"GITHUB_TOKEN": "synthetic-private-token"}
			}
			ready, err := orchestrator.codeReviewInputsPreparationFinished(ctx, reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40)), sandbox)
			require.False(t, ready, "failed marker inspection must never admit a reviewer")
			require.Error(t, err, "marker inspection failure must remain explicit")
			if test.waiting {
				require.ErrorIs(t, err, ErrSandboxWorkspaceNotReady, "only actual transport loss may retry marker readiness")
			} else {
				require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "trust, cleanup and command failures must remain fatal")
			}
			if test.legacy || test.validation != nil {
				require.Zero(t, provider.executions.Load(), "failed trust validation must prevent execution")
			}
			if test.execution == ErrCodeReviewPreparationCleanup {
				require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "cleanup uncertainty must retain its terminal identity")
			}
			if test.cancel {
				require.ErrorIs(t, err, context.Canceled, "fatal cleanup must also preserve caller cancellation")
			}
			require.NotContains(t, err.Error(), "synthetic-private-token", "marker trust failure must never expose legacy authentication")
		})
	}
}

func TestSharedReviewInputMarkerCompletionPreservesCleanupFailure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &markerReadinessFailureProvider{executionError: ErrCodeReviewPreparationCleanup, cancel: cancel}
	orchestrator := &Orchestrator{provider: provider}
	session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "captured identity should validate")
	err = orchestrator.finishCodeReviewInputPreparation(ctx, session, &Sandbox{ID: "shared", WorkDir: "/workspace"}, identity)
	require.ErrorIs(t, err, ErrCodeReviewPreparationCleanup, "marker completion must not lose a cleanup failure during owner cancellation")
	require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "uncertain completion must remain terminal")
}

type stalledAcknowledgementProvider struct{ localReviewEvidenceProvider }

func (p stalledAcknowledgementProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, input io.Reader, stdout, stderr io.Writer) (int, error) {
	hook := `original_replace = os.replace
def stalled_replace(source, destination):
    if destination.endswith(".done"):
        with open(source) as prepared, open("pending-ack-status", "w") as observed:
            observed.write(prepared.read())
        while not os.path.exists("permit-ack-publication"):
            time.sleep(0.01)
    original_replace(source, destination)
os.replace = stalled_replace
`
	instrumented := strings.Replace(reviewWorkspacePreparationPython, "p = json.loads(sys.argv[1])", hook+"p = json.loads(sys.argv[1])", 1)
	command = strings.Replace(command, shellEscapeSingleQuote(reviewWorkspacePreparationPython), shellEscapeSingleQuote(instrumented), 1)
	return p.localReviewEvidenceProvider.ExecWithStdin(ctx, sandbox, command, input, stdout, stderr)
}

func TestManagedSharedPreparationAcknowledgementPublishesOnlyCompleteStatus(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sandbox := &Sandbox{ID: "shared", WorkDir: directory}
	orchestrator := &Orchestrator{provider: stalledAcknowledgementProvider{}, logger: zerolog.Nop()}
	ctx, cancel := codeReviewPreparationContext(context.Background())
	defer cancel()
	deadline, err := reviewPreparationDeadlineUnix(ctx)
	require.NoError(t, err, "the fixture must carry the actual preparation deadline")
	cancelPath := filepath.Join(directory, "acknowledgement")
	permission := filepath.Join(directory, "permit-ack-publication")
	completed := make(chan error, 1)
	var finished atomic.Bool
	go func() {
		completed <- orchestrator.execReviewPreparationPayload(ctx, sandbox, reviewWorkspacePreparationPayload{WorkDir: directory, Standalone: true, PlatformCommand: true, Prepare: "/usr/bin/true", DeadlineUnix: deadline, CancelPath: cancelPath}, reviewGitAuthentication{}, zerolog.Nop())
		finished.Store(true)
	}()
	t.Cleanup(func() {
		if err := os.WriteFile(permission, nil, 0o600); err != nil {
			t.Errorf("failed to release acknowledgement fixture: %v", err)
		}
		cancel()
		require.Eventually(t, finished.Load, 8*time.Second, 10*time.Millisecond, "stalled acknowledgement must release before its fixture directory is removed")
	})
	require.Eventually(t, func() bool {
		status, err := os.ReadFile(filepath.Join(directory, "pending-ack-status"))
		return err == nil && string(status) == "cleaned\n"
	}, 5*time.Second, 10*time.Millisecond, "publication barrier must observe the complete status in the temporary file")
	require.NoFileExists(t, cancelPath+".done", "the completion acknowledgement must remain absent before atomic publication")
	require.NoFileExists(t, filepath.Join(directory, ".143-test-cleanup-quarantine"), "pending atomic publication must not expose a partial acknowledgement to confirmation")
	require.NoError(t, os.WriteFile(permission, nil, 0o600), "the fixture should permit atomic acknowledgement publication")
	select {
	case err := <-completed:
		require.NoError(t, err, "a complete atomic acknowledgement should confirm successful cleanup")
	case <-time.After(8 * time.Second):
		t.Fatal("managed cleanup must complete after acknowledgement publication is released")
	}
	require.NoFileExists(t, cancelPath+".done", "trusted confirmation should consume the complete acknowledgement")
}
