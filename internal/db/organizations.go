package db

import (
	"context"
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
