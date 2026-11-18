package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/packagecredentials"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type packageRegistryServiceFake struct {
	row                           models.PackageRegistryCredentialSummary
	err                           error
	scope                         []uuid.UUID
	token, registry, packageScope string
	calls                         int
}

func (s *packageRegistryServiceFake) Metadata(_ context.Context, org, repo uuid.UUID) ([]models.PackageRegistryCredentialSummary, error) {
	s.scope = []uuid.UUID{org, repo}
	s.calls++
	return []models.PackageRegistryCredentialSummary{s.row}, s.err
}
func (s *packageRegistryServiceFake) Create(_ context.Context, org, repo uuid.UUID, registry, scope, token string) (models.PackageRegistryCredentialSummary, error) {
	s.scope = []uuid.UUID{org, repo}
	s.registry, s.packageScope, s.token = registry, scope, token
	s.calls++
	return s.row, s.err
}
func (s *packageRegistryServiceFake) Rotate(_ context.Context, org, repo, id, revision uuid.UUID, token string) (models.PackageRegistryCredentialSummary, error) {
	s.scope = []uuid.UUID{org, repo, id, revision}
	s.token = token
	s.calls++
	return s.row, s.err
}
func (s *packageRegistryServiceFake) Delete(_ context.Context, org, repo, id, revision uuid.UUID) error {
	s.scope = []uuid.UUID{org, repo, id, revision}
	s.calls++
	return s.err
}

func packageRegistryTestRouter(service *packageRegistryServiceFake) http.Handler {
	h := NewPackageRegistryCredentialHandler(service, nil)
	router := chi.NewRouter()
	router.Use(middleware.RequireRole("admin"))
	router.Get("/repositories/{id}/package-registry-credentials", h.List)
	router.Post("/repositories/{id}/package-registry-credentials", h.Create)
	router.Put("/repositories/{id}/package-registry-credentials/{credential_id}", h.Rotate)
	router.Delete("/repositories/{id}/package-registry-credentials/{credential_id}", h.Delete)
	return router
}
func packageRegistryRequest(method, path, body, role string, orgID uuid.UUID) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New(), OrgID: uuid.New(), Role: "viewer"})
	ctx = middleware.WithActiveRole(ctx, role)
	return req.WithContext(ctx)
}

func TestPackageRegistryCredentialHandlerContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, method string
		status       int
	}{{"list", http.MethodGet, http.StatusOK}, {"create", http.MethodPost, http.StatusCreated}, {"rotate", http.MethodPut, http.StatusOK}, {"delete", http.MethodDelete, http.StatusNoContent}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID, id, revision := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			row := models.PackageRegistryCredentialSummary{ID: id, RepositoryID: repoID, RegistryURL: "https://registry.example.test/", PackageScope: "@example", Revision: revision, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC()}
			service := &packageRegistryServiceFake{row: row}
			path := "/repositories/" + repoID.String() + "/package-registry-credentials"
			body := ""
			wantScope := []uuid.UUID{orgID, repoID}
			if tt.method == http.MethodPost {
				body = `{"registry_url":"https://registry.example.test/","package_scope":"@example","token":"synthetic-token"}`
			}
			if tt.method == http.MethodPut {
				path += "/" + id.String()
				body = `{"expected_revision":"` + revision.String() + `","token":"synthetic-token"}`
				wantScope = append(wantScope, id, revision)
			}
			if tt.method == http.MethodDelete {
				path += "/" + id.String() + "?expected_revision=" + revision.String()
				wantScope = append(wantScope, id, revision)
			}
			recorder := httptest.NewRecorder()
			packageRegistryTestRouter(service).ServeHTTP(recorder, packageRegistryRequest(tt.method, path, body, "admin", orgID))
			require.Equal(t, tt.status, recorder.Code, "operation should use the documented response status")
			require.Equal(t, wantScope, service.scope, "handler must use the active org and exact repository and revision")
			require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"), "credential metadata must not enter browser HTTP caches")
			require.NotContains(t, recorder.Body.String(), "synthetic-token", "responses must never echo the submitted token")
			if tt.method == http.MethodGet {
				var response models.ListResponse[models.PackageRegistryCredentialSummary]
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), "list should be standard JSON")
				require.Equal(t, []models.PackageRegistryCredentialSummary{row}, response.Data, "list response should contain only summaries")
			} else if tt.method != http.MethodDelete {
				var response models.SingleResponse[models.PackageRegistryCredentialSummary]
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), "mutation should return standard JSON")
				require.Equal(t, row, response.Data, "mutation should return only metadata")
				require.Equal(t, "synthetic-token", service.token, "token should reach only the credential service")
			}
		})
	}
}

func TestPackageRegistryCredentialHandlerErrorsAreSecretSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not found", pgx.ErrNoRows, http.StatusNotFound, "PACKAGE_REGISTRY_CREDENTIAL_NOT_FOUND"},
		{"revision conflict", db.ErrPackageRegistryCredentialConflict, http.StatusConflict, "PACKAGE_REGISTRY_CREDENTIAL_CHANGED"},
		{"duplicate origin or scope", db.ErrPackageRegistryScopeConflict, http.StatusConflict, "PACKAGE_REGISTRY_CREDENTIAL_EXISTS"},
		{"encryption unavailable", packagecredentials.ErrEncryptionUnavailable, http.StatusServiceUnavailable, "PACKAGE_REGISTRY_ENCRYPTION_UNAVAILABLE"},
		{"invalid credential", packagecredentials.ErrInvalidCredential, http.StatusBadRequest, "INVALID_PACKAGE_REGISTRY_CREDENTIAL"},
		{"raw secret-bearing error", errors.New("driver error synthetic-token ciphertext"), http.StatusInternalServerError, "PACKAGE_REGISTRY_CREDENTIAL_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := &packageRegistryServiceFake{err: tt.err}
			req := packageRegistryRequest(http.MethodPost, "/repositories/"+uuid.NewString()+"/package-registry-credentials", `{"registry_url":"https://registry.example.test/","package_scope":"@example","token":"synthetic-token"}`, "admin", uuid.New())
			var logs bytes.Buffer
			logger := zerolog.New(&logs)
			req = req.WithContext(logger.WithContext(req.Context()))
			recorder := httptest.NewRecorder()
			packageRegistryTestRouter(service).ServeHTTP(recorder, req)
			require.Equal(t, tt.status, recorder.Code, "errors should map to a stable safe status")
			var response models.ErrorResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), "errors should use the standard JSON shape")
			require.Equal(t, tt.code, response.Error.Code, "errors should have stable machine-readable codes")
			require.NotContains(t, recorder.Body.String(), "synthetic-token", "response must hide secret-bearing errors")
			require.NotContains(t, logs.String(), "synthetic-token", "request logs must hide secret-bearing errors")
		})
	}
}

func TestPackageRegistryCredentialHandlerRejectsMalformedRequests(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, method, body, query string }{
		{"unknown field", http.MethodPost, `{"token":"synthetic-token","unknown":"synthetic-token"}`, ""},
		{"trailing JSON", http.MethodPost, `{} {"token":"synthetic-token"}`, ""},
		{"oversized body", http.MethodPost, `{"token":"` + strings.Repeat("x", 17000) + `"}`, ""},
		{"missing rotate revision", http.MethodPut, `{"token":"synthetic-token"}`, ""},
		{"malformed rotate revision", http.MethodPut, `{"expected_revision":"synthetic-token","token":"synthetic-token"}`, ""},
		{"missing delete revision", http.MethodDelete, "", ""},
		{"malformed delete revision", http.MethodDelete, "", "?expected_revision=synthetic-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := &packageRegistryServiceFake{}
			path := "/repositories/" + uuid.NewString() + "/package-registry-credentials"
			if tt.method != http.MethodPost {
				path += "/" + uuid.NewString()
			}
			path += tt.query
			recorder := httptest.NewRecorder()
			packageRegistryTestRouter(service).ServeHTTP(recorder, packageRegistryRequest(tt.method, path, tt.body, "admin", uuid.New()))
			require.Equal(t, http.StatusBadRequest, recorder.Code, "invalid requests should be rejected before invoking services")
			require.Equal(t, 0, service.calls, "invalid requests must not invoke credential services")
			require.NotContains(t, recorder.Body.String(), "synthetic-token", "parse errors must not echo request values")
		})
	}
}

func TestPackageRegistryCredentialHandlerAdminOnly(t *testing.T) {
	t.Parallel()
	tests := []struct{ role, method string }{}
	for _, role := range []string{"member", "builder", "viewer"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			tests = append(tests, struct{ role, method string }{role, method})
		}
	}
	for _, tt := range tests {
		t.Run(tt.role+" "+tt.method, func(t *testing.T) {
			t.Parallel()
			service := &packageRegistryServiceFake{}
			path := "/repositories/" + uuid.NewString() + "/package-registry-credentials"
			if tt.method == http.MethodPut || tt.method == http.MethodDelete {
				path += "/" + uuid.NewString()
			}
			recorder := httptest.NewRecorder()
			packageRegistryTestRouter(service).ServeHTTP(recorder, packageRegistryRequest(tt.method, path, "{}", tt.role, uuid.New()))
			require.Equal(t, http.StatusForbidden, recorder.Code, "all credential operations require the active organization admin role")
			require.Equal(t, 0, service.calls, "non-admins must never reach credential services")
		})
	}
}
