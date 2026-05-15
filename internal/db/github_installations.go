package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type GitHubInstallationStore struct {
	db DBTX
}

func NewGitHubInstallationStore(db DBTX) *GitHubInstallationStore {
	return &GitHubInstallationStore{db: db}
}

// UpsertInstallation writes global GitHub App installation metadata.
// lint:allow-no-orgid reason="global GitHub App installation identity shared by multiple 143 organizations"
func (s *GitHubInstallationStore) UpsertInstallation(ctx context.Context, installation *models.GitHubInstallation) error {
	query := `
		INSERT INTO github_installations (installation_id, account_id, account_login, account_type, repository_selection, status)
		VALUES (@installation_id, @account_id, @account_login, @account_type, @repository_selection, @status)
		ON CONFLICT (installation_id) DO UPDATE
		SET account_id = CASE
		        WHEN EXCLUDED.account_id <> 0 THEN EXCLUDED.account_id
		        ELSE github_installations.account_id
		    END,
		    account_login = CASE
		        WHEN EXCLUDED.account_login <> '' AND EXCLUDED.account_login <> 'unknown' THEN EXCLUDED.account_login
		        ELSE github_installations.account_login
		    END,
		    account_type = COALESCE(EXCLUDED.account_type, github_installations.account_type),
		    repository_selection = COALESCE(EXCLUDED.repository_selection, github_installations.repository_selection),
		    status = EXCLUDED.status,
		    updated_at = now()
		RETURNING account_id, account_login, account_type, repository_selection, status, created_at, updated_at`
	status := installation.Status
	if status == "" {
		status = "active"
	}
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"installation_id":      installation.InstallationID,
		"account_id":           installation.AccountID,
		"account_login":        installation.AccountLogin,
		"account_type":         installation.AccountType,
		"repository_selection": installation.RepositorySelection,
		"status":               status,
	}).Scan(
		&installation.AccountID,
		&installation.AccountLogin,
		&installation.AccountType,
		&installation.RepositorySelection,
		&installation.Status,
		&installation.CreatedAt,
		&installation.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert github installation: %w", err)
	}
	return nil
}

func (s *GitHubInstallationStore) UpsertOrgLink(ctx context.Context, link *models.GitHubInstallationOrgLink) error {
	query := `
		INSERT INTO github_installation_org_links (org_id, integration_id, installation_id, account_login, linked_by_user_id, status)
		VALUES (@org_id, @integration_id, @installation_id, @account_login, @linked_by_user_id, @status)
		ON CONFLICT (org_id, installation_id) WHERE status = 'active' DO UPDATE
		SET integration_id = EXCLUDED.integration_id,
		    account_login = CASE
		        WHEN EXCLUDED.account_login <> '' AND EXCLUDED.account_login <> 'unknown' THEN EXCLUDED.account_login
		        ELSE github_installation_org_links.account_login
		    END,
		    linked_by_user_id = COALESCE(EXCLUDED.linked_by_user_id, github_installation_org_links.linked_by_user_id),
		    updated_at = now()
		RETURNING id, account_login, created_at, updated_at`
	status := link.Status
	if status == "" {
		status = "active"
	}
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":            link.OrgID,
		"integration_id":    link.IntegrationID,
		"installation_id":   link.InstallationID,
		"account_login":     link.AccountLogin,
		"linked_by_user_id": link.LinkedByUserID,
		"status":            status,
	}).Scan(&link.ID, &link.AccountLogin, &link.CreatedAt, &link.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert github installation org link: %w", err)
	}
	link.Status = status
	return nil
}

func (s *GitHubInstallationStore) GetOrgLink(ctx context.Context, orgID uuid.UUID, installationID int64) (models.GitHubInstallationOrgLink, error) {
	query := `
		SELECT l.id, l.org_id, l.integration_id, l.installation_id, l.account_login, l.linked_by_user_id, l.status, l.created_at, l.updated_at
		FROM github_installation_org_links l
		JOIN github_installations gi ON gi.installation_id = l.installation_id
		WHERE l.org_id = @org_id AND l.installation_id = @installation_id AND l.status = 'active' AND gi.status = 'active'`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "installation_id": installationID})
	if err != nil {
		return models.GitHubInstallationOrgLink{}, fmt.Errorf("query github installation org link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.GitHubInstallationOrgLink])
}

func (s *GitHubInstallationStore) FirstOrgLink(ctx context.Context, orgID uuid.UUID) (models.GitHubInstallationOrgLink, error) {
	query := `
		SELECT l.id, l.org_id, l.integration_id, l.installation_id, l.account_login, l.linked_by_user_id, l.status, l.created_at, l.updated_at
		FROM github_installation_org_links l
		JOIN github_installations gi ON gi.installation_id = l.installation_id
		WHERE l.org_id = @org_id AND l.status = 'active' AND gi.status = 'active'
		ORDER BY l.created_at DESC
		LIMIT 1`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return models.GitHubInstallationOrgLink{}, fmt.Errorf("query first github installation org link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.GitHubInstallationOrgLink])
}

// GetByInstallationID reads global GitHub App installation metadata.
// lint:allow-no-orgid reason="global GitHub App installation lookup; installation rows are shared by org links"
func (s *GitHubInstallationStore) GetByInstallationID(ctx context.Context, installationID int64) (models.GitHubInstallation, error) {
	query := `
		SELECT installation_id, account_id, account_login, account_type, repository_selection, status, created_at, updated_at
		FROM github_installations
		WHERE installation_id = @installation_id`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"installation_id": installationID})
	if err != nil {
		return models.GitHubInstallation{}, fmt.Errorf("query github installation: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.GitHubInstallation])
}

// SetInstallationStatus updates a global installation lifecycle state.
// lint:allow-no-orgid reason="GitHub installation uninstall webhook has no 143 org context and applies globally"
func (s *GitHubInstallationStore) SetInstallationStatus(ctx context.Context, installationID int64, status string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE github_installations
		SET status = @status, updated_at = now()
		WHERE installation_id = @installation_id`,
		pgx.NamedArgs{"installation_id": installationID, "status": status},
	)
	if err != nil {
		return fmt.Errorf("update github installation status: %w", err)
	}
	return nil
}

// RefreshOrgLinkAccountLogin updates placeholder account labels once a webhook
// provides authoritative GitHub account metadata.
// lint:allow-no-orgid reason="GitHub installation metadata is shared by all org links for the installation"
func (s *GitHubInstallationStore) RefreshOrgLinkAccountLogin(ctx context.Context, installationID int64, accountLogin string) error {
	if accountLogin == "" || accountLogin == "unknown" {
		return nil
	}
	_, err := s.db.Exec(ctx, `
		UPDATE github_installation_org_links
		SET account_login = @account_login, updated_at = now()
		WHERE installation_id = @installation_id
		  AND status = 'active'
		  AND account_login IN ('', 'unknown')`,
		pgx.NamedArgs{"installation_id": installationID, "account_login": accountLogin},
	)
	if err != nil {
		return fmt.Errorf("refresh github installation org link account login: %w", err)
	}
	return nil
}

// DeactivateOrgLinksByInstallationID deactivates every org link for an uninstalled GitHub App installation.
// lint:allow-no-orgid reason="GitHub installation uninstall webhook has no 143 org context and applies globally"
func (s *GitHubInstallationStore) DeactivateOrgLinksByInstallationID(ctx context.Context, installationID int64) error {
	_, err := s.db.Exec(ctx, `
		UPDATE github_installation_org_links
		SET status = 'deleted', updated_at = now()
		WHERE installation_id = @installation_id AND status = 'active'`,
		pgx.NamedArgs{"installation_id": installationID},
	)
	if err != nil {
		return fmt.Errorf("deactivate github installation org links: %w", err)
	}
	return nil
}

func (s *GitHubInstallationStore) DeactivateOrgLinksByIntegration(ctx context.Context, orgID, integrationID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE github_installation_org_links
		SET status = 'inactive', updated_at = now()
		WHERE org_id = @org_id AND integration_id = @integration_id AND status = 'active'`,
		pgx.NamedArgs{"org_id": orgID, "integration_id": integrationID},
	)
	if err != nil {
		return fmt.Errorf("deactivate github installation org links by integration: %w", err)
	}
	return nil
}
