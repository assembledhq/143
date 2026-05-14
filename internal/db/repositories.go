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
	db   DBTX
	pool TxStarter
}

func NewRepositoryStore(db DBTX) *RepositoryStore {
	s := &RepositoryStore{db: db}
	if pool, ok := db.(TxStarter); ok {
		s.pool = pool
	}
	return s
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

// RepositoryFilters controls optional predicates on ListByOrg. Default behavior
// (zero value) returns only active repos, which is what every picker UI wants;
// set IncludeDisconnected to surface historical repo rows for settings views.
type RepositoryFilters struct {
	IncludeDisconnected bool
}

func (s *RepositoryStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters RepositoryFilters) ([]models.Repository, error) {
	query := `
		SELECT id, org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, last_synced_at, context_quality, settings, created_at, updated_at
		FROM repositories
		WHERE org_id = @org_id`
	if !filters.IncludeDisconnected {
		query += ` AND status = 'active'`
	}
	query += ` ORDER BY full_name ASC`

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

// SetStatus flips a repo's status within an org. Returns the refreshed row so
// callers can echo it back to the client without an extra round-trip. The
// status parameter is typed (models.RepositoryStatus) so callers must pass one
// of the named constants — a stray string literal won't compile.
func (s *RepositoryStore) SetStatus(ctx context.Context, orgID, repoID uuid.UUID, status models.RepositoryStatus) (models.Repository, error) {
	query := `
		UPDATE repositories
		SET status = @status, updated_at = now()
		WHERE id = @id AND org_id = @org_id
		RETURNING id, org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, last_synced_at, context_quality, settings, created_at, updated_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     repoID,
		"org_id": orgID,
		"status": string(status),
	})
	if err != nil {
		return models.Repository{}, fmt.Errorf("update repository status: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Repository])
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

type GitHubRepoOwner struct {
	RepositoryID uuid.UUID `db:"repository_id"`
	OrgID        uuid.UUID `db:"org_id"`
	OrgName      string    `db:"org_name"`
	GitHubID     int64     `db:"github_id"`
	FullName     string    `db:"full_name"`
	Status       string    `db:"status"`
}

// GetActiveOwnerByGitHubID returns the sole active 143 owner for a GitHub repo.
// lint:allow-no-orgid reason="global ownership lookup by GitHub repo id before routing webhooks or claim conflicts"
func (s *RepositoryStore) GetActiveOwnerByGitHubID(ctx context.Context, githubID int64) (GitHubRepoOwner, error) {
	query := `
		SELECT r.id AS repository_id, r.org_id, o.name AS org_name, r.github_id, r.full_name, r.status
		FROM repositories r
		JOIN organizations o ON o.id = r.org_id
		WHERE r.github_id = @github_id AND r.status = 'active'`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"github_id": githubID})
	if err != nil {
		return GitHubRepoOwner{}, fmt.Errorf("query active github repo owner: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[GitHubRepoOwner])
}

func (s *RepositoryStore) GetByOrgAndGitHubIDAnyStatus(ctx context.Context, orgID uuid.UUID, githubID int64) (models.Repository, error) {
	query := `
		SELECT id, org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, last_synced_at, context_quality, settings, created_at, updated_at
		FROM repositories
		WHERE org_id = @org_id AND github_id = @github_id`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "github_id": githubID})
	if err != nil {
		return models.Repository{}, fmt.Errorf("query repository by org and github id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Repository])
}

func (s *RepositoryStore) CountActiveByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM repositories
		WHERE org_id = @org_id AND status = 'active'`,
		pgx.NamedArgs{"org_id": orgID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active repositories by org: %w", err)
	}
	return count, nil
}

func (s *RepositoryStore) ClaimFromGitHub(ctx context.Context, repo *models.Repository) error {
	query := `
		INSERT INTO repositories (org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, settings)
		VALUES (@org_id, @integration_id, @github_id, @full_name, @default_branch, @private, @language, @description, @clone_url, @installation_id, 'active', @settings)
		ON CONFLICT (org_id, github_id) DO UPDATE
		SET integration_id = EXCLUDED.integration_id,
		    full_name = EXCLUDED.full_name,
		    default_branch = EXCLUDED.default_branch,
		    private = EXCLUDED.private,
		    language = EXCLUDED.language,
		    description = EXCLUDED.description,
		    clone_url = EXCLUDED.clone_url,
		    installation_id = EXCLUDED.installation_id,
		    status = 'active',
		    updated_at = now()
		RETURNING id, created_at, updated_at`

	settings := repo.Settings
	if settings == nil {
		settings = json.RawMessage(`{}`)
	}

	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
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
		"settings":        settings,
	}).Scan(&repo.ID, &repo.CreatedAt, &repo.UpdatedAt)
	if err != nil {
		return fmt.Errorf("claim github repository: %w", err)
	}
	repo.Status = string(models.RepositoryStatusActive)
	return nil
}

func (s *RepositoryStore) ApplyGitHubClaims(ctx context.Context, orgID uuid.UUID, repos []*models.Repository, transferOwners map[int64]uuid.UUID) error {
	if s.pool == nil {
		for _, repo := range repos {
			if repo.OrgID != orgID {
				return fmt.Errorf("claim repository org mismatch: %s != %s", repo.OrgID, orgID)
			}
			if ownerOrgID, ok := transferOwners[repo.GitHubID]; ok {
				if err := s.disconnectByOrgAndGitHubID(ctx, ownerOrgID, repo.GitHubID); err != nil {
					return err
				}
			}
			if err := s.ClaimFromGitHub(ctx, repo); err != nil {
				return err
			}
		}
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin github repo claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txStore := NewRepositoryStore(tx)
	for _, repo := range repos {
		if repo.OrgID != orgID {
			return fmt.Errorf("claim repository org mismatch: %s != %s", repo.OrgID, orgID)
		}
		if ownerOrgID, ok := transferOwners[repo.GitHubID]; ok {
			if err := txStore.disconnectByOrgAndGitHubID(ctx, ownerOrgID, repo.GitHubID); err != nil {
				return err
			}
		}
		if err := txStore.ClaimFromGitHub(ctx, repo); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit github repo claims: %w", err)
	}
	return nil
}

func (s *RepositoryStore) disconnectByOrgAndGitHubID(ctx context.Context, orgID uuid.UUID, githubID int64) error {
	_, err := s.db.Exec(ctx, `
		UPDATE repositories
		SET status = 'disconnected', updated_at = now()
		WHERE org_id = @org_id AND github_id = @github_id AND status = 'active'`,
		pgx.NamedArgs{"org_id": orgID, "github_id": githubID},
	)
	if err != nil {
		return fmt.Errorf("disconnect active github repo owner: %w", err)
	}
	return nil
}

// GetByFullName returns the active repository with the given owner/name slug,
// scoped to the provided org. Scoping matters because the same GitHub repo
// can legitimately be connected to more than one org (e.g. a contractor who
// installs the app into two separate customer orgs), and an unscoped lookup
// would error on multiple rows or silently cross org boundaries.
func (s *RepositoryStore) GetByFullName(ctx context.Context, orgID uuid.UUID, fullName string) (models.Repository, error) {
	query := `
		SELECT id, org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, last_synced_at, context_quality, settings, created_at, updated_at
		FROM repositories
		WHERE org_id = @org_id AND full_name = @full_name AND status = 'active'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "full_name": fullName})
	if err != nil {
		return models.Repository{}, fmt.Errorf("query repository by full name: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Repository])
}

// GetByFullNameAnyStatus returns a repository by owner/name regardless of its
// connection status. This is used by read-only flows that need metadata for an
// already-linked repo after the user has intentionally disconnected it.
func (s *RepositoryStore) GetByFullNameAnyStatus(ctx context.Context, orgID uuid.UUID, fullName string) (models.Repository, error) {
	query := `
		SELECT id, org_id, integration_id, github_id, full_name, default_branch, private, language, description, clone_url, installation_id, status, last_synced_at, context_quality, settings, created_at, updated_at
		FROM repositories
		WHERE org_id = @org_id AND full_name = @full_name`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "full_name": fullName})
	if err != nil {
		return models.Repository{}, fmt.Errorf("query repository by full name: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Repository])
}

// GetAnyInstallationIDByOrg returns one non-zero GitHub installation_id for an
// active repo in the org. This is a repair-path fallback for orgs whose
// integrations.github config is missing installation_id even though repo sync
// already populated repository rows with the correct installation.
func (s *RepositoryStore) GetAnyInstallationIDByOrg(ctx context.Context, orgID uuid.UUID) (int64, error) {
	query := `
		SELECT installation_id
		FROM repositories
		WHERE org_id = @org_id
		  AND status = 'active'
		  AND installation_id > 0
		ORDER BY full_name ASC
		LIMIT 1`

	var installationID int64
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID}).Scan(&installationID)
	if err != nil {
		return 0, err
	}
	return installationID, nil
}

// DisconnectByIntegration marks every repo under the given integration as
// disconnected within the org. This keeps repo state aligned with an explicit
// user disconnect of the parent GitHub integration.
func (s *RepositoryStore) DisconnectByIntegration(ctx context.Context, orgID, integrationID uuid.UUID) error {
	query := `
		UPDATE repositories
		SET status = 'disconnected', updated_at = now()
		WHERE org_id = @org_id AND integration_id = @integration_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":         orgID,
		"integration_id": integrationID,
	})
	return err
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
			COUNT(DISTINCT session_repo.id) FILTER (
				WHERE session_repo.status IN ('running', 'pending', 'needs_human_guidance', 'awaiting_input')
			) AS active_session_count,
			latest_s.status AS latest_session_status,
			COUNT(DISTINCT p.id) FILTER (
				WHERE p.status IN ('active', 'planning')
			) AS active_project_count
	FROM repositories r
	LEFT JOIN (
		SELECT
			s.id,
			s.org_id,
			s.status,
			s.repository_id AS resolved_repository_id
		FROM sessions s
		WHERE s.deleted_at IS NULL
	) session_repo ON session_repo.org_id = r.org_id AND session_repo.resolved_repository_id = r.id
	LEFT JOIN projects p ON p.repository_id = r.id AND p.org_id = r.org_id
	LEFT JOIN LATERAL (
		SELECT s2.status FROM sessions s2
		WHERE s2.org_id = r.org_id
		  AND s2.deleted_at IS NULL
		  AND s2.repository_id = r.id
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
