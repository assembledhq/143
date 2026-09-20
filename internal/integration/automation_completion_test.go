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

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/automations"
)

// completionHarness extends the turn harness with the completer.
type completionHarness struct {
	*turnHarness
	completer *automations.TurnCompleter
}

func newCompletionHarness(t *testing.T) *completionHarness {
	t.Helper()
	h := newTurnHarness(t)
	return &completionHarness{
		turnHarness: h,
		completer:   automations.NewTurnCompleter(h.pool, h.runs, h.targets, h.jobs, zerolog.Nop()),
	}
}

// endAttempt writes the marker for the harness run's current attempt in the
// same transaction as the session's turn-complete write, as the
// orchestrator does.
func (h *completionHarness) endAttempt(t *testing.T, outcome models.AutomationRunResultOutcome) {
	t.Helper()
	ctx := context.Background()
	marker := &models.AutomationRunResult{
		RunID: h.run.ID, OrgID: h.orgID, Attempt: h.run.Attempt, AttemptLockToken: h.lockToken,
		ThreadID: *h.run.ThreadID, TurnNumber: *h.run.TurnNumber, Outcome: outcome,
		ReviewComplete: outcome == models.AutomationRunResultTurnCompleted, NativeContext: true,
	}
	err := h.store.EndAttempt(ctx, func(ctx context.Context, tx pgx.Tx, sessions agent.SessionStore) error {
		if err := sessions.UpdateTurnComplete(ctx, h.orgID, h.outcome.SessionID, 1, nil, "agent-1", ""); err != nil {
			return err
		}
		written, err := h.store.WriteResult(ctx, tx, h.orgID, h.jobID, marker)
		if err != nil {
			return err
		}
		require.True(t, written, "the lease holder writes the marker")
		return nil
	})
	require.NoError(t, err, "attempt end commits")
}

func (h *completionHarness) target(t *testing.T) models.AutomationTarget {
	t.Helper()
	target, err := h.targets.GetByID(context.Background(), h.orgID, *h.run.TargetID)
	require.NoError(t, err, "reload target")
	return target
}

// wakeJobs returns the status and run_at of every wake job for the target.
func (h *completionHarness) wakeJobs(t *testing.T) []completionJobRow {
	t.Helper()
	return h.jobsByDedupe(t, automations.AutomationTargetWakeDedupePrefix+h.run.TargetID.String())
}

type completionJobRow struct {
	ID     uuid.UUID
	Status string
	RunAt  time.Time
}

func (h *completionHarness) jobsByDedupe(t *testing.T, dedupe string) []completionJobRow {
	t.Helper()
	rows, err := h.pool.Query(context.Background(), `SELECT id, status, run_at FROM jobs WHERE org_id = $1 AND dedupe_key = $2 ORDER BY created_at`, h.orgID, dedupe)
	require.NoError(t, err, "list jobs by dedupe key")
	defer rows.Close()
	var out []completionJobRow
	for rows.Next() {
		var row completionJobRow
		require.NoError(t, rows.Scan(&row.ID, &row.Status, &row.RunAt), "scan job")
		out = append(out, row)
	}
	return out
}

func (h *completionHarness) threadStatus(t *testing.T) string {
	t.Helper()
	var status string
	require.NoError(t, h.pool.QueryRow(context.Background(), `SELECT status FROM session_threads WHERE id = $1`, *h.run.ThreadID).Scan(&status), "thread status")
	return status
}

// deadLetterJob puts the harness job in the state the worker leaves after
// exhausting retries: terminal, lease cleared.
func (h *completionHarness) deadLetterJob(t *testing.T) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(), `UPDATE jobs SET status = 'dead_letter', lock_token = NULL, lease_expires_at = NULL, completed_at = now() WHERE id = $1`, h.jobID)
	require.NoError(t, err, "dead-letter the job")
}

// TestAutomationCompletion_CompleteFromMarker proves completion from a
// turn_completed marker: the run's terminal record, the generation's
// counters and baseline, the ownership release, the wake outbox with its
// job, the fence against another lease, and idempotency.
func TestAutomationCompletion_CompleteFromMarker(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()

	result, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion without a marker should not error")
	require.False(t, result.Applied, "nothing completes before the attempt ended")

	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	result, err = h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, uuid.New())
	require.NoError(t, err, "another lease should not error")
	require.False(t, result.Applied, "the fence rejects another lease while the job runs")
	require.Equal(t, models.AutomationRunStatusRunning, h.reload(t, h.run.ID).Status, "the run is untouched")

	result, err = h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	require.True(t, result.Applied, "the lease holder completes the run")
	require.Equal(t, models.AutomationRunOutcomeTurnCompleted, result.Outcome, "outcome reported")
	require.False(t, result.GenerationRetired, "a completed review keeps the generation")
	run := h.reload(t, h.run.ID)
	require.Equal(t, models.AutomationRunStatusCompleted, run.Status, "run status")
	require.Equal(t, models.AutomationRunOutcomeTurnCompleted, *run.OutcomeReason, "outcome reason")
	require.Equal(t, models.AutomationRunDispatchDone, *run.DispatchState, "dispatch done")
	require.NotNil(t, run.CompletedAt, "completed_at set")
	require.NotNil(t, run.NativeContext, "native context recorded")
	require.True(t, *run.NativeContext, "native context copied from the marker")
	generation := h.generation(t)
	require.Equal(t, 1, generation.TurnCount, "the turn counted")
	require.Equal(t, h.run.ID, *generation.LastRunID, "last run recorded")
	require.NotNil(t, generation.LastTurnAt, "last turn time recorded")
	require.Equal(t, "1111111111111111111111111111111111111111", *generation.LastReviewedHeadSHA, "the baseline advanced to the reviewed head")
	require.Equal(t, *h.run.HeadEpoch, generation.LastReviewedEpoch, "the baseline epoch is the run's epoch")
	require.NotNil(t, h.target(t).WakeRequestedAt, "a wake is requested")
	jobs := h.wakeJobs(t)
	require.Len(t, jobs, 1, "one wake job is enqueued with the completion")
	require.Equal(t, "pending", jobs[0].Status, "the wake job is runnable")
	require.NotNil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session stays owned by the active generation")

	result, err = h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "second completion")
	require.False(t, result.Applied, "completion is idempotent")
	require.Equal(t, 1, h.generation(t).TurnCount, "the turn is not counted twice")
	require.Len(t, h.wakeJobs(t), 1, "no second wake job")
}

// TestAutomationCompletion_BaselineMonotonicity proves the baseline never
// moves backwards or on a null epoch, and never on a retired generation,
// while the turn still counts.
func TestAutomationCompletion_BaselineMonotonicity(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, h *completionHarness)
	}{
		{name: "a higher recorded epoch keeps the baseline", prepare: func(t *testing.T, h *completionHarness) {
			_, err := h.pool.Exec(context.Background(), `UPDATE automation_target_sessions SET last_reviewed_head_sha = 'newer', last_reviewed_epoch = 9 WHERE id = $1`, h.generation(t).ID)
			require.NoError(t, err, "record a newer baseline")
		}},
		{name: "a null run epoch keeps the baseline", prepare: func(t *testing.T, h *completionHarness) {
			_, err := h.pool.Exec(context.Background(), `UPDATE automation_target_sessions SET last_reviewed_head_sha = 'newer', last_reviewed_epoch = 1 WHERE id = $1`, h.generation(t).ID)
			require.NoError(t, err, "record a baseline")
			_, err = h.pool.Exec(context.Background(), `UPDATE automation_runs SET head_epoch = NULL WHERE id = $1`, h.run.ID)
			require.NoError(t, err, "strip the run's epoch")
		}},
		{name: "a retired generation keeps the baseline", prepare: func(t *testing.T, h *completionHarness) {
			ctx := context.Background()
			_, err := h.pool.Exec(ctx, `UPDATE automation_target_sessions SET last_reviewed_head_sha = 'newer', last_reviewed_epoch = 0 WHERE id = $1`, h.generation(t).ID)
			require.NoError(t, err, "record a baseline")
			tx, err := h.pool.Begin(ctx)
			require.NoError(t, err, "begin retirement")
			_, err = h.targets.RetireGeneration(ctx, tx, h.orgID, h.generation(t).ID, models.AutomationTargetRetiredPRClosed)
			require.NoError(t, err, "retire during execution")
			require.NoError(t, tx.Commit(ctx), "commit retirement")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newCompletionHarness(t)
			ctx := context.Background()
			generationID := h.generation(t).ID
			tt.prepare(t, h)
			h.endAttempt(t, models.AutomationRunResultTurnCompleted)
			result, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
			require.NoError(t, err, "completion")
			require.True(t, result.Applied, "the run completes")
			generation, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
			require.NoError(t, err, "reload generation")
			require.Equal(t, generationID, generation.ID, "same generation")
			require.Equal(t, "newer", *generation.LastReviewedHeadSHA, "the baseline did not move")
			require.Equal(t, 1, generation.TurnCount, "the turn still counted")
			require.Equal(t, h.run.ID, *generation.LastRunID, "last run still recorded")
		})
	}
}

// TestAutomationCompletion_AwaitingInputRetiresGeneration proves an
// awaiting_input marker ends the run as failed/awaiting_input, retires the
// generation, and releases the session so a person can answer in it.
func TestAutomationCompletion_AwaitingInputRetiresGeneration(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	h.endAttempt(t, models.AutomationRunResultAwaitingInput)
	result, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	require.True(t, result.Applied, "the run completes")
	require.True(t, result.GenerationRetired, "the generation is retired")
	run := h.reload(t, h.run.ID)
	require.Equal(t, models.AutomationRunStatusFailed, run.Status, "awaiting input is a failed run")
	require.Equal(t, models.AutomationRunOutcomeAwaitingInput, *run.OutcomeReason, "outcome reason")
	generation, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.Equal(t, models.AutomationTargetSessionStatusRetired, generation.Status, "generation retired")
	require.Equal(t, models.AutomationTargetRetiredAwaitingInput, *generation.RetiredReason, "retired reason")
	require.Equal(t, 1, generation.TurnCount, "the turn counted before retirement")
	require.Nil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session is no longer owned")
	require.Equal(t, models.SessionStatusIdle, h.session(t).Status, "the session is idle for the person")
	require.Len(t, h.wakeJobs(t), 1, "one wake job")
}

// TestAutomationCompletion_PendingOwnershipRelease proves a retirement that
// happened during execution is applied by the completion: the owner marker
// clears once the executing run is done.
func TestAutomationCompletion_PendingOwnershipRelease(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err, "begin retirement")
	retired, err := h.targets.RetireGeneration(ctx, tx, h.orgID, h.generation(t).ID, models.AutomationTargetRetiredPRClosed)
	require.NoError(t, err, "retire during execution")
	require.NoError(t, tx.Commit(ctx), "commit retirement")
	require.True(t, retired.OwnershipReleasePending, "the release waits for the executing run")
	require.NotNil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session stays owned while the turn executes")

	h.endAttempt(t, models.AutomationRunResultAgentFailed)
	result, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	require.True(t, result.Applied, "the run completes")
	require.Equal(t, models.AutomationRunOutcomeAgentFailed, result.Outcome, "agent failure recorded")
	require.Equal(t, models.AutomationRunStatusFailed, h.reload(t, h.run.ID).Status, "failed run")
	require.Nil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the pending release cleared the marker")
	generation, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.False(t, generation.OwnershipReleasePending, "the release is no longer pending")
}

// TestAutomationCompletion_TerminalJobRecovery proves a run whose job died
// is settled: with no marker it fails with retries_exhausted and releases
// the session and thread; with a marker it completes without a lease. The
// scheduler sweep finds both.
func TestAutomationCompletion_TerminalJobRecovery(t *testing.T) {
	t.Run("no marker fails with retries exhausted", func(t *testing.T) {
		h := newCompletionHarness(t)
		ctx := context.Background()
		failed, err := h.completer.FailRetriesExhausted(ctx, h.orgID, h.run.ID)
		require.NoError(t, err, "retries exhausted with a live job")
		require.False(t, failed, "a live job is not exhausted")

		h.deadLetterJob(t)
		outcome, err := h.completer.RecoverTerminalJob(ctx, h.orgID, h.run.ID, h.jobID)
		require.NoError(t, err, "recover")
		require.Equal(t, models.AutomationRunOutcomeRetriesExhausted, outcome, "no marker means retries exhausted")
		run := h.reload(t, h.run.ID)
		require.Equal(t, models.AutomationRunStatusFailed, run.Status, "failed run")
		require.Equal(t, models.AutomationRunDispatchDone, *run.DispatchState, "dispatch done")
		require.Equal(t, models.SessionStatusIdle, h.session(t).Status, "the owned session returns to idle")
		require.Equal(t, "idle", h.threadStatus(t), "the primary thread returns to idle")
		require.Equal(t, 0, h.generation(t).TurnCount, "no turn counted")
		require.NotNil(t, h.target(t).WakeRequestedAt, "a wake is requested")
		require.Len(t, h.wakeJobs(t), 1, "one wake job")
		outcome, err = h.completer.RecoverTerminalJob(ctx, h.orgID, h.run.ID, h.jobID)
		require.NoError(t, err, "second recover")
		require.Empty(t, outcome, "recovery is idempotent")
	})

	t.Run("a marker completes without a lease through the sweep", func(t *testing.T) {
		h := newCompletionHarness(t)
		ctx := context.Background()
		h.endAttempt(t, models.AutomationRunResultTurnCompleted)
		h.deadLetterJob(t)
		report, err := h.completer.Sweep(ctx, h.orgID)
		require.NoError(t, err, "sweep")
		require.Equal(t, 1, report.RecoveredCompletions, "the sweep completed the run from its marker")
		require.Equal(t, 0, report.RetriesExhausted, "nothing was exhausted")
		run := h.reload(t, h.run.ID)
		require.Equal(t, models.AutomationRunStatusCompleted, run.Status, "completed run")
		require.Equal(t, models.AutomationRunOutcomeTurnCompleted, *run.OutcomeReason, "outcome from the marker")
		require.Equal(t, 1, h.generation(t).TurnCount, "the turn counted")
		orgs, err := h.completer.ListOrgsWithWork(ctx)
		require.NoError(t, err, "list orgs")
		require.Contains(t, orgs, h.orgID, "the org still has an outstanding wake request")
	})
}

// TestAutomationCompletion_WakeNudgesNextWaiter proves the wake makes the
// next waiter's automation_run job runnable now, enqueues a fresh job when
// the old one is gone, and clears the outbox marker it observed.
func TestAutomationCompletion_WakeNudgesNextWaiter(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	run2 := h.push(t, "2222222222222222222222222222222222222222", recentDeliveryTime().Add(time.Second))
	waiting := h.dispatch(t, run2, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchWaiting, waiting.Kind, "the second run waits behind the executing turn")
	runDedupe := "automation_run:" + run2.ID.String()
	_, err := h.pool.Exec(ctx, `UPDATE jobs SET run_at = now() + interval '30 seconds' WHERE org_id = $1 AND dedupe_key = $2`, h.orgID, runDedupe)
	require.NoError(t, err, "put the waiter's job into its poll backoff")

	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	_, err = h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	outcome, err := h.completer.Wake(ctx, h.orgID, *h.run.TargetID)
	require.NoError(t, err, "wake")
	require.NotNil(t, outcome.Nudged, "a waiter was nudged")
	require.Equal(t, run2.ID, *outcome.Nudged, "the next waiter is the second run")
	require.False(t, outcome.Enqueued, "its own job was rescheduled")
	require.False(t, outcome.Requeue, "no newer request arrived")
	jobs := h.jobsByDedupe(t, runDedupe)
	require.Len(t, jobs, 1, "still one automation_run job for the waiter")
	require.False(t, jobs[0].RunAt.After(time.Now()), "the job is runnable now")
	require.Nil(t, h.target(t).WakeRequestedAt, "the observed wake request is cleared")

	// The waiter's job is gone: the wake enqueues a fresh one.
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET status = 'dead_letter' WHERE org_id = $1 AND dedupe_key = $2`, h.orgID, runDedupe)
	require.NoError(t, err, "lose the waiter's job")
	outcome, err = h.completer.Wake(ctx, h.orgID, *h.run.TargetID)
	require.NoError(t, err, "wake again")
	require.Equal(t, run2.ID, *outcome.Nudged, "the same waiter")
	require.True(t, outcome.Enqueued, "a fresh automation_run job was enqueued")
	jobs = h.jobsByDedupe(t, runDedupe)
	require.Len(t, jobs, 2, "the fresh job joins the dead one")
	require.Equal(t, "pending", jobs[1].Status, "the fresh job is pending")
	payload := h.jobPayload(t, jobs[1].ID)
	require.Equal(t, models.JobTypeAutomationRun, payload["__job_type"], "job type")
	require.Equal(t, run2.ID.String(), payload["automation_run_id"], "the payload names the run")
	require.Equal(t, h.automation.ID.String(), payload["automation_id"], "the payload names the automation")

	// A wake with nothing waiting clears the marker and nudges nothing.
	_, err = h.pool.Exec(ctx, `UPDATE automation_runs SET status = 'skipped', dispatch_state = 'done', outcome_reason = 'pr_closed', completed_at = now() WHERE id = $1`, run2.ID)
	require.NoError(t, err, "take the waiter out of the queue")
	requestedAt, err := h.targets.RequestWake(ctx, nil, h.orgID, *h.run.TargetID)
	require.NoError(t, err, "request a wake")
	require.False(t, requestedAt.IsZero(), "request recorded")
	outcome, err = h.completer.Wake(ctx, h.orgID, *h.run.TargetID)
	require.NoError(t, err, "idle wake")
	require.Nil(t, outcome.Nudged, "nothing waits")
	require.Nil(t, h.target(t).WakeRequestedAt, "the marker is cleared")
}

// TestAutomationCompletion_WakeRetriesPendingResolution proves a target
// with only ambiguous candidates and a pending resolution nudges a
// candidate's job so the dispatch-time lookup runs again.
func TestAutomationCompletion_WakeRetriesPendingResolution(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	tie := recentDeliveryTime().Add(time.Minute)
	runA := h.push(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", tie)
	runB := h.push(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", tie)
	require.Equal(t, models.AutomationRunHeadAmbiguous, *runB.HeadResolution, "the tie is ambiguous")
	require.True(t, h.target(t).HeadResolutionPending, "resolution pending")
	_, err := h.pool.Exec(ctx, `UPDATE automation_runs SET status = 'skipped', dispatch_state = 'done', outcome_reason = 'superseded', completed_at = now() WHERE id = $1`, runA.ID)
	require.NoError(t, err, "take the authoritative candidate out of the way")
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET run_at = now() + interval '10 minutes' WHERE org_id = $1 AND dedupe_key = $2`, h.orgID, "automation_run:"+runB.ID.String())
	require.NoError(t, err, "back the candidate's job off")
	outcome, err := h.completer.Wake(ctx, h.orgID, *h.run.TargetID)
	require.NoError(t, err, "wake")
	require.NotNil(t, outcome.Nudged, "an ambiguous candidate is nudged for the lookup")
	require.Equal(t, runB.ID, *outcome.Nudged, "the ambiguous candidate")
	jobs := h.jobsByDedupe(t, "automation_run:"+runB.ID.String())
	require.Len(t, jobs, 1, "the candidate's job")
	require.False(t, jobs[0].RunAt.After(time.Now()), "the candidate's job is runnable now")
}

// TestAutomationCompletion_WaitTimeoutAndAmbiguityDeadline proves the wait
// sweep fails only non-ambiguous waiters past two hours, and the ambiguity
// deadline converts ambiguous candidates to unresolved with a restarted
// wait clock, both waking the target.
func TestAutomationCompletion_WaitTimeoutAndAmbiguityDeadline(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	tie := recentDeliveryTime().Add(time.Minute)
	runA := h.push(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", tie)
	runB := h.push(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", tie)
	require.Equal(t, models.AutomationRunHeadAuthoritative, *runA.HeadResolution, "first candidate is authoritative")
	require.Equal(t, models.AutomationRunHeadAmbiguous, *runB.HeadResolution, "second candidate is ambiguous")
	// With a tie pending and no head lookup available, dispatch holds the
	// authoritative candidate with backoff; it stays a waiting run.
	h.dispatch(t, runA, models.AgentTypeCodex)
	require.Equal(t, models.AutomationRunDispatchWaiting, *h.reload(t, runA.ID).DispatchState, "the authoritative candidate is waiting")
	_, err := h.pool.Exec(ctx, `UPDATE automation_runs SET wait_started_at = now() - interval '3 hours' WHERE id = ANY($1)`, []uuid.UUID{runA.ID, runB.ID})
	require.NoError(t, err, "age both waits")
	_, err = h.pool.Exec(ctx, `DELETE FROM jobs WHERE org_id = $1 AND dedupe_key = $2`, h.orgID, automations.AutomationTargetWakeDedupePrefix+h.run.TargetID.String())
	require.NoError(t, err, "start with no wake job")

	report, err := h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep")
	require.EqualValues(t, 1, report.WaitTimeouts, "only the non-ambiguous waiter timed out")
	require.Equal(t, 0, report.AmbiguityDeadlines, "the deadline has not passed")
	runA = h.reload(t, runA.ID)
	require.Equal(t, models.AutomationRunStatusSkipped, runA.Status, "wait timeout skips the run")
	require.Equal(t, models.AutomationRunOutcomeWaitTimeout, *runA.OutcomeReason, "outcome wait_timeout")
	require.Equal(t, models.AutomationRunDispatchDone, *runA.DispatchState, "dispatch done")
	runB = h.reload(t, runB.ID)
	require.Equal(t, models.AutomationRunStatusPending, runB.Status, "the ambiguous candidate is untouched by the wait sweep")
	require.Len(t, h.wakeJobs(t), 1, "the timeout woke the target")

	_, err = h.pool.Exec(ctx, `UPDATE automation_targets SET head_resolution_deadline_at = now() - interval '1 minute' WHERE id = $1`, *h.run.TargetID)
	require.NoError(t, err, "expire the ambiguity deadline")
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET status = 'succeeded' WHERE org_id = $1 AND dedupe_key = $2`, h.orgID, automations.AutomationTargetWakeDedupePrefix+h.run.TargetID.String())
	require.NoError(t, err, "consume the earlier wake job")
	report, err = h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep after the deadline")
	require.Equal(t, 1, report.AmbiguityDeadlines, "the deadline resolved")
	require.EqualValues(t, 0, report.WaitTimeouts, "the converted candidate's clock restarted")
	runB = h.reload(t, runB.ID)
	require.Equal(t, models.AutomationRunHeadUnresolved, *runB.HeadResolution, "the candidate is unresolved")
	require.Nil(t, runB.HeadEpoch, "with a null epoch")
	require.Equal(t, models.AutomationRunStatusPending, runB.Status, "and still pending")
	require.WithinDuration(t, time.Now(), *runB.WaitStartedAt, time.Minute, "its wait clock restarted")
	target := h.target(t)
	require.False(t, target.HeadResolutionPending, "resolution is no longer pending")
	require.Nil(t, target.HeadResolutionDeadlineAt, "the deadline is cleared")
	jobs := h.wakeJobs(t)
	require.Len(t, jobs, 2, "the deadline woke the target again")
	require.Equal(t, "pending", jobs[1].Status, "the new wake job is pending")

	// The converted candidate is now an ordinary dispatchable waiter.
	next, err := h.runs.NextWaiting(ctx, nil, h.orgID, *h.run.TargetID)
	require.NoError(t, err, "next waiting")
	require.Equal(t, runB.ID, next.ID, "the unresolved candidate dispatches next")
}

// TestAutomationCompletion_ReconcileWakes proves reconciliation enqueues a
// wake for a target with waiters and no executing run when its wake job is
// missing or its request is stale, and leaves busy targets alone.
func TestAutomationCompletion_ReconcileWakes(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	run2 := h.push(t, "2222222222222222222222222222222222222222", recentDeliveryTime().Add(time.Second))
	waiting := h.dispatch(t, run2, models.AgentTypeCodex)
	require.Equal(t, automations.DispatchWaiting, waiting.Kind, "the second run waits")

	report, err := h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep while busy")
	require.Equal(t, 0, report.WakesReconciled, "a busy target is not woken")

	// The completion woke the target, but the wake job was lost.
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	_, err = h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	_, err = h.pool.Exec(ctx, `DELETE FROM jobs WHERE org_id = $1 AND dedupe_key = $2`, h.orgID, automations.AutomationTargetWakeDedupePrefix+h.run.TargetID.String())
	require.NoError(t, err, "lose the wake job")
	report, err = h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep with a lost job")
	require.Equal(t, 1, report.WakesReconciled, "the lost wake is re-enqueued")
	jobs := h.wakeJobs(t)
	require.Len(t, jobs, 1, "one wake job again")
	require.Equal(t, "pending", jobs[0].Status, "pending")

	// A pending wake job with a fresh request needs nothing.
	report, err = h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep with a pending job")
	require.Equal(t, 0, report.WakesReconciled, "a pending wake job satisfies the target")

	// A stale request with a stuck job is woken again.
	_, err = h.pool.Exec(ctx, `UPDATE automation_targets SET wake_requested_at = now() - interval '5 minutes' WHERE id = $1`, *h.run.TargetID)
	require.NoError(t, err, "age the request")
	report, err = h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep with a stale request")
	require.Equal(t, 1, report.WakesReconciled, "a stale request is reconciled")
	require.WithinDuration(t, time.Now(), *h.target(t).WakeRequestedAt, time.Minute, "the request is refreshed")
}

// TestAutomationCompletion_RescheduleActiveByDedupeKey proves the job nudge
// only touches a pending job whose run_at is in the future.
func TestAutomationCompletion_RescheduleActiveByDedupeKey(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	dedupe := "nudge:" + uuid.NewString()
	jobID, err := h.jobs.Enqueue(ctx, h.orgID, "default", models.JobTypeAutomationRun, map[string]string{"org_id": h.orgID.String()}, 5, &dedupe)
	require.NoError(t, err, "enqueue")
	moved, err := h.jobs.RescheduleActiveByDedupeKey(ctx, h.orgID, dedupe)
	require.NoError(t, err, "reschedule a runnable job")
	require.False(t, moved, "a runnable job is left alone")
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET run_at = now() + interval '1 hour' WHERE id = $1`, jobID)
	require.NoError(t, err, "back the job off")
	moved, err = h.jobs.RescheduleActiveByDedupeKey(ctx, h.orgID, dedupe)
	require.NoError(t, err, "reschedule a backed-off job")
	require.True(t, moved, "the backed-off job is made runnable")
	jobs := h.jobsByDedupe(t, dedupe)
	require.False(t, jobs[0].RunAt.After(time.Now()), "run_at is now")
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET status = 'running', run_at = now() + interval '1 hour' WHERE id = $1`, jobID)
	require.NoError(t, err, "mark the job running")
	moved, err = h.jobs.RescheduleActiveByDedupeKey(ctx, h.orgID, dedupe)
	require.NoError(t, err, "reschedule a running job")
	require.False(t, moved, "a running job is not rescheduled")
	_ = db.ErrAutomationRunNotFound
}
