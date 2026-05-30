package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var verifiedDomainHandlerColumns = []string{
	"id", "org_id", "domain", "status", "verification_token", "verified_at",
	"auto_join_enabled", "auto_join_role", "created_by", "created_at", "updated_at",
}

func TestVerifiedDomainHandler_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO org_verified_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status", "created_at", "updated_at"}).
			AddRow(uuid.New(), models.VerifiedDomainStatusPending, now, now))

	handler := NewVerifiedDomainHandler(db.NewVerifiedDomainStore(mock), nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/domains", bytes.NewBufferString(`{"domain":"Example.COM","auto_join_role":"member"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin}))
	w := httptest.NewRecorder()

	handler.Create(w, req)
	require.Equal(t, http.StatusCreated, w.Code, "Create should return 201")
	require.Contains(t, w.Body.String(), "_143-domain-verification.example.com", "Create should return DNS verification host")
	require.Contains(t, w.Body.String(), "143-domain-verification=", "Create should return DNS verification record")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestVerifiedDomainHandler_VerifyChecksTXTRecord(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	domainID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM org_verified_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(verifiedDomainHandlerColumns).
			AddRow(domainID, orgID, "example.com", models.VerifiedDomainStatusPending, "token", nil, true, models.RoleMember, userID, now, now))
	mock.ExpectQuery("UPDATE org_verified_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(verifiedDomainHandlerColumns).
			AddRow(domainID, orgID, "example.com", models.VerifiedDomainStatusVerified, "token", &now, true, models.RoleMember, userID, now, now))

	handler := NewVerifiedDomainHandler(db.NewVerifiedDomainStore(mock), func(ctx context.Context, host string) ([]string, error) {
		require.Equal(t, "_143-domain-verification.example.com", host, "Verify should query the expected DNS host")
		return []string{"143-domain-verification=token"}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/domains/"+domainID.String()+"/verify", nil)
	req = req.WithContext(withVerifiedDomainChiParam(req.Context(), "id", domainID.String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Verify(w, req)
	require.Equal(t, http.StatusOK, w.Code, "Verify should return the verified row")
	require.Contains(t, w.Body.String(), `"status":"verified"`, "Verify should mark the domain verified")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestVerifiedDomainHandler_VerifyRejectsMissingTXTRecord(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	domainID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM org_verified_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(verifiedDomainHandlerColumns).
			AddRow(domainID, orgID, "example.com", models.VerifiedDomainStatusPending, "token", nil, true, models.RoleMember, uuid.New(), now, now))

	handler := NewVerifiedDomainHandler(db.NewVerifiedDomainStore(mock), func(ctx context.Context, host string) ([]string, error) {
		return []string{"different=value"}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/domains/"+domainID.String()+"/verify", nil)
	req = req.WithContext(withVerifiedDomainChiParam(req.Context(), "id", domainID.String()))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Verify(w, req)
	require.Equal(t, http.StatusConflict, w.Code, "Verify should reject when DNS TXT record is absent")
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "error response should be valid JSON")
	require.Contains(t, w.Body.String(), "DOMAIN_NOT_VERIFIED", "Verify should return a domain verification error code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func withVerifiedDomainChiParam(ctx context.Context, key, value string) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}
