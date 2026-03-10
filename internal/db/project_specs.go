package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ProjectSpecStore struct {
	db DBTX
}

func NewProjectSpecStore(db DBTX) *ProjectSpecStore {
	return &ProjectSpecStore{db: db}
}

const specColumns = `id, project_id, org_id, title, content, spec_type, sort_order, version, created_by, created_at, updated_at`

func scanSpec(row pgx.Row) (models.ProjectSpec, error) {
	var s models.ProjectSpec
	err := row.Scan(
		&s.ID, &s.ProjectID, &s.OrgID, &s.Title, &s.Content, &s.SpecType,
		&s.SortOrder, &s.Version, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt,
	)
	return s, err
}

func scanSpecs(rows pgx.Rows) ([]models.ProjectSpec, error) {
	var specs []models.ProjectSpec
	for rows.Next() {
		var s models.ProjectSpec
		err := rows.Scan(
			&s.ID, &s.ProjectID, &s.OrgID, &s.Title, &s.Content, &s.SpecType,
			&s.SortOrder, &s.Version, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		specs = append(specs, s)
	}
	return specs, rows.Err()
}

func (s *ProjectSpecStore) Create(ctx context.Context, spec *models.ProjectSpec) error {
	query := `
		INSERT INTO project_specs (
			project_id, org_id, title, content, spec_type, sort_order, created_by
		) VALUES (
			@project_id, @org_id, @title, @content, @spec_type, @sort_order, @created_by
		) RETURNING id, version, created_at, updated_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"project_id": spec.ProjectID,
		"org_id":     spec.OrgID,
		"title":      spec.Title,
		"content":    spec.Content,
		"spec_type":  spec.SpecType,
		"sort_order": spec.SortOrder,
		"created_by": spec.CreatedBy,
	})
	return row.Scan(&spec.ID, &spec.Version, &spec.CreatedAt, &spec.UpdatedAt)
}

func (s *ProjectSpecStore) ListByProject(ctx context.Context, orgID, projectID uuid.UUID) ([]models.ProjectSpec, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_specs
		WHERE project_id = @project_id AND org_id = @org_id
		ORDER BY sort_order ASC, created_at ASC`, specColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"project_id": projectID,
		"org_id":     orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query project specs: %w", err)
	}
	defer rows.Close()
	return scanSpecs(rows)
}

func (s *ProjectSpecStore) GetByID(ctx context.Context, orgID, specID uuid.UUID) (models.ProjectSpec, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_specs WHERE id = @id AND org_id = @org_id`, specColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     specID,
		"org_id": orgID,
	})
	return scanSpec(row)
}

func (s *ProjectSpecStore) Update(ctx context.Context, spec *models.ProjectSpec) error {
	query := `UPDATE project_specs SET
		title = @title, content = @content, spec_type = @spec_type,
		sort_order = @sort_order, version = version + 1, updated_at = now()
		WHERE id = @id AND org_id = @org_id
		RETURNING version, updated_at`
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":         spec.ID,
		"org_id":     spec.OrgID,
		"title":      spec.Title,
		"content":    spec.Content,
		"spec_type":  spec.SpecType,
		"sort_order": spec.SortOrder,
	})
	return row.Scan(&spec.Version, &spec.UpdatedAt)
}

func (s *ProjectSpecStore) Delete(ctx context.Context, orgID, specID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM project_specs WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": specID, "org_id": orgID},
	)
	return err
}
