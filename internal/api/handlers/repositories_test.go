package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func repoColumns() []string {
	return []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
		"private", "language", "description", "clone_url", "installation_id", "status",
		"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
}

func TestRepositoryHandler_List_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, integrationID, int64(1001), "test-org/repo1", "main",
				false, nil, nil, "https://github.com/test-org/repo1.git", int64(12345), "active",
				nil, nil, json.RawMessage(`{}`), now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.Repository]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp.Data, 1)
	assert.Equal(t, "test-org/repo1", resp.Data[0].FullName)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryHandler_List_Empty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.Repository]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp.Data, 0)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryHandler_Get_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, integrationID, int64(1001), "test-org/repo1", "main",
				false, nil, nil, "https://github.com/test-org/repo1.git", int64(12345), "active",
				nil, nil, json.RawMessage(`{}`), now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repoID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Get(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.Repository]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "test-org/repo1", resp.Data.FullName)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryHandler_Get_InvalidID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/not-a-uuid", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Get(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestRepositoryHandler_Update_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	// GetByID
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, integrationID, int64(1001), "test-org/repo1", "main",
				false, nil, nil, "https://github.com/test-org/repo1.git", int64(12345), "active",
				nil, nil, json.RawMessage(`{}`), now, now,
			),
		)

	// Update (uses QueryRow -> ExpectQuery, 4 named args: id, org_id, status, settings)
	mock.ExpectQuery("UPDATE repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
		)

	body := `{"status":"paused"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/repositories/"+repoID.String(), strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Update(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.Repository]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "paused", resp.Data.Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryHandler_Update_InvalidJSON(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/repositories/"+uuid.New().String(), strings.NewReader(`not json`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.New().String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Update(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_JSON")
}

func TestRepositoryHandler_Delete_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	mock.ExpectExec("DELETE FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/repositories/"+repoID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Delete(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.NoError(t, mock.ExpectationsWereMet())
}
