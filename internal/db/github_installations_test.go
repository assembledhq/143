package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestGitHubInstallationStore_UpsertInstallation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	now := time.Now()
	store := NewGitHubInstallationStore(mock)
	installation := &models.GitHubInstallation{
		InstallationID: 12345,
		AccountID:      99,
		AccountLogin:   "assembledhq",
		Status:         "active",
	}

	mock.ExpectQuery("INSERT INTO github_installations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"account_id", "account_login", "account_type", "repository_selection", "status", "created_at", "updated_at",
		}).AddRow(int64(99), "assembledhq", nil, nil, "active", now, now))

	err = store.UpsertInstallation(context.Background(), installation)
	require.NoError(t, err, "UpsertInstallation should persist installation metadata")
	require.Equal(t, int64(99), installation.AccountID, "UpsertInstallation should scan the stored account id")
	require.Equal(t, "assembledhq", installation.AccountLogin, "UpsertInstallation should scan the stored account login")
	require.Equal(t, now, installation.CreatedAt, "UpsertInstallation should scan created_at")
	require.Equal(t, now, installation.UpdatedAt, "UpsertInstallation should scan updated_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGitHubInstallationStore_UpsertInstallation_PreservesExistingMetadataForPlaceholders(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("github_installations.go")
	require.NoError(t, err, "test should read github installation store source")
	query := string(src)

	require.Contains(t, query, "EXCLUDED.account_id <> 0", "UpsertInstallation should not replace a real account_id with the callback placeholder")
	require.Contains(t, query, "EXCLUDED.account_login <> 'unknown'", "UpsertInstallation should not replace a real account_login with the callback placeholder")
	require.True(t,
		strings.Contains(query, "github_installations.account_login") && strings.Contains(query, "github_installations.account_id"),
		"UpsertInstallation should preserve existing account metadata when callback metadata is incomplete",
	)
}

func TestGitHubInstallationStore_UpsertOrgLink(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	now := time.Now()
	linkID := uuid.New()
	integrationID := uuid.New()
	userID := uuid.New()
	store := NewGitHubInstallationStore(mock)
	link := &models.GitHubInstallationOrgLink{
		OrgID:          uuid.New(),
		IntegrationID:  &integrationID,
		InstallationID: 12345,
		AccountLogin:   "assembledhq",
		LinkedByUserID: &userID,
		Status:         "active",
	}

	mock.ExpectQuery("INSERT INTO github_installation_org_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "account_login", "created_at", "updated_at"}).AddRow(linkID, "assembledhq", now, now))

	err = store.UpsertOrgLink(context.Background(), link)
	require.NoError(t, err, "UpsertOrgLink should persist the org-to-installation link")
	require.Equal(t, linkID, link.ID, "UpsertOrgLink should scan the link id")
	require.Equal(t, "assembledhq", link.AccountLogin, "UpsertOrgLink should scan the stored account login")
	require.Equal(t, now, link.CreatedAt, "UpsertOrgLink should scan created_at")
	require.Equal(t, now, link.UpdatedAt, "UpsertOrgLink should scan updated_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGitHubInstallationStore_GetOrgLinkRequiresActiveInstallation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	now := time.Now()
	linkID := uuid.New()
	orgID := uuid.New()
	integrationID := uuid.New()
	userID := uuid.New()
	store := NewGitHubInstallationStore(mock)

	mock.ExpectQuery("JOIN github_installations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "installation_id", "account_login", "linked_by_user_id", "status", "created_at", "updated_at",
		}).AddRow(linkID, orgID, &integrationID, int64(12345), "assembledhq", &userID, "active", now, now))

	link, err := store.GetOrgLink(context.Background(), orgID, 12345)

	require.NoError(t, err, "GetOrgLink should return an active link for an active installation")
	require.Equal(t, linkID, link.ID, "GetOrgLink should scan the link id")
	require.Equal(t, orgID, link.OrgID, "GetOrgLink should scan the org id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGitHubInstallationStore_DeactivateOrgLinksByInstallationID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	store := NewGitHubInstallationStore(mock)
	mock.ExpectExec("UPDATE github_installation_org_links").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	err = store.DeactivateOrgLinksByInstallationID(context.Background(), 12345)

	require.NoError(t, err, "DeactivateOrgLinksByInstallationID should deactivate active links")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGitHubInstallationStore_RefreshOrgLinkAccountLogin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	store := NewGitHubInstallationStore(mock)
	mock.ExpectExec("UPDATE github_installation_org_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	err = store.RefreshOrgLinkAccountLogin(context.Background(), 12345, "assembledhq")

	require.NoError(t, err, "RefreshOrgLinkAccountLogin should update placeholder link labels")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
