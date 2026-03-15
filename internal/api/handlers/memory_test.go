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
	"github.com/stretchr/testify/require"
)

var memoryColumns = []string{
	"id", "org_id", "repo", "rule", "category", "source_comment_ids",
	"occurrence_count", "status", "manually_curated", "active",
	"scope", "source", "last_used_at", "times_reinforced", "file_patterns", "created_at",
}

func TestMemoryHandler_ListByRepo_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	memoryID := uuid.New()
	createdAt := time.Now()
	sourceCommentID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memoryColumns).AddRow(
				memoryID, orgID, "org/repo", "Check nil pointers", "bug_risk", []uuid.UUID{sourceCommentID},
				3, "active", false, true,
				"repo", "review", &createdAt, 3, []string(nil), createdAt,
			),
		)

	memoryStore := db.NewMemoryStore(mock)
	commentStore := db.NewReviewCommentStore(mock)
	handler := NewMemoryHandler(memoryStore, commentStore)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/org/repo", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("*", "org/repo")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListByRepo(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200")

	var resp models.ListResponse[models.Memory]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return one memory")
	require.Equal(t, "Check nil pointers", resp.Data[0].Rule)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemoryHandler_ListByRepo_MissingRepo(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	memoryStore := db.NewMemoryStore(mock)
	commentStore := db.NewReviewCommentStore(mock)
	handler := NewMemoryHandler(memoryStore, commentStore)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("*", "")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListByRepo(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for missing repo")
	require.Contains(t, w.Body.String(), "MISSING_REPO")
}

func TestMemoryHandler_ListComments_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	commentID := uuid.New()
	prID := uuid.New()
	createdAt := time.Now()

	mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "pull_request_id", "org_id", "github_comment_id", "reviewer", "body",
				"diff_path", "diff_position", "filter_status", "category", "actionable",
				"generalizable", "generalized_rule", "summary", "applied", "created_at",
			}).AddRow(
				commentID, prID, orgID, int64(123), "user1", "Fix this",
				nil, nil, "accepted", nil, true,
				false, nil, nil, false, createdAt,
			),
		)

	memoryStore := db.NewMemoryStore(mock)
	commentStore := db.NewReviewCommentStore(mock)
	handler := NewMemoryHandler(memoryStore, commentStore)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/review/comments", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListComments(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200")

	var resp models.ListResponse[models.ReviewComment]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return one comment")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemoryHandler_UpdateStatus_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	memoryStore := db.NewMemoryStore(mock)
	commentStore := db.NewReviewCommentStore(mock)
	handler := NewMemoryHandler(memoryStore, commentStore)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/memories/bad-id/status", strings.NewReader(`{"status":"active"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.UpdateStatus(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestMemoryHandler_UpdateStatus_InvalidStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	memoryID := uuid.New()
	memoryStore := db.NewMemoryStore(mock)
	commentStore := db.NewReviewCommentStore(mock)
	handler := NewMemoryHandler(memoryStore, commentStore)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/memories/"+memoryID.String()+"/status", strings.NewReader(`{"status":"invalid"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", memoryID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.UpdateStatus(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid status")
	require.Contains(t, w.Body.String(), "INVALID_STATUS")
}

func TestMemoryHandler_UpdateRule_MissingRule(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	memoryID := uuid.New()
	memoryStore := db.NewMemoryStore(mock)
	commentStore := db.NewReviewCommentStore(mock)
	handler := NewMemoryHandler(memoryStore, commentStore)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/memories/"+memoryID.String()+"/rule", strings.NewReader(`{"rule":""}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", memoryID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.UpdateRule(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for missing rule")
	require.Contains(t, w.Body.String(), "MISSING_RULE")
}

func TestMemoryHandler_UpdateEndpointsReturnNewActiveMemory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		reqBody   string
		setupPath func(memoryID string) string
		call      func(handler *MemoryHandler, w *httptest.ResponseRecorder, req *http.Request)
	}{
		{
			name:    "UpdateStatus returns inserted active version",
			reqBody: `{"status":"dismissed"}`,
			setupPath: func(memoryID string) string {
				return "/api/v1/memories/" + memoryID + "/status"
			},
			call: func(handler *MemoryHandler, w *httptest.ResponseRecorder, req *http.Request) {
				handler.UpdateStatus(w, req)
			},
		},
		{
			name:    "UpdateRule returns inserted active version",
			reqBody: `{"rule":"Always check nil before dereference"}`,
			setupPath: func(memoryID string) string {
				return "/api/v1/memories/" + memoryID + "/rule"
			},
			call: func(handler *MemoryHandler, w *httptest.ResponseRecorder, req *http.Request) {
				handler.UpdateRule(w, req)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			memoryID := uuid.New()
			insertedID := uuid.New()
			createdAt := time.Now()
			sourceCommentID := uuid.New()

			mock.ExpectBegin()
			mock.ExpectQuery("UPDATE memories SET active = false WHERE id .+ AND org_id .+ AND active = true RETURNING").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows([]string{
						"org_id", "repo", "rule", "category", "source_comment_ids",
						"occurrence_count", "status", "manually_curated",
						"scope", "source", "last_used_at", "times_reinforced", "file_patterns",
					}).AddRow(orgID, "org/repo", "Original rule", "nit", []uuid.UUID{sourceCommentID}, 1, "candidate", false,
						"repo", "review", &createdAt, 0, []string(nil)),
				)
			mock.ExpectQuery("INSERT INTO memories").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(memoryColumns).AddRow(
						insertedID, orgID, "org/repo", "Always check nil before dereference", "nit", []uuid.UUID{sourceCommentID},
						1, "dismissed", true, true,
						"repo", "review", &createdAt, 0, []string(nil), createdAt,
					),
				)
			mock.ExpectCommit()

			memoryStore := db.NewMemoryStore(mock)
			commentStore := db.NewReviewCommentStore(mock)
			handler := NewMemoryHandler(memoryStore, commentStore)

			path := tt.setupPath(memoryID.String())
			req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(tt.reqBody))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", memoryID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			tt.call(handler, w, req)
			require.Equal(t, http.StatusOK, w.Code, "update endpoint should return status 200")

			var resp models.SingleResponse[models.Memory]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "update endpoint should return a single memory response body")
			require.Equal(t, insertedID, resp.Data.ID, "update endpoint should return the newly inserted memory ID")
			require.Equal(t, orgID, resp.Data.OrgID, "update endpoint should return the same org ID")
			require.True(t, resp.Data.Active, "update endpoint should return the new active memory version")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
