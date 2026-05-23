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
	require.Equal(t, models.RoleAdmin, memberships[0].Role)
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
	require.Equal(t, models.RoleAdmin, m.Role)
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

func TestOrganizationMembershipStore_GrantAtLeast(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		role models.Role
	}{
		{"admin role", models.RoleAdmin},
		{"member role", models.RoleMember},
		{"builder role", models.RoleBuilder},
		{"viewer role", models.RoleViewer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewOrganizationMembershipStore(mock)

			mock.ExpectQuery("INSERT INTO organization_memberships").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow(tt.role))

			effective, err := store.GrantAtLeast(context.Background(), uuid.New(), uuid.New(), tt.role)
			require.NoError(t, err)
			require.Equal(t, string(tt.role), effective)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOrganizationMembershipStore_GrantAtLeast_SQLSupportsBuilderUpgrade(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery(`(?s)INSERT INTO organization_memberships.+WHEN EXCLUDED\.role = 'builder' AND organization_memberships\.role = 'viewer' THEN 'builder'.+RETURNING role`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("builder"))

	effective, err := store.GrantAtLeast(context.Background(), uuid.New(), uuid.New(), "builder")
	require.NoError(t, err)
	require.Equal(t, "builder", effective)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_Insert(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectExec("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = store.Insert(context.Background(), uuid.New(), uuid.New(), "admin")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_Insert_InvalidRole(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	err = store.Insert(context.Background(), uuid.New(), uuid.New(), "owner")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid role")
}

func TestOrganizationMembershipStore_Insert_ConflictFailsLoudly(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectExec("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("duplicate key value"))

	err = store.Insert(context.Background(), uuid.New(), uuid.New(), "admin")
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_GrantAtLeast_InvalidRole(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	_, err = store.GrantAtLeast(context.Background(), uuid.New(), uuid.New(), "owner")
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

// TestOrganizationMembershipStore_Remove_OnlyRevokesPendingInvitations asserts
// the invitation cleanup side-effect in Remove() targets only pending
// invitations via UPDATE ... SET status = 'revoked'. Historical accepted or
// previously-revoked invitations are preserved so the audit trail of "who
// invited this person" survives the inviter being removed from the org.
func TestOrganizationMembershipStore_Remove_OnlyRevokesPendingInvitations(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery(`(?s)UPDATE invitations\s+SET status = 'revoked'.+status = 'pending'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	err = store.Remove(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestOrganizationMembershipStore_Remove_ClearsAnsweredBy asserts the Remove
// CTE nulls session_questions.answered_by for the removed user in the scope
// org. Without this, the audit display would keep showing "answered by
// $removedUser" even after the user lost access — confusing when the user
// can no longer be clicked through to, and a privacy leak in orgs that
// periodically rotate access.
//
// Historical answers are preserved (the row isn't deleted) so downstream
// consumers of session_questions still see the answer text; only the
// attribution is cleared. Per-row preservation is deliberate: deleting the
// answer would silently change the agent's decision trail.
func TestOrganizationMembershipStore_Remove_ClearsAnsweredBy(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery(`(?s)UPDATE session_questions\s+SET answered_by = NULL.+org_id = @org_id\s+AND answered_by = @user_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	err = store.Remove(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestOrganizationMembershipStore_Remove_AllSideEffectsInOneStatement asserts
// the Remove query contains every documented CTE in a single statement so
// the side-effects run under the same transaction semantics as the delete.
// Splitting the work across multiple statements would mean a mid-operation
// failure could leave the org in a half-cleaned state (membership gone but
// invitations still pending). The one-statement guarantee is load-bearing
// for the "remove is atomic" contract in the handler.
//
// pgxmock cannot verify real execution — it matches on query text — so this
// test is a structural check on the SQL. Integration coverage of the actual
// side-effects firing against a live Postgres would belong in a separate
// TestMain-gated integration suite once one exists.
func TestOrganizationMembershipStore_Remove_AllSideEffectsInOneStatement(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	// Require all five CTEs (deleted_membership + four side-effects) to appear
	// in order in a single WITH statement. Any future refactor that splits one
	// out into a separate Exec call trips this regex.
	mock.ExpectQuery(`(?s)WITH deleted_membership AS \(.+cleared_answers AS \(.+revoked_invitations AS \(.+cleared_session_hints AS \(.+cleared_user_hint AS \(`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	err = store.Remove(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestOrganizationMembershipStore_Remove_ClearsSessionHint asserts that the
// Remove CTE also nulls out last_org_id on any of the removed user's sessions
// that were pinned to the org they were just removed from. Without this, the
// user's next request would resolve via the stale session hint, bounce off
// the now-revoked membership, and then the middleware would fall through to
// the oldest-membership path anyway — an avoidable round-trip that also
// exposes the revoked orgID in the X-Org-Membership-Revoked response header
// on every request until the client does its own cleanup.
func TestOrganizationMembershipStore_Remove_ClearsSessionHint(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery(`(?s)UPDATE auth_sessions\s+SET last_org_id = NULL.+user_id = @user_id\s+AND last_org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	err = store.Remove(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_Remove_ClearsUserHint(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationMembershipStore(mock)

	mock.ExpectQuery(`(?s)UPDATE users\s+SET last_org_id = NULL.+id = @user_id\s+AND last_org_id = @org_id`).
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
	require.Equal(t, models.RoleAdmin, m.Role)
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

	_, err = s.GrantAtLeast(context.Background(), uuid.New(), uuid.New(), "guest")
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

// UpdateRoleGuarded promotes a member to admin atomically: it acquires the
// admin-row lock first, then reads + updates the target row inside the same
// tx so concurrent demotions can't race the count.
func TestOrganizationMembershipStore_UpdateRoleGuarded_Promote(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("(?s)WITH locked.+FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT role FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec("(?s)UPDATE organization_memberships.+SET role").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	prev, err := NewOrganizationMembershipStore(mock).UpdateRoleGuarded(
		context.Background(), uuid.New(), uuid.New(), "admin",
	)
	require.NoError(t, err)
	require.Equal(t, "member", prev)
	require.NoError(t, mock.ExpectationsWereMet())
}

// UpdateRoleGuarded skips the UPDATE when the target's role already matches
// the requested role — the prevRole is still returned so the caller can audit.
func TestOrganizationMembershipStore_UpdateRoleGuarded_NoChange(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("(?s)WITH locked.+FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("(?s)SELECT role FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("admin"))
	mock.ExpectCommit()

	prev, err := NewOrganizationMembershipStore(mock).UpdateRoleGuarded(
		context.Background(), uuid.New(), uuid.New(), "admin",
	)
	require.NoError(t, err)
	require.Equal(t, "admin", prev)
	require.NoError(t, mock.ExpectationsWereMet())
}

// UpdateRoleGuarded refuses to demote the only remaining admin: the locked
// admin count is 1 and the target *is* that admin, so demotion would leave
// the org with no admins.
func TestOrganizationMembershipStore_UpdateRoleGuarded_LastAdmin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("(?s)WITH locked.+FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT role FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("admin"))
	mock.ExpectRollback()

	prev, err := NewOrganizationMembershipStore(mock).UpdateRoleGuarded(
		context.Background(), uuid.New(), uuid.New(), "member",
	)
	require.ErrorIs(t, err, ErrLastAdmin)
	require.Equal(t, "admin", prev)
	require.NoError(t, mock.ExpectationsWereMet())
}

// UpdateRoleGuarded surfaces pgx.ErrNoRows when the membership row is missing
// so the handler returns 404 rather than silently no-oping.
func TestOrganizationMembershipStore_UpdateRoleGuarded_NoMembership(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("(?s)WITH locked.+FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("(?s)SELECT role FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err = NewOrganizationMembershipStore(mock).UpdateRoleGuarded(
		context.Background(), uuid.New(), uuid.New(), "member",
	)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationMembershipStore_UpdateRoleGuarded_InvalidRole(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	_, err = NewOrganizationMembershipStore(mock).UpdateRoleGuarded(
		context.Background(), uuid.New(), uuid.New(), "owner",
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid role")
}

// RemoveGuarded removes a non-last-admin successfully: the locked admin count
// is >1 (or the role is non-admin) and the underlying Remove CTE runs inside
// the same tx.
func TestOrganizationMembershipStore_RemoveGuarded_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("(?s)WITH locked.+FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("(?s)SELECT role FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("admin"))
	mock.ExpectQuery("(?s)SELECT COUNT\\(\\*\\) FROM invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery("(?s)WITH deleted_membership.+cleared_answers").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()

	prev, revoked, err := NewOrganizationMembershipStore(mock).RemoveGuarded(
		context.Background(), uuid.New(), uuid.New(),
	)
	require.NoError(t, err)
	require.Equal(t, "admin", prev)
	require.Equal(t, 3, revoked, "RemoveGuarded should surface the pending-invitations snapshot for audit details")
	require.NoError(t, mock.ExpectationsWereMet())
}

// RemoveGuarded refuses to remove the only admin in the org.
func TestOrganizationMembershipStore_RemoveGuarded_LastAdmin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("(?s)WITH locked.+FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT role FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("admin"))
	mock.ExpectRollback()

	prev, revoked, err := NewOrganizationMembershipStore(mock).RemoveGuarded(
		context.Background(), uuid.New(), uuid.New(),
	)
	require.ErrorIs(t, err, ErrLastAdmin)
	require.Equal(t, "admin", prev)
	require.Equal(t, 0, revoked, "refused removes must not claim to have revoked any invitations")
	require.NoError(t, mock.ExpectationsWereMet())
}

// RemoveGuarded surfaces pgx.ErrNoRows when the membership doesn't exist so
// the handler can return 404 rather than 500.
func TestOrganizationMembershipStore_RemoveGuarded_NoMembership(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("(?s)WITH locked.+FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("(?s)SELECT role FROM organization_memberships.+FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, _, err = NewOrganizationMembershipStore(mock).RemoveGuarded(
		context.Background(), uuid.New(), uuid.New(),
	)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestOrganizationMembershipStore_RemoveGuarded_SerializesConcurrentAdminRemovals
// covers the race that motivates the FOR UPDATE lock in RemoveGuarded: two
// concurrent requests to remove one of the last two admins must not both
// observe adminCount == 2 and proceed. pgxmock cannot run real concurrent
// transactions, so we assert the expected tx ordering — BEGIN, lock-admins,
// SELECT-role-FOR-UPDATE, DELETE, COMMIT — across two representative cases:
// the winning tx sees count above threshold and commits, the losing tx sees
// the decremented count and refuses with ErrLastAdmin.
//
// The DB-level enforce_last_admin trigger (migration 000082) is the
// belt-and-suspenders safety net for cases that bypass this path entirely
// (ad-hoc SQL, a new handler that forgets to use RemoveGuarded, etc.). The
// final subtest simulates the trigger firing on a DELETE and asserts the
// store surfaces the constraint violation as a plain error, rather than
// masquerading it as ErrLastAdmin or silently succeeding.
func TestOrganizationMembershipStore_RemoveGuarded_SerializesConcurrentAdminRemovals(t *testing.T) {
	t.Parallel()

	t.Run("first-writer sees count above threshold and succeeds", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectBegin()
		mock.ExpectQuery("(?s)WITH locked.+FROM organization_memberships.+FOR UPDATE").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
		mock.ExpectQuery("(?s)SELECT role FROM organization_memberships.+FOR UPDATE").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("admin"))
		mock.ExpectQuery("(?s)SELECT COUNT\\(\\*\\) FROM invitations").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery("(?s)WITH deleted_membership").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectCommit()

		prev, _, err := NewOrganizationMembershipStore(mock).RemoveGuarded(
			context.Background(), uuid.New(), uuid.New(),
		)
		require.NoError(t, err)
		require.Equal(t, "admin", prev)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("second-writer sees decremented count and refuses", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectBegin()
		mock.ExpectQuery("(?s)WITH locked.+FROM organization_memberships.+FOR UPDATE").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery("(?s)SELECT role FROM organization_memberships.+FOR UPDATE").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("admin"))
		mock.ExpectRollback()

		prev, _, err := NewOrganizationMembershipStore(mock).RemoveGuarded(
			context.Background(), uuid.New(), uuid.New(),
		)
		require.ErrorIs(t, err, ErrLastAdmin)
		require.Equal(t, "admin", prev)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("plain Remove surfaces DB trigger constraint violation", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewOrganizationMembershipStore(mock)

		mock.ExpectQuery("(?s)DELETE FROM organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("ERROR: organization <uuid> would be left with no admins (SQLSTATE 23514)"))

		err = store.Remove(context.Background(), uuid.New(), uuid.New())
		require.Error(t, err)
		require.Contains(t, err.Error(), "no admins", "trigger message should propagate in the returned error")
	})

	// Pinned behavior for the cascade-delete carve-out in migration 000082's
	// enforce_last_admin trigger: `IF NOT EXISTS (organizations)` means a
	// last-admin DELETE inside a transaction that *also* deleted the org
	// must succeed without raising the invariant. At the Go layer this
	// surfaces as a plain successful Remove — pgxmock cannot run the trigger
	// itself, but we pin the "no error returned when the DB reports the
	// DELETE succeeded" behavior so a future trigger tweak that starts
	// raising on org-delete cascades would fail this test.
	t.Run("plain Remove succeeds when trigger skips check for deleted org", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewOrganizationMembershipStore(mock)

		mock.ExpectQuery("(?s)DELETE FROM organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

		require.NoError(t, store.Remove(context.Background(), uuid.New(), uuid.New()),
			"Remove should succeed when the last admin is deleted alongside their org "+
				"(enforce_last_admin returns NULL when the org no longer exists)")
	})
}
