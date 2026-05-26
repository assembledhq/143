package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

type mockPreviewSecretBundleStore struct {
	listFn   func(ctx context.Context, orgID uuid.UUID) ([]models.PreviewSecretBundleSummary, error)
	upsertFn func(ctx context.Context, orgID, createdBy uuid.UUID, name string, env map[string]string) error
	deleteFn func(ctx context.Context, orgID uuid.UUID, name string) error
}

func (m *mockPreviewSecretBundleStore) ListSummaries(ctx context.Context, orgID uuid.UUID) ([]models.PreviewSecretBundleSummary, error) {
	if m.listFn != nil {
		return m.listFn(ctx, orgID)
	}
	return nil, nil
}

func (m *mockPreviewSecretBundleStore) UpsertEnv(ctx context.Context, orgID, createdBy uuid.UUID, name string, env map[string]string) error {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, orgID, createdBy, name, env)
	}
	return nil
}

func (m *mockPreviewSecretBundleStore) Delete(ctx context.Context, orgID uuid.UUID, name string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, orgID, name)
	}
	return nil
}

func newPreviewSecretBundleRequest(t *testing.T, method, path, name string, body any, orgID, userID uuid.UUID) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body), "request body should encode as JSON")
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	if name != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", name)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return req.WithContext(ctx)
}

func TestPreviewSecretBundleHandler_List(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	handler := NewPreviewSecretBundleHandler(&mockPreviewSecretBundleStore{
		listFn: func(_ context.Context, gotOrgID uuid.UUID) ([]models.PreviewSecretBundleSummary, error) {
			require.Equal(t, orgID, gotOrgID, "List should scope preview secret bundles to the active org")
			return []models.PreviewSecretBundleSummary{{Name: "staging", EnvNames: []string{"API_TOKEN"}}}, nil
		},
	})
	req := newPreviewSecretBundleRequest(t, http.MethodGet, "/api/v1/settings/preview-secret-bundles", "", nil, orgID, uuid.New())
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "List should return OK")
	var resp models.ListResponse[models.PreviewSecretBundleSummary]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "List response should be valid JSON")
	require.Equal(t, []models.PreviewSecretBundleSummary{{Name: "staging", EnvNames: []string{"API_TOKEN"}}}, resp.Data, "List should return bundle summaries")
}

func TestPreviewSecretBundleHandler_UpsertScopesAndValidates(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	handler := NewPreviewSecretBundleHandler(&mockPreviewSecretBundleStore{
		upsertFn: func(_ context.Context, gotOrgID, gotCreatedBy uuid.UUID, name string, env map[string]string) error {
			require.Equal(t, orgID, gotOrgID, "Upsert should scope preview secret bundles to the active org")
			require.Equal(t, userID, gotCreatedBy, "Upsert should record the current user as creator")
			require.Equal(t, "staging", name, "Upsert should use the URL name")
			require.Equal(t, map[string]string{"API_TOKEN": "secret"}, env, "Upsert should pass the submitted env map")
			return nil
		},
	})
	body := models.PreviewSecretBundleInput{Name: "staging", Env: map[string]string{"API_TOKEN": "secret"}}
	req := newPreviewSecretBundleRequest(t, http.MethodPut, "/api/v1/settings/preview-secret-bundles/staging", "staging", body, orgID, userID)
	rr := httptest.NewRecorder()

	handler.Upsert(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code, "Upsert should return no content after saving")
}

func TestPreviewSecretBundleHandler_DeleteMissingReturnsNotFound(t *testing.T) {
	t.Parallel()

	handler := NewPreviewSecretBundleHandler(&mockPreviewSecretBundleStore{
		deleteFn: func(_ context.Context, _ uuid.UUID, _ string) error {
			return pgx.ErrNoRows
		},
	})
	req := newPreviewSecretBundleRequest(t, http.MethodDelete, "/api/v1/settings/preview-secret-bundles/staging", "staging", nil, uuid.New(), uuid.New())
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "Delete should map missing bundles to not found")
}
