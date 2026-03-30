package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PRTemplate is the DB row for cached repository PR templates.
type PRTemplate struct {
	ID              uuid.UUID `db:"id"`
	RepositoryID    uuid.UUID `db:"repository_id"`
	OrgID           uuid.UUID `db:"org_id"`
	TemplateContent string    `db:"template_content"`
	TemplatePath    string    `db:"template_path"`
	FetchedAt       time.Time `db:"fetched_at"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
}

// PRTemplateStore handles cached PR template persistence.
type PRTemplateStore struct {
	db DBTX
}

// NewPRTemplateStore creates a new PR template store.
func NewPRTemplateStore(db DBTX) *PRTemplateStore {
	return &PRTemplateStore{db: db}
}

// GetByRepositoryID returns the cached PR template for a repository, if any.
func (s *PRTemplateStore) GetByRepositoryID(ctx context.Context, repoID uuid.UUID) (*PRTemplate, error) {
	query := `
		SELECT id, repository_id, org_id, template_content, template_path,
		       fetched_at, created_at, updated_at
		FROM repository_pr_templates
		WHERE repository_id = @repository_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"repository_id": repoID,
	})
	if err != nil {
		return nil, fmt.Errorf("query pr template: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[PRTemplate])
	if err != nil {
		return nil, fmt.Errorf("get pr template: %w", err)
	}
	return &row, nil
}

// Upsert inserts or updates the cached PR template for a repository.
func (s *PRTemplateStore) Upsert(ctx context.Context, repoID, orgID uuid.UUID, content, path string) error {
	query := `
		INSERT INTO repository_pr_templates (repository_id, org_id, template_content, template_path, fetched_at)
		VALUES (@repository_id, @org_id, @template_content, @template_path, now())
		ON CONFLICT (repository_id) DO UPDATE SET
			template_content = EXCLUDED.template_content,
			template_path = EXCLUDED.template_path,
			fetched_at = now(),
			updated_at = now()`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"repository_id":    repoID,
		"org_id":           orgID,
		"template_content": content,
		"template_path":    path,
	})
	return err
}
