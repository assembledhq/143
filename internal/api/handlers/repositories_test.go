package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func repoColumns() []string {
	return []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
		"private", "language", "description", "clone_url", "installation_id", "status",
		"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
}

func TestRepositoryHandler_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedLen  int
	}{
		{
			name: "returns repositories for org successfully",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				repoID := uuid.New()
				integrationID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(repoColumns()).AddRow(
							repoID, orgID, integrationID, int64(1001), "test-org/repo1", "main",
							false, nil, nil, "https://github.com/test-org/repo1.git", int64(12345), "active",
							nil, nil, json.RawMessage(`{}`), now, now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
		},
		{
			name: "returns empty list when no repositories exist",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(repoColumns()))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewRepositoryStore(mock)
			handler := NewRepositoryHandler(store)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.List(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.Repository]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of repositories")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestRepositoryHandler_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		idParam      string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name:    "returns repository by ID successfully",
			idParam: "", // will be set to a valid UUID in the subtest
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				repoID := uuid.New()
				integrationID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(repoColumns()).AddRow(
							repoID, orgID, integrationID, int64(1001), "test-org/repo1", "main",
							false, nil, nil, "https://github.com/test-org/repo1.git", int64(12345), "active",
							nil, nil, json.RawMessage(`{}`), now, now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "test-org/repo1",
		},
		{
			name:         "returns bad request for invalid UUID",
			idParam:      "not-a-uuid",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewRepositoryStore(mock)
			handler := NewRepositoryHandler(store)

			tt.setupMock(mock, orgID)

			idParam := tt.idParam
			if idParam == "" {
				idParam = uuid.New().String()
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+idParam, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Get(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestRepositoryHandler_Update(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, repoID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name:         "rejects repository status updates",
			body:         `{"status":"paused"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, repoID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "REPOSITORY_STATUS_IMMUTABLE",
		},
		{
			name: "updates repository settings successfully",
			body: `{"settings":{"pm":{"pm_schedule_hours":4}}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, repoID uuid.UUID) {
				now := time.Now()
				integrationID := uuid.New()
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
				// Update
				mock.ExpectQuery("UPDATE repositories").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "pm_schedule_hours",
		},
		{
			name:         "returns bad request for invalid JSON body",
			body:         `not json`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, repoID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			repoID := uuid.New()
			store := db.NewRepositoryStore(mock)
			handler := NewRepositoryHandler(store)

			tt.setupMock(mock, orgID, repoID)

			req := httptest.NewRequest(http.MethodPatch, "/api/v1/repositories/"+repoID.String(), strings.NewReader(tt.body))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", repoID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Update(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestRepositoryHandler_Summary(t *testing.T) {
	t.Parallel()

	repoID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	latestStatus := "running"

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expected     []models.RepoSummary
	}{
		{
			name: "returns summary successfully",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				cols := []string{
					"repository_id", "full_name", "active_session_count",
					"latest_session_status", "active_project_count",
				}
				mock.ExpectQuery("SELECT .+ FROM repositories r").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(cols).AddRow(repoID, "org/repo", 2, &latestStatus, 1),
					)
			},
			expectedCode: http.StatusOK,
			expected: []models.RepoSummary{
				{
					RepositoryID:        repoID,
					FullName:            "org/repo",
					ActiveSessionCount:  2,
					LatestSessionStatus: &latestStatus,
					ActiveProjectCount:  1,
				},
			},
		},
		{
			name: "returns empty list when no repositories",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				cols := []string{
					"repository_id", "full_name", "active_session_count",
					"latest_session_status", "active_project_count",
				}
				mock.ExpectQuery("SELECT .+ FROM repositories r").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(cols))
			},
			expectedCode: http.StatusOK,
			expected:     []models.RepoSummary{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewRepositoryStore(mock)
			handler := NewRepositoryHandler(store)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/summary", nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Summary(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.RepoSummary]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expected, resp.Data, "should return expected summaries")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestRepositoryHandler_ListBranches_NoPRService(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store) // no SetPRService call

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repoID.String()+"/branches", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListBranches(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "should return 503 when prService is nil")
	require.Contains(t, w.Body.String(), "GITHUB_NOT_CONFIGURED", "error code should indicate GitHub is not configured")
}

func TestRepositoryHandler_ListBranches_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)
	// Set a non-nil prService so we get past the nil check.
	handler.prService = &ghservice.PRService{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/not-a-uuid/branches", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListBranches(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid UUID")
	require.Contains(t, w.Body.String(), "INVALID_ID", "error code should indicate invalid ID")
}

func TestRepositoryHandler_ListBranches_RepoNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)
	handler.prService = &ghservice.PRService{}

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("no rows"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repoID.String()+"/branches", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListBranches(w, req)
	require.Equal(t, http.StatusNotFound, w.Code, "should return 404 when repo is not found")
	require.Contains(t, w.Body.String(), "NOT_FOUND", "error code should indicate not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryHandler_Disconnect_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("UPDATE repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, integrationID, int64(1001), "test-org/repo1", "main",
				false, nil, nil, "https://github.com/test-org/repo1.git", int64(12345), "disconnected",
				nil, nil, json.RawMessage(`{}`), now, now,
			),
		)

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/"+repoID.String()+"/disconnect", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Disconnect(w, req)
	require.Equal(t, http.StatusOK, w.Code, "disconnect should return 200")
	require.Contains(t, w.Body.String(), "disconnected", "response should echo new status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryHandler_Reconnect_RequiresClaimFlow(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/"+repoID.String()+"/reconnect", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Reconnect(w, req)
	require.Equal(t, http.StatusConflict, w.Code, "reconnect should force GitHub repos through claim flow")
	require.Contains(t, w.Body.String(), "GITHUB_REPO_CLAIM_REQUIRED", "response should explain how to reactivate the repo")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryHandler_Disconnect_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/not-a-uuid/disconnect", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Disconnect(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "invalid UUID should 400")
	require.Contains(t, w.Body.String(), "INVALID_ID", "error code should flag invalid ID")
}

func TestRepositoryHandler_Disconnect_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()

	mock.ExpectQuery("UPDATE repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/"+repoID.String()+"/disconnect", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Disconnect(w, req)
	require.Equal(t, http.StatusNotFound, w.Code, "missing repo should 404")
	require.Contains(t, w.Body.String(), "NOT_FOUND", "error code should flag not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryHandler_Disconnect_UpdateFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()

	mock.ExpectQuery("UPDATE repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection refused"))

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/"+repoID.String()+"/disconnect", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Disconnect(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code, "db error should 500")
	require.Contains(t, w.Body.String(), "UPDATE_FAILED", "error code should flag update failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryHandler_ListBranches_RejectsDisconnected(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, integrationID, int64(1001), "test-org/repo1", "main",
				false, nil, nil, "https://github.com/test-org/repo1.git", int64(12345), "disconnected",
				nil, nil, json.RawMessage(`{}`), now, now,
			),
		)

	store := db.NewRepositoryStore(mock)
	handler := NewRepositoryHandler(store)
	handler.prService = &ghservice.PRService{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repoID.String()+"/branches", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListBranches(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "branches on a disconnected repo should 400")
	require.Contains(t, w.Body.String(), "REPO_DISCONNECTED", "error code should flag disconnected state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryHandler_Delete_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
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
	require.Equal(t, http.StatusNoContent, w.Code, "should return 204 No Content on successful delete")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
