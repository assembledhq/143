package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

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
			err := waitForSandboxWorkspaceReady(waitingCtx, provider, sandbox, "review", head, 80*time.Millisecond, time.Millisecond, 4*time.Millisecond, nil, func(ctx context.Context) (bool, error) {
				return orchestrator.codeReviewInputsPreparationFinished(ctx, session, sandbox)
			})
			if tt.recovered {
				require.ErrorIs(t, err, ErrStaleSandboxIDCleared, "a fenced incomplete workspace should request the existing cold retry")
				require.Equal(t, int64(1), provider.destroys.Load(), "only the recovery CAS winner may destroy the incomplete workspace")
			} else {
				require.ErrorIs(t, err, ErrSandboxWorkspaceNotReady, "live or retaken ownership must preserve the bounded readiness wait")
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
