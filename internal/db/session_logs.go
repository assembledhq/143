package db

import (
	"context"
	"fmt"
	"math"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type SessionLogStore struct {
	db      DBTX
	streams *cache.SessionStreams
	logger  zerolog.Logger
}

type SessionLogFilterOptions struct {
	TurnNumbers []int
}

func NewSessionLogStore(db DBTX) *SessionLogStore {
	return &SessionLogStore{db: db, logger: zerolog.Nop()}
}

// SetStreams injects the Redis stream helper used for live session log fan-out.
// lint:allow-no-orgid reason="process-wide dependency injection for Redis session log streaming"
func (s *SessionLogStore) SetStreams(streams *cache.SessionStreams) {
	s.streams = streams
}

// SetLogger injects the structured logger used for best-effort stream publishing.
// lint:allow-no-orgid reason="process-wide dependency injection for store logging"
func (s *SessionLogStore) SetLogger(logger zerolog.Logger) {
	s.logger = logger
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
	if err := row.Scan(&log.ID, &log.Timestamp); err != nil {
		return err
	}
	if s.streams != nil {
		if err := s.streams.PublishLog(ctx, log); err != nil {
			s.logger.Warn().Err(err).Str("session_id", log.SessionID.String()).Msg("failed to publish session log to Redis")
		}
	}
	return nil
}

func (s *SessionLogStore) MarkAssistantTranscriptDuplicate(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID, turnNumber int, message string) error {
	query := `
		UPDATE session_logs
		SET metadata = COALESCE(NULLIF(metadata, 'null'::jsonb), '{}'::jsonb) || '{"type":"assistant_final","duplicate_of_transcript":true}'::jsonb
		WHERE id = (
			SELECT id
			FROM session_logs
			WHERE org_id = @org_id
			  AND session_id = @session_id
			  AND thread_id IS NOT DISTINCT FROM @thread_id
			  AND turn_number = @turn_number
			  AND level = 'output'
			  AND message = @message
			ORDER BY id DESC
			LIMIT 1
		)`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":      orgID,
		"session_id":  sessionID,
		"thread_id":   threadID,
		"turn_number": turnNumber,
		"message":     message,
	})
	if err != nil {
		return fmt.Errorf("mark assistant transcript duplicate: %w", err)
	}
	return nil
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

func (s *SessionLogStore) ListByThreadTurns(ctx context.Context, orgID, threadID uuid.UUID, turnNumbers []int) ([]models.SessionLog, error) {
	if len(turnNumbers) == 0 {
		return s.ListByThread(ctx, orgID, threadID)
	}
	pgTurns := make([]int32, 0, len(turnNumbers))
	for _, turnNumber := range turnNumbers {
		if turnNumber < 0 || turnNumber > math.MaxInt32 {
			continue
		}
		pgTurns = append(pgTurns, int32(turnNumber))
	}
	if len(pgTurns) == 0 {
		return []models.SessionLog{}, nil
	}
	query := `
		SELECT sl.id, sl.session_id, sl.org_id, sl.thread_id, sl.timestamp, sl.level, sl.message, sl.metadata, sl.turn_number
		FROM session_logs sl
		WHERE sl.thread_id = @thread_id AND sl.org_id = @org_id
		  AND sl.turn_number = ANY(@turn_numbers::int[])
		ORDER BY sl.id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"thread_id":    threadID,
		"org_id":       orgID,
		"turn_numbers": pgTurns,
	})
	if err != nil {
		return nil, fmt.Errorf("query thread logs by turns: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionLog])
}

// DeleteExpired removes session logs older than the given number of days.
// lint:allow-no-orgid reason="cross-org retention cleanup across all orgs"
func (s *SessionLogStore) DeleteExpired(ctx context.Context, retentionDays int) (int64, error) {
	var deleted int64
	err := s.db.QueryRow(ctx,
		"SELECT delete_expired_session_logs($1)", retentionDays,
	).Scan(&deleted)
	return deleted, err
}
