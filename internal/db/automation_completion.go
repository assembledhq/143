package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// Per-target continuity: completion, wake-up, and sweeps (design doc 125,
// "Automation turn completer", "Target Wake-up", "Clocks and the stuck-run
// reaper"). Completion is fenced twice like every attempt write; the sweeps
// take the target lock so they serialize with arrival and dispatch.

// AutomationRunCompletion is what CompleteFromMarker applied.
type AutomationRunCompletion struct {
	Applied          bool
	TargetID         uuid.UUID
	TargetGeneration int
	SessionID        uuid.UUID
	GenerationID     uuid.UUID
	Outcome          models.AutomationRunOutcomeReason
	// RetireGeneration is set for an awaiting_input outcome: the turn ended
	// and a person can answer in the now-ordinary session.
	RetireGeneration bool
}

// automationRunNoLiveLease is true when no worker can still be executing
// the run's attempt: the job holds no live lease and no session executor is
// active for it. It is the recovery paths' evidence that an attempt is over
// even though the process that ran it never said so.
const automationRunNoLiveLease = `(
			NOT EXISTS (
				SELECT 1 FROM jobs j
				WHERE j.id = r.job_id AND j.org_id = r.org_id
				  AND j.status = 'running' AND j.lease_expires_at > now())
			AND NOT EXISTS (
				SELECT 1 FROM session_executors e
				WHERE e.job_id = r.job_id AND e.org_id = r.org_id
				  AND e.status IN ('starting', 'running', 'draining')
				  AND (e.lease_expires_at IS NULL OR e.lease_expires_at > now()))
		)`

// automationRunJobTerminal is true when the run's job reached a terminal
// state. A job that ended `succeeded` while its run is still executing is
// an orchestrator bug rather than an exhausted retry budget, but the run is
// abandoned either way and must be settled.
const automationRunJobTerminal = `EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.id = r.job_id AND j.org_id = r.org_id
			  AND j.status IN ('succeeded', 'failed', 'dead_letter', 'cancelled'))`

// automationRunCompletionFence restricts completion to the run's executing
// attempt that wrote the marker, under the caller's job: either the job is
// running with the caller's lease, or no live lease remains, in which case
// the attempt is over whatever happened to the worker and the marker is
// final. The second case is how a run whose job died with a marker already
// written is completed without a lease.
const automationRunCompletionFence = `
		  AND r.dispatch_state = 'executing'
		  AND r.job_id = @job_id
		  AND EXISTS (
			SELECT 1 FROM automation_run_results m
			WHERE m.run_id = r.id AND m.org_id = r.org_id AND m.attempt = r.attempt)
		  AND (EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.id = r.job_id AND j.org_id = r.org_id
			  AND j.status = 'running' AND j.lock_token = @lock_token)
		       OR ` + automationRunNoLiveLease + `)`

// CompleteFromMarker records the run's terminal status from the result
// marker of its current attempt and updates the generation's counters.
// The run update is fenced by the job row (the caller's live lease, or a
// terminal job) and by the marker matching the current attempt; the
// generation update is keyed by target_generation. Baseline fields
// (last_reviewed_head_sha, last_reviewed_epoch) advance only for a completed
// review whose head epoch is non-null and at or above the recorded epoch,
// and only while the generation is active; turn_count, last_run_id, and
// last_turn_at are recorded for a retired generation too. The completion
// applies a pending ownership release and requests a target wake. A second
// call for the same attempt matches zero run rows and reports
// Applied=false, so completion is idempotent.
func (s *AutomationRunStore) CompleteFromMarker(ctx context.Context, tx pgx.Tx, orgID, runID, jobID, lockToken uuid.UUID, resultSummary *string) (AutomationRunCompletion, error) {
	var out AutomationRunCompletion
	var headSHA *string
	var headEpoch *int
	var markerOutcome models.AutomationRunResultOutcome
	var reviewComplete, nativeContext bool
	var threadID uuid.UUID
	var turnNumber int
	var agentSessionID *string
	// Locks are taken target, job, run, generation: the order every other
	// writer uses, so a completion and a lifecycle change or an attempt
	// claim never deadlock. A recovery call (no lock token) also revokes an
	// expired lease under the job lock, so the worker it is recovering from
	// cannot renew and resume.
	// Revocation is unconditional: the fence below also accepts a completion
	// when no live lease remains, which a caller holding a stale non-nil
	// token can reach after another attempt claimed and then stalled. Doing
	// it only for nil-token recovery would free the target while that
	// attempt's token stayed renewable.
	if err := lockAutomationRunLifecycle(ctx, tx, orgID, runID, true); err != nil {
		if errors.Is(err, ErrAutomationTargetNotFound) {
			return AutomationRunCompletion{}, nil
		}
		return AutomationRunCompletion{}, err
	}
	err := tx.QueryRow(ctx, `
		SELECT m.outcome, m.review_complete, m.native_context, m.thread_id, m.turn_number, m.agent_session_id
		FROM automation_run_results m
		JOIN automation_runs r ON r.id = m.run_id AND r.org_id = m.org_id AND r.attempt = m.attempt
		WHERE m.run_id = @id AND m.org_id = @org_id AND r.dispatch_state = 'executing'`,
		pgx.NamedArgs{"id": runID, "org_id": orgID}).Scan(&markerOutcome, &reviewComplete, &nativeContext, &threadID, &turnNumber, &agentSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutomationRunCompletion{}, nil
	}
	if err != nil {
		return AutomationRunCompletion{}, fmt.Errorf("read automation run result: %w", err)
	}
	outcome := markerOutcome.OutcomeReason()
	if err := outcome.Validate(); err != nil {
		return AutomationRunCompletion{}, fmt.Errorf("complete automation run: %w", err)
	}
	err = tx.QueryRow(ctx, `
		UPDATE automation_runs r
		SET status = @status,
		    outcome_reason = @outcome,
		    dispatch_state = 'done',
		    native_context = @native_context,
		    completed_at = now(),
		    -- The continuation prompt reads these summaries back
		    -- (ListCompletedTurnSummaries), so the run keeps the turn's own
		    -- summary. The session is asked first: its summary is written in
		    -- the same transaction as the marker, so it is always this
		    -- turn's. The thread, which the worker writes afterwards, is
		    -- accepted only once it has reached this turn, or a turn whose
		    -- worker died before that write would inherit the previous
		    -- turn's text.
		    result_summary = COALESCE(
		        @result_summary,
		        (SELECT NULLIF(se.result_summary, '') FROM sessions se
		         WHERE se.id = r.session_id AND se.org_id = r.org_id
		           AND se.current_turn >= @turn_number),
		        (SELECT NULLIF(th.result_summary, '') FROM session_threads th
		         WHERE th.id = @thread_id AND th.org_id = r.org_id
		           AND th.current_turn >= @turn_number),
		        r.result_summary),
		    updated_at = now()
		WHERE r.id = @id AND r.org_id = @org_id`+automationRunCompletionFence+`
		RETURNING r.target_id, r.target_generation, r.session_id,
		          COALESCE(r.resolved_head_sha, r.config_snapshot->'github'->>'head_sha'), r.head_epoch`,
		pgx.NamedArgs{
			"id": runID, "org_id": orgID, "job_id": jobID, "lock_token": lockToken,
			"status": outcome.RunStatus(), "outcome": outcome, "native_context": nativeContext,
			"result_summary": resultSummary, "thread_id": threadID, "turn_number": turnNumber,
		},
	).Scan(&out.TargetID, &out.TargetGeneration, &out.SessionID, &headSHA, &headEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutomationRunCompletion{}, nil
	}
	if err != nil {
		return AutomationRunCompletion{}, fmt.Errorf("complete automation run: %w", err)
	}
	out.Applied = true
	out.Outcome = outcome

	reviewed := markerOutcome == models.AutomationRunResultTurnCompleted && reviewComplete
	err = tx.QueryRow(ctx, `
		UPDATE automation_target_sessions g
		SET turn_count = turn_count + 1,
		    last_run_id = @run_id,
		    last_turn_at = now(),
		    last_reviewed_head_sha = CASE
		        WHEN @reviewed::bool AND g.status = 'active' AND @head_epoch::int IS NOT NULL
		             AND (g.last_reviewed_epoch IS NULL OR @head_epoch::int >= g.last_reviewed_epoch)
		        THEN COALESCE(@head_sha::text, g.last_reviewed_head_sha)
		        ELSE g.last_reviewed_head_sha END,
		    last_reviewed_epoch = CASE
		        WHEN @reviewed::bool AND g.status = 'active' AND @head_epoch::int IS NOT NULL
		             AND (g.last_reviewed_epoch IS NULL OR @head_epoch::int >= g.last_reviewed_epoch)
		        THEN @head_epoch::int
		        ELSE g.last_reviewed_epoch END,
		    updated_at = now()
		WHERE g.org_id = @org_id AND g.target_id = @target_id AND g.generation = @generation
		RETURNING g.id`,
		pgx.NamedArgs{
			"org_id": orgID, "target_id": out.TargetID, "generation": out.TargetGeneration, "run_id": runID,
			"reviewed": reviewed, "head_sha": headSHA, "head_epoch": headEpoch,
		},
	).Scan(&out.GenerationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutomationRunCompletion{}, fmt.Errorf("complete automation run: generation %d of target %s not found", out.TargetGeneration, out.TargetID)
	}
	if err != nil {
		return AutomationRunCompletion{}, fmt.Errorf("record automation turn on generation: %w", err)
	}
	out.RetireGeneration = markerOutcome == models.AutomationRunResultAwaitingInput
	// The attempt is over, so the turn hold is too. Clearing it here repairs
	// a worker that died between the marker and its deferred release: the
	// container stays recorded for the next turn's inherited-container path,
	// but a retirement in this transaction can release ownership at once
	// instead of leaving ownership_release_pending with no executing run to
	// clear it. In the ordinary path the orchestrator already released it
	// and this matches no row.
	if _, err := tx.Exec(ctx, `
		UPDATE sessions SET turn_holding_container = FALSE
		WHERE id = @id AND org_id = @org_id AND turn_holding_container`,
		pgx.NamedArgs{"id": out.SessionID, "org_id": orgID}); err != nil {
		return AutomationRunCompletion{}, fmt.Errorf("release turn hold at completion: %w", err)
	}
	// The primary thread returns to idle from the marker's own turn number,
	// so a turn whose job died after the marker still frees the thread for
	// the next dispatch. The handler's richer write (summary, diff) has
	// already landed in the ordinary path and left the thread idle, which
	// this statement does not touch.
	if _, err := tx.Exec(ctx, `
		UPDATE session_threads
		SET status = 'idle',
		    current_turn = GREATEST(current_turn, @turn_number),
		    last_activity_at = now(),
		    agent_session_id = COALESCE(@agent_session_id, agent_session_id)
		WHERE id = @id AND org_id = @org_id AND status IN ('pending', 'running')`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID, "turn_number": turnNumber, "agent_session_id": agentSessionID}); err != nil {
		return AutomationRunCompletion{}, fmt.Errorf("release thread at completion: %w", err)
	}
	if err := applyPendingOwnershipRelease(ctx, tx, orgID, out.SessionID); err != nil {
		return AutomationRunCompletion{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation_targets SET wake_requested_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": out.TargetID, "org_id": orgID}); err != nil {
		return AutomationRunCompletion{}, fmt.Errorf("request wake after completion: %w", err)
	}
	return out, nil
}

// FailRetriesExhausted records retries_exhausted on an abandoned executing
// run: no result marker for the current attempt, no live lease, and either
// a terminal job or an attempt older than staleBefore (design doc 125,
// "Retry and recovery"). The session and its primary thread return to idle
// without counting a turn, the turn hold is released, the target is woken,
// and a pending ownership release is applied. Returns the target that was
// released, or false when the run was not in that state.
func (s *AutomationRunStore) FailRetriesExhausted(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, staleBefore time.Time) (uuid.UUID, bool, error) {
	var sessionID, threadID, targetID uuid.UUID
	if err := lockAutomationRunLifecycle(ctx, tx, orgID, runID, true); err != nil {
		if errors.Is(err, ErrAutomationTargetNotFound) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	err := tx.QueryRow(ctx, `
		UPDATE automation_runs r
		SET status = 'failed', dispatch_state = 'done', outcome_reason = @outcome,
		    completed_at = now(), result_summary = 'the turn''s job ended without a result', updated_at = now()
		WHERE r.id = @id AND r.org_id = @org_id
		  AND r.dispatch_state = 'executing' AND r.job_id IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM automation_run_results m
			WHERE m.run_id = r.id AND m.org_id = r.org_id AND m.attempt = r.attempt)
		  AND `+automationRunNoLiveLease+`
		  AND (`+automationRunJobTerminal+`
		       OR COALESCE(r.attempt_started_at, r.execution_started_at, r.triggered_at) < @stale_before)
		RETURNING r.session_id, r.thread_id, r.target_id`,
		pgx.NamedArgs{"id": runID, "org_id": orgID, "outcome": models.AutomationRunOutcomeRetriesExhausted, "stale_before": staleBefore},
	).Scan(&sessionID, &threadID, &targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("fail automation run after exhausted retries: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions SET status = 'idle', turn_holding_container = FALSE, last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND (status IN ('pending', 'running') OR turn_holding_container)`,
		pgx.NamedArgs{"id": sessionID, "org_id": orgID}); err != nil {
		return uuid.Nil, false, fmt.Errorf("release session after exhausted retries: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session_threads SET status = 'idle'
		WHERE id = @id AND org_id = @org_id AND status IN ('pending', 'running')`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID}); err != nil {
		return uuid.Nil, false, fmt.Errorf("release thread after exhausted retries: %w", err)
	}
	if err := applyPendingOwnershipRelease(ctx, tx, orgID, sessionID); err != nil {
		return uuid.Nil, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation_targets SET wake_requested_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}); err != nil {
		return uuid.Nil, false, fmt.Errorf("request wake after exhausted retries: %w", err)
	}
	return targetID, true, nil
}

// ListAbandonedExecutingRuns returns the org's executing runs that no
// worker can still be running, oldest first: no live lease, and either a
// terminal job or an attempt that started before staleBefore. They are the
// recovery sweep's input, completed from their marker or failed with
// retries_exhausted. The stuck-run reaper deliberately leaves executing
// runs to this path, which releases the session, the thread, and ownership
// rather than terminalizing the run row alone.
func (s *AutomationRunStore) ListAbandonedExecutingRuns(ctx context.Context, orgID uuid.UUID, staleBefore time.Time) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id FROM automation_runs r
		WHERE r.org_id = @org_id AND r.dispatch_state = 'executing' AND r.job_id IS NOT NULL
		  AND `+automationRunNoLiveLease+`
		  AND (`+automationRunJobTerminal+`
		       OR COALESCE(r.attempt_started_at, r.execution_started_at, r.triggered_at) < @stale_before)
		ORDER BY r.triggered_at, r.id`,
		pgx.NamedArgs{"org_id": orgID, "stale_before": staleBefore})
	if err != nil {
		return nil, fmt.Errorf("list abandoned executing runs: %w", err)
	}
	defer rows.Close()
	return collectIDs(rows)
}

// AutomationStrandedOwnership is a retired generation whose ownership
// release never completed because no executing run remains to apply it.
type AutomationStrandedOwnership struct {
	GenerationID uuid.UUID
	TargetID     uuid.UUID
}

// ListStrandedOwnershipReleases returns the org's generations that are
// waiting on an ownership release with no executing run left to apply it,
// the state a worker that died between its result and its cleanup leaves
// behind.
func (s *AutomationTargetStore) ListStrandedOwnershipReleases(ctx context.Context, orgID uuid.UUID) ([]AutomationStrandedOwnership, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.id, g.target_id
		FROM automation_target_sessions g
		WHERE g.org_id = @org_id AND g.ownership_release_pending
		  AND NOT EXISTS (
			SELECT 1 FROM automation_runs r
			WHERE r.org_id = g.org_id AND r.target_id = g.target_id
			  AND r.target_generation = g.generation AND r.dispatch_state = 'executing')
		ORDER BY g.updated_at, g.id`,
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list stranded ownership releases: %w", err)
	}
	defer rows.Close()
	out := make([]AutomationStrandedOwnership, 0, 8)
	for rows.Next() {
		var row AutomationStrandedOwnership
		if err := rows.Scan(&row.GenerationID, &row.TargetID); err != nil {
			return nil, fmt.Errorf("scan stranded ownership release: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ReleaseStrandedOwnership clears the owner marker of a generation whose
// release is pending with no executing run left to apply it, under the
// target lock. The session's turn hold is cleared with it: the hold can
// only be a dead worker's, because an owned session accepts no human turn
// and no run of this generation is executing. The target is woken.
func (s *AutomationTargetStore) ReleaseStrandedOwnership(ctx context.Context, tx pgx.Tx, orgID, generationID, targetID uuid.UUID) (bool, error) {
	if err := lockAutomationTarget(ctx, tx, orgID, targetID); err != nil {
		return false, err
	}
	var sessionID uuid.UUID
	err := tx.QueryRow(ctx, `
		UPDATE automation_target_sessions g
		SET ownership_release_pending = false, updated_at = now()
		WHERE g.id = @id AND g.org_id = @org_id AND g.target_id = @target_id
		  AND g.ownership_release_pending
		  AND NOT EXISTS (
			SELECT 1 FROM automation_runs r
			WHERE r.org_id = g.org_id AND r.target_id = g.target_id
			  AND r.target_generation = g.generation AND r.dispatch_state = 'executing')
		RETURNING g.session_id`,
		pgx.NamedArgs{"id": generationID, "org_id": orgID, "target_id": targetID}).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("release stranded automation ownership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		SET automation_owner_generation_id = NULL, turn_holding_container = FALSE
		WHERE id = @session_id AND org_id = @org_id AND automation_owner_generation_id = @generation_id`,
		pgx.NamedArgs{"session_id": sessionID, "org_id": orgID, "generation_id": generationID}); err != nil {
		return false, fmt.Errorf("clear session ownership after stranded release: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation_targets SET wake_requested_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}); err != nil {
		return false, fmt.Errorf("request wake after stranded release: %w", err)
	}
	return true, nil
}

// HasResultForCurrentAttempt reports whether a result marker exists for the
// run's current attempt, the retry path's signal to complete instead of
// claiming a new attempt.
func (s *AutomationRunStore) HasResultForCurrentAttempt(ctx context.Context, orgID, runID uuid.UUID) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM automation_runs r
			JOIN automation_run_results m ON m.run_id = r.id AND m.org_id = r.org_id AND m.attempt = r.attempt
			WHERE r.id = @id AND r.org_id = @org_id AND r.dispatch_state = 'executing')`,
		pgx.NamedArgs{"id": runID, "org_id": orgID}).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check automation run result: %w", err)
	}
	return exists, nil
}

// ListTargetsWithTimedOutWaits returns the org's targets that have a
// waiting run whose wait started before cutoff, excluding ambiguous
// candidates (they belong to the ambiguity deadline).
func (s *AutomationTargetStore) ListTargetsWithTimedOutWaits(ctx context.Context, orgID uuid.UUID, cutoff time.Time) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT r.target_id
		FROM automation_runs r
		WHERE r.org_id = @org_id AND r.target_id IS NOT NULL
		  AND r.status = 'pending' AND r.dispatch_state = 'waiting'
		  AND r.wait_started_at < @cutoff
		  AND r.head_resolution IS DISTINCT FROM 'ambiguous'`,
		pgx.NamedArgs{"org_id": orgID, "cutoff": cutoff})
	if err != nil {
		return nil, fmt.Errorf("list targets with timed out waits: %w", err)
	}
	defer rows.Close()
	return collectIDs(rows)
}

// FailTimedOutWaits fails the target's waiting runs whose wait started
// before cutoff with wait_timeout, under the target lock so the sweep
// serializes with arrival and dispatch. Ambiguous candidates are excluded;
// the ambiguity deadline owns them. The target is woken when a run was
// failed. Returns the number of runs failed.
func (s *AutomationRunStore) FailTimedOutWaits(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, cutoff time.Time) (int64, error) {
	if err := lockAutomationTarget(ctx, tx, orgID, targetID); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE automation_runs r
		SET status = @status, dispatch_state = 'done', outcome_reason = @outcome,
		    completed_at = now(), result_summary = 'the run waited too long for its target', updated_at = now()
		WHERE r.org_id = @org_id AND r.target_id = @target_id
		  AND r.status = 'pending' AND r.dispatch_state = 'waiting'
		  AND r.wait_started_at < @cutoff
		  AND r.head_resolution IS DISTINCT FROM 'ambiguous'`,
		pgx.NamedArgs{
			"org_id": orgID, "target_id": targetID, "cutoff": cutoff,
			"outcome": models.AutomationRunOutcomeWaitTimeout,
			"status":  models.AutomationRunOutcomeWaitTimeout.RunStatus(),
		})
	if err != nil {
		return 0, fmt.Errorf("fail timed out automation waits: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation_targets SET wake_requested_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}); err != nil {
		return 0, fmt.Errorf("request wake after wait timeout: %w", err)
	}
	return tag.RowsAffected(), nil
}

// lockAutomationRunLifecycle takes the run's target lock, then the run row,
// then its job row, so every writer acquires locks in one order: target,
// run, job, generation. Arrival, dispatch, and retirement take the target
// first; every fenced attempt write takes the run before the job.
//
// revokeAbandonedLease is set by every writer that may proceed without a
// live lease. Under the job row lock it settles a job whose lease has
// expired: the fencing token is cleared, because RenewLease matches only
// the token and would otherwise let a paused worker renew an expired lease
// and carry on writing after the session, thread, and container hold were
// released, and the job is recorded failed, because a job left running
// with no lease is reclaimed by nothing and counts against a drain
// forever. A live lease matches neither statement, so a caller that still
// owns its job revokes nothing.
func lockAutomationRunLifecycle(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, revokeAbandonedLease bool) error {
	var targetID uuid.UUID
	var jobID *uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT t.id, r.job_id
		FROM automation_targets t
		JOIN automation_runs r ON r.target_id = t.id AND r.org_id = t.org_id
		WHERE r.id = @run_id AND r.org_id = @org_id
		FOR UPDATE OF t`,
		pgx.NamedArgs{"run_id": runID, "org_id": orgID}).Scan(&targetID, &jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAutomationTargetNotFound
	}
	if err != nil {
		return fmt.Errorf("lock automation target of run: %w", err)
	}
	if jobID == nil {
		return nil
	}
	// The run row is locked before the job row, the order every fenced
	// attempt write takes them in (FOR UPDATE OF r, j) and the attempt
	// claim now takes them in too.
	var lockedRunID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM automation_runs WHERE id = @run_id AND org_id = @org_id FOR UPDATE`,
		pgx.NamedArgs{"run_id": runID, "org_id": orgID}).Scan(&lockedRunID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAutomationTargetNotFound
		}
		return fmt.Errorf("lock automation run: %w", err)
	}
	var jobStatus string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM jobs WHERE id = @job_id AND org_id = @org_id FOR UPDATE`,
		pgx.NamedArgs{"job_id": *jobID, "org_id": orgID}).Scan(&jobStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lock automation turn job: %w", err)
	}
	if !revokeAbandonedLease {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE jobs
		SET status = 'failed',
		    last_error = COALESCE(NULLIF(last_error, ''), 'the automation turn was recovered after its lease expired'),
		    completed_at = now(),
		    locked_by_node_id = NULL,
		    run_owner_id = NULL,
		    owner_kind = 'worker',
		    lock_token = NULL,
		    locked_at = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = @job_id AND org_id = @org_id
		  AND status = 'running'
		  AND (lease_expires_at IS NULL OR lease_expires_at <= now())`,
		pgx.NamedArgs{"job_id": *jobID, "org_id": orgID}); err != nil {
		return fmt.Errorf("revoke abandoned automation turn lease: %w", err)
	}
	return nil
}

func lockAutomationTarget(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM automation_targets WHERE id = @id AND org_id = @org_id FOR UPDATE`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAutomationTargetNotFound
	}
	if err != nil {
		return fmt.Errorf("lock automation target: %w", err)
	}
	return nil
}

// ListOrgsWithPerTargetWork returns the orgs that have a waiting or
// executing per-target run, a pending head resolution, an outstanding wake
// request, or a pending ownership release: the orgs the scheduler's
// per-target sweeps visit.
// lint:allow-no-orgid reason="scheduler sweep enumerates orgs with per-target work across all tenants"
func (s *AutomationTargetStore) ListOrgsWithPerTargetWork(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT org_id FROM automation_runs WHERE dispatch_state IN ('waiting', 'executing')
		UNION
		SELECT DISTINCT org_id FROM automation_targets WHERE head_resolution_pending OR wake_requested_at IS NOT NULL
		UNION
		SELECT DISTINCT org_id FROM automation_target_sessions WHERE ownership_release_pending`)
	if err != nil {
		return nil, fmt.Errorf("list orgs with per-target work: %w", err)
	}
	defer rows.Close()
	return collectIDs(rows)
}

// ListTargetsWithExpiredAmbiguity returns the org's targets whose ambiguity
// deadline has passed while still pending.
func (s *AutomationTargetStore) ListTargetsWithExpiredAmbiguity(ctx context.Context, orgID uuid.UUID, now time.Time) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id FROM automation_targets
		WHERE org_id = @org_id AND head_resolution_pending
		  AND head_resolution_deadline_at IS NOT NULL AND head_resolution_deadline_at <= @now
		ORDER BY head_resolution_deadline_at`,
		pgx.NamedArgs{"org_id": orgID, "now": now})
	if err != nil {
		return nil, fmt.Errorf("list targets with expired ambiguity: %w", err)
	}
	defer rows.Close()
	return collectIDs(rows)
}

// ResolveAmbiguityDeadline converts the target's ambiguous candidates to
// unresolved with a null epoch and a restarted wait clock, clears the
// pending flag and the deadline, and wakes the target, all under the
// target lock (design doc 125, "Ambiguity deadline"). Returns the number
// of candidates converted; zero when the deadline has not passed or the
// target is no longer pending.
func (s *AutomationTargetStore) ResolveAmbiguityDeadline(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, now time.Time) (bool, int64, error) {
	var pending bool
	var deadline *time.Time
	err := tx.QueryRow(ctx, `
		SELECT head_resolution_pending, head_resolution_deadline_at
		FROM automation_targets WHERE id = @id AND org_id = @org_id FOR UPDATE`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}).Scan(&pending, &deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0, ErrAutomationTargetNotFound
	}
	if err != nil {
		return false, 0, fmt.Errorf("lock target for ambiguity deadline: %w", err)
	}
	if !pending || deadline == nil || deadline.After(now) {
		return false, 0, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE automation_runs
		SET head_resolution = @unresolved, head_epoch = NULL, wait_started_at = now(), updated_at = now()
		WHERE org_id = @org_id AND target_id = @target_id
		  AND status = 'pending' AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)
		  AND head_resolution = @ambiguous`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID, "unresolved": models.AutomationRunHeadUnresolved, "ambiguous": models.AutomationRunHeadAmbiguous})
	if err != nil {
		return false, 0, fmt.Errorf("convert ambiguous candidates: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation_targets
		SET head_resolution_pending = false, head_resolution_deadline_at = NULL,
		    wake_requested_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}); err != nil {
		return false, 0, fmt.Errorf("clear ambiguity deadline: %w", err)
	}
	// The deadline transition applied even when it converted nothing: the
	// candidates can have left through the per-run fallback, and a stale
	// pending flag would block every later push while a lookup is failing.
	return true, tag.RowsAffected(), nil
}

// ListTargetsNeedingWake returns the org's targets that have waiting runs,
// no executing run, and either a wake request older than staleAfter or a
// pending wake request with no live wake job (design doc 125,
// "Reconciliation"). wakeJobDedupePrefix is the wake job's dedupe key
// prefix; the target id completes it.
func (s *AutomationTargetStore) ListTargetsNeedingWake(ctx context.Context, orgID uuid.UUID, staleBefore time.Time, wakeJobDedupePrefix string) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id
		FROM automation_targets t
		WHERE t.org_id = @org_id
		  AND EXISTS (
			SELECT 1 FROM automation_runs w
			WHERE w.org_id = t.org_id AND w.target_id = t.id
			  AND w.status = 'pending' AND (w.dispatch_state = 'waiting' OR w.dispatch_state IS NULL))
		  AND NOT EXISTS (
			SELECT 1 FROM automation_runs e
			WHERE e.org_id = t.org_id AND e.target_id = t.id AND e.dispatch_state = 'executing')
		  AND (
			t.wake_requested_at IS NULL
			OR t.wake_requested_at < @stale_before
			OR NOT EXISTS (
				SELECT 1 FROM jobs j
				WHERE j.org_id = t.org_id AND j.dedupe_key = @prefix || t.id::text
				  AND j.status IN ('pending', 'running')))
		ORDER BY t.wake_requested_at NULLS FIRST, t.id`,
		pgx.NamedArgs{"org_id": orgID, "stale_before": staleBefore, "prefix": wakeJobDedupePrefix})
	if err != nil {
		return nil, fmt.Errorf("list targets needing wake: %w", err)
	}
	defer rows.Close()
	return collectIDs(rows)
}

// RescheduleActiveByDedupeKey makes a pending job with the dedupe key
// runnable now, so a waiting run's poll re-enters dispatch immediately on a
// wake. Returns whether a job was rescheduled.
func (s *JobStore) RescheduleActiveByDedupeKey(ctx context.Context, orgID uuid.UUID, dedupeKey string) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE jobs SET run_at = now()
		WHERE org_id = @org_id AND dedupe_key = @dedupe_key AND status = 'pending' AND run_at > now()`,
		pgx.NamedArgs{"org_id": orgID, "dedupe_key": dedupeKey})
	if err != nil {
		return false, fmt.Errorf("reschedule job by dedupe key: %w", err)
	}
	if tag.RowsAffected() > 0 {
		s.Wake(ctx)
		return true, nil
	}
	return false, nil
}
