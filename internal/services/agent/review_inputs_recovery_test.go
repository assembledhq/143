package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type transientReviewMarkerProvider struct {
	reviewInputsProvider
	failures int
}

func (p *transientReviewMarkerProvider) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	if p.failures > 0 {
		p.failures--
		return -1, errors.New("temporary executor connection failure")
	}
	return p.reviewInputsProvider.Exec(ctx, sandbox, command, stdout, stderr)
}

func TestReviewPreparationTransientAvailabilityUsesReadinessRetry(t *testing.T) {
	t.Parallel()
	for _, database := range []bool{false, true} {
		t.Run(map[bool]string{false: "executor probe", true: "lease database"}[database], func(t *testing.T) {
			t.Parallel()
			directory, base, head, _ := newReviewInputsGitFixture(t)
			session := reviewInputsSession(t, base, head)
			sandbox := &Sandbox{ID: "source", WorkDir: directory}
			provider := &transientReviewMarkerProvider{}
			store := &reviewInputsRecoverySessions{recovered: true}
			orchestrator := &Orchestrator{provider: provider, sessions: store}
			if database {
				require.NoError(t, orchestrator.markCodeReviewInputsPreparing(reviewPreparationContext(uuid.New(), uuid.New()), session, sandbox), "crashed owner marker should exist")
				store.errors = []error{errors.New("temporary database connection failure")}
			} else {
				provider.failures = 1
			}
			err := waitForSandboxWorkspaceReady(reviewPreparationContext(uuid.New(), uuid.New()), provider, sandbox, "review", head, 5*time.Second, time.Millisecond, 4*time.Millisecond, nil, func(ctx context.Context) (bool, error) {
				return orchestrator.codeReviewInputsPreparationFinished(ctx, session, sandbox)
			})
			if database {
				require.ErrorIs(t, err, ErrStaleSandboxIDCleared, "transient lease failure should retry until the source can be safely fenced")
				require.Len(t, store.params, 2, "lease proof should be retried inside the same readiness attempt")
			} else {
				require.NoError(t, err, "transient executor probe should retry until a completed checkout is ready")
			}
		})
	}
}

func TestReviewPreparationTransientFailureTimeoutKeepsWaitingClassification(t *testing.T) {
	t.Parallel()
	directory, base, head, _ := newReviewInputsGitFixture(t)
	session := reviewInputsSession(t, base, head)
	provider := &transientReviewMarkerProvider{failures: 100}
	sandbox := &Sandbox{ID: "source", WorkDir: directory}
	orchestrator := &Orchestrator{provider: provider}
	err := waitForSandboxWorkspaceReady(context.Background(), provider, sandbox, "review", head, 30*time.Millisecond, time.Millisecond, 4*time.Millisecond, nil, func(ctx context.Context) (bool, error) {
		return orchestrator.codeReviewInputsPreparationFinished(ctx, session, sandbox)
	})
	require.ErrorIs(t, err, ErrSandboxWorkspaceNotReady, "temporary availability loss should preserve the worker's no-attempt waiting retry")
	require.Zero(t, provider.destroys.Load(), "availability errors must never authorize source destruction")
}

func TestReviewPreparationOwnerRecoveryBeforeCheckoutReady(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		recovered bool
	}{
		{name: "crashed executor lease fenced", recovered: true},
		{name: "live sibling lease protects workspace"},
		{name: "replacement wins recovery CAS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory, base, head, _ := newReviewInputsGitFixture(t)
			session := reviewInputsSession(t, base, head)
			ownerJob, ownerToken, waitingJob, waitingToken := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			ownerCtx, waitingCtx := reviewPreparationContext(ownerJob, ownerToken), reviewPreparationContext(waitingJob, waitingToken)
			provider := &reviewInputsProvider{}
			store := &reviewInputsRecoverySessions{recovered: tt.recovered}
			orchestrator := &Orchestrator{provider: provider, sessions: store}
			sandbox := &Sandbox{ID: "incomplete-container", WorkDir: directory}
			require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ownerCtx, session, sandbox), "preparation should record exact job and fencing token")
			reviewInputsGit(t, directory, "checkout", "-q", "main")
			if tt.recovered {
				err := waitForSandboxWorkspaceReady(waitingCtx, provider, sandbox, "review", head, 5*time.Second, time.Millisecond, 4*time.Millisecond, nil, func(ctx context.Context) (bool, error) {
					return orchestrator.codeReviewInputsPreparationFinished(ctx, session, sandbox)
				})
				require.ErrorIs(t, err, ErrStaleSandboxIDCleared, "a fenced incomplete workspace should request the existing cold retry")
				require.Equal(t, int64(1), provider.destroys.Load(), "only the recovery CAS winner may destroy the incomplete workspace")
			} else {
				ready, err := orchestrator.codeReviewInputsPreparationFinished(waitingCtx, session, sandbox)
				require.NoError(t, err, "live or retaken ownership should return its preparation decision without a transport deadline")
				require.False(t, ready, "live or retaken ownership must keep the workspace incomplete")
				require.Equal(t, int64(0), provider.destroys.Load(), "a protected or retaken workspace must never be destroyed")
			}
			require.NotEmpty(t, store.params, "owner lease proof must run even when the captured checkout never completed")
			require.Equal(t, db.RecoverIncompleteCodeReviewWorkspaceParams{
				SessionID: session.ID, ContainerID: sandbox.ID, Repository: "example/repo", BaseSHA: base, HeadSHA: head,
				PreparationJobID: ownerJob, PreparationLockToken: ownerToken, WaitingJobID: waitingJob, WaitingLockToken: waitingToken,
			}, store.params[0], "recovery must bind both preparation and waiting job attempts")
		})
	}
}

func TestReviewPreparationLegacyMarkerRequiresRestart(t *testing.T) {
	t.Parallel()
	directory, base, head, _ := newReviewInputsGitFixture(t)
	session := reviewInputsSession(t, base, head)
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "fixture identity should be valid")
	provider := &reviewInputsProvider{}
	sandbox := &Sandbox{ID: "container", WorkDir: directory}
	marker := codeReviewInputPreparationMarker(sandbox, identity)
	require.NoError(t, os.WriteFile(marker, nil, 0o600), "legacy ownerless marker should be present")
	store := &reviewInputsRecoverySessions{recovered: true}
	orchestrator := &Orchestrator{provider: provider, sessions: store}
	_, err = orchestrator.codeReviewInputsPreparationFinished(reviewPreparationContext(uuid.New(), uuid.New()), session, sandbox)
	require.ErrorContains(t, err, "restart the review workspace", "unverifiable legacy ownership must fail closed with an operator recovery path")
	require.Empty(t, store.params, "ownerless markers must never enter destructive recovery")
	require.Equal(t, int64(0), provider.destroys.Load(), "legacy markers must preserve their container")
}

func TestReviewPreparationCleanupCannotClearSuccessorMarker(t *testing.T) {
	t.Parallel()
	directory, base, head, _ := newReviewInputsGitFixture(t)
	session := reviewInputsSession(t, base, head)
	orchestrator := &Orchestrator{provider: &reviewInputsProvider{}}
	sandbox := &Sandbox{ID: "container", WorkDir: directory}
	first, successor := reviewPreparationContext(uuid.New(), uuid.New()), reviewPreparationContext(uuid.New(), uuid.New())
	require.NoError(t, orchestrator.markCodeReviewInputsPreparing(first, session, sandbox), "first attempt should own a marker")
	require.NoError(t, orchestrator.markCodeReviewInputsPreparing(successor, session, sandbox), "successor should replace ownership atomically")
	orchestrator.clearCodeReviewInputPreparation(first, session, sandbox)
	identity, err := codeReviewInputIdentityFromSession(session)
	require.NoError(t, err, "fixture identity should remain valid")
	_, err = os.Stat(codeReviewInputPreparationMarker(sandbox, identity))
	require.NoError(t, err, "stale cleanup must not remove the successor's marker")
	orchestrator.clearCodeReviewInputPreparation(successor, session, sandbox)
	_, err = os.Stat(filepath.Clean(codeReviewInputPreparationMarker(sandbox, identity)))
	require.ErrorIs(t, err, os.ErrNotExist, "only the exact owner may clear its marker")
}
