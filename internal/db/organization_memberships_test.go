package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestOrganizationMembershipStore_ListByUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)
	userID := uuid.New()
	orgA := uuid.New()
	orgB := uuid.New()

	mock.ExpectQuery("SELECT m.org_id, o.name AS org_name, m.role").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"org_id", "org_name", "role"}).
				AddRow(orgA, "Org A", "admin").
				AddRow(orgB, "Org B", "member"),
		)

	memberships, err := store.ListByUser(context.Background(), userID)
	require.NoError(t, err)
	require.Len(t, memberships, 2)
	require.Equal(t, orgA, memberships[0].OrgID)
	require.Equal(t, "Org A", memberships[0].OrgName)
	require.Equal(t, "admin", memberships[0].Role)
	require.Equal(t, orgB, memberships[1].OrgID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_ListByUser_Empty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery("SELECT m.org_id, o.name AS org_name, m.role").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "role"}))

	memberships, err := store.ListByUser(context.Background(), uuid.New())
	require.NoError(t, err)
	require.Empty(t, memberships)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_Get(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)
	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}).
				AddRow(userID, orgID, "admin", now),
		)

	m, err := store.Get(context.Background(), userID, orgID)
	require.NoError(t, err)
	require.Equal(t, userID, m.UserID)
	require.Equal(t, orgID, m.OrgID)
	require.Equal(t, "admin", m.Role)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_Get_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}))

	_, err = store.Get(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_Upsert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		role string
	}{
		{"admin role", "admin"},
		{"member role", "member"},
		{"viewer role", "viewer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewOrganizationMembershipStore(mock)

			mock.ExpectExec("INSERT INTO organization_memberships").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))

			err = store.Upsert(context.Background(), uuid.New(), uuid.New(), tt.role)
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOrganizationMembershipStore_Upsert_InvalidRole(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	err = store.Upsert(context.Background(), uuid.New(), uuid.New(), "owner")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid role")
}

func TestOrganizationMembershipStore_UpdateRole(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectExec("UPDATE organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateRole(context.Background(), uuid.New(), uuid.New(), "admin")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_UpdateRole_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectExec("UPDATE organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = store.UpdateRole(context.Background(), uuid.New(), uuid.New(), "admin")
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_UpdateRole_InvalidRole(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	err = store.UpdateRole(context.Background(), uuid.New(), uuid.New(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid role")
}

func TestOrganizationMembershipStore_Remove(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery("(?s)DELETE FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	err = store.Remove(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_Remove_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery("(?s)DELETE FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	err = store.Remove(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_CountForUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	count, err := store.CountForUser(context.Background(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_CountAdmins(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountAdmins(context.Background(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_OldestForUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)
	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ ORDER BY created_at").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}).
				AddRow(userID, orgID, "admin", now),
		)

	m, err := store.OldestForUser(context.Background(), userID)
	require.NoError(t, err)
	require.Equal(t, orgID, m.OrgID)
	require.Equal(t, "admin", m.Role)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_OldestForUser_NoMemberships(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ ORDER BY created_at").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}))

	_, err = store.OldestForUser(context.Background(), uuid.New())
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_ListUserIDsByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)
	u1 := uuid.New()
	u2 := uuid.New()

	mock.ExpectQuery("(?s)SELECT user_id.+FROM organization_memberships").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"user_id"}).
				AddRow(u1).
				AddRow(u2),
		)

	ids, err := store.ListUserIDsByOrg(context.Background(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{u1, u2}, ids)
	require.NoError(t, mock.ExpectationsWereMet())
}
