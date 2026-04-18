package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type RepositoryStore struct {
	db DBTX
}

func NewRepositoryStore(db DBTX) *RepositoryStore {
	return &RepositoryStore{db: db}
}

func (s *RepositoryStore) Create(ctx context.Context, repo *models.Repository) error {
	query := `
		INSERT INTO repositories (org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, settings)
		VALUES (@org_id, @integration_id, @github_id, @full_name, @default_branch, @private, @language, @description, @clone_url, @installation_id, @status, @settings)
		RETURNING id, created_at, updated_at`

	args := pgx.NamedArgs{
		"org_id":          repo.OrgID,
		"integration_id":  repo.IntegrationID,
		"github_id":       repo.GitHubID,
		"full_name":       repo.FullName,
		"default_branch":  repo.DefaultBranch,
		"private":         repo.Private,
		"language":        repo.Language,
		"description":     repo.Description,
		"clone_url":       repo.CloneURL,
		"installation_id": repo.InstallationID,
		"status":          repo.Status,
		"settings":        repo.Settings,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&repo.ID, &repo.CreatedAt, &repo.UpdatedAt)
}

func (s *RepositoryStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Repository, error) {
	query := `
		SELECT id, org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, last_synced_at, context_quality, settings, created_at, updated_at
		FROM repositories
		WHERE org_id = @org_id
		ORDER BY full_name ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query repositories: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Repository])
}

func (s *RepositoryStore) GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error) {
	query := `
		SELECT id, org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, last_synced_at, context_quality, settings, created_at, updated_at
		FROM repositories
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     repoID,
		"org_id": orgID,
	})
	if err != nil {
		return models.Repository{}, fmt.Errorf("query repository: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Repository])
}

func (s *RepositoryStore) Update(ctx context.Context, repo *models.Repository) error {
	query := `
		UPDATE repositories
		SET status = @status, settings = @settings, updated_at = now()
		WHERE id = @id AND org_id = @org_id
		RETURNING updated_at`

	args := pgx.NamedArgs{
		"id":       repo.ID,
		"org_id":   repo.OrgID,
		"status":   repo.Status,
		"settings": repo.Settings,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&repo.UpdatedAt)
}

func (s *RepositoryStore) Delete(ctx context.Context, orgID, repoID uuid.UUID) error {
	query := `DELETE FROM repositories WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     repoID,
		"org_id": orgID,
	})
	return err
}

func (s *RepositoryStore) UpsertFromGitHub(ctx context.Context, repo *models.Repository) error {
	query := `
		INSERT INTO repositories (org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, settings)
		VALUES (@org_id, @integration_id, @github_id, @full_name, @default_branch, @private, @language, @description, @clone_url, @installation_id, @status, @settings)
		ON CONFLICT (org_id, github_id) DO UPDATE
		SET full_name = EXCLUDED.full_name,
		    default_branch = EXCLUDED.default_branch,
		    private = EXCLUDED.private,
		    language = EXCLUDED.language,
		    description = EXCLUDED.description,
		    clone_url = EXCLUDED.clone_url,
		    installation_id = EXCLUDED.installation_id,
		    updated_at = now()
		RETURNING id, created_at, updated_at`

	settings := repo.Settings
	if settings == nil {
		settings = json.RawMessage(`{}`)
	}

	args := pgx.NamedArgs{
		"org_id":          repo.OrgID,
		"integration_id":  repo.IntegrationID,
		"github_id":       repo.GitHubID,
		"full_name":       repo.FullName,
		"default_branch":  repo.DefaultBranch,
		"private":         repo.Private,
		"language":        repo.Language,
		"description":     repo.Description,
		"clone_url":       repo.CloneURL,
		"installation_id": repo.InstallationID,
		"status":          repo.Status,
		"settings":        settings,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&repo.ID, &repo.CreatedAt, &repo.UpdatedAt)
}

// GetByFullName returns the active repository with the given owner/name slug.
// lint:allow-no-orgid reason="pre-auth lookup in GitHub webhook handlers; no org context available"
func (s *RepositoryStore) GetByFullName(ctx context.Context, fullName string) (models.Repository, error) {
	query := `
		SELECT id, org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, last_synced_at, context_quality, settings, created_at, updated_at
		FROM repositories
		WHERE full_name = @full_name AND status = 'active'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"full_name": fullName})
	if err != nil {
		return models.Repository{}, fmt.Errorf("query repository by full name: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Repository])
}

// DisconnectByInstallationID marks every repo under the given GitHub
// installation as disconnected (cascades on app uninstall).
// lint:allow-no-orgid reason="cross-org cascade when a GitHub app installation is uninstalled"
func (s *RepositoryStore) DisconnectByInstallationID(ctx context.Context, installationID int64) error {
	query := `
		UPDATE repositories
		SET status = 'disconnected', updated_at = now()
		WHERE installation_id = @installation_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{"installation_id": installationID})
	return err
}

// RepoSummary contains aggregated counts for the repo context switcher.
type RepoSummary struct {
	RepositoryID        uuid.UUID `db:"repository_id"`
	FullName            string    `db:"full_name"`
	ActiveSessionCount  int       `db:"active_session_count"`
	LatestSessionStatus *string   `db:"latest_session_status"`
	ActiveProjectCount  int       `db:"active_project_count"`
}

func (s *RepositoryStore) GetSummary(ctx context.Context, orgID uuid.UUID) ([]RepoSummary, error) {
	query := `
		SELECT
			r.id AS repository_id,
			r.full_name,
			COUNT(DISTINCT s.id) FILTER (
				WHERE s.status IN ('running', 'pending', 'needs_human_guidance', 'awaiting_input')
			) AS active_session_count,
			latest_s.status AS latest_session_status,
			COUNT(DISTINCT p.id) FILTER (
				WHERE p.status IN ('active', 'planning')
			) AS active_project_count
		FROM repositories r
		LEFT JOIN issues i ON i.repository_id = r.id AND i.org_id = r.org_id
		LEFT JOIN sessions s ON s.issue_id = i.id AND s.org_id = r.org_id
		LEFT JOIN projects p ON p.repository_id = r.id AND p.org_id = r.org_id
		LEFT JOIN LATERAL (
			SELECT s2.status FROM sessions s2
			JOIN issues i2 ON s2.issue_id = i2.id
			WHERE i2.repository_id = r.id AND s2.org_id = r.org_id
			ORDER BY s2.created_at DESC LIMIT 1
		) latest_s ON true
		WHERE r.org_id = @org_id AND r.status = 'active'
		GROUP BY r.id, r.full_name, latest_s.status
		ORDER BY active_session_count DESC, r.full_name ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query repository summary: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[RepoSummary])
}

// DisconnectByGitHubID marks the matching repo as disconnected when GitHub
// reports it deleted via webhook.
// lint:allow-no-orgid reason="cross-org cascade when a GitHub repo is deleted via webhook"
func (s *RepositoryStore) DisconnectByGitHubID(ctx context.Context, installationID, githubID int64) error {
	query := `
		UPDATE repositories
		SET status = 'disconnected', updated_at = now()
		WHERE installation_id = @installation_id AND github_id = @github_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"installation_id": installationID,
		"github_id":       githubID,
	})
	return err
}
