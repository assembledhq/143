package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrAutomationRunResultNotFound is returned when no result marker exists
// for the run.
var ErrAutomationRunResultNotFound = errors.New("automation run result not found")

// AutomationRunResultStore persists the run-keyed result marker the
// orchestrator writes at every attempt end (design doc 125, "Completion").
type AutomationRunResultStore struct {
	db TxStarter
}

func NewAutomationRunResultStore(db TxStarter) *AutomationRunResultStore {
	return &AutomationRunResultStore{db: db}
}

const automationRunResultColumns = `run_id, org_id, attempt, attempt_lock_token, thread_id, turn_number, outcome,
	review_complete, checkpoint_key, checkpoint_published, checkpoint_head_sha, native_context,
	dependency_fingerprint, agent_session_id, recorded_at`

// prefixColumns qualifies each comma-separated column in list with prefix,
// for projections that join the marker table with the run row.
func prefixColumns(prefix, list string) string {
	parts := strings.Split(list, ",")
	for i, part := range parts {
		parts[i] = prefix + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}

func scanAutomationRunResult(row pgx.Row) (models.AutomationRunResult, error) {
	var r models.AutomationRunResult
	err := row.Scan(
		&r.RunID, &r.OrgID, &r.Attempt, &r.AttemptLockToken, &r.ThreadID, &r.TurnNumber, &r.Outcome,
		&r.ReviewComplete, &r.CheckpointKey, &r.CheckpointPublished, &r.CheckpointHeadSHA, &r.NativeContext,
		&r.DependencyFingerprint, &r.AgentSessionID, &r.RecordedAt,
	)
	return r, err
}

// Write records the marker for the attempt identified by result.Attempt and
// result.AttemptLockToken. It is fenced twice, as every attempt write is:
// the run row must still be executing under this attempt and token for
// jobID, and the jobs row must still be running under the same lock token.
// One row exists per run and it is replaced only by a strictly newer
// attempt. A repeated write by the same attempt and token leaves the stored
// marker untouched and returns it, so duplicate end-of-attempt callbacks
// cannot change the evidence recovery reads. Returns false when a fence
// rejects the write (a worker that lost its lease) and no marker for this
// attempt exists, which callers treat as "not the owner" rather than an
// error. q is the transaction carrying the session status write for the
// attempt end.
func (s *AutomationRunResultStore) Write(ctx context.Context, q DBTX, orgID, jobID uuid.UUID, result *models.AutomationRunResult) (bool, error) {
	if err := result.Outcome.Validate(); err != nil {
		return false, err
	}
	if result.Attempt <= 0 {
		return false, fmt.Errorf("automation run result attempt must be positive")
	}
	if result.AttemptLockToken == uuid.Nil {
		return false, fmt.Errorf("automation run result requires an attempt lock token")
	}
	// review_complete is true only for a completed turn.
	reviewComplete := result.ReviewComplete && result.Outcome == models.AutomationRunResultTurnCompleted
	if q == nil {
		q = s.db
	}
	row := q.QueryRow(ctx, `
		INSERT INTO automation_run_results (
			run_id, org_id, attempt, attempt_lock_token, thread_id, turn_number, outcome,
			review_complete, checkpoint_key, checkpoint_published, checkpoint_head_sha, native_context,
			dependency_fingerprint, agent_session_id
		)
		SELECT r.id, r.org_id, @attempt, @attempt_lock_token, @thread_id, @turn_number, @outcome,
			@review_complete, @checkpoint_key, @checkpoint_published, @checkpoint_head_sha, @native_context,
			@dependency_fingerprint, @agent_session_id
		FROM automation_runs r
		WHERE r.id = @run_id AND r.org_id = @org_id
		  AND r.dispatch_state = 'executing'
		  AND r.attempt = @attempt
		  AND r.attempt_lock_token = @attempt_lock_token
		  AND r.job_id = @job_id
		  AND EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.id = @job_id AND j.org_id = @org_id
			  AND j.status = 'running' AND j.lock_token = @attempt_lock_token)
		ON CONFLICT (run_id) DO UPDATE SET
			attempt = EXCLUDED.attempt,
			attempt_lock_token = EXCLUDED.attempt_lock_token,
			thread_id = EXCLUDED.thread_id,
			turn_number = EXCLUDED.turn_number,
			outcome = EXCLUDED.outcome,
			review_complete = EXCLUDED.review_complete,
			checkpoint_key = EXCLUDED.checkpoint_key,
			checkpoint_published = EXCLUDED.checkpoint_published,
			checkpoint_head_sha = EXCLUDED.checkpoint_head_sha,
			native_context = EXCLUDED.native_context,
			dependency_fingerprint = EXCLUDED.dependency_fingerprint,
			agent_session_id = EXCLUDED.agent_session_id,
			recorded_at = now()
		WHERE automation_run_results.attempt < EXCLUDED.attempt
		RETURNING `+automationRunResultColumns,
		pgx.NamedArgs{
			"run_id":                 result.RunID,
			"org_id":                 orgID,
			"job_id":                 jobID,
			"attempt":                result.Attempt,
			"attempt_lock_token":     result.AttemptLockToken,
			"thread_id":              result.ThreadID,
			"turn_number":            result.TurnNumber,
			"outcome":                result.Outcome,
			"review_complete":        reviewComplete,
			"checkpoint_key":         result.CheckpointKey,
			"checkpoint_published":   result.CheckpointPublished,
			"checkpoint_head_sha":    result.CheckpointHeadSHA,
			"native_context":         result.NativeContext,
			"dependency_fingerprint": result.DependencyFingerprint,
			"agent_session_id":       result.AgentSessionID,
		})
	written, err := scanAutomationRunResult(row)
	if err == nil {
		*result = written
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("write automation run result: %w", err)
	}
	// No row: either a fence rejected the write, or a marker for this attempt
	// (or a newer one) already exists and the conflict clause left it alone.
	// A same-attempt duplicate by a caller that still passes both fences is
	// reported as success with the stored marker; anything else, including a
	// worker whose lease is gone, is "not the owner".
	existing, err := scanAutomationRunResult(q.QueryRow(ctx, `
		SELECT `+prefixColumns("m.", automationRunResultColumns)+`
		FROM automation_run_results m
		JOIN automation_runs r ON r.id = m.run_id AND r.org_id = m.org_id
		WHERE m.run_id = @run_id AND m.org_id = @org_id
		  AND m.attempt = @attempt AND m.attempt_lock_token = @attempt_lock_token
		  AND r.dispatch_state = 'executing'
		  AND r.attempt = @attempt
		  AND r.attempt_lock_token = @attempt_lock_token
		  AND r.job_id = @job_id
		  AND EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.id = @job_id AND j.org_id = @org_id
			  AND j.status = 'running' AND j.lock_token = @attempt_lock_token)`,
		pgx.NamedArgs{
			"run_id":             result.RunID,
			"org_id":             orgID,
			"attempt":            result.Attempt,
			"attempt_lock_token": result.AttemptLockToken,
			"job_id":             jobID,
		}))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read existing automation run result: %w", err)
	}
	*result = existing
	return true, nil
}

// GetByRun returns the marker for runID, or ErrAutomationRunResultNotFound.
func (s *AutomationRunResultStore) GetByRun(ctx context.Context, orgID, runID uuid.UUID) (models.AutomationRunResult, error) {
	row := s.db.QueryRow(ctx, `SELECT `+automationRunResultColumns+`
		FROM automation_run_results
		WHERE run_id = @run_id AND org_id = @org_id`,
		pgx.NamedArgs{"run_id": runID, "org_id": orgID})
	result, err := scanAutomationRunResult(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationRunResult{}, ErrAutomationRunResultNotFound
	}
	if err != nil {
		return models.AutomationRunResult{}, fmt.Errorf("get automation run result: %w", err)
	}
	return result, nil
}
