package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type IntegrationStore struct {
	db DBTX
}

func NewIntegrationStore(db DBTX) *IntegrationStore {
	return &IntegrationStore{db: db}
}

func (s *IntegrationStore) Create(ctx context.Context, integration *models.Integration) error {
	query := `
		INSERT INTO integrations (org_id, provider, config, status)
		VALUES (@org_id, @provider, @config, @status)
		RETURNING id, created_at`

	cfg := integration.Config
	if cfg == nil {
		cfg = json.RawMessage(`{}`)
	}

	args := pgx.NamedArgs{
		"org_id":   integration.OrgID,
		"provider": integration.Provider,
		"config":   cfg,
		"status":   integration.Status,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&integration.ID, &integration.CreatedAt)
}

func (s *IntegrationStore) GetByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider string) (models.Integration, error) {
	query := `
		SELECT id, org_id, provider, config, status, last_synced_at, created_at
		FROM integrations
		WHERE org_id = @org_id AND provider = @provider`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": provider,
	})
	if err != nil {
		return models.Integration{}, fmt.Errorf("query integration: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Integration])
}

func (s *IntegrationStore) GetByID(ctx context.Context, id uuid.UUID) (models.Integration, error) {
	query := `
		SELECT id, org_id, provider, config, status, last_synced_at, created_at
		FROM integrations
		WHERE id = @id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id})
	if err != nil {
		return models.Integration{}, fmt.Errorf("query integration by id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Integration])
}

func (s *IntegrationStore) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	query := `UPDATE integrations SET status = @status WHERE id = @id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"status": status,
	})
	return err
}
