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
		SELECT id, name, release_channel, settings, created_at, updated_at
		FROM organizations
		WHERE id = @id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id})
	if err != nil {
		return models.Organization{}, fmt.Errorf("query organization: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Organization])
}

// GetReleaseChannel returns the org's release channel. Used on the hot path
// by the canary host guard, so it selects the single column instead of the
// whole row.
// lint:allow-no-orgid reason="organizations is the root tenant table; id IS the org"
func (s *OrganizationStore) GetReleaseChannel(ctx context.Context, id uuid.UUID) (models.ReleaseChannel, error) {
	var channel models.ReleaseChannel
	err := s.db.QueryRow(ctx, `
		SELECT release_channel
		FROM organizations
		WHERE id = @id`, pgx.NamedArgs{"id": id}).Scan(&channel)
	if err != nil {
		return "", fmt.Errorf("query organization release channel: %w", err)
	}
	return channel, nil
}

// ListIDs returns tenant root IDs for system cleanup tasks.
// lint:allow-no-orgid reason="organizations is the root tenant table; cleanup iterates org IDs before org-scoped deletes"
func (s *OrganizationStore) ListIDs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id
		FROM organizations
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("query organization ids: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
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

// UpdateSettings replaces the org's settings JSONB blob. Caller is
// responsible for read-modify-write semantics — this method simply
// overwrites whatever is there. Used by admin endpoints that own a
// well-defined sub-section of the settings (e.g. LinearAgent.Enabled)
// and have already constructed the merged document.
func (s *OrganizationStore) UpdateSettings(ctx context.Context, orgID uuid.UUID, settings []byte) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE organizations SET settings = @settings, updated_at = now()
		WHERE id = @id`,
		pgx.NamedArgs{"id": orgID, "settings": settings})
	if err != nil {
		return fmt.Errorf("update organization settings: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("organization not found")
	}
	return nil
}

// MergeLinearAgentSettings replaces ONLY the `linear_agent` sub-key of
// organizations.settings, leaving every other key untouched. Used by the
// admin enable-toggle and the OAuth post-install bootstrap so a concurrent
// writer mutating a different settings sub-key (e.g. AgentConfig, OrgSize)
// doesn't get clobbered by a whole-blob UPDATE.
//
// The caller passes the desired LinearAgentSettings value — we marshal it
// and jsonb_set it into place. NULL is normalized to empty object so a
// fresh row gets a coherent shape.
// lint:allow-no-orgid reason="organizations is the root tenant table; orgID IS the org"
func (s *OrganizationStore) MergeLinearAgentSettings(ctx context.Context, orgID uuid.UUID, settings models.LinearAgentSettings) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal linear_agent settings: %w", err)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE organizations
		SET settings = jsonb_set(
			COALESCE(settings, '{}'::jsonb),
			'{linear_agent}',
			@linear_agent::jsonb,
			true
		),
		    updated_at = now()
		WHERE id = @id`,
		pgx.NamedArgs{"id": orgID, "linear_agent": raw})
	if err != nil {
		return fmt.Errorf("merge linear_agent settings: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("organization not found")
	}
	return nil
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
