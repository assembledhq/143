package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestExternalUserLinkStore_GetActiveByExternal(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	linkID := uuid.New()
	now := time.Now().UTC()
	email := "alice@example.com"
	handle := "alice"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	mock.ExpectQuery(`FROM external_user_links\s+WHERE org_id = @org_id\s+AND provider = @provider\s+AND provider_workspace_id = @provider_workspace_id\s+AND provider_user_id = @provider_user_id\s+AND status = 'active'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(externalUserLinkRows().
			AddRow(linkID, orgID, models.ExternalIdentityProviderSlack, "T123", "U123", userID,
				models.ExternalUserLinkSourceSelfLinked, models.ExternalUserLinkStatusActive, 100,
				&email, &handle, nil, &userID, now, nil))

	store := NewExternalUserLinkStore(mock)
	link, err := store.GetActiveByExternal(context.Background(), orgID, models.ExternalIdentityProviderSlack, "T123", "U123")
	require.NoError(t, err, "GetActiveByExternal should return a matching active link")
	require.Equal(t, linkID, link.ID, "GetActiveByExternal should scan the expected link")
	require.Equal(t, orgID, link.OrgID, "GetActiveByExternal should keep the query scoped to org_id")
	require.Equal(t, userID, link.UserID, "GetActiveByExternal should return the mapped user")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestExternalUserLinkStore_CreateClaim(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	claimID := uuid.New()
	now := time.Now().UTC()
	expiresAt := now.Add(30 * time.Minute)
	tokenHash := []byte("hashed-token")
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()
	mock.ExpectQuery(`INSERT INTO external_user_link_claims \([\s\S]*org_id, provider, provider_workspace_id, provider_user_id[\s\S]*RETURNING`).
		WithArgs(orgID, models.ExternalIdentityProviderSlack, "T123", "U123", tokenHash, []byte(`{"surface":"slack"}`), expiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "provider_workspace_id", "provider_user_id", "source_context", "expires_at", "claimed_by_user_id", "claimed_at", "created_at"}).
			AddRow(claimID, orgID, models.ExternalIdentityProviderSlack, "T123", "U123", []byte(`{"surface":"slack"}`), expiresAt, nil, nil, now))
	store := NewExternalUserLinkStore(mock)
	claim, err := store.CreateClaim(context.Background(), models.ExternalUserLinkClaim{OrgID: orgID, Provider: models.ExternalIdentityProviderSlack, ProviderWorkspaceID: "T123", ProviderUserID: "U123", SourceContext: []byte(`{"surface":"slack"}`), ExpiresAt: expiresAt}, tokenHash)
	require.NoError(t, err, "CreateClaim should persist an org-scoped hashed claim")
	require.Equal(t, claimID, claim.ID, "CreateClaim should return the persisted claim")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestExternalUserLinkStore_UpsertEmailMatchDoesNotOverwriteTrustedLinks(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	existingUserID := uuid.New()
	emailUserID := uuid.New()
	linkID := uuid.New()
	now := time.Now().UTC()
	email := "alice@example.com"
	displayName := "Alice"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	mock.ExpectQuery(`ON CONFLICT \(org_id, provider, provider_workspace_id, provider_user_id\)\s+WHERE status = 'active'\s+DO UPDATE SET[\s\S]*WHEN external_user_links.source IN \('self_linked', 'admin_linked'\) THEN external_user_links.user_id[\s\S]*RETURNING`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(externalUserLinkRows().
			AddRow(linkID, orgID, models.ExternalIdentityProviderLinear, "lin_ws", "lin_user", existingUserID,
				models.ExternalUserLinkSourceAdminLinked, models.ExternalUserLinkStatusActive, 90,
				&email, nil, &displayName, nil, now, nil))

	store := NewExternalUserLinkStore(mock)
	link, err := store.UpsertActive(context.Background(), models.ExternalUserLink{
		OrgID:               orgID,
		Provider:            models.ExternalIdentityProviderLinear,
		ProviderWorkspaceID: "lin_ws",
		ProviderUserID:      "lin_user",
		UserID:              emailUserID,
		Source:              models.ExternalUserLinkSourceEmailMatch,
		Confidence:          80,
		ExternalEmail:       &email,
		ExternalDisplayName: &displayName,
	})
	require.NoError(t, err, "UpsertActive should return the persisted row")
	require.Equal(t, existingUserID, link.UserID, "email matches should not overwrite admin-linked or self-linked mappings")
	require.Equal(t, models.ExternalUserLinkSourceAdminLinked, link.Source, "trusted source should be preserved on email-match conflict")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestExternalUserLinkStore_UpsertAdminActiveOverwritesTrustedLinks(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	adminUserID := uuid.New()
	linkID := uuid.New()
	now := time.Now().UTC()
	email := "alice@example.com"
	displayName := "Alice"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	mock.ExpectQuery(`ON CONFLICT \(org_id, provider, provider_workspace_id, provider_user_id\)\s+WHERE status = 'active'\s+DO UPDATE SET[\s\S]*user_id = EXCLUDED.user_id[\s\S]*source = 'admin_linked'[\s\S]*RETURNING`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(externalUserLinkRows().
			AddRow(linkID, orgID, models.ExternalIdentityProviderLinear, "lin_ws", "lin_user", adminUserID,
				models.ExternalUserLinkSourceAdminLinked, models.ExternalUserLinkStatusActive, 90,
				&email, nil, &displayName, &adminUserID, now, nil))

	store := NewExternalUserLinkStore(mock)
	link, err := store.UpsertAdminActive(context.Background(), models.ExternalUserLink{
		OrgID:               orgID,
		Provider:            models.ExternalIdentityProviderLinear,
		ProviderWorkspaceID: "lin_ws",
		ProviderUserID:      "lin_user",
		UserID:              adminUserID,
		Confidence:          90,
		ExternalEmail:       &email,
		ExternalDisplayName: &displayName,
		LinkedByUserID:      &adminUserID,
	})
	require.NoError(t, err, "UpsertAdminActive should return the persisted row")
	require.Equal(t, adminUserID, link.UserID, "admin upsert should overwrite the mapped user")
	require.Equal(t, models.ExternalUserLinkSourceAdminLinked, link.Source, "admin upsert should mark the source as admin linked")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestExternalUserLinkStore_UpsertSelfActiveOverwritesTrustedLinks(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	linkID := uuid.New()
	now := time.Now().UTC()
	email := "alice@example.com"
	displayName := "Alice"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	mock.ExpectQuery(`ON CONFLICT \(org_id, provider, provider_workspace_id, provider_user_id\)\s+WHERE status = 'active'\s+DO UPDATE SET[\s\S]*user_id = EXCLUDED.user_id[\s\S]*source = 'self_linked'[\s\S]*RETURNING`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(externalUserLinkRows().
			AddRow(linkID, orgID, models.ExternalIdentityProviderSlack, "T123", "U123", userID,
				models.ExternalUserLinkSourceSelfLinked, models.ExternalUserLinkStatusActive, 100,
				&email, nil, &displayName, &userID, now, nil))

	store := NewExternalUserLinkStore(mock)
	link, err := store.UpsertSelfActive(context.Background(), models.ExternalUserLink{
		OrgID:               orgID,
		Provider:            models.ExternalIdentityProviderSlack,
		ProviderWorkspaceID: "T123",
		ProviderUserID:      "U123",
		UserID:              userID,
		Confidence:          100,
		ExternalEmail:       &email,
		ExternalDisplayName: &displayName,
		LinkedByUserID:      &userID,
	})
	require.NoError(t, err, "UpsertSelfActive should return the persisted row")
	require.Equal(t, userID, link.UserID, "self upsert should overwrite the mapped user")
	require.Equal(t, models.ExternalUserLinkSourceSelfLinked, link.Source, "self upsert should mark the source as self linked")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestExternalUserLinkStore_RevokeActiveByExternal(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	mock.ExpectExec(`UPDATE external_user_links\s+SET status = 'revoked',\s+revoked_at = now\(\)\s+WHERE org_id = @org_id\s+AND provider = @provider\s+AND provider_workspace_id = @provider_workspace_id\s+AND provider_user_id = @provider_user_id\s+AND status = 'active'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewExternalUserLinkStore(mock)
	err = store.RevokeActiveByExternal(context.Background(), orgID, models.ExternalIdentityProviderSlack, "T123", "U123")
	require.NoError(t, err, "RevokeActiveByExternal should revoke the active provider mapping")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestExternalUserLinkStore_ClaimSelfLink(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	claimID := uuid.New()
	linkID := uuid.New()
	now := time.Now().UTC()
	tokenHash := []byte("hashed-token")

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM organization_memberships\s+WHERE user_id = @user_id AND org_id = @org_id`).
		WithArgs(userID, orgID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}).
			AddRow(userID, orgID, models.RoleMember, now))
	mock.ExpectQuery(`FROM external_user_link_claims[\s\S]*token_hash = @token_hash[\s\S]*claimed_at IS NULL[\s\S]*expires_at > now\(\)[\s\S]*FOR UPDATE`).
		WithArgs(orgID, tokenHash).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "provider", "provider_workspace_id", "provider_user_id",
			"source_context", "expires_at", "claimed_by_user_id", "claimed_at", "created_at",
		}).AddRow(
			claimID, orgID, models.ExternalIdentityProviderSlack, "T123", "U123",
			[]byte(`{"slack_channel":"C123"}`), now.Add(10*time.Minute), nil, nil, now,
		))
	mock.ExpectQuery(`ON CONFLICT \(org_id, provider, provider_workspace_id, provider_user_id\)\s+WHERE status = 'active'\s+DO UPDATE SET[\s\S]*source = 'self_linked'`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(externalUserLinkRows().
			AddRow(linkID, orgID, models.ExternalIdentityProviderSlack, "T123", "U123", userID,
				models.ExternalUserLinkSourceSelfLinked, models.ExternalUserLinkStatusActive, 100,
				nil, nil, nil, &userID, now, nil))
	mock.ExpectExec(`UPDATE external_user_link_claims\s+SET claimed_by_user_id = @claimed_by_user_id,\s+claimed_at = now\(\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	store := NewExternalUserLinkStore(mock)
	link, err := store.ClaimSelfLink(context.Background(), orgID, tokenHash, userID)
	require.NoError(t, err, "ClaimSelfLink should atomically claim the external identity")
	require.Equal(t, linkID, link.ID, "ClaimSelfLink should return the created self-link")
	require.Equal(t, models.ExternalUserLinkSourceSelfLinked, link.Source, "ClaimSelfLink should create a self-linked mapping")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func externalUserLinkRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "org_id", "provider", "provider_workspace_id", "provider_user_id", "user_id",
		"source", "status", "confidence", "external_email", "external_handle",
		"external_display_name", "linked_by_user_id", "created_at", "revoked_at",
	})
}
