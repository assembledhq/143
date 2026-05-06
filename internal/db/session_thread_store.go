package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionThreadStore struct {
	db DBTX
}

func NewSessionThreadStore(db DBTX) *SessionThreadStore {
	return &SessionThreadStore{db: db}
}

const sessionThreadSelectColumns = `id, session_id, org_id, agent_type, model_override,
	label, instructions, file_scope, status, agent_session_id, current_turn, last_activity_at,
	confidence_score, result_summary, diff, failure_explanation, failure_category,
	started_at, completed_at, created_at,
	base_snapshot_key, cost_cents, pending_message_count, cancel_requested_at`

// ErrThreadLimitReached is returned when the maximum number of threads per session
// has been reached and a new thread cannot be created.
var ErrThreadLimitReached = fmt.Errorf("thread limit reached")

func (s *SessionThreadStore) Create(ctx context.Context, thread *models.SessionThread, maxThreads int) error {
	query := `
		INSERT INTO session_threads (
			session_id, org_id, agent_type, model_override,
			label, instructions, file_scope, status
		)
		SELECT @session_id, @org_id, @agent_type, @model_override,
			@label, @instructions, @file_scope, @status
		WHERE (SELECT count(*) FROM session_threads WHERE session_id = @session_id AND org_id = @org_id) < @max_threads
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"session_id":     thread.SessionID,
		"org_id":         thread.OrgID,
		"agent_type":     thread.AgentType,
		"model_override": thread.ModelOverride,
		"label":          thread.Label,
		"instructions":   thread.Instructions,
		"file_scope":     thread.FileScope,
		"status":         thread.Status,
		"max_threads":    maxThreads,
	}

	row := s.db.QueryRow(ctx, query, args)
	err := row.Scan(&thread.ID, &thread.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrThreadLimitReached
		}
		return err
	}
	return nil
}

func (s *SessionThreadStore) GetByID(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error) {
	query := `
		SELECT ` + sessionThreadSelectColumns + `
		FROM session_threads
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     threadID,
		"org_id": orgID,
	})
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("query session thread: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
}

func (s *SessionThreadStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error) {
	query := `
		SELECT ` + sessionThreadSelectColumns + `
		FROM session_threads
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session threads: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionThread])
}

func (s *SessionThreadStore) CountBySession(ctx context.Context, orgID, sessionID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM session_threads WHERE org_id = @org_id AND session_id = @session_id`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID},
	).Scan(&count)
	return count, err
}

func (s *SessionThreadStore) UpdateStatus(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus) error {
	query := `UPDATE session_threads SET status = @status WHERE id = @id AND org_id = @org_id`
	if status == models.ThreadStatusRunning {
		query = `UPDATE session_threads SET status = @status, started_at = now() WHERE id = @id AND org_id = @org_id`
	} else if status == models.ThreadStatusCompleted || status == models.ThreadStatusFailed || status == models.ThreadStatusCancelled {
		query = `UPDATE session_threads SET status = @status, completed_at = now() WHERE id = @id AND org_id = @org_id`
	}
	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     threadID,
		"org_id": orgID,
		"status": status,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("thread not found")
	}
	return nil
}

func (s *SessionThreadStore) UpdateEditable(ctx context.Context, thread *models.SessionThread) error {
	query := `
		UPDATE session_threads
		SET agent_type = @agent_type,
		    model_override = @model_override,
		    label = @label
		WHERE id = @id
		  AND org_id = @org_id
		  AND session_id = @session_id
		  AND status = 'idle'
		  AND current_turn = 0
		RETURNING ` + sessionThreadSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":             thread.ID,
		"org_id":         thread.OrgID,
		"session_id":     thread.SessionID,
		"agent_type":     thread.AgentType,
		"model_override": thread.ModelOverride,
		"label":          thread.Label,
	})
	if err != nil {
		return fmt.Errorf("update editable thread fields: %w", err)
	}

	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
	if err != nil {
		return err
	}
	*thread = updated
	return nil
}

// CompleteTurn marks the thread idle and advances its current_turn. It is the
// Phase 1 success path: the shared session UpdateTurnComplete already records
// confidence_score, result_summary, and diff at the session level, so this
// method intentionally does not duplicate them on the thread row. Phase 2
// (thread-scoped runtime execution) will need to populate the thread's own
// result columns — switch to UpdateTurnComplete on the thread store at that
// point so per-thread review surfaces have data to show.
func (s *SessionThreadStore) CompleteTurn(ctx context.Context, orgID, threadID uuid.UUID, turn int, agentSessionID string) error {
	query := `
		UPDATE session_threads
		SET status = 'idle',
		    current_turn = @current_turn,
		    last_activity_at = now(),
		    agent_session_id = COALESCE(@agent_session_id, agent_session_id)
		WHERE id = @id AND org_id = @org_id`

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":               threadID,
		"org_id":           orgID,
		"current_turn":     turn,
		"agent_session_id": emptyStringNil(agentSessionID),
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("thread not found")
	}
	return nil
}

func emptyStringNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// UpdateResult persists agent results on a thread.
//
// COALESCE on diff preserves the prior thread diff when the current turn did
// not produce one — same rationale as the session-level update; see
// session_store.go updateResultRow for full details.
func (s *SessionThreadStore) UpdateResult(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus, result *models.SessionResult) error {
	query := `
		UPDATE session_threads
		SET status = @status,
		    completed_at = CASE
		        WHEN @status IN ('completed', 'failed', 'cancelled')
		            THEN now()
		        ELSE completed_at
		    END,
		    confidence_score = @confidence_score,
		    result_summary = @result_summary,
		    diff = COALESCE(@diff, diff),
		    failure_explanation = @failure_explanation,
		    failure_category = @failure_category
		WHERE id = @id AND org_id = @org_id`

	var failureExplanation *string
	if result.Error != nil {
		failureExplanation = result.Error
	}

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                  threadID,
		"org_id":              orgID,
		"status":              status,
		"confidence_score":    result.ConfidenceScore,
		"result_summary":      result.ResultSummary,
		"diff":                result.Diff,
		"failure_explanation": failureExplanation,
		"failure_category":    result.FailureCategory,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("thread not found")
	}
	return nil
}

// ClaimIdle atomically transitions an idle thread to running. Used when a user
// sends a follow-up message to a specific thread.
func (s *SessionThreadStore) ClaimIdle(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error) {
	query := `
		UPDATE session_threads
		SET status = 'running'
		WHERE id = @id AND org_id = @org_id AND status = 'idle'
		RETURNING ` + sessionThreadSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     threadID,
		"org_id": orgID,
	})
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("claim idle thread: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
}

// ErrThreadRunningLimitReached signals that the requested thread is idle and
// could otherwise be claimed, but the session has already reached the maximum
// allowed number of concurrent running siblings.
var ErrThreadRunningLimitReached = fmt.Errorf("session running thread limit reached")

// ClaimIdleForSession atomically transitions an idle thread to running while
// enforcing the session-wide running-thread cap. The CTE locks every thread
// row in the session before evaluating sibling state so concurrent sends to
// different idle tabs serialize on the database row locks. Allows up to
// maxRunning sibling tabs (including this one) to be active at the same time;
// callers should pass models.MaxRunningThreadsPerSession.
//
// Returns ErrThreadRunningLimitReached when the target thread is idle but
// adding it would exceed maxRunning. Callers should distinguish this from
// "thread not idle" to render the right composer affordance: in the former
// case, queueing a pending message is appropriate; in the latter, the user is
// trying to send to a tab whose own turn is still in flight.
func (s *SessionThreadStore) ClaimIdleForSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, error) {
	if maxRunning <= 0 {
		maxRunning = models.MaxRunningThreadsPerSession
	}
	query := `
		WITH locked_threads AS (
			SELECT id, status
			FROM session_threads
			WHERE org_id = @org_id AND session_id = @session_id
			FOR UPDATE
		), target_idle AS (
			SELECT 1
			FROM locked_threads
			WHERE id = @id AND status = 'idle'
		), running_count AS (
			SELECT count(*) AS n
			FROM locked_threads
			WHERE id <> @id
			  AND status IN ('pending', 'running', 'awaiting_input')
		), eligible AS (
			-- target is idle AND adding it (siblings + 1) stays at-or-under
			-- max_running, i.e. siblings_active < max_running.
			SELECT 1
			FROM target_idle, running_count
			WHERE running_count.n < @max_running
		)
		UPDATE session_threads
		SET status = 'running',
		    started_at = now(),
		    last_activity_at = now()
		WHERE id = @id
		  AND org_id = @org_id
		  AND session_id = @session_id
		  AND EXISTS (SELECT 1 FROM eligible)
		RETURNING ` + sessionThreadSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":          threadID,
		"org_id":      orgID,
		"session_id":  sessionID,
		"max_running": maxRunning,
	})
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("claim idle session thread: %w", err)
	}
	thread, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
	if err != nil {
		// pgx.ErrNoRows means the WHERE-with-EXISTS predicate did not match.
		// Distinguish "limit reached" from "not idle" by inspecting current
		// state — saves the caller a second query on the happy paths.
		if errors.Is(err, pgx.ErrNoRows) {
			limited, lookupErr := s.isAtRunningLimit(ctx, orgID, sessionID, threadID, maxRunning)
			if lookupErr == nil && limited {
				return models.SessionThread{}, ErrThreadRunningLimitReached
			}
		}
		return models.SessionThread{}, err
	}
	return thread, nil
}

// isAtRunningLimit returns true when the target thread is idle (so otherwise
// claimable) but the session already has maxRunning sibling threads active.
// Used by ClaimIdleForSession to differentiate "limit reached" from "thread
// status changed under us" without holding the FOR UPDATE lock.
//
// Uses COALESCE on the target subquery so a deleted-out-from-under-us row
// scans as the empty string instead of erroring on NULL → string. In that
// case we report "not limited" — the caller will fall through to its
// generic ErrNoRows path which the service maps to ErrThreadNotFound.
func (s *SessionThreadStore) isAtRunningLimit(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (bool, error) {
	var targetStatus string
	var siblingActive int
	err := s.db.QueryRow(ctx, `
		SELECT
		    COALESCE((SELECT status FROM session_threads WHERE id = @id AND org_id = @org_id), '') AS target_status,
		    (SELECT count(*) FROM session_threads
		        WHERE org_id = @org_id AND session_id = @session_id AND id <> @id
		          AND status IN ('pending', 'running', 'awaiting_input')) AS sibling_active
	`, pgx.NamedArgs{"id": threadID, "org_id": orgID, "session_id": sessionID}).Scan(&targetStatus, &siblingActive)
	if err != nil {
		return false, err
	}
	return targetStatus == string(models.ThreadStatusIdle) && siblingActive >= maxRunning, nil
}

// CountActive returns the number of threads in pending/running/awaiting_input
// state for a session. Used by the thread service for admission checks and by
// the API to expose running-tab counts to the UI.
func (s *SessionThreadStore) CountActive(ctx context.Context, orgID, sessionID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM session_threads
		WHERE org_id = @org_id AND session_id = @session_id
		  AND status IN ('pending', 'running', 'awaiting_input')
	`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).Scan(&count)
	return count, err
}

// SetBaseSnapshot stamps the thread-start checkpoint key onto the thread row.
// Idempotent: only sets when base_snapshot_key is currently NULL so a
// re-running first turn does not overwrite the original recovery point.
func (s *SessionThreadStore) SetBaseSnapshot(ctx context.Context, orgID, threadID uuid.UUID, key string) error {
	if key == "" {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads
		 SET base_snapshot_key = @key
		 WHERE id = @id AND org_id = @org_id AND base_snapshot_key IS NULL`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID, "key": key},
	)
	return err
}

// AddCost increments the thread's cost meter. cost_cents accumulates over the
// thread's lifetime so the UI can surface a per-tab cost without a separate
// query against usage events.
func (s *SessionThreadStore) AddCost(ctx context.Context, orgID, threadID uuid.UUID, cents float64) error {
	if cents <= 0 {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads SET cost_cents = cost_cents + @cents WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID, "cents": cents},
	)
	return err
}

// IncrementPendingMessages is called when a user queues a message on a tab
// that is currently busy with another turn. The composer reads this counter
// to render a "queued (N)" affordance.
func (s *SessionThreadStore) IncrementPendingMessages(ctx context.Context, orgID, threadID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads SET pending_message_count = pending_message_count + 1 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID},
	)
	return err
}

// ClearPendingMessages resets the queued-message counter when a turn drains
// its pending queue. Stored value is bounded below at 0 so a missed increment
// does not produce a negative count.
func (s *SessionThreadStore) ClearPendingMessages(ctx context.Context, orgID, threadID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads SET pending_message_count = 0 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID},
	)
	return err
}

// MarkCancelRequested stamps the thread with a cancel timestamp so the
// orchestrator and UI can distinguish "user pressed Cancel on this tab" from
// a session-wide cancel. Idempotent: re-cancel does not move the timestamp.
func (s *SessionThreadStore) MarkCancelRequested(ctx context.Context, orgID, threadID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads
		 SET cancel_requested_at = COALESCE(cancel_requested_at, now())
		 WHERE id = @id AND org_id = @org_id AND status IN ('pending', 'running', 'awaiting_input')`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID},
	)
	return err
}

// UpdateTurnComplete sets the thread to idle and persists turn metadata.
// COALESCE on diff: see UpdateResult above.
func (s *SessionThreadStore) UpdateTurnComplete(ctx context.Context, orgID, threadID uuid.UUID, turn int, result *models.SessionResult, agentSessionID string) error {
	query := `
		UPDATE session_threads
		SET status = 'idle', current_turn = @current_turn, last_activity_at = now(),
		    agent_session_id = @agent_session_id,
		    confidence_score = @confidence_score,
		    result_summary = @result_summary,
		    diff = COALESCE(@diff, diff)
		WHERE id = @id AND org_id = @org_id`

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":               threadID,
		"org_id":           orgID,
		"current_turn":     turn,
		"agent_session_id": agentSessionID,
		"confidence_score": result.ConfidenceScore,
		"result_summary":   result.ResultSummary,
		"diff":             result.Diff,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("thread not found")
	}
	return nil
}
