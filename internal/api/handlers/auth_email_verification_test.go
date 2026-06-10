package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// fakeEmailSender records sends for assertions.
type fakeEmailSender struct {
	verificationTo  string
	verificationURL string
	err             error
}

func (f *fakeEmailSender) SendInvitation(_ context.Context, to, inviterName, orgName, acceptURL string) error {
	return f.err
}

func (f *fakeEmailSender) SendEmailVerification(_ context.Context, to, verifyURL string) error {
	f.verificationTo = to
	f.verificationURL = verifyURL
	return f.err
}

func TestSendEmailVerification_IssuesTokenAndSends(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()

	mock.ExpectQuery("SELECT email_verified_at FROM users").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"email_verified_at"}).AddRow(nil))
	// A resend invalidates earlier links before minting the new one.
	mock.ExpectExec("DELETE FROM email_verification_tokens").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectQuery("INSERT INTO email_verification_tokens").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), time.Now()))

	sender := &fakeEmailSender{}
	handler := NewAuthHandler(&config.Config{FrontendURL: "http://frontend.test"}, mock, db.NewUserStore(mock), nil, nil, nil)
	handler.SetEmailVerificationDeps(db.NewEmailVerificationStore(mock), sender)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/email-verifications", nil)
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, Email: "bob@assembledhq.com"}))
	w := httptest.NewRecorder()

	handler.SendEmailVerification(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)
	require.Equal(t, "bob@assembledhq.com", sender.verificationTo)
	require.Contains(t, sender.verificationURL, "http://frontend.test/verify-email?token=", "link must point at the frontend confirm page")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSendEmailVerification_AlreadyVerifiedShortCircuits(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	verifiedAt := time.Now()
	mock.ExpectQuery("SELECT email_verified_at FROM users").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"email_verified_at"}).AddRow(&verifiedAt))

	sender := &fakeEmailSender{}
	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
	handler.SetEmailVerificationDeps(db.NewEmailVerificationStore(mock), sender)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/email-verifications", nil)
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, Email: "bob@assembledhq.com"}))
	w := httptest.NewRecorder()

	handler.SendEmailVerification(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "already_verified")
	require.Empty(t, sender.verificationTo, "no email should be sent to an already-verified address")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConfirmEmailVerification_StampsAndAutoJoins(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()
	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	// Single-use claim of the token row.
	mock.ExpectQuery("UPDATE email_verification_tokens").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "email", "token", "expires_at", "consumed_at", "created_at"}).
			AddRow(uuid.New(), userID, "bob@assembledhq.com", "tok", now.Add(time.Hour), &now, now))
	mock.ExpectQuery("SELECT .+ FROM users.*WHERE id = @id").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows(userColumns).
			AddRow(userID, orgID, "bob@assembledhq.com", "Bob", "admin", nil, nil, nil, nil, strPtr("hash"), nil, now))
	mock.ExpectExec("UPDATE users SET email_verified_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Domain capture: matching org found, not yet a member → grant member.
	mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
		WithArgs("assembledhq.com").
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}).
			AddRow(orgID, "Assembled", "assembledhq.com"))
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}))
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec("UPDATE users SET last_org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, db.NewOrganizationMembershipStore(mock))
	handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))
	handler.SetEmailVerificationDeps(db.NewEmailVerificationStore(mock), nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/email-verifications/confirm", bytes.NewBufferString(`{"token":"tok"}`))
	w := httptest.NewRecorder()

	handler.ConfirmEmailVerification(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Verified  bool `json:"verified"`
			JoinedOrg *struct {
				OrgID   uuid.UUID `json:"org_id"`
				OrgName string    `json:"org_name"`
			} `json:"joined_org"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Data.Verified)
	require.NotNil(t, resp.Data.JoinedOrg, "verifying a captured-domain email should auto-join the org")
	require.Equal(t, orgID, resp.Data.JoinedOrg.OrgID)
	require.Equal(t, "Assembled", resp.Data.JoinedOrg.OrgName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConfirmEmailVerification_InvalidTokenIsGone(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// Expired / consumed / unknown all collapse to zero rows from the
	// single-use claim UPDATE.
	mock.ExpectQuery("UPDATE email_verification_tokens").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "email", "token", "expires_at", "consumed_at", "created_at"}))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
	handler.SetEmailVerificationDeps(db.NewEmailVerificationStore(mock), nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/email-verifications/confirm", bytes.NewBufferString(`{"token":"nope"}`))
	w := httptest.NewRecorder()

	handler.ConfirmEmailVerification(w, req)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "VERIFICATION_INVALID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConfirmEmailVerification_EmailChangedSinceIssue(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	now := time.Now()
	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("UPDATE email_verification_tokens").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "email", "token", "expires_at", "consumed_at", "created_at"}).
			AddRow(uuid.New(), userID, "old@assembledhq.com", "tok", now.Add(time.Hour), &now, now))
	// Account email has since changed: the token's proof no longer applies.
	mock.ExpectQuery("SELECT .+ FROM users.*WHERE id = @id").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows(userColumns).
			AddRow(userID, uuid.New(), "new@elsewhere.com", "Bob", "admin", nil, nil, nil, nil, strPtr("hash"), nil, now))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
	handler.SetEmailVerificationDeps(db.NewEmailVerificationStore(mock), nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/email-verifications/confirm", bytes.NewBufferString(`{"token":"tok"}`))
	w := httptest.NewRecorder()

	handler.ConfirmEmailVerification(w, req)
	require.Equal(t, http.StatusGone, w.Code, "a token for a superseded address must not verify the new one")
	require.NoError(t, mock.ExpectationsWereMet())
}
