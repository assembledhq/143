package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/assembledhq/143/internal/models"
)

var ErrGitHubOrgAlreadyCaptured = errors.New("github organization already captured")

type GitHubInstallationStore struct {
	db   DBTX
	pool TxStarter
}

func NewGitHubInstallationStore(db DBTX) *GitHubInstallationStore {
	s := &GitHubInstallationStore{db: db}
	if pool, ok := db.(TxStarter); ok {
		s.pool = pool
	}
	return s
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
		RETURNING account_id, account_login, account_type, repository_selection, status, roster_synced_at, created_at, updated_at`
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
		&installation.RosterSyncedAt,
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
		RETURNING id, account_login, auto_join_enabled, created_at, updated_at`
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
	}).Scan(&link.ID, &link.AccountLogin, &link.AutoJoinEnabled, &link.CreatedAt, &link.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert github installation org link: %w", err)
	}
	link.Status = status
	return nil
}

func (s *GitHubInstallationStore) GetOrgLink(ctx context.Context, orgID uuid.UUID, installationID int64) (models.GitHubInstallationOrgLink, error) {
	query := `
		SELECT l.id, l.org_id, l.integration_id, l.installation_id, l.account_login, l.linked_by_user_id, l.status, l.auto_join_enabled, l.created_at, l.updated_at
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
		SELECT l.id, l.org_id, l.integration_id, l.installation_id, l.account_login, l.linked_by_user_id, l.status, l.auto_join_enabled, l.created_at, l.updated_at
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
		SELECT installation_id, account_id, account_login, account_type, repository_selection, status, roster_synced_at, created_at, updated_at
		FROM github_installations
		WHERE installation_id = @installation_id`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"installation_id": installationID})
	if err != nil {
		return models.GitHubInstallation{}, fmt.Errorf("query github installation: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.GitHubInstallation])
}

func (s *GitHubInstallationStore) ListOrgAutoJoinSummaries(ctx context.Context, orgID uuid.UUID) ([]models.GitHubOrgAutoJoinSummary, error) {
	query := `
		SELECT l.installation_id, l.account_login, gi.account_type, l.auto_join_enabled, gi.roster_synced_at,
		       EXISTS (
		           SELECT 1
		           FROM github_installation_org_links other
		           WHERE other.installation_id = l.installation_id
		             AND other.status = 'active'
		             AND other.auto_join_enabled
		             AND other.org_id <> @org_id
		       ) AS captured_by_other_org
		FROM github_installation_org_links l
		JOIN github_installations gi ON gi.installation_id = l.installation_id
		WHERE l.org_id = @org_id AND l.status = 'active' AND gi.status = 'active'
		ORDER BY l.created_at DESC, l.installation_id DESC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query github org auto-join summaries: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.GitHubOrgAutoJoinSummary])
}

// ListEnabledAutoJoinLinksDueForRosterSync returns enabled captures ordered by oldest roster sync.
// lint:allow-no-orgid reason="scheduler reconciliation spans enabled GitHub captures across organizations"
func (s *GitHubInstallationStore) ListEnabledAutoJoinLinksDueForRosterSync(ctx context.Context, syncedBefore time.Time, limit int) ([]models.GitHubOrgAutoJoinCandidate, error) {
	query := `
		SELECT l.org_id, o.name AS org_name, l.installation_id, l.account_login, gi.account_type, l.updated_at AS enabled_at
		FROM github_installation_org_links l
		JOIN github_installations gi ON gi.installation_id = l.installation_id
		JOIN organizations o ON o.id = l.org_id
		WHERE l.status = 'active'
		  AND l.auto_join_enabled
		  AND gi.status = 'active'
		  AND (gi.roster_synced_at IS NULL OR gi.roster_synced_at < @synced_before)
		ORDER BY gi.roster_synced_at ASC NULLS FIRST, l.updated_at ASC, l.org_id ASC
		LIMIT @limit`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"synced_before": syncedBefore, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("query github org roster sync due links: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.GitHubOrgAutoJoinCandidate])
}

// ListEnabledAutoJoinLinksByInstallation returns active captures for a GitHub installation.
// lint:allow-no-orgid reason="GitHub installation webhook/job has installation scope, not a 143 org context"
func (s *GitHubInstallationStore) ListEnabledAutoJoinLinksByInstallation(ctx context.Context, installationID int64) ([]models.GitHubOrgAutoJoinCandidate, error) {
	query := `
		SELECT l.org_id, o.name AS org_name, l.installation_id, l.account_login, gi.account_type, l.updated_at AS enabled_at
		FROM github_installation_org_links l
		JOIN github_installations gi ON gi.installation_id = l.installation_id
		JOIN organizations o ON o.id = l.org_id
		WHERE l.installation_id = @installation_id
		  AND l.status = 'active'
		  AND l.auto_join_enabled
		  AND gi.status = 'active'
		ORDER BY l.updated_at ASC, l.org_id ASC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"installation_id": installationID})
	if err != nil {
		return nil, fmt.Errorf("query github org enabled links by installation: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.GitHubOrgAutoJoinCandidate])
}

func (s *GitHubInstallationStore) SetOrgLinkAutoJoin(ctx context.Context, orgID uuid.UUID, installationID int64, enabled bool) (models.GitHubInstallationOrgLink, error) {
	query := `
		UPDATE github_installation_org_links
		SET auto_join_enabled = @enabled, updated_at = now()
		WHERE org_id = @org_id AND installation_id = @installation_id AND status = 'active'
		RETURNING id, org_id, integration_id, installation_id, account_login, linked_by_user_id, status, auto_join_enabled, created_at, updated_at`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"installation_id": installationID,
		"enabled":         enabled,
	})
	if err != nil {
		return models.GitHubInstallationOrgLink{}, mapGitHubOrgCaptureError(err)
	}
	link, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.GitHubInstallationOrgLink])
	if err != nil {
		return models.GitHubInstallationOrgLink{}, mapGitHubOrgCaptureError(err)
	}
	return link, nil
}

func (s *GitHubInstallationStore) DisableOrgLinkAutoJoin(ctx context.Context, orgID uuid.UUID, installationID int64) (models.GitHubInstallationOrgLink, error) {
	return s.SetOrgLinkAutoJoin(ctx, orgID, installationID, false)
}

func mapGitHubOrgCaptureError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "idx_github_install_links_auto_join" {
		return ErrGitHubOrgAlreadyCaptured
	}
	return err
}

// ClearRosterForInstallation removes a GitHub member roster when capture is disabled or the installation is removed.
// lint:allow-no-orgid reason="global GitHub org roster keyed by installation, shared like github_installations"
func (s *GitHubInstallationStore) ClearRosterForInstallation(ctx context.Context, installationID int64) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM github_org_members
		WHERE installation_id = @installation_id`,
		pgx.NamedArgs{"installation_id": installationID},
	)
	if err != nil {
		return fmt.Errorf("clear github org roster: %w", err)
	}
	return nil
}

// UpsertOrgMember records a webhook-observed member only while a capture is enabled.
// lint:allow-no-orgid reason="GitHub organization webhook has installation scope, not a 143 org context"
func (s *GitHubInstallationStore) UpsertOrgMember(ctx context.Context, installationID, githubUserID int64, githubLogin string) error {
	if githubUserID == 0 || githubLogin == "" {
		return nil
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO github_org_members (installation_id, github_user_id, github_login, synced_at)
		SELECT @installation_id, @github_user_id, @github_login, now()
		WHERE EXISTS (
			SELECT 1 FROM github_installation_org_links
			WHERE installation_id = @installation_id
			  AND status = 'active'
			  AND auto_join_enabled
		)
		ON CONFLICT (installation_id, github_user_id) DO UPDATE
		SET github_login = EXCLUDED.github_login, synced_at = now()`,
		pgx.NamedArgs{
			"installation_id": installationID,
			"github_user_id":  githubUserID,
			"github_login":    githubLogin,
		},
	)
	if err != nil {
		return fmt.Errorf("upsert github org member: %w", err)
	}
	return nil
}

// DeleteOrgMember removes a webhook-observed member from the discovery roster.
// lint:allow-no-orgid reason="GitHub organization webhook has installation scope, not a 143 org context"
func (s *GitHubInstallationStore) DeleteOrgMember(ctx context.Context, installationID, githubUserID int64) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM github_org_members
		WHERE installation_id = @installation_id AND github_user_id = @github_user_id`,
		pgx.NamedArgs{"installation_id": installationID, "github_user_id": githubUserID},
	)
	if err != nil {
		return fmt.Errorf("delete github org member: %w", err)
	}
	return nil
}

// ReplaceRosterForInstallation atomically replaces the discovery roster for one GitHub App installation.
// Uses COPY for bulk inserts so large orgs don't generate O(N) round trips inside the transaction.
// lint:allow-no-orgid reason="global GitHub org roster keyed by installation, shared like github_installations"
func (s *GitHubInstallationStore) ReplaceRosterForInstallation(ctx context.Context, installationID int64, members []models.GitHubOrgMember) error {
	if s.pool == nil {
		return fmt.Errorf("github installation store requires TxStarter for roster replacement")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin github org roster replacement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM github_org_members WHERE installation_id = @installation_id`, pgx.NamedArgs{"installation_id": installationID}); err != nil {
		return fmt.Errorf("delete github org roster: %w", err)
	}
	if len(members) > 0 {
		_, err = tx.CopyFrom(
			ctx,
			pgx.Identifier{"github_org_members"},
			[]string{"installation_id", "github_user_id", "github_login"},
			pgx.CopyFromSlice(len(members), func(i int) ([]any, error) {
				return []any{installationID, members[i].GitHubUserID, members[i].GitHubLogin}, nil
			}),
		)
		if err != nil {
			return fmt.Errorf("copy github org roster members: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE github_installations
		SET roster_synced_at = now(), updated_at = now()
		WHERE installation_id = @installation_id`,
		pgx.NamedArgs{"installation_id": installationID}); err != nil {
		return fmt.Errorf("stamp github org roster sync: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit github org roster replacement: %w", err)
	}
	return nil
}

// FindAutoJoinCandidatesByGitHubUserID discovers enabled captures whose synced roster contains the GitHub user.
// lint:allow-no-orgid reason="pre-auth GitHub OAuth lookup by globally unique GitHub user id"
func (s *GitHubInstallationStore) FindAutoJoinCandidatesByGitHubUserID(ctx context.Context, githubUserID int64) ([]models.GitHubOrgAutoJoinCandidate, error) {
	query := `
		SELECT l.org_id, o.name AS org_name, l.installation_id, l.account_login, gi.account_type, l.updated_at AS enabled_at
		FROM github_org_members gom
		JOIN github_installation_org_links l ON l.installation_id = gom.installation_id
		JOIN github_installations gi ON gi.installation_id = l.installation_id
		JOIN organizations o ON o.id = l.org_id
		WHERE gom.github_user_id = @github_user_id
		  AND l.status = 'active'
		  AND l.auto_join_enabled
		  AND gi.status = 'active'
		ORDER BY l.updated_at ASC, l.org_id ASC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"github_user_id": githubUserID})
	if err != nil {
		return nil, fmt.Errorf("query github org auto-join candidates: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.GitHubOrgAutoJoinCandidate])
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
		SET status = 'deleted', auto_join_enabled = false, updated_at = now()
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
		SET status = 'inactive', auto_join_enabled = false, updated_at = now()
		WHERE org_id = @org_id AND integration_id = @integration_id AND status = 'active'`,
		pgx.NamedArgs{"org_id": orgID, "integration_id": integrationID},
	)
	if err != nil {
		return fmt.Errorf("deactivate github installation org links by integration: %w", err)
	}
	return nil
}
