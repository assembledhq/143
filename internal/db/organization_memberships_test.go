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

// Query-error paths return a wrapped error rather than silently succeeding,
// so the surrounding handler can map the failure to a 500 instead of an empty
// result set.
func TestOrganizationMembershipStore_QueryErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		run    func(context.Context, *OrganizationMembershipStore) error
		expect func(mock pgxmock.PgxPoolIface)
	}{
		{
			name: "ListByUser",
			run: func(ctx context.Context, s *OrganizationMembershipStore) error {
				_, err := s.ListByUser(ctx, uuid.New())
				return err
			},
			expect: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT m.org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(errors.New("boom"))
			},
		},
		{
			name: "Get",
			run: func(ctx context.Context, s *OrganizationMembershipStore) error {
				_, err := s.Get(ctx, uuid.New(), uuid.New())
				return err
			},
			expect: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM organization_memberships").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("boom"))
			},
		},
		{
			name: "OldestForUser",
			run: func(ctx context.Context, s *OrganizationMembershipStore) error {
				_, err := s.OldestForUser(ctx, uuid.New())
				return err
			},
			expect: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ ORDER BY created_at").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(errors.New("boom"))
			},
		},
		{
			name: "ListUserIDsByOrg",
			run: func(ctx context.Context, s *OrganizationMembershipStore) error {
				_, err := s.ListUserIDsByOrg(ctx, uuid.New())
				return err
			},
			expect: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT user_id.+FROM organization_memberships").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(errors.New("boom"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()
			tc.expect(mock)
			err = tc.run(context.Background(), NewOrganizationMembershipStore(mock))
			require.Error(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// UpdateRole returns pgx.ErrNoRows when the membership row doesn't exist so
// handlers can surface a 404 rather than silently succeeding.
func TestOrganizationMembershipStore_UpdateRole_NoRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("UPDATE organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = NewOrganizationMembershipStore(mock).UpdateRole(context.Background(), uuid.New(), uuid.New(), "member")
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
	require.NoError(t, mock.ExpectationsWereMet())
}

// UpdateRole and Upsert reject invalid role strings up front so bogus values
// can't reach the DB with ambiguous error messages.
func TestOrganizationMembershipStore_RejectInvalidRole(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	s := NewOrganizationMembershipStore(mock)

	err = s.UpdateRole(context.Background(), uuid.New(), uuid.New(), "superadmin")
	require.Error(t, err)

	err = s.Upsert(context.Background(), uuid.New(), uuid.New(), "guest")
	require.Error(t, err)
}

// UpdateRole returns the raw Exec error when the DB fails (e.g. connection
// lost mid-update), distinct from the NoRows race.
func TestOrganizationMembershipStore_UpdateRole_ExecError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("UPDATE organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	err = NewOrganizationMembershipStore(mock).UpdateRole(context.Background(), uuid.New(), uuid.New(), "member")
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Remove surfaces non-ErrNoRows query errors so the handler can 500 rather
// than silently returning success.
func TestOrganizationMembershipStore_Remove_ScanError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("(?s)WITH deleted_membership.+cleared_answers").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	err = NewOrganizationMembershipStore(mock).Remove(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ListUserIDsByOrg wraps row-scan errors so the caller sees a specific
// "scan member id" failure rather than a generic rows error.
func TestOrganizationMembershipStore_ListUserIDsByOrg_ScanError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("(?s)SELECT user_id.+FROM organization_memberships").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"user_id"}).AddRow("not-a-uuid"),
		)

	_, err = NewOrganizationMembershipStore(mock).ListUserIDsByOrg(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "scan member id")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Remove with pgx.ErrNoRows is surfaced so the handler can return a 404
// rather than pretending the delete happened.
func TestOrganizationMembershipStore_Remove_NoRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("(?s)WITH deleted_membership.+cleared_answers").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	err = NewOrganizationMembershipStore(mock).Remove(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows), "expected pgx.ErrNoRows, got %v", err)
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
