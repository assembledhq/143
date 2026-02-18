package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type AgentRunLogStore struct {
	db DBTX
}

func NewAgentRunLogStore(db DBTX) *AgentRunLogStore {
	return &AgentRunLogStore{db: db}
}

func (s *AgentRunLogStore) Create(ctx context.Context, log *models.AgentRunLog) error {
	query := `
		INSERT INTO agent_run_logs (agent_run_id, level, message, metadata)
		VALUES (@agent_run_id, @level, @message, @metadata)
		RETURNING id, timestamp`

	args := pgx.NamedArgs{
		"agent_run_id": log.AgentRunID,
		"level":        log.Level,
		"message":      log.Message,
		"metadata":     log.Metadata,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&log.ID, &log.Timestamp)
}

func (s *AgentRunLogStore) ListByRunID(ctx context.Context, agentRunID uuid.UUID) ([]models.AgentRunLog, error) {
	query := `
		SELECT id, agent_run_id, timestamp, level, message, metadata
		FROM agent_run_logs
		WHERE agent_run_id = @agent_run_id
		ORDER BY id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"agent_run_id": agentRunID,
	})
	if err != nil {
		return nil, fmt.Errorf("query agent run logs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.AgentRunLog])
}

func (s *AgentRunLogStore) ListByRunIDSince(ctx context.Context, agentRunID uuid.UUID, sinceID int64) ([]models.AgentRunLog, error) {
	query := `
		SELECT id, agent_run_id, timestamp, level, message, metadata
		FROM agent_run_logs
		WHERE agent_run_id = @agent_run_id AND id > @since_id
		ORDER BY id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"agent_run_id": agentRunID,
		"since_id":     sinceID,
	})
	if err != nil {
		return nil, fmt.Errorf("query agent run logs since: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.AgentRunLog])
}
