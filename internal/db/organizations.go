package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type OrganizationStore struct {
	db DBTX
}

func NewOrganizationStore(db DBTX) *OrganizationStore {
	return &OrganizationStore{db: db}
}

// Create inserts a new organization row.
// lint:allow-no-orgid reason="organizations is the root tenant table; this row IS the org"
func (s *OrganizationStore) Create(ctx context.Context, org *models.Organization) error {
	query := `
		INSERT INTO organizations (name, settings)
		VALUES (@name, @settings)
		RETURNING id, created_at, updated_at`

	args := pgx.NamedArgs{
		"name":     org.Name,
		"settings": org.Settings,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&org.ID, &org.CreatedAt, &org.UpdatedAt)
}

// GetByID returns the organization with the given id.
// lint:allow-no-orgid reason="organizations is the root tenant table; id IS the org"
func (s *OrganizationStore) GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error) {
	query := `
		SELECT id, name, settings, created_at, updated_at
		FROM organizations
		WHERE id = @id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id})
	if err != nil {
		return models.Organization{}, fmt.Errorf("query organization: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Organization])
}

// Update mutates the organization identified by org.ID.
// lint:allow-no-orgid reason="organizations is the root tenant table; org.ID IS the org"
func (s *OrganizationStore) Update(ctx context.Context, org *models.Organization) error {
	query := `
		UPDATE organizations
		SET name = @name, settings = @settings, updated_at = now()
		WHERE id = @id
		RETURNING updated_at`

	args := pgx.NamedArgs{
		"id":       org.ID,
		"name":     org.Name,
		"settings": org.Settings,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&org.UpdatedAt)
}

// MergeCodingAgentDefaults merges non-secret Amp/Pi defaults into
// settings.agent_config.<agent> without replacing unrelated settings keys.
// lint:allow-no-orgid reason="organizations is the root tenant table; orgID IS the org"
func (s *OrganizationStore) MergeCodingAgentDefaults(ctx context.Context, orgID uuid.UUID, agent models.AgentType, defaults map[string]string) error {
	if len(defaults) == 0 {
		return nil
	}
	if err := models.ValidateSettingsModels(models.OrgSettings{
		AgentConfig: models.AgentEnvConfig{
			string(agent): defaults,
		},
	}); err != nil {
		return err
	}

	defaultsJSON, err := json.Marshal(defaults)
	if err != nil {
		return fmt.Errorf("marshal coding agent defaults: %w", err)
	}

	query := `
		UPDATE organizations
		SET settings = jsonb_set(
			COALESCE(settings, '{}'::jsonb),
			ARRAY['agent_config', @agent],
			COALESCE(COALESCE(settings, '{}'::jsonb)->'agent_config'->@agent, '{}'::jsonb) || @defaults::jsonb,
			true
		),
		    updated_at = now()
		WHERE id = @id
		RETURNING updated_at`

	var updatedAt any
	err = s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":       orgID,
		"agent":    string(agent),
		"defaults": defaultsJSON,
	}).Scan(&updatedAt)
	if err != nil {
		return fmt.Errorf("merge coding agent defaults: %w", err)
	}
	return nil
}
