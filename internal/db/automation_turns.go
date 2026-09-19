package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// Per-target continuity: the orchestrator turn path (design doc 125,
// "Workspace Preparation", "Completion"). These writes are made by the
// worker running a reserved turn, so every one of them is fenced by the
// attempt lock token and the job row, as the result marker is.

// automationRunAttemptFence restricts a write to the run's executing
// attempt under the caller's job lease.
const automationRunAttemptFence = `
		  AND r.dispatch_state = 'executing'
		  AND r.attempt_lock_token = @lock_token
		  AND EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.id = r.job_id AND j.org_id = r.org_id
			  AND j.status = 'running' AND j.lock_token = @lock_token)`

// RecordTurnWorkspace stores the prepared workspace's facts on the executing
// run. Returns false when the attempt fence rejects the write.
func (s *AutomationRunStore) RecordTurnWorkspace(ctx context.Context, orgID, runID, lockToken uuid.UUID, ws models.AutomationTurnWorkspace) (bool, error) {
	if lockToken == uuid.Nil {
		return false, errors.New("record automation turn workspace: lock token is required")
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE automation_runs r
		SET base_sha = COALESCE(NULLIF(@base_sha, ''), base_sha),
		    worker_node_id = COALESCE(NULLIF(@worker_node_id, ''), worker_node_id),
		    restore_snapshot_bytes = COALESCE(@restore_snapshot_bytes, restore_snapshot_bytes),
		    restore_duration_ms = COALESCE(@restore_duration_ms, restore_duration_ms),
		    updated_at = now()
		WHERE r.id = @id AND r.org_id = @org_id`+automationRunAttemptFence,
		pgx.NamedArgs{
			"id": runID, "org_id": orgID, "lock_token": lockToken,
			"base_sha": ws.BaseSHA, "worker_node_id": ws.WorkerNodeID,
			"restore_snapshot_bytes": ws.RestoreSnapshotBytes, "restore_duration_ms": ws.RestoreDurationMS,
		})
	if err != nil {
		return false, fmt.Errorf("record automation turn workspace: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RecordTurnDuration stores how long the attempt's agent execution took, in
// the transaction that ends the attempt. Returns false when the attempt
// fence rejects the write.
func (s *AutomationRunStore) RecordTurnDuration(ctx context.Context, q DBTX, orgID, runID, lockToken uuid.UUID, durationMS int) (bool, error) {
	if q == nil {
		q = s.db
	}
	tag, err := q.Exec(ctx, `
		UPDATE automation_runs r
		SET turn_duration_ms = @turn_duration_ms, updated_at = now()
		WHERE r.id = @id AND r.org_id = @org_id`+automationRunAttemptFence,
		pgx.NamedArgs{"id": runID, "org_id": orgID, "lock_token": lockToken, "turn_duration_ms": durationMS})
	if err != nil {
		return false, fmt.Errorf("record automation turn duration: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// AttemptOwned reports whether the run is still executing under this
// attempt lock token and the job lease is still held. Attempt-end writes
// that carry no marker (the drain path) check it inside their transaction.
func (s *AutomationRunStore) AttemptOwned(ctx context.Context, q DBTX, orgID, runID, lockToken uuid.UUID) (bool, error) {
	if q == nil {
		q = s.db
	}
	var owned bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM automation_runs r
			WHERE r.id = @id AND r.org_id = @org_id`+automationRunAttemptFence+`
		)`, pgx.NamedArgs{"id": runID, "org_id": orgID, "lock_token": lockToken}).Scan(&owned)
	if err != nil {
		return false, fmt.Errorf("check automation attempt ownership: %w", err)
	}
	return owned, nil
}

// RecordContinuationFallback records that a continued turn could not
// restore its checkpoint and is rebuilding the workspace in the same
// session (design doc 125, readiness "rebuild" with restore_failed).
func (s *AutomationRunStore) RecordContinuationFallback(ctx context.Context, orgID, runID, lockToken uuid.UUID, reason models.AutomationRunContinuationReason) (bool, error) {
	if err := reason.Validate(); err != nil {
		return false, err
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE automation_runs r
		SET continuation_mode = @mode, continuation_reason = @reason, updated_at = now()
		WHERE r.id = @id AND r.org_id = @org_id`+automationRunAttemptFence,
		pgx.NamedArgs{"id": runID, "org_id": orgID, "lock_token": lockToken, "mode": models.AutomationRunContinuationReconstructed, "reason": reason})
	if err != nil {
		return false, fmt.Errorf("record automation continuation fallback: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListCompletedTurnSummaries returns the completed runs of a generation in
// completion order, oldest first, bounded to the newest limit rows. The
// head is the run's resolved head when dispatch resolved one, else the
// delivered head from the run's snapshot.
func (s *AutomationRunStore) ListCompletedTurnSummaries(ctx context.Context, orgID, targetID uuid.UUID, generation, limit int) ([]models.AutomationTurnSummary, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, COALESCE(resolved_head_sha, config_snapshot->'github'->>'head_sha', ''), COALESCE(turn_number, 0),
		       COALESCE(result_summary, ''), to_char(completed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM (
			SELECT id, resolved_head_sha, config_snapshot, turn_number, result_summary, completed_at
			FROM automation_runs
			WHERE org_id = @org_id AND target_id = @target_id AND target_generation = @generation
			  AND status = 'completed' AND outcome_reason = 'turn_completed'
			ORDER BY completed_at DESC, id DESC
			LIMIT @limit
		) newest
		ORDER BY completed_at ASC, id ASC`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID, "generation": generation, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list completed automation turn summaries: %w", err)
	}
	defer rows.Close()
	var out []models.AutomationTurnSummary
	for rows.Next() {
		var item models.AutomationTurnSummary
		if err := rows.Scan(&item.RunID, &item.HeadSHA, &item.TurnNumber, &item.Summary, &item.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan automation turn summary: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CompleteExecutingPreflight ends a reserved run that could not start its
// turn (design doc 125, "Executing preflight outcomes"): an unreachable
// head (stale_head), a lifecycle change since reservation (pr_closed), or
// a failed repository authorization (repository_unavailable). Fenced by the
// attempt lock token and the job row, in one transaction it sets the run's
// terminal status and dispatch_state done, returns the session and its
// primary thread (pending for a fresh generation, running for a claimed
// one) to idle without advancing turn counters, deletes the user
// message the reservation inserted, requests a target wake, and applies a
// pending ownership release. No result marker is written. Returns false
// when the fence rejects the write.
func (s *AutomationRunStore) CompleteExecutingPreflight(ctx context.Context, tx pgx.Tx, orgID, runID, lockToken uuid.UUID, outcome models.AutomationRunOutcomeReason, summary string) (bool, error) {
	switch outcome {
	case models.AutomationRunOutcomeStaleHead, models.AutomationRunOutcomePRClosed, models.AutomationRunOutcomeRepositoryUnavailable:
	default:
		return false, fmt.Errorf("complete executing preflight: %q is not a preflight outcome", outcome)
	}
	if lockToken == uuid.Nil {
		return false, errors.New("complete executing preflight: lock token is required")
	}
	var sessionID, threadID, targetID uuid.UUID
	err := tx.QueryRow(ctx, `
		UPDATE automation_runs r
		SET status = @status, dispatch_state = 'done', outcome_reason = @outcome,
		    completed_at = now(), result_summary = NULLIF(@summary, ''), updated_at = now()
		WHERE r.id = @id AND r.org_id = @org_id`+automationRunAttemptFence+`
		RETURNING r.session_id, r.thread_id, r.target_id`,
		pgx.NamedArgs{"id": runID, "org_id": orgID, "lock_token": lockToken, "status": outcome.RunStatus(), "outcome": outcome, "summary": summary},
	).Scan(&sessionID, &threadID, &targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("complete executing preflight: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions SET status = 'idle', last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND status IN ('pending', 'running')`,
		pgx.NamedArgs{"id": sessionID, "org_id": orgID}); err != nil {
		return false, fmt.Errorf("release session after preflight: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session_threads SET status = 'idle'
		WHERE id = @id AND org_id = @org_id AND status IN ('pending', 'running')`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID}); err != nil {
		return false, fmt.Errorf("release thread after preflight: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM session_messages
		WHERE org_id = @org_id AND automation_run_id = @run_id AND role = 'user' AND source = @source`,
		pgx.NamedArgs{"org_id": orgID, "run_id": runID, "source": models.SessionMessageSourceAutomationTurn}); err != nil {
		return false, fmt.Errorf("delete reservation message after preflight: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation_targets SET wake_requested_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}); err != nil {
		return false, fmt.Errorf("request wake after preflight: %w", err)
	}
	if err := applyPendingOwnershipRelease(ctx, tx, orgID, sessionID); err != nil {
		return false, err
	}
	return true, nil
}

// applyPendingOwnershipRelease clears the session's owner marker when the
// owning generation was retired while a run executed
// (ownership_release_pending), and resets the flag.
func applyPendingOwnershipRelease(ctx context.Context, q DBTX, orgID, sessionID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		WITH released AS (
			UPDATE automation_target_sessions g
			SET ownership_release_pending = false, updated_at = now()
			FROM sessions s
			WHERE s.id = @session_id AND s.org_id = @org_id
			  AND g.id = s.automation_owner_generation_id AND g.org_id = s.org_id
			  AND g.ownership_release_pending
			RETURNING g.id
		)
		UPDATE sessions s
		SET automation_owner_generation_id = NULL
		FROM released r
		WHERE s.id = @session_id AND s.org_id = @org_id AND s.automation_owner_generation_id = r.id`,
		pgx.NamedArgs{"session_id": sessionID, "org_id": orgID})
	if err != nil {
		return fmt.Errorf("apply pending ownership release: %w", err)
	}
	return nil
}
