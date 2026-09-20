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

	"github.com/assembledhq/143/internal/cluster"
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

// writeThreadTurn writes the turn's thread result the way the worker does:
// in a transaction, under the run row lock and the attempt fence.
func (h *completionHarness) writeThreadTurn(t *testing.T, lockToken uuid.UUID, result *models.SessionResult) (bool, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err, "begin thread write")
	defer func() { _ = tx.Rollback(ctx) }()
	written, err := h.runs.CompleteThreadTurnForAttempt(ctx, tx, h.orgID, h.run.ID, lockToken, *h.run.ThreadID, *h.run.TurnNumber, result, "agent-1")
	if err != nil || !written {
		return written, err
	}
	return true, tx.Commit(ctx)
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
	require.Equal(t, "idle", h.threadStatus(t), "the primary thread is released for the next turn")
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
		failed, err := h.completer.FailRetriesExhausted(ctx, h.orgID, h.run.ID, time.Now())
		require.NoError(t, err, "retries exhausted with a live job")
		require.False(t, failed, "a live lease is never abandoned")

		h.deadLetterJob(t)
		outcome, err := h.completer.RecoverAbandonedRun(ctx, h.orgID, h.run.ID, h.jobID, time.Now())
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
		outcome, err = h.completer.RecoverAbandonedRun(ctx, h.orgID, h.run.ID, h.jobID, time.Now())
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
	require.Equal(t, models.AutomationRunStatusFailed, runA.Status, "a wait timeout is a failed run, as the shared outcome mapping says")
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

// TestAutomationCompletion_ReleasesThreadForTheNextTurn proves a completed
// turn leaves the primary thread claimable: the next waiter dispatches
// instead of waiting for a thread that stayed running. This is the state a
// continued turn reaches when its handler returns before writing the
// thread, so the completion itself has to be the durable release.
func TestAutomationCompletion_ReleasesThreadForTheNextTurn(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	head2 := "2222222222222222222222222222222222222222"
	run2 := h.push(t, head2, recentDeliveryTime().Add(time.Second))
	require.Equal(t, automations.DispatchWaiting, h.dispatch(t, run2, models.AgentTypeCodex).Kind, "the second run waits")

	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	// The orchestrator wrote the session and the marker; the worker died
	// before its own thread write, so the thread is still running.
	_, err := h.pool.Exec(ctx, `UPDATE session_threads SET status = 'running' WHERE id = $1`, *h.run.ThreadID)
	require.NoError(t, err, "leave the thread running")
	result, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	require.True(t, result.Applied, "the run completes")
	require.Equal(t, "idle", h.threadStatus(t), "the completion released the thread")

	var currentTurn int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT current_turn FROM session_threads WHERE id = $1`, *h.run.ThreadID).Scan(&currentTurn), "thread turn")
	require.Equal(t, *h.run.TurnNumber, currentTurn, "the thread's turn advanced to the marker's turn, so the next message does not reuse it")

	// The next waiter now dispatches rather than waiting on the thread.
	outcome := h.dispatch(t, h.reload(t, run2.ID), models.AgentTypeCodex)
	require.Equal(t, automations.DispatchReserved, outcome.Kind, "the next turn is reserved on the freed thread")
	require.Equal(t, h.outcome.SessionID, outcome.SessionID, "on the same generation session")
}

// TestAutomationCompletion_RichThreadWriteSurvivesCompletion proves the
// completion does not overwrite the handler's summary and diff when the
// ordinary path already wrote them and left the thread idle.
func TestAutomationCompletion_RichThreadWriteSurvivesCompletion(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	summary := "reviewed the diff"
	diff := "--- a\n+++ b\n"
	require.NoError(t, db.NewSessionThreadStore(h.pool).UpdateTurnComplete(ctx, h.orgID, *h.run.ThreadID, *h.run.TurnNumber,
		&models.SessionResult{ResultSummary: &summary, Diff: &diff}, "agent-1"), "the handler writes the thread result")

	result, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	require.True(t, result.Applied, "the run completes")
	var storedSummary, storedDiff *string
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT result_summary, diff FROM session_threads WHERE id = $1`, *h.run.ThreadID).Scan(&storedSummary, &storedDiff), "thread result")
	require.Equal(t, summary, *storedSummary, "the handler's summary survives")
	require.Equal(t, diff, *storedDiff, "the handler's diff survives")
}

// TestAutomationCompletion_AwaitingInputWithOutstandingHold proves a worker
// that died between its awaiting_input marker and its deferred hold release
// does not leave the session automation-owned forever: the completion
// clears the stale hold, so the retirement releases ownership at once and a
// person can answer in the session.
func TestAutomationCompletion_AwaitingInputWithOutstandingHold(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	h.endAttempt(t, models.AutomationRunResultAwaitingInput)
	_, err := h.pool.Exec(ctx, `UPDATE sessions SET turn_holding_container = TRUE, container_id = 'container-1' WHERE id = $1`, h.outcome.SessionID)
	require.NoError(t, err, "leave the turn hold outstanding, as a crashed worker does")
	h.deadLetterJob(t)

	outcome, err := h.completer.RecoverAbandonedRun(ctx, h.orgID, h.run.ID, h.jobID, time.Now())
	require.NoError(t, err, "recover")
	require.Equal(t, models.AutomationRunOutcomeAwaitingInput, outcome, "the marker's outcome is recorded")
	generation, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.Equal(t, models.AutomationTargetSessionStatusRetired, generation.Status, "the generation is retired")
	require.False(t, generation.OwnershipReleasePending, "the release did not stay pending behind the stale hold")
	require.Nil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session is no longer owned, so a person can answer")
	var holding bool
	var containerID *string
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT turn_holding_container, container_id FROM sessions WHERE id = $1`, h.outcome.SessionID).Scan(&holding, &containerID), "session hold")
	require.False(t, holding, "the stale hold is cleared")
	require.Equal(t, "container-1", *containerID, "the container stays recorded for the next turn's inherited-container path")
}

// TestAutomationCompletion_StrandedOwnershipRelease proves the sweep
// releases an ownership release that no executing run remains to apply,
// whatever stranded it.
func TestAutomationCompletion_StrandedOwnershipRelease(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	generationID := h.generation(t).ID
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	_, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	// The run is done, so nothing is executing; strand the release as a
	// crash between a retirement and the run's completion would.
	_, err = h.pool.Exec(ctx, `UPDATE automation_target_sessions SET status = 'retired', retired_reason = 'pr_closed', retired_at = now(), ownership_release_pending = true WHERE id = $1`, generationID)
	require.NoError(t, err, "strand the release")
	_, err = h.pool.Exec(ctx, `UPDATE sessions SET turn_holding_container = TRUE WHERE id = $1`, h.outcome.SessionID)
	require.NoError(t, err, "leave a stale hold behind too")

	report, err := h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep")
	require.Equal(t, 1, report.OwnershipReleases, "the stranded release is applied")
	require.Nil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session is released")
	var holding bool
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT turn_holding_container FROM sessions WHERE id = $1`, h.outcome.SessionID).Scan(&holding), "session hold")
	require.False(t, holding, "the stale hold is cleared with it")
	generation, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.False(t, generation.OwnershipReleasePending, "the pending flag is cleared")

	report, err = h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "second sweep")
	require.Equal(t, 0, report.OwnershipReleases, "nothing is left to release")
}

// TestAutomationCompletion_StrandedOwnershipWaitsForAnExecutingRun proves
// the reconciliation does not race an executing run: a release pending
// behind a live turn is left for that turn's completion.
func TestAutomationCompletion_StrandedOwnershipWaitsForAnExecutingRun(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err, "begin retirement")
	retired, err := h.targets.RetireGeneration(ctx, tx, h.orgID, h.generation(t).ID, models.AutomationTargetRetiredPRClosed)
	require.NoError(t, err, "retire while the turn executes")
	require.NoError(t, tx.Commit(ctx), "commit retirement")
	require.True(t, retired.OwnershipReleasePending, "the release waits for the executing run")

	report, err := h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep")
	require.Equal(t, 0, report.OwnershipReleases, "an executing run still owns the release")
	require.NotNil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session stays owned while the turn runs")
}

// TestAutomationCompletion_StaleAttemptWithoutADeadJob proves a worker that
// vanished without dead-lettering its job does not hold its target forever:
// once the attempt is older than the stale bound and no lease is live, the
// sweep settles it and releases the session and thread.
func TestAutomationCompletion_StaleAttemptWithoutADeadJob(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()

	report, err := h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep with a live lease")
	require.Equal(t, 0, report.RetriesExhausted, "a live lease is never abandoned")
	require.Equal(t, models.AutomationRunStatusRunning, h.reload(t, h.run.ID).Status, "the run is untouched")

	// The lease expires and the attempt ages past the bound; the job row
	// still says running, which is what a vanished worker leaves behind.
	_, err = h.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = now() - interval '5 minutes' WHERE id = $1`, h.jobID)
	require.NoError(t, err, "expire the lease")
	_, err = h.pool.Exec(ctx, `UPDATE automation_runs SET attempt_started_at = now() - interval '3 hours' WHERE id = $1`, h.run.ID)
	require.NoError(t, err, "age the attempt")

	report, err = h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep after the bound")
	require.Equal(t, 1, report.RetriesExhausted, "the abandoned attempt is settled")
	run := h.reload(t, h.run.ID)
	require.Equal(t, models.AutomationRunStatusFailed, run.Status, "failed run")
	require.Equal(t, models.AutomationRunOutcomeRetriesExhausted, *run.OutcomeReason, "retries exhausted")
	require.Equal(t, models.SessionStatusIdle, h.session(t).Status, "the session is released")
	require.Equal(t, "idle", h.threadStatus(t), "the thread is released")
}

// TestAutomationCompletion_StuckRunReaperLeavesPerTargetRuns proves the
// legacy reaper no longer terminalizes a per-target run behind the
// completer's back, which would strand its session, thread, and ownership.
func TestAutomationCompletion_StuckRunReaperLeavesPerTargetRuns(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	run2 := h.push(t, "2222222222222222222222222222222222222222", recentDeliveryTime().Add(time.Second))
	require.Equal(t, automations.DispatchWaiting, h.dispatch(t, run2, models.AgentTypeCodex).Kind, "the second run waits")
	_, err := h.pool.Exec(ctx, `UPDATE automation_runs SET triggered_at = now() - interval '5 hours', attempt_started_at = now() - interval '5 hours' WHERE org_id = $1`, h.orgID)
	require.NoError(t, err, "age every run past the reaper threshold")

	reaped, err := h.runs.ReapStuckRuns(ctx, h.orgID, time.Hour)
	require.NoError(t, err, "reap")
	require.EqualValues(t, 0, reaped, "neither the executing nor the waiting per-target run is reaped")
	require.Equal(t, models.AutomationRunStatusRunning, h.reload(t, h.run.ID).Status, "the executing run keeps its session and thread")
	require.Equal(t, models.AutomationRunStatusPending, h.reload(t, run2.ID).Status, "the waiting run keeps its place in the queue")
	require.Equal(t, models.AutomationRunDispatchExecuting, *h.reload(t, h.run.ID).DispatchState, "dispatch state is untouched")
}

// TestAutomationCompletion_SerializesWithRetirement proves completion takes
// the target lock first, like arrival, dispatch, and retirement do, so a
// concurrent lifecycle change blocks it rather than deadlocking with it.
func TestAutomationCompletion_SerializesWithRetirement(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)

	// A reset holds the target lock and has already touched the generation.
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err, "begin retirement")
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = h.targets.RetireGeneration(ctx, tx, h.orgID, h.generation(t).ID, models.AutomationTargetRetiredManualReset)
	require.NoError(t, err, "retire under the target lock")

	done := make(chan error, 1)
	go func() {
		_, completeErr := h.completer.Complete(context.Background(), h.orgID, h.run.ID, h.jobID, h.lockToken)
		done <- completeErr
	}()
	select {
	case err := <-done:
		require.FailNowf(t, "completion should block on the target lock", "it returned early: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, tx.Commit(ctx), "commit the retirement")
	select {
	case err := <-done:
		require.NoError(t, err, "completion proceeds once the target lock is free, with no deadlock")
	case <-time.After(10 * time.Second):
		require.FailNow(t, "completion did not finish after the retirement committed")
	}
	run := h.reload(t, h.run.ID)
	require.Equal(t, models.AutomationRunStatusCompleted, run.Status, "the run still completed")
	generation, err := h.targets.GetGenerationByNumber(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration)
	require.NoError(t, err, "reload generation")
	require.Equal(t, 1, generation.TurnCount, "the retired generation still counted the turn")
	require.False(t, generation.OwnershipReleasePending, "the completion applied the release the retirement left pending")
	require.Nil(t, sessionOwnerMarker(t, h.pool, h.orgID, h.outcome.SessionID), "the session is released")
}

// TestAutomationCompletion_AmbiguityDeadlineWithoutCandidates proves the
// deadline transition commits even when no candidate is left to convert:
// the stale pending flag would otherwise block every later push while the
// head lookup is unavailable.
func TestAutomationCompletion_AmbiguityDeadlineWithoutCandidates(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	tie := recentDeliveryTime().Add(time.Minute)
	h.push(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", tie)
	runB := h.push(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", tie)
	require.Equal(t, models.AutomationRunHeadAmbiguous, *runB.HeadResolution, "the tie is ambiguous")
	require.True(t, h.target(t).HeadResolutionPending, "resolution pending")

	// The candidates leave through the per-run fallback, as the kill switch
	// or a continuity switch back to per_run makes them.
	_, err := h.pool.Exec(ctx, `UPDATE automation_runs SET status = 'completed', dispatch_state = NULL, head_resolution = NULL, completed_at = now() WHERE org_id = $1 AND head_resolution = 'ambiguous'`, h.orgID)
	require.NoError(t, err, "take the candidates out through the per-run path")
	_, err = h.pool.Exec(ctx, `UPDATE automation_targets SET head_resolution_deadline_at = now() - interval '1 minute' WHERE id = $1`, *h.run.TargetID)
	require.NoError(t, err, "expire the deadline")

	report, err := h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep")
	require.Equal(t, 1, report.AmbiguityDeadlines, "the deadline transition applied")
	target := h.target(t)
	require.False(t, target.HeadResolutionPending, "the stale pending flag is cleared")
	require.Nil(t, target.HeadResolutionDeadlineAt, "and so is the deadline")
}

// TestAutomationCompletion_FencedThreadWriteAndSummary proves the turn's
// thread result is written under the attempt fence, so a worker that lost
// its lease cannot overwrite a thread another run has claimed, and that the
// completion carries that summary onto the run, where the continuation
// prompt reads it back.
func TestAutomationCompletion_FencedThreadWriteAndSummary(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	summary := "found two issues in the diff"
	diff := "--- a\n+++ b\n"
	result := &models.SessionResult{ResultSummary: &summary, Diff: &diff}

	written, err := h.writeThreadTurn(t, uuid.New(), result)
	require.NoError(t, err, "a stale lease should not error")
	require.False(t, written, "a worker that lost its lease cannot write the thread")
	var storedSummary *string
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT result_summary FROM session_threads WHERE id = $1`, *h.run.ThreadID).Scan(&storedSummary), "thread summary")
	require.Nil(t, storedSummary, "the fenced-out write left the thread alone")

	written, err = h.writeThreadTurn(t, h.lockToken, result)
	require.NoError(t, err, "the lease holder writes the thread")
	require.True(t, written, "the attempt is still ours")

	completion, err := h.completer.Complete(ctx, h.orgID, h.run.ID, h.jobID, h.lockToken)
	require.NoError(t, err, "completion")
	require.True(t, completion.Applied, "the run completes")
	require.Equal(t, summary, *h.reload(t, h.run.ID).ResultSummary, "the run keeps the turn's summary")
	summaries, err := h.runs.ListCompletedTurnSummaries(ctx, h.orgID, *h.run.TargetID, *h.run.TargetGeneration, 5)
	require.NoError(t, err, "list summaries")
	require.Len(t, summaries, 1, "the completed turn is in the continuation history")
	require.Equal(t, summary, summaries[0].Summary, "with its summary, not an empty string")
}

// TestAutomationCompletion_RevokesAbandonedLease proves recovery revokes an
// expired lease under the job lock: a paused worker's heartbeat can no
// longer renew it, so it cannot resume writing after its session, thread,
// and container hold were released.
func TestAutomationCompletion_RevokesAbandonedLease(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	// The lease expires while the worker is paused. RenewLease matches only
	// the token, so without revocation it would happily renew.
	_, err := h.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = now() - interval '5 minutes' WHERE id = $1`, h.jobID)
	require.NoError(t, err, "expire the lease")
	_, err = h.pool.Exec(ctx, `UPDATE automation_runs SET attempt_started_at = now() - interval '3 hours' WHERE id = $1`, h.run.ID)
	require.NoError(t, err, "age the attempt past the stale bound")

	report, err := h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep")
	require.Equal(t, 1, report.RetriesExhausted, "the abandoned attempt is settled")

	_, renewed, err := h.jobs.RenewLease(ctx, h.jobID, h.lockToken, time.Minute)
	require.NoError(t, err, "renew should not error")
	require.False(t, renewed, "the paused worker cannot renew a revoked lease")
	var lockToken *uuid.UUID
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT lock_token FROM jobs WHERE id = $1`, h.jobID).Scan(&lockToken), "job lock token")
	require.Nil(t, lockToken, "the fencing token is cleared")
}

// TestSchedulerAdvisoryLock_PinsItsConnection proves the sweep loop's lock
// is held by one connection and released on that same connection: taken on
// a pooled connection and unlocked on another, it would stay held and no
// replica would ever run the loop again.
func TestSchedulerAdvisoryLock_PinsItsConnection(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	first := cluster.NewAutomationTargetSweepLock(pool)
	second := cluster.NewAutomationTargetSweepLock(pool)

	acquired, err := first.TryAcquire(ctx)
	require.NoError(t, err, "acquire")
	require.True(t, acquired, "the first caller takes the lock")
	held, err := second.TryAcquire(ctx)
	require.NoError(t, err, "second acquire")
	require.False(t, held, "a second caller is refused while it is held")

	require.NoError(t, first.Release(ctx), "release on the pinned connection")
	held, err = second.TryAcquire(ctx)
	require.NoError(t, err, "acquire after release")
	require.True(t, held, "the lock is genuinely free again")
	require.NoError(t, second.Release(ctx), "release")
}

// TestAutomationCompletion_SummaryFollowsTheMarkersTurn proves a turn whose
// worker died before writing the thread takes its own summary from the
// session, not the previous turn's text still on the thread.
func TestAutomationCompletion_SummaryFollowsTheMarkersTurn(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	// The previous turn's summary is still on the thread, at its own turn.
	_, err := h.pool.Exec(ctx, `UPDATE session_threads SET result_summary = 'the previous turn', current_turn = 0 WHERE id = $1`, *h.run.ThreadID)
	require.NoError(t, err, "leave the previous turn's summary on the thread")
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	// The orchestrator's session write carries this turn's summary, in the
	// same transaction as the marker.
	_, err = h.pool.Exec(ctx, `UPDATE sessions SET result_summary = 'this turn' WHERE id = $1`, h.outcome.SessionID)
	require.NoError(t, err, "record this turn's summary on the session")
	h.deadLetterJob(t)

	outcome, err := h.completer.RecoverAbandonedRun(ctx, h.orgID, h.run.ID, h.jobID, time.Now())
	require.NoError(t, err, "recover")
	require.Equal(t, models.AutomationRunOutcomeTurnCompleted, outcome, "the marker completed the run")
	require.Equal(t, "this turn", *h.reload(t, h.run.ID).ResultSummary, "the run records its own summary, not the previous turn's")
}

// TestAutomationCompletion_RevokedLeaseSettlesTheJob proves a revoked lease
// leaves no job counted as running: nothing reclaims a running job with a
// null lease, so it would block a drain forever.
func TestAutomationCompletion_RevokedLeaseSettlesTheJob(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	_, err := h.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = now() - interval '5 minutes' WHERE id = $1`, h.jobID)
	require.NoError(t, err, "expire the lease")
	_, err = h.pool.Exec(ctx, `UPDATE automation_runs SET attempt_started_at = now() - interval '3 hours' WHERE id = $1`, h.run.ID)
	require.NoError(t, err, "age the attempt")

	_, err = h.completer.Sweep(ctx, h.orgID)
	require.NoError(t, err, "sweep")
	var status string
	var lockToken *uuid.UUID
	var leaseExpiresAt, completedAt *time.Time
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT status, lock_token, lease_expires_at, completed_at FROM jobs WHERE id = $1`, h.jobID).
		Scan(&status, &lockToken, &leaseExpiresAt, &completedAt), "job row")
	require.Equal(t, "failed", status, "the recovered job is terminal, not left running")
	require.Nil(t, lockToken, "its fencing token is cleared")
	require.Nil(t, leaseExpiresAt, "its lease is cleared")
	require.NotNil(t, completedAt, "it is stamped complete")
}

// TestAutomationCompletion_ThreadWriteBlocksOnRecovery proves the fenced
// thread write serializes with recovery through the run row lock: a worker
// whose statement starts before a recovery commits still cannot write the
// thread afterwards.
func TestAutomationCompletion_ThreadWriteBlocksOnRecovery(t *testing.T) {
	h := newCompletionHarness(t)
	ctx := context.Background()
	h.endAttempt(t, models.AutomationRunResultTurnCompleted)
	_, err := h.pool.Exec(ctx, `UPDATE session_threads SET status = 'running' WHERE id = $1`, *h.run.ThreadID)
	require.NoError(t, err, "the worker has not written its thread result yet")

	// A recovery holds the run row and has not committed.
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err, "begin recovery")
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := h.runs.LockAttempt(ctx, tx, h.orgID, h.run.ID, h.lockToken)
	require.NoError(t, err, "lock the attempt")
	require.True(t, locked, "recovery holds the run row")
	_, err = tx.Exec(ctx, `UPDATE automation_runs SET dispatch_state = 'done', status = 'completed' WHERE id = $1`, h.run.ID)
	require.NoError(t, err, "recovery settles the run")

	summary := "the lost worker's summary"
	done := make(chan bool, 1)
	errs := make(chan error, 1)
	go func() {
		written, writeErr := h.writeThreadTurn(t, h.lockToken, &models.SessionResult{ResultSummary: &summary})
		errs <- writeErr
		done <- written
	}()
	select {
	case <-done:
		require.FailNow(t, "the thread write should block on the run row lock")
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, tx.Commit(ctx), "commit the recovery")
	require.NoError(t, <-errs, "the thread write should not error")
	require.False(t, <-done, "the write is fenced out once recovery has taken the attempt")
	var storedSummary *string
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT result_summary FROM session_threads WHERE id = $1`, *h.run.ThreadID).Scan(&storedSummary), "thread summary")
	require.Nil(t, storedSummary, "the lost worker wrote nothing")
}
