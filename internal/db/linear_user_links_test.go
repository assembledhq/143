package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var linearUserLinkColumns = []string{
	"id", "org_id", "integration_id", "user_id", "linear_workspace_id", "linear_user_id",
	"linear_email", "linear_display_name", "source", "linked_at", "created_at", "updated_at",
}

func TestLinearUserLinkStore_GetByLinearUserScopesByOrgAndWorkspace(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	userID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	email := "creator@example.com"
	store := NewLinearUserLinkStore(mock)

	mock.ExpectQuery(`WHERE org_id = @org_id\s+AND linear_workspace_id = @linear_workspace_id\s+AND linear_user_id = @linear_user_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(linearUserLinkColumns).AddRow(
			linkID, orgID, integrationID, &userID, "lin_workspace_1", "lin_user_1", &email, "Creator User",
			models.LinearUserLinkSourceEmailMatch, &now, now, now,
		))

	link, err := store.GetByLinearUser(context.Background(), orgID, "lin_workspace_1", "lin_user_1")

	require.NoError(t, err, "GetByLinearUser should return a Linear user mapping")
	require.Equal(t, userID, *link.UserID, "GetByLinearUser should return the mapped 143 user")
	require.Equal(t, "lin_user_1", link.LinearUserID, "GetByLinearUser should return the Linear user id")
	require.NoError(t, mock.ExpectationsWereMet(), "GetByLinearUser should satisfy expected SQL")
}

func TestLinearUserLinkStore_UpsertEmailMatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	userID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	email := "creator@example.com"
	store := NewLinearUserLinkStore(mock)

	mock.ExpectQuery(`ON CONFLICT \(org_id, linear_workspace_id, linear_user_id\)`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(linearUserLinkColumns).AddRow(
			linkID, orgID, integrationID, &userID, "lin_workspace_1", "lin_user_1", &email, "Creator User",
			models.LinearUserLinkSourceEmailMatch, &now, now, now,
		))

	link := &models.LinearUserLink{
		OrgID:             orgID,
		IntegrationID:     integrationID,
		UserID:            &userID,
		LinearWorkspaceID: "lin_workspace_1",
		LinearUserID:      "lin_user_1",
		LinearEmail:       &email,
		LinearDisplayName: "Creator User",
	}

	err = store.UpsertEmailMatch(context.Background(), link)

	require.NoError(t, err, "UpsertEmailMatch should persist an email-derived mapping")
	require.Equal(t, models.LinearUserLinkSourceEmailMatch, link.Source, "UpsertEmailMatch should mark mapping as email matched")
	require.Equal(t, linkID, link.ID, "UpsertEmailMatch should scan the stored link")
	require.NoError(t, mock.ExpectationsWereMet(), "UpsertEmailMatch should satisfy expected SQL")
}
