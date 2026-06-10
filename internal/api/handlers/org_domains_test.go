package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

var orgDomainHandlerColumns = []string{
	"id", "org_id", "domain", "verification_token", "status", "auto_join_enabled",
	"created_by", "created_at", "verified_at", "last_checked_at", "failed_checks",
}

// fakeDomainVerifier satisfies orgDomainVerifier without touching DNS.
type fakeDomainVerifier struct {
	ok  bool
	err error
	// records the last (domain, token) pair so tests can assert the handler
	// passed through the row's own values.
	domain, token string
}

func (f *fakeDomainVerifier) Verify(_ context.Context, domain, token string) (bool, error) {
	f.domain, f.token = domain, token
	return f.ok, f.err
}

func newOrgDomainsHandler(mock pgxmock.PgxPoolIface, verifier orgDomainVerifier) *OrgDomainsHandler {
	return NewOrgDomainsHandler(
		db.NewOrganizationDomainStore(mock),
		db.NewOrganizationMembershipStore(mock),
		db.NewUserStore(mock),
		verifier,
	)
}

func withOrgDomainChiParam(ctx context.Context, key, value string) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}

func TestOrgDomainsHandler_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status", "auto_join_enabled", "created_at"}).
			AddRow(uuid.New(), "pending", true, now))

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	// Mixed case + trailing dot: the handler must normalize before storing.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/domains", bytes.NewBufferString(`{"domain":"AssembledHQ.com."}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin}))
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusCreated, w.Code, "Create should return 201")
	require.Contains(t, w.Body.String(), `"domain":"assembledhq.com"`, "domain should be stored normalized to lowercase without the trailing dot")
	require.Contains(t, w.Body.String(), `"dns_record_name":"_143-verify.assembledhq.com"`, "response should carry the TXT record name to publish")
	require.Contains(t, w.Body.String(), `"dns_record_value":"143-domain-verify=`, "response should carry the TXT record value to publish")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Create_RejectsPublicEmailDomain(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// No DB expectations: the blocklist must reject before any query runs.
	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/domains", bytes.NewBufferString(`{"domain":"gmail.com"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New(), Role: models.RoleAdmin}))
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "public email providers must not be claimable")
	require.Contains(t, w.Body.String(), "INVALID_DOMAIN")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Create_DuplicateReturnsConflict(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "idx_org_domains_org_domain"})

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/domains", bytes.NewBufferString(`{"domain":"assembledhq.com"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New(), Role: models.RoleAdmin}))
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "DOMAIN_EXISTS")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Verify_MarksVerifiedWhenTXTMatches(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	domainID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT (.+) FROM organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgDomainHandlerColumns).
			AddRow(domainID, orgID, "assembledhq.com", "tok123", "pending", true, nil, now, nil, nil, 0))
	mock.ExpectQuery("UPDATE organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgDomainHandlerColumns).
			AddRow(domainID, orgID, "assembledhq.com", "tok123", "verified", true, nil, now, &now, &now, 0))

	verifier := &fakeDomainVerifier{ok: true}
	handler := newOrgDomainsHandler(mock, verifier)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/domains/"+domainID.String()+"/verify", nil)
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", domainID.String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Verify(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"status":"verified"`)
	require.Equal(t, "assembledhq.com", verifier.domain, "verifier must receive the stored domain, not client input")
	require.Equal(t, "tok123", verifier.token, "verifier must receive the row's own token")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Verify_MissingTXTRecord(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	domainID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT (.+) FROM organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgDomainHandlerColumns).
			AddRow(domainID, orgID, "assembledhq.com", "tok123", "pending", true, nil, now, nil, nil, 0))
	// A failed check must still stamp last_checked_at so the admin UI can
	// show when the last attempt happened.
	mock.ExpectExec("UPDATE organization_domains SET last_checked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{ok: false})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/domains/"+domainID.String()+"/verify", nil)
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", domainID.String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Verify(w, req)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "missing TXT record is a retryable user-fixable state, not a server error")
	require.Contains(t, w.Body.String(), "DOMAIN_NOT_VERIFIED")
	require.Contains(t, w.Body.String(), "_143-verify.assembledhq.com", "error message should tell the admin exactly where to publish the record")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Verify_DomainClaimedByOtherOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	domainID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT (.+) FROM organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgDomainHandlerColumns).
			AddRow(domainID, orgID, "assembledhq.com", "tok123", "pending", true, nil, now, nil, nil, 0))
	// DNS check passes, but another org won the verification race: the
	// partial unique index rejects the flip.
	mock.ExpectQuery("UPDATE organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "idx_org_domains_verified_domain"})

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{ok: true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/domains/"+domainID.String()+"/verify", nil)
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", domainID.String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Verify(w, req)
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "DOMAIN_CLAIMED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Verify_AlreadyVerifiedSkipsDNS(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	domainID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT (.+) FROM organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgDomainHandlerColumns).
			AddRow(domainID, orgID, "assembledhq.com", "tok123", "verified", true, nil, now, &now, &now, 0))

	// A verifier that errors loudly: it must never be called for an
	// already-verified row.
	verifier := &fakeDomainVerifier{err: context.DeadlineExceeded}
	handler := newOrgDomainsHandler(mock, verifier)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/domains/"+domainID.String()+"/verify", nil)
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", domainID.String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Verify(w, req)
	require.Equal(t, http.StatusOK, w.Code, "re-verifying an already-verified domain is an idempotent no-op")
	require.Empty(t, verifier.domain, "DNS verifier must not run for already-verified rows")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Update_TogglesAutoJoin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	domainID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("UPDATE organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgDomainHandlerColumns).
			AddRow(domainID, orgID, "assembledhq.com", "tok123", "verified", false, nil, now, &now, &now, 0))

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/team/domains/"+domainID.String(), bytes.NewBufferString(`{"auto_join_enabled":false}`))
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", domainID.String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"auto_join_enabled":false`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Update_MissingFieldRejected(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/team/domains/"+uuid.New().String(), bytes.NewBufferString(`{}`))
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", uuid.New().String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Delete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	domainID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT (.+) FROM organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgDomainHandlerColumns).
			AddRow(domainID, orgID, "assembledhq.com", "tok123", "verified", true, nil, now, &now, &now, 0))
	mock.ExpectExec("DELETE FROM organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/domains/"+domainID.String(), nil)
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", domainID.String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Delete(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Delete_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT (.+) FROM organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgDomainHandlerColumns))

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	id := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/team/domains/"+id.String(), nil)
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", id.String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.Delete(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "DOMAIN_NOT_FOUND")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_ListJoinable_UnverifiedEmailSeesNothing(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	// email_verified_at is NULL → the per-user joinable query must never
	// run; instead the handler probes (org-blind) whether the domain is
	// captured so the UI can prompt for verification.
	mock.ExpectQuery("SELECT email_verified_at FROM users").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"email_verified_at"}).AddRow(nil))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/joinable", nil)
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, Email: "alice@assembledhq.com"}))
	w := httptest.NewRecorder()

	handler.ListJoinable(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"data":[]`, "unverified email must see no joinable orgs")
	require.Contains(t, w.Body.String(), `"email_verification_required":true`,
		"a captured domain + unverified email should prompt for verification without naming the org")
	require.NotContains(t, w.Body.String(), "org_name", "org identity must stay hidden until the address is proven")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_ListJoinable_PublicDomainShortCircuits(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// No DB expectations at all: a gmail.com user can never have joinable
	// orgs, so no query should run.
	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/joinable", nil)
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New(), Email: "alice@gmail.com"}))
	w := httptest.NewRecorder()

	handler.ListJoinable(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"data":[]`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Join_GrantsMembership(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	orgID := uuid.New()
	verifiedAt := time.Now()

	mock.ExpectQuery("SELECT email_verified_at FROM users").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"email_verified_at"}).AddRow(&verifiedAt))
	mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}).
			AddRow(orgID, "Assembled", "assembledhq.com"))
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/"+orgID.String()+"/join", nil)
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", orgID.String()))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, Email: "alice@assembledhq.com"}))
	w := httptest.NewRecorder()

	handler.Join(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"role":"member"`, "domain joins always grant the lowest-privilege member role")
	require.Contains(t, w.Body.String(), `"org_name":"Assembled"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrgDomainsHandler_Join_RejectsIneligibleOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	verifiedAt := time.Now()

	mock.ExpectQuery("SELECT email_verified_at FROM users").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"email_verified_at"}).AddRow(&verifiedAt))
	// The eligible set is empty — the caller-supplied org id must be
	// rejected, never trusted. No membership INSERT may run.
	mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}))

	handler := newOrgDomainsHandler(mock, &fakeDomainVerifier{})
	target := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/"+target.String()+"/join", nil)
	req = req.WithContext(withOrgDomainChiParam(req.Context(), "id", target.String()))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, Email: "alice@assembledhq.com"}))
	w := httptest.NewRecorder()

	handler.Join(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "NOT_ELIGIBLE")
	require.NoError(t, mock.ExpectationsWereMet())
}
