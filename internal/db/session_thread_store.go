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
	started_at, completed_at, created_at`

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

// ClaimIdleForSession atomically transitions an idle thread to running only
// when no sibling thread for the same session is active. The CTE locks every
// thread row in the session before evaluating active siblings so concurrent
// sends to different idle tabs serialize on the database row locks.
func (s *SessionThreadStore) ClaimIdleForSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	query := `
		WITH locked_threads AS (
			SELECT id, status
			FROM session_threads
			WHERE org_id = @org_id AND session_id = @session_id
			FOR UPDATE
		), eligible AS (
			SELECT 1
			FROM locked_threads target
			WHERE target.id = @id
			  AND target.status = 'idle'
			  AND NOT EXISTS (
			      SELECT 1
			      FROM locked_threads sibling
			      WHERE sibling.id <> @id
			        AND sibling.status IN ('pending', 'running', 'awaiting_input')
			  )
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
		"id":         threadID,
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("claim idle session thread: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
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
