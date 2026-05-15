package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// invitationRowColumns matches the InvitationStore column projection used by
// GetByToken / GetByID — duplicated here rather than imported because the
// test file lives in the handlers package, not internal/db.
var invitationRowColumns = []string{
	"id", "org_id", "email", "github_username", "acceptance_method", "role",
	"invited_by", "token", "status", "expires_at", "created_at", "accepted_at",
}

// pendingForUserRowColumns matches the wrapper-projection of ListPendingForUser
// (org_name + inviter_name come from the JOIN, token/email/etc. are dropped).
var pendingForUserRowColumns = []string{
	"id", "org_id", "org_name", "role", "invited_by", "inviter_name", "expires_at", "created_at",
}

// requestWithChiURLParam wraps the request context so chi.URLParam("id")
// resolves to the supplied value. Required because the handlers read the
// URL-named id without going through the chi router (httptest skips routing).
func requestWithChiURLParam(req *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// ListPendingInvitations rejects unauthenticated requests with 401 — anyone
// reading this endpoint must have a session, since the response is filtered
// to the caller's email/github_login.
func TestAuthHandler_ListPendingInvitations_Unauthenticated(t *testing.T) {
	t.Parallel()

	handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/invitations/pending", nil)
	w := httptest.NewRecorder()

	handler.ListPendingInvitations(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// Empty result is a 200 with `data: []`, not a 404 — the org-switcher poll
// distinguishes "no invites" from "list lookup failed" by status code.
func TestAuthHandler_ListPendingInvitations_Empty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("FROM invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(pendingForUserRowColumns))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/invitations/pending", nil)
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.ListPendingInvitations(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"data":[]`)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Happy path: the wrapper row projection decodes into the JSON response shape
// the frontend consumes, with org_name and inviter UserBrief populated.
func TestAuthHandler_ListPendingInvitations_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	inviterID := uuid.New()
	invID := uuid.New()

	mock.ExpectQuery("FROM invitations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pendingForUserRowColumns).
				AddRow(invID, orgID, "Acme", "member", inviterID, "Alice", now.Add(72*time.Hour), now),
		)

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "invitee@example.com"}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/invitations/pending", nil)
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.ListPendingInvitations(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	require.Contains(t, body, `"org_name":"Acme"`)
	require.Contains(t, body, `"role":"member"`)
	require.Contains(t, body, `"name":"Alice"`)
	require.Contains(t, body, orgID.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

// AcceptInvitationByID rejects requests without an authenticated user.
func TestAuthHandler_AcceptInvitationByID_Unauthenticated(t *testing.T) {
	t.Parallel()

	handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/accept", nil), "id", uuid.New().String())
	w := httptest.NewRecorder()

	handler.AcceptInvitationByID(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// Malformed id in the URL surfaces as 400 — the handler shouldn't call into
// the store with a zero-value uuid.
func TestAuthHandler_AcceptInvitationByID_InvalidID(t *testing.T) {
	t.Parallel()

	handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/accept", nil), "id", "not-a-uuid")
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.AcceptInvitationByID(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

// Happy path. Validation runs on the pool, then the accept tx fires Begin →
// UPDATE invitations → INSERT memberships → Commit. last_org_id is NOT
// updated (acceptOptions{updateLastOrgID:false}) so the user stays in their
// current org.
func TestAuthHandler_AcceptInvitationByID_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	userID := uuid.New()
	invOrgID := uuid.New()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, invOrgID, strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(time.Hour), now, nil),
		)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectCommit()
	// audit_logs INSERT for the accepted action.
	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))
	user := &models.User{ID: userID, Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/accept", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.AcceptInvitationByID(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), invOrgID.String())
	require.Contains(t, w.Body.String(), `"role":"member"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A user trying to accept an invitation addressed to someone else gets 403,
// not 400 — the id in the URL names a specific row, so the failure is an
// authorization problem, not bad input.
func TestAuthHandler_AcceptInvitationByID_Mismatch_Returns403(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, uuid.New(), strPtr("someone-else@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(time.Hour), now, nil),
		)

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "wrong@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/accept", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.AcceptInvitationByID(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_MISMATCH")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Expired invitations return 410 GONE so the frontend knows to drop them
// from the dropdown rather than retry.
func TestAuthHandler_AcceptInvitationByID_Expired_Returns410(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, uuid.New(), strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(-time.Hour), now.Add(-2*time.Hour), nil),
		)

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/accept", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.AcceptInvitationByID(w, req)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_EXPIRED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Race: invitation is pending at SELECT time but a concurrent revoke / accept
// landed before our UPDATE. The Accept WHERE status='pending' clause returns
// ErrNoRows, which acceptValidatedInvitation maps to INVITE_INVALID 410.
func TestAuthHandler_AcceptInvitationByID_AcceptRace_Returns410(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, uuid.New(), strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(time.Hour), now, nil),
		)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()
	// Failed-claim audit fires after the rollback — invitation row is loaded,
	// so the audit emitter writes against the inviting org.
	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/accept", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.AcceptInvitationByID(w, req)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_INVALID")
	require.NoError(t, mock.ExpectationsWereMet())
}

// AcceptInvitationByID does NOT update the user's last_org_id — that's
// reserved for the email-link claim path. Verified here by the absence of a
// UPDATE users ... last_org_id expectation; if the handler regressed to
// updating it, pgxmock would fail the test on an unexpected exec.
func TestAuthHandler_AcceptInvitationByID_DoesNotUpdateLastOrgID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, uuid.New(), strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(time.Hour), now, nil),
		)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectCommit()
	// Audit emitter is left nil so no audit_logs INSERT is expected; pgxmock
	// flags any unexpected statement as a failure, which is the test guard.

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/accept", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.AcceptInvitationByID(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// DeclineInvitationByID happy path: load → match-check → revoke → audit emit.
func TestAuthHandler_DeclineInvitationByID_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	invID := uuid.New()
	invOrgID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, invOrgID, strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(time.Hour), now, nil),
		)
	mock.ExpectExec("UPDATE invitations SET status = 'revoked'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Decline audit row fires post-revoke.
	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/decline", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.DeclineInvitationByID(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Decline by a non-recipient is also 403 — same authorization model as accept.
// No audit row is written for the failed attempt (we don't have org context
// for an attempted decline by a non-recipient that's worth persisting).
func TestAuthHandler_DeclineInvitationByID_Mismatch_Returns403(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, uuid.New(), strPtr("someone-else@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(time.Hour), now, nil),
		)

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "wrong@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/decline", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.DeclineInvitationByID(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_MISMATCH")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Concurrent revoke or accept landed first → revoke returns ErrNoRows → 410.
func TestAuthHandler_DeclineInvitationByID_AlreadyResolved_Returns410(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, uuid.New(), strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(time.Hour), now, nil),
		)
	mock.ExpectExec("UPDATE invitations SET status = 'revoked'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/decline", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.DeclineInvitationByID(w, req)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_INVALID")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Decline allows expired invitations: the row is still the recipient's to
// dismiss even if the dropdown staled across expiry. Revoke's status='pending'
// WHERE clause remains the real concurrency guard — here it succeeds because
// we model the expired row as still pending (nothing sweeps them today).
func TestAuthHandler_DeclineInvitationByID_Expired_AllowsDismissal(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	invID := uuid.New()
	invOrgID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, invOrgID, strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(-time.Hour), now.Add(-2*time.Hour), nil),
		)
	mock.ExpectExec("UPDATE invitations SET status = 'revoked'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Audit still emits — the user took an action, and downstream analytics
	// should see the decline regardless of expiry state.
	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/decline", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.DeclineInvitationByID(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Accept does NOT allow expired invitations — the user is trying to claim a
// grant that has timed out, which is substantively different from declining
// one. 410 GONE is the honest status so the dropdown drops the row.
func TestAuthHandler_AcceptInvitationByID_Expired_Still410(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(invitationRowColumns).
				AddRow(invID, uuid.New(), strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "tok", "pending", now.Add(-time.Hour), now.Add(-2*time.Hour), nil),
		)

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/accept", nil), "id", invID.String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.AcceptInvitationByID(w, req)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_EXPIRED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// 404 path: the URL names an id that doesn't exist.
func TestAuthHandler_AcceptInvitationByID_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(invitationRowColumns))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := requestWithChiURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/invitations/x/accept", nil), "id", uuid.New().String())
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.AcceptInvitationByID(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_NOT_FOUND")
	require.NoError(t, mock.ExpectationsWereMet())
}
