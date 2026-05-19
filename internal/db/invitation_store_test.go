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
	"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at",
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

	email := "new@example.com"
	inv := &models.Invitation{
		OrgID:            orgID,
		Email:            &email,
		AcceptanceMethod: models.InvitationAcceptanceMethodEmail,
		Role:             "member",
		InvitedBy:        invitedBy,
		Token:            "tok_abc123",
		ExpiresAt:        now.Add(72 * time.Hour),
	}

	mock.ExpectQuery("INSERT INTO invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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

func TestInvitationStore_Create_DefaultsEmptyAcceptanceMethod(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	store := NewInvitationStore(mock)
	now := time.Now()
	email := "new@example.com"
	inv := &models.Invitation{
		OrgID:     uuid.New(),
		Email:     &email,
		Role:      "member",
		InvitedBy: uuid.New(),
		Token:     "tok_default",
		ExpiresAt: now.Add(72 * time.Hour),
	}

	mock.ExpectQuery("INSERT INTO invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "status", "created_at"}).
				AddRow(uuid.New(), "pending", now),
		)

	err = store.Create(context.Background(), inv)
	require.NoError(t, err, "Create should persist invitations with legacy zero-value acceptance methods")
	require.Equal(t, models.InvitationAcceptanceMethodEither, inv.AcceptanceMethod, "Create should normalize empty acceptance methods before insert")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestInvitationStore_Create_Error(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	email := "new@example.com"
	inv := &models.Invitation{
		OrgID:            uuid.New(),
		Email:            &email,
		AcceptanceMethod: models.InvitationAcceptanceMethodEmail,
		Role:             "member",
		InvitedBy:        uuid.New(),
		Token:            "tok_abc",
		ExpiresAt:        time.Now().Add(72 * time.Hour),
	}

	mock.ExpectQuery("INSERT INTO invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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
				AddRow(id, orgID, strPtr("user@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", invitedBy, "tok_xyz", "pending", now.Add(72*time.Hour), now, nil),
		)

	inv, err := store.GetByToken(context.Background(), "tok_xyz")
	require.NoError(t, err)
	require.Equal(t, id, inv.ID)
	require.NotNil(t, inv.Email)
	require.Equal(t, "user@example.com", *inv.Email)
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
				AddRow(uuid.New(), orgID, strPtr("a@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok_a", "pending", now.Add(72*time.Hour), now, nil).
				AddRow(uuid.New(), orgID, strPtr("b@example.com"), nil, models.InvitationAcceptanceMethodEither, "admin", uuid.New(), "tok_b", "pending", now.Add(72*time.Hour), now, nil),
		)

	invs, err := store.ListPendingByOrg(context.Background(), orgID)
	require.NoError(t, err)
	require.Len(t, invs, 2)
	require.NotNil(t, invs[0].Email)
	require.Equal(t, "a@example.com", *invs[0].Email)
	require.NotNil(t, invs[1].Email)
	require.Equal(t, "b@example.com", *invs[1].Email)
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

func TestInvitationStore_GetByID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)
	now := time.Now()
	id := uuid.New()
	orgID := uuid.New()
	invitedBy := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationColumns).
				AddRow(id, orgID, strPtr("invitee@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", invitedBy, "tok_abc", "pending", now.Add(72*time.Hour), now, nil),
		)

	inv, err := store.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, id, inv.ID)
	require.NotNil(t, inv.Email)
	require.Equal(t, "invitee@example.com", *inv.Email)
	require.Equal(t, "pending", inv.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(invitationColumns))

	_, err = store.GetByID(context.Background(), uuid.New())
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// pendingForUserColumns mirrors the column projection of ListPendingForUser.
// The query joins invitations with organizations and users and projects a
// flatter row than the bare invitation columns above; tests need a matching
// shape so pgxmock can drive RowToStructByName decoding.
var pendingForUserColumns = []string{
	"id", "org_id", "org_name", "role", "invited_by", "inviter_name", "expires_at", "created_at",
}

func TestInvitationStore_ListPendingForUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)
	now := time.Now()
	userID := uuid.New()
	orgA := uuid.New()
	orgB := uuid.New()
	inviterA := uuid.New()
	inviterB := uuid.New()

	// Two distinct orgs come back from the dedupe layer; the row order is
	// the wrapper SELECT's ORDER BY created_at DESC, so the most-recent
	// invite is first.
	mock.ExpectQuery("FROM invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pendingForUserColumns).
				AddRow(uuid.New(), orgA, "Acme", "member", inviterA, "Alice", now.Add(72*time.Hour), now).
				AddRow(uuid.New(), orgB, "Globex", "admin", inviterB, "Bob", now.Add(72*time.Hour), now.Add(-time.Hour)),
		)

	rows, err := store.ListPendingForUser(context.Background(), userID, "invitee@example.com", "invitee-gh")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "Acme", rows[0].OrgName)
	require.Equal(t, "Alice", rows[0].InviterName)
	require.Equal(t, "member", rows[0].Role)
	require.Equal(t, "Globex", rows[1].OrgName)
	require.Equal(t, "Bob", rows[1].InviterName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_ListPendingForUser_Empty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	mock.ExpectQuery("FROM invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(pendingForUserColumns))

	rows, err := store.ListPendingForUser(context.Background(), uuid.New(), "nobody@example.com", "")
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_ListPendingForUser_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	mock.ExpectQuery("FROM invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("connection lost"))

	_, err = store.ListPendingForUser(context.Background(), uuid.New(), "x@example.com", "")
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// The query must use DISTINCT ON (i.org_id) so a user with both an email-
// invite and a github-invite for the same org sees a single dropdown row,
// and must filter out invites for orgs the user is already a member of so
// the dropdown can't surface an invite that would 409 on accept. pgxmock
// can't evaluate the SQL semantically; this test pins the query *shape*
// so a refactor that drops either guard breaks loudly instead of silently
// regressing the user-facing dedupe and already-member behaviors.
func TestInvitationStore_ListPendingForUser_QueryShape(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	// Both regex anchors must match the SAME query string. ExpectQuery's
	// argument is a regex, so escape the parens for the DISTINCT ON clause.
	mock.ExpectQuery(`DISTINCT ON \(i\.org_id\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(pendingForUserColumns))

	_, err = store.ListPendingForUser(context.Background(), uuid.New(), "u@example.com", "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationStore_ListPendingForUser_FiltersExistingMemberships(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	mock.ExpectQuery(`NOT EXISTS.*organization_memberships`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(pendingForUserColumns))

	_, err = store.ListPendingForUser(context.Background(), uuid.New(), "u@example.com", "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Expired invites are pending in the DB until they're swept (and there is no
// sweeper today), so the query must filter them with expires_at > now() to
// avoid surfacing rows that would 410 on accept. Same anti-drift rationale as
// the DISTINCT ON / NOT EXISTS pin above.
func TestInvitationStore_ListPendingForUser_FiltersExpired(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewInvitationStore(mock)

	mock.ExpectQuery(`expires_at > now\(\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(pendingForUserColumns))

	_, err = store.ListPendingForUser(context.Background(), uuid.New(), "u@example.com", "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
