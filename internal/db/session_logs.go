package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionLogStore struct {
	db DBTX
}

func NewSessionLogStore(db DBTX) *SessionLogStore {
	return &SessionLogStore{db: db}
}

func (s *SessionLogStore) Create(ctx context.Context, log *models.SessionLog) error {
	query := `
		INSERT INTO session_logs (session_id, org_id, thread_id, level, message, metadata, turn_number)
		VALUES (@session_id, @org_id, @thread_id, @level, @message, @metadata, @turn_number)
		RETURNING id, timestamp`

	args := pgx.NamedArgs{
		"session_id":  log.SessionID,
		"org_id":      log.OrgID,
		"thread_id":   log.ThreadID,
		"level":       log.Level,
		"message":     log.Message,
		"metadata":    log.Metadata,
		"turn_number": log.TurnNumber,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&log.ID, &log.Timestamp)
}

func (s *SessionLogStore) ListByRunID(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionLog, error) {
	query := `
		SELECT sl.id, sl.session_id, sl.org_id, sl.thread_id, sl.timestamp, sl.level, sl.message, sl.metadata, sl.turn_number
		FROM session_logs sl
		WHERE sl.session_id = @session_id AND sl.org_id = @org_id
		ORDER BY sl.id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id": sessionID,
		"org_id":     orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session logs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionLog])
}

func (s *SessionLogStore) ListByRunIDSince(ctx context.Context, orgID, sessionID uuid.UUID, sinceID int64) ([]models.SessionLog, error) {
	query := `
		SELECT sl.id, sl.session_id, sl.org_id, sl.thread_id, sl.timestamp, sl.level, sl.message, sl.metadata, sl.turn_number
		FROM session_logs sl
		WHERE sl.session_id = @session_id AND sl.org_id = @org_id AND sl.id > @since_id
		ORDER BY sl.id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id": sessionID,
		"org_id":     orgID,
		"since_id":   sinceID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session logs since: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionLog])
}

func (s *SessionLogStore) ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionLog, error) {
	query := `
		SELECT sl.id, sl.session_id, sl.org_id, sl.thread_id, sl.timestamp, sl.level, sl.message, sl.metadata, sl.turn_number
		FROM session_logs sl
		WHERE sl.thread_id = @thread_id AND sl.org_id = @org_id
		ORDER BY sl.id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"thread_id": threadID,
		"org_id":    orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query thread logs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionLog])
}

// DeleteExpired removes session logs older than the given number of days.
func (s *SessionLogStore) DeleteExpired(ctx context.Context, retentionDays int) (int64, error) {
	var deleted int64
	err := s.db.QueryRow(ctx,
		"SELECT delete_expired_session_logs($1)", retentionDays,
	).Scan(&deleted)
	return deleted, err
}
