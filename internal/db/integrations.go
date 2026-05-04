package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// IntegrationStore manages third-party platform connections (GitHub, Sentry, Linear, etc.).
// Integrations store OAuth credentials, webhook configs, and sync state for external platforms.
// For AI model API keys and infrastructure credentials, see OrgCredentialStore.
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

// GetByID returns the integration with the given id, regardless of org.
// lint:allow-no-orgid reason="pre-auth lookup used by webhook handlers; returned integration carries the org"
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

func (s *IntegrationStore) ListByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider string) ([]models.Integration, error) {
	query := `
		SELECT id, org_id, provider, config, status, last_synced_at, created_at
		FROM integrations
		WHERE org_id = @org_id AND provider = @provider AND status = 'active'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": provider,
	})
	if err != nil {
		return nil, fmt.Errorf("query integrations by org and provider: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Integration])
}

// ListReusableForReconnect returns rows for (org, provider) that the
// OAuth reconnect path can reuse: active rows first, then rows the worker
// flipped to error after a 401, so a reconnect converges back onto the
// same row instead of creating a duplicate. Single round trip with the
// ORDER BY pinned in SQL — callers can take the first row.
func (s *IntegrationStore) ListReusableForReconnect(ctx context.Context, orgID uuid.UUID, provider string) ([]models.Integration, error) {
	query := `
		SELECT id, org_id, provider, config, status, last_synced_at, created_at
		FROM integrations
		WHERE org_id = @org_id
		  AND provider = @provider
		  AND status IN ('active', 'error')
		ORDER BY (status = 'active') DESC, created_at DESC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": provider,
	})
	if err != nil {
		return nil, fmt.Errorf("query reusable integrations by org and provider: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Integration])
}

func (s *IntegrationStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Integration, error) {
	query := `
		SELECT id, org_id, provider, config, status, last_synced_at, created_at
		FROM integrations
		WHERE org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query integrations by org: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Integration])
}

func (s *IntegrationStore) UpdateLastSyncedAt(ctx context.Context, orgID, id uuid.UUID, syncedAt time.Time) error {
	query := `UPDATE integrations SET last_synced_at = @last_synced_at WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":             id,
		"org_id":         orgID,
		"last_synced_at": syncedAt,
	})
	return err
}

func (s *IntegrationStore) UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status string) error {
	query := `UPDATE integrations SET status = @status WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"status": status,
	})
	return err
}

func (s *IntegrationStore) UpdateConfig(ctx context.Context, orgID, integrationID uuid.UUID, config json.RawMessage) error {
	query := `UPDATE integrations SET config = @config WHERE org_id = @org_id AND id = @id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     integrationID,
		"org_id": orgID,
		"config": config,
	})
	return err
}

// UpdateStatusAndConfig flips status and rewrites config in a single SQL
// statement so the auth-error mark / clear paths can't observe a partial
// state where one field updated and the other didn't.
func (s *IntegrationStore) UpdateStatusAndConfig(ctx context.Context, orgID, integrationID uuid.UUID, status string, config json.RawMessage) error {
	query := `UPDATE integrations SET status = @status, config = @config WHERE org_id = @org_id AND id = @id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     integrationID,
		"org_id": orgID,
		"status": status,
		"config": config,
	})
	return err
}

// GetByGitHubInstallationID returns the active GitHub integration for the
// given installation id, used by webhook dispatch to map an event to an org.
// lint:allow-no-orgid reason="GitHub webhook lookup by installation ID; no org context available pre-auth"
func (s *IntegrationStore) GetByGitHubInstallationID(ctx context.Context, installationID int64) (models.Integration, error) {
	query := `
		SELECT id, org_id, provider, config, status, last_synced_at, created_at
		FROM integrations
		WHERE provider = 'github'
		  AND (config->>'installation_id')::bigint = @installation_id
		  AND status = 'active'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"installation_id": installationID})
	if err != nil {
		return models.Integration{}, fmt.Errorf("query integration by github installation id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Integration])
}

// ListOrgsWithActiveIntegrations returns every org that currently has at
// least one active integration, used by background sync schedulers.
// lint:allow-no-orgid reason="deliberately cross-org scan enumerating orgs with active integrations"
func (s *IntegrationStore) ListOrgsWithActiveIntegrations(ctx context.Context) ([]uuid.UUID, error) {
	query := `SELECT DISTINCT org_id FROM integrations WHERE status = 'active'`
	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query orgs with active integrations: %w", err)
	}

	var orgIDs []uuid.UUID
	for rows.Next() {
		var orgID uuid.UUID
		if err := rows.Scan(&orgID); err != nil {
			return nil, fmt.Errorf("scan org id: %w", err)
		}
		orgIDs = append(orgIDs, orgID)
	}
	return orgIDs, nil
}
