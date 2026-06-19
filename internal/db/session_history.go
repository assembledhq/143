package db

import (
	"context"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SessionHistoryFilters struct {
	Query         string
	Status        string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Limit         int
	Cursor        string
}

type SessionHistorySummary struct {
	ID              uuid.UUID            `db:"id" json:"id"`
	Title           *string              `db:"title" json:"title,omitempty"`
	Status          models.SessionStatus `db:"status" json:"status"`
	Origin          models.SessionOrigin `db:"origin" json:"origin"`
	ResultSummary   *string              `db:"result_summary" json:"result_summary,omitempty"`
	FailureCategory *string              `db:"failure_category" json:"failure_category,omitempty"`
	CreatedAt       time.Time            `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time            `db:"updated_at" json:"updated_at"`
}

type SessionHistoryStore struct {
	db DBTX
}

func NewSessionHistoryStore(db DBTX) *SessionHistoryStore {
	return &SessionHistoryStore{db: db}
}

func (s *SessionHistoryStore) Search(ctx context.Context, orgID, repoID, currentSessionID uuid.UUID, filters SessionHistoryFilters) ([]SessionHistorySummary, error) {
	limit := filters.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	var cursor *uuid.UUID
	if filters.Cursor != "" {
		parsed, err := uuid.Parse(filters.Cursor)
		if err != nil {
			return nil, fmt.Errorf("parse session history cursor: %w", err)
		}
		cursor = &parsed
	}
	// The cursor is the ID of the last row from the previous page. We use a
	// subquery to resolve its (created_at, id) position so the comparison is
	// correct for the ORDER BY created_at DESC, id DESC sort.
	rows, err := s.db.Query(ctx, `
		SELECT id, title, status, origin, result_summary, failure_category, created_at, updated_at
		FROM sessions
		WHERE org_id = @org_id
		  AND repository_id = @repo_id
		  AND id <> @current_session_id
		  AND deleted_at IS NULL
		  AND (@status::text IS NULL OR status = @status)
		  AND (@created_after::timestamptz IS NULL OR created_at >= @created_after)
		  AND (@created_before::timestamptz IS NULL OR created_at <= @created_before)
		  AND (
		    @cursor::uuid IS NULL
		    OR (created_at, id) < (
		        SELECT created_at, id FROM sessions WHERE id = @cursor AND org_id = @org_id
		    )
		  )
		  AND (
		    @query = ''
		    OR title ILIKE '%' || @query || '%'
		    OR result_summary ILIKE '%' || @query || '%'
		    OR failure_category ILIKE '%' || @query || '%'
		  )
		ORDER BY created_at DESC, id DESC
		LIMIT @limit`,
		pgx.NamedArgs{
			"org_id":             orgID,
			"repo_id":            repoID,
			"current_session_id": currentSessionID,
			"status":             nullableString(filters.Status),
			"created_after":      filters.CreatedAfter,
			"created_before":     filters.CreatedBefore,
			"cursor":             cursor,
			"query":              filters.Query,
			"limit":              limit,
		})
	if err != nil {
		return nil, fmt.Errorf("query session history: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[SessionHistorySummary])
}

func (s *SessionHistoryStore) Get(ctx context.Context, orgID, repoID, sessionID uuid.UUID) (SessionHistorySummary, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, title, status, origin, result_summary, failure_category, created_at, updated_at
		FROM sessions
		WHERE org_id = @org_id
		  AND repository_id = @repo_id
		  AND id = @session_id
		  AND deleted_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "repo_id": repoID, "session_id": sessionID})
	if err != nil {
		return SessionHistorySummary{}, fmt.Errorf("query session history item: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[SessionHistorySummary])
}

func (s *SessionHistoryStore) ThreadBelongsToSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (bool, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM session_threads
			WHERE org_id = @org_id AND session_id = @session_id AND id = @thread_id
		)`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "thread_id": threadID},
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check session history thread: %w", err)
	}
	return exists, nil
}
