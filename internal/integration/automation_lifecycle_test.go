//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/automations"
)

// lifecycleHarness drives the pull request lifecycle notification against a
// live target.
type lifecycleHarness struct {
	*completionHarness
	lifecycle *automations.TargetLifecycle
}

func newLifecycleHarness(t *testing.T) *lifecycleHarness {
	t.Helper()
	h := newCompletionHarness(t)
	return &lifecycleHarness{
		completionHarness: h,
		lifecycle:         automations.NewTargetLifecycle(h.pool, h.targets, h.completer, zerolog.Nop()),
	}
}

func (h *lifecycleHarness) closePR(t *testing.T, merged bool) {
	t.Helper()
	require.NoError(t, h.lifecycle.OnPullRequestClosed(context.Background(), h.orgID, "acme/web", 42, merged, time.Now().UTC()), "notify closed")
}

// TestAutomationLifecycle_ClosedPullRequest proves an unmerged close stops
// the target: its waiters are skipped as pr_closed, the executing turn is
// left to finish, the generation is retired once it does, and the session
// comes back to its people.
func TestAutomationLifecycle_ClosedPullRequest(t *testing.T) {
	h := newLifecycleHarness(t)
	ctx := context.Background()
	run2 := h.push(t, "2222222222222222222222222222222222222222", recentDeliveryTime().Add(time.Second))
	require.Equal(t, automations.DispatchWaiting, h.dispatch(t, run2, models.AgentTypeCodex).Kind, "the second run waits")

	h.closePR(t, false)

	target := h.target(t)
	require.Equal(t, models.AutomationTargetLifecycleClosed, target.LifecycleState, "the target is closed")
	waiter := h.reload(t, run2.ID)
	require.Equal(t, models.AutomationRunStatusSkipped, waiter.Status, "the waiter is skipped")
	require.Equal(t, models.AutomationRunOutcomePRClosed, *waiter.OutcomeReason, "as pr_closed")
	require.Equal(t, models.AutomationRunStatusRunning, h.reload(t, h.run.ID).Status, "the executing turn is left to finish")
	// The generation is retired at once. Its ownership release waits for the
	// executing turn, which is what hands the session back however that turn
	// ends, including through a path that writes no result.
	generation, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.Equal(t, models.AutomationTargetSessionStatusRetired, generation.Status, "the close retires the generation")
	require.Equal(t, models.AutomationTargetRetiredPRClosed, *generation.RetiredReason, "with pr_closed")
	require.True(t, generation.OwnershipReleasePending, "the release waits for the executing turn")
	require.NotNil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session stays owned until that turn ends")
	require.Len(t, h.wakeJobs(t), 1, "the close woke the target")

	// However the turn ends, the session comes back. Here it ends without a
	// result, the path that writes no marker.
	h.deadLetterJob(t)
	outcome, err := h.completer.RecoverAbandonedRun(ctx, h.orgID, h.run.ID, h.jobID, time.Now())
	require.NoError(t, err, "recover")
	require.Equal(t, models.AutomationRunOutcomeRetriesExhausted, outcome, "the turn is settled")
	require.Nil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session is handed back")
	generation, err = h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.False(t, generation.OwnershipReleasePending, "and the release is applied")
}

// TestAutomationLifecycle_MergedKeepsTheFinalTurn proves a merged pull
// request keeps its subscribed merged run as the final turn while skipping
// every other waiter, and retires the generation once that turn is done.
func TestAutomationLifecycle_MergedKeepsTheFinalTurn(t *testing.T) {
	h := newLifecycleHarness(t)
	ctx := context.Background()
	comment := h.deliver(t, automations.GitHubEventTriggerRequest{
		Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "synchronize",
		HeadSHA: "3333333333333333333333333333333333333333", PullRequestUpdatedAt: timePtr(recentDeliveryTime().Add(time.Second)), BaseBranch: "main",
	})
	// Subscribe the automation to the merged event, so the merge delivers a
	// final turn rather than nothing.
	_, err := h.pool.Exec(ctx, `UPDATE automations SET github_event_triggers = $2 WHERE id = $1`,
		h.automation.ID, []string{string(models.AutomationGitHubEventPullRequestUpdated), string(models.AutomationGitHubEventPullRequestMerged)})
	require.NoError(t, err, "subscribe the automation to merges")
	h.automation.GitHubEventTriggers = append(h.automation.GitHubEventTriggers, models.AutomationGitHubEventPullRequestMerged)
	merged := h.deliver(t, automations.GitHubEventTriggerRequest{
		Event: models.AutomationGitHubEventPullRequestMerged, PullRequestAction: "closed",
		HeadSHA: "3333333333333333333333333333333333333333", PullRequestUpdatedAt: timePtr(recentDeliveryTime().Add(2 * time.Second)), BaseBranch: "main",
	})

	h.closePR(t, true)

	require.Equal(t, models.AutomationTargetLifecycleMerged, h.target(t).LifecycleState, "the target is merged")
	require.Equal(t, models.AutomationRunStatusSkipped, h.reload(t, comment.ID).Status, "an ordinary waiter is skipped")
	mergedRun := h.reload(t, merged.ID)
	require.Equal(t, models.AutomationRunStatusPending, mergedRun.Status, "the merged run keeps its final turn")
	generation, genErr := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, genErr, "reload generation")
	require.Equal(t, models.AutomationTargetSessionStatusActive, generation.Status, "the generation lives until the final turn runs")

	// The executing turn completes, and must NOT retire the generation: the
	// merged run still has to run on it.
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	result, completeErr := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, completeErr, "completion")
	require.True(t, result.Applied, "the first turn completes")
	require.False(t, result.GenerationRetired, "the generation waits for the merged final turn")
	generationID := h.activeGeneration(t, *h.run.TargetID).ID

	// The merged run dispatches onto the same generation, and its own
	// completion retires it.
	finalOutcome := h.dispatch(t, h.reload(t, merged.ID), models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, finalOutcome.Kind, "the merged run is the final turn")
	require.Equal(t, h.outcome.SessionID, finalOutcome.SessionID, "on the same session")
	require.Equal(t, generationID, h.activeGeneration(t, *h.run.TargetID).ID, "and the same generation")

	finalRun := h.reload(t, merged.ID)
	finalToken := uuid.New()
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET status = 'running', lock_token = $2, lease_expires_at = now() + interval '5 minutes' WHERE id = $1`, finalOutcome.JobID, finalToken)
	require.NoError(t, err, "lease the final turn's job")
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err, "begin claim")
	_, owned, err := h.runs.ClaimAttempt(ctx, tx, h.orgID, finalRun.ID, finalOutcome.JobID, finalToken)
	require.NoError(t, err, "claim the final attempt")
	require.NoError(t, tx.Commit(ctx), "commit claim")
	require.True(t, owned, "the lease holder owns the final attempt")
	finalRun = h.reload(t, merged.ID)
	marker := &models.AutomationRunResult{
		RunID: finalRun.ID, OrgID: h.orgID, Attempt: finalRun.Attempt, AttemptLockToken: finalToken,
		ThreadID: *finalRun.ThreadID, TurnNumber: *finalRun.TurnNumber, Outcome: models.AutomationRunResultTurnCompleted,
		ReviewComplete: true, NativeContext: true,
	}
	require.NoError(t, h.store.EndAttempt(ctx, func(ctx context.Context, tx pgx.Tx, sessions agent.SessionStore) error {
		if err := sessions.UpdateTurnComplete(ctx, h.orgID, h.outcome.SessionID, *finalRun.TurnNumber, nil, "agent-1", ""); err != nil {
			return err
		}
		_, err := h.store.WriteResult(ctx, tx, h.orgID, finalOutcome.JobID, marker)
		return err
	}), "end the final attempt")

	result, completeErr = h.completer.Complete(ctx, h.orgID, finalRun.ID, finalOutcome.JobID, finalToken)
	require.NoError(t, completeErr, "complete the final turn")
	require.True(t, result.Applied, "the final turn completes")
	require.True(t, result.GenerationRetired, "the merged pull request retires the generation after its final turn")
	require.Equal(t, models.AutomationTargetRetiredPRMerged, result.RetiredReason, "with pr_merged")
}

// TestAutomationLifecycle_ReopenedTarget proves a reopened pull request
// accepts work again: the target is open, the retired generation stays
// retired, and the next dispatch starts a new one.
func TestAutomationLifecycle_ReopenedTarget(t *testing.T) {
	h := newLifecycleHarness(t)
	ctx := context.Background()
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	_, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "finish the first turn")
	h.closePR(t, false)
	require.Equal(t, models.AutomationTargetLifecycleClosed, h.target(t).LifecycleState, "closed")
	firstGeneration := h.activeGenerationIDBefore(t, *h.run.TargetID)

	require.NoError(t, h.lifecycle.OnPullRequestReopened(ctx, h.orgID, "acme/web", 42, time.Now().UTC()), "notify reopened")
	require.Equal(t, models.AutomationTargetLifecycleOpen, h.target(t).LifecycleState, "the target is open again")

	next := h.push(t, "4444444444444444444444444444444444444444", time.Now().UTC().Truncate(time.Second))
	outcome := h.dispatch(t, next, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, outcome.Kind, "the reopened target takes work again")
	generation := h.activeGeneration(t, *h.run.TargetID)
	require.NotEqual(t, firstGeneration, generation.ID, "on a new generation")
	require.Equal(t, 2, generation.Generation, "the second generation")
}

// TestAutomationLifecycle_IsIdempotent proves a redelivered close changes
// nothing the first one did not already do.
func TestAutomationLifecycle_IsIdempotent(t *testing.T) {
	h := newLifecycleHarness(t)
	ctx := context.Background()
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	_, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "finish the turn")

	h.closePR(t, false)
	generation, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.Equal(t, models.AutomationTargetSessionStatusRetired, generation.Status, "retired by the close")
	retiredAt := generation.RetiredAt

	h.closePR(t, false)
	again, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.Equal(t, models.AutomationTargetRetiredPRClosed, *again.RetiredReason, "still pr_closed")
	require.Equal(t, retiredAt, again.RetiredAt, "the redelivery did not re-retire it")
}

// TestAutomationLifecycle_OwnedSessionGuard proves the ownership lookup the
// human-entry paths share: an owned session reports its target and reset
// link, and an ordinary session reports nothing.
func TestAutomationLifecycle_OwnedSessionGuard(t *testing.T) {
	h := newLifecycleHarness(t)
	ctx := context.Background()

	err := h.sessions.RejectIfAutomationOwned(ctx, h.orgID, h.outcome.SessionID)
	var owned *models.SessionAutomationOwnedError
	require.ErrorAs(t, err, &owned, "the generation's session is owned")
	require.Equal(t, *h.run.TargetID, owned.Owner.TargetID, "it names the target")
	require.Equal(t, h.automation.ID, owned.Owner.AutomationID, "and the automation")
	require.Equal(t, models.SessionAutomationResetURL(h.automation.ID, *h.run.TargetID), owned.Owner.ResetURL, "and the reset action")
	require.False(t, owned.Owner.ReleasePending, "no release is pending while the generation is active")

	// A retirement during the turn marks the release pending, which the
	// guard reports so the UI can say the session is about to come back.
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err, "begin retirement")
	_, err = h.targets.RetireGeneration(ctx, tx, h.orgID, h.generation(t).ID, models.AutomationTargetRetiredManualReset)
	require.NoError(t, err, "retire during the turn")
	require.NoError(t, tx.Commit(ctx), "commit retirement")
	err = h.sessions.RejectIfAutomationOwned(ctx, h.orgID, h.outcome.SessionID)
	require.ErrorAs(t, err, &owned, "still owned until the turn finishes")
	require.True(t, owned.Owner.ReleasePending, "with the release pending")

	// Once the turn completes, the session is an ordinary session again.
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	_, err = h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	require.NoError(t, h.sessions.RejectIfAutomationOwned(ctx, h.orgID, h.outcome.SessionID), "the released session accepts human turns again")
}

// TestAutomationLifecycle_ClosedDuringExecutionThenReopened proves a close
// that lands while a turn is executing still ends the generation, so a
// reopen starts a fresh one rather than resuming the conversation about a
// pull request that was closed in the meantime.
func TestAutomationLifecycle_ClosedDuringExecutionThenReopened(t *testing.T) {
	h := newLifecycleHarness(t)
	ctx := context.Background()
	firstGeneration := h.generation(t).ID

	h.closePR(t, false)
	require.NoError(t, h.lifecycle.OnPullRequestReopened(ctx, h.orgID, "acme/web", 42, time.Now().UTC()), "reopened before the turn finished")
	require.Equal(t, models.AutomationTargetLifecycleOpen, h.target(t).LifecycleState, "the target is open again")

	// The turn that was running when the close landed finishes. Its
	// generation was already retired by the close, so the reopened target
	// starts a new one instead of continuing the old conversation.
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	result, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	require.True(t, result.Applied, "the turn completes")
	generation, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.Equal(t, models.AutomationTargetSessionStatusRetired, generation.Status, "the close retired it, and the reopen did not revive it")
	require.Nil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session is handed back")

	next := h.push(t, "5555555555555555555555555555555555555555", time.Now().UTC().Truncate(time.Second))
	outcome := h.dispatch(t, next, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, outcome.Kind, "the reopened target takes work")
	require.NotEqual(t, firstGeneration, h.activeGeneration(t, *h.run.TargetID).ID, "on a new generation")
}

// TestAutomationLifecycle_NotifiesWithoutAPullRequestMirror proves the
// notification keys on the event's own repository and number: a pull
// request 143 never created has no mirror row, but its automation targets
// must still close with it.
func TestAutomationLifecycle_NotifiesWithoutAPullRequestMirror(t *testing.T) {
	h := newLifecycleHarness(t)
	ctx := context.Background()
	var mirrors int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM pull_requests WHERE org_id = $1`, h.orgID).Scan(&mirrors), "count mirrors")
	require.Zero(t, mirrors, "this target's pull request was never mirrored by 143")

	h.closePR(t, false)
	require.Equal(t, models.AutomationTargetLifecycleClosed, h.target(t).LifecycleState, "the target closed anyway")
}
