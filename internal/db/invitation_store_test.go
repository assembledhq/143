package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var invitationColumns = []string{
	"id", "org_id", "email", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at",
}

func TestInvitationStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	orgID := uuid.New()
	invitedBy := uuid.New()

	inv := &models.Invitation{
		OrgID:     orgID,
		Email:     "new@example.com",
		Role:      "member",
		InvitedBy: invitedBy,
		Token:     "tok_abc123",
		ExpiresAt: now.Add(72 * time.Hour),
	}

	mock.ExpectQuery("INSERT INTO invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "status", "created_at"}).
				AddRow(generatedID, "pending", now),
		)

	err = store.Create(context.Background(), inv)
	require.NoError(t, err)
	require.Equal(t, generatedID, inv.ID)
	require.Equal(t, "pending", inv.Status)
	require.Equal(t, now, inv.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_Create_Error(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	inv := &models.Invitation{
		OrgID:     uuid.New(),
		Email:     "new@example.com",
		Role:      "member",
		InvitedBy: uuid.New(),
		Token:     "tok_abc",
		ExpiresAt: time.Now().Add(72 * time.Hour),
	}

	mock.ExpectQuery("INSERT INTO invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("unique violation"))

	err = store.Create(context.Background(), inv)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_GetByToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)
	now := time.Now()
	id := uuid.New()
	orgID := uuid.New()
	invitedBy := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationColumns).
				AddRow(id, orgID, "user@example.com", "member", invitedBy, "tok_xyz", "pending", now.Add(72*time.Hour), now, nil),
		)

	inv, err := store.GetByToken(context.Background(), "tok_xyz")
	require.NoError(t, err)
	require.Equal(t, id, inv.ID)
	require.Equal(t, "user@example.com", inv.Email)
	require.Equal(t, "pending", inv.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_GetByToken_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(invitationColumns))

	_, err = store.GetByToken(context.Background(), "nonexistent")
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_ListPendingByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)
	now := time.Now()
	orgID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationColumns).
				AddRow(uuid.New(), orgID, "a@example.com", "member", uuid.New(), "tok_a", "pending", now.Add(72*time.Hour), now, nil).
				AddRow(uuid.New(), orgID, "b@example.com", "admin", uuid.New(), "tok_b", "pending", now.Add(72*time.Hour), now, nil),
		)

	invs, err := store.ListPendingByOrg(context.Background(), orgID)
	require.NoError(t, err)
	require.Len(t, invs, 2)
	require.Equal(t, "a@example.com", invs[0].Email)
	require.Equal(t, "b@example.com", invs[1].Email)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_ListPendingByOrg_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("connection lost"))

	_, err = store.ListPendingByOrg(context.Background(), uuid.New())
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_Accept(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)
	id := uuid.New()

	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.Accept(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_Accept_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = store.Accept(context.Background(), uuid.New())
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_Revoke(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE invitations SET status = 'revoked'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.Revoke(context.Background(), orgID, id)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_Revoke_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	mock.ExpectExec("UPDATE invitations SET status = 'revoked'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = store.Revoke(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}
