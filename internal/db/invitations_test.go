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

func TestInvitationStore_ListPendingByOrgWithInviter_ProjectsExpiredStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewInvitationStore(mock)
	orgID := uuid.New()
	invID := uuid.New()
	inviterID := uuid.New()
	email := "expired@example.com"
	expiresAt := time.Now().Add(-1 * time.Hour)
	createdAt := time.Now().Add(-8 * 24 * time.Hour)

	mock.ExpectQuery(`(?s)CASE\s+WHEN i\.status = 'pending' AND i\.expires_at <= now\(\) THEN 'expired'.+WHERE i\.org_id = @org_id AND i\.status = 'pending'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at", "inviter_name",
		}).AddRow(
			invID, orgID, &email, nil, "email", "member", inviterID, "token", "expired", expiresAt, createdAt, nil, "Admin User",
		))

	invitations, err := store.ListPendingByOrgWithInviter(context.Background(), orgID)
	require.NoError(t, err, "ListPendingByOrgWithInviter should load pending invitation rows")
	require.Equal(t, models.InvitationStatusExpired, invitations[0].Status, "expired pending invitations should be projected with expired status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
