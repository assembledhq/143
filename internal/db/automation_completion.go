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

// automationRunCompletionFence restricts completion to the run's executing
// attempt that wrote the marker, under the caller's job: either the job is
// running with the caller's lease, or the job has reached a terminal state
// (no attempt can be live, so the marker is final). The terminal case is
// how a dead-lettered job with a marker is completed without a lease.
const automationRunCompletionFence = `
		  AND r.dispatch_state = 'executing'
		  AND r.job_id = @job_id
		  AND EXISTS (
			SELECT 1 FROM automation_run_results m
			WHERE m.run_id = r.id AND m.org_id = r.org_id AND m.attempt = r.attempt)
		  AND EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.id = r.job_id AND j.org_id = r.org_id
			  AND ((j.status = 'running' AND j.lock_token = @lock_token)
			       OR j.status IN ('succeeded', 'failed', 'dead_letter', 'cancelled')))`

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
	err := tx.QueryRow(ctx, `
		SELECT m.outcome, m.review_complete, m.native_context
		FROM automation_run_results m
		JOIN automation_runs r ON r.id = m.run_id AND r.org_id = m.org_id AND r.attempt = m.attempt
		WHERE m.run_id = @id AND m.org_id = @org_id AND r.dispatch_state = 'executing'
		FOR UPDATE OF r`,
		pgx.NamedArgs{"id": runID, "org_id": orgID}).Scan(&markerOutcome, &reviewComplete, &nativeContext)
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
		    result_summary = COALESCE(@result_summary, result_summary),
		    updated_at = now()
		WHERE r.id = @id AND r.org_id = @org_id`+automationRunCompletionFence+`
		RETURNING r.target_id, r.target_generation, r.session_id,
		          COALESCE(r.resolved_head_sha, r.config_snapshot->'github'->>'head_sha'), r.head_epoch`,
		pgx.NamedArgs{
			"id": runID, "org_id": orgID, "job_id": jobID, "lock_token": lockToken,
			"status": outcome.RunStatus(), "outcome": outcome, "native_context": nativeContext,
			"result_summary": resultSummary,
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

// FailRetriesExhausted records retries_exhausted on an executing run whose
// job reached a terminal state (dead-lettered, failed, cancelled, or
// succeeded without ending the attempt) without a result marker for the
// current attempt (design doc 125, "Retry and recovery"). The session and its
// primary thread return to idle without counting a turn, the target is
// woken, and a pending ownership release is applied. Returns the target
// that was released, or false when the run was not in that state.
func (s *AutomationRunStore) FailRetriesExhausted(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID) (uuid.UUID, bool, error) {
	var sessionID, threadID, targetID uuid.UUID
	err := tx.QueryRow(ctx, `
		UPDATE automation_runs r
		SET status = 'failed', dispatch_state = 'done', outcome_reason = @outcome,
		    completed_at = now(), result_summary = 'the turn''s job ended without a result', updated_at = now()
		WHERE r.id = @id AND r.org_id = @org_id
		  AND r.dispatch_state = 'executing' AND r.job_id IS NOT NULL
		  AND EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.id = r.job_id AND j.org_id = r.org_id
			  AND j.status IN ('succeeded', 'failed', 'dead_letter', 'cancelled'))
		  AND NOT EXISTS (
			SELECT 1 FROM automation_run_results m
			WHERE m.run_id = r.id AND m.org_id = r.org_id AND m.attempt = r.attempt)
		RETURNING r.session_id, r.thread_id, r.target_id`,
		pgx.NamedArgs{"id": runID, "org_id": orgID, "outcome": models.AutomationRunOutcomeRetriesExhausted},
	).Scan(&sessionID, &threadID, &targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("fail automation run after exhausted retries: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions SET status = 'idle', last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND status IN ('pending', 'running')`,
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

// ListExecutingRunsWithTerminalJobs returns the org's executing runs whose
// job reached a terminal state, oldest first: the crash-recovery input for
// completion from a marker or retries_exhausted.
func (s *AutomationRunStore) ListExecutingRunsWithTerminalJobs(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id FROM automation_runs r
		JOIN jobs j ON j.id = r.job_id AND j.org_id = r.org_id
		WHERE r.org_id = @org_id AND r.dispatch_state = 'executing'
		  AND j.status IN ('succeeded', 'failed', 'dead_letter', 'cancelled')
		ORDER BY r.triggered_at, r.id`,
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list executing runs with terminal jobs: %w", err)
	}
	defer rows.Close()
	return collectIDs(rows)
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
		SET status = 'skipped', dispatch_state = 'done', outcome_reason = @outcome,
		    completed_at = now(), result_summary = 'the run waited too long for its target', updated_at = now()
		WHERE r.org_id = @org_id AND r.target_id = @target_id
		  AND r.status = 'pending' AND r.dispatch_state = 'waiting'
		  AND r.wait_started_at < @cutoff
		  AND r.head_resolution IS DISTINCT FROM 'ambiguous'`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID, "cutoff": cutoff, "outcome": models.AutomationRunOutcomeWaitTimeout})
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
// executing per-target run, a pending head resolution, or an outstanding
// wake request: the orgs the scheduler's per-target sweeps visit.
// lint:allow-no-orgid reason="scheduler sweep enumerates orgs with per-target work across all tenants"
func (s *AutomationTargetStore) ListOrgsWithPerTargetWork(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT org_id FROM automation_runs WHERE dispatch_state IN ('waiting', 'executing')
		UNION
		SELECT DISTINCT org_id FROM automation_targets WHERE head_resolution_pending OR wake_requested_at IS NOT NULL`)
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
func (s *AutomationTargetStore) ResolveAmbiguityDeadline(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, now time.Time) (int64, error) {
	var pending bool
	var deadline *time.Time
	err := tx.QueryRow(ctx, `
		SELECT head_resolution_pending, head_resolution_deadline_at
		FROM automation_targets WHERE id = @id AND org_id = @org_id FOR UPDATE`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}).Scan(&pending, &deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrAutomationTargetNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lock target for ambiguity deadline: %w", err)
	}
	if !pending || deadline == nil || deadline.After(now) {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE automation_runs
		SET head_resolution = @unresolved, head_epoch = NULL, wait_started_at = now(), updated_at = now()
		WHERE org_id = @org_id AND target_id = @target_id
		  AND status = 'pending' AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)
		  AND head_resolution = @ambiguous`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID, "unresolved": models.AutomationRunHeadUnresolved, "ambiguous": models.AutomationRunHeadAmbiguous})
	if err != nil {
		return 0, fmt.Errorf("convert ambiguous candidates: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation_targets
		SET head_resolution_pending = false, head_resolution_deadline_at = NULL,
		    wake_requested_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}); err != nil {
		return 0, fmt.Errorf("clear ambiguity deadline: %w", err)
	}
	return tag.RowsAffected(), nil
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
