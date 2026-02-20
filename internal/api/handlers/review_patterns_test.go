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

func TestReviewPatternHandler_UpdateEndpointsReturnNewActivePattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		reqBody   string
		setupPath func(patternID string) string
		call      func(handler *ReviewPatternHandler, w *httptest.ResponseRecorder, req *http.Request)
	}{
		{
			name:    "UpdateStatus returns inserted active version",
			reqBody: `{"status":"dismissed"}`,
			setupPath: func(patternID string) string {
				return "/api/v1/review/patterns/" + patternID + "/status"
			},
			call: func(handler *ReviewPatternHandler, w *httptest.ResponseRecorder, req *http.Request) {
				handler.UpdateStatus(w, req)
			},
		},
		{
			name:    "UpdateRule returns inserted active version",
			reqBody: `{"rule":"Always check nil before dereference"}`,
			setupPath: func(patternID string) string {
				return "/api/v1/review/patterns/" + patternID + "/rule"
			},
			call: func(handler *ReviewPatternHandler, w *httptest.ResponseRecorder, req *http.Request) {
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
			patternID := uuid.New()
			insertedID := uuid.New()
			createdAt := time.Now()
			sourceCommentID := uuid.New()

			mock.ExpectBegin()
			mock.ExpectQuery("UPDATE review_patterns SET active = false WHERE id .+ AND org_id .+ AND active = true RETURNING").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows([]string{
						"org_id", "repo", "rule", "category", "source_comment_ids",
						"occurrence_count", "status", "manually_curated",
					}).AddRow(orgID, "org/repo", "Original rule", "nit", []uuid.UUID{sourceCommentID}, 1, "candidate", false),
				)
			mock.ExpectQuery("INSERT INTO review_patterns").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows([]string{
						"id", "org_id", "repo", "rule", "category", "source_comment_ids",
						"occurrence_count", "status", "manually_curated", "active", "created_at",
					}).AddRow(
						insertedID, orgID, "org/repo", "Always check nil before dereference", "nit", []uuid.UUID{sourceCommentID},
						1, "dismissed", true, true, createdAt,
					),
				)
			mock.ExpectCommit()

			patternStore := db.NewReviewPatternStore(mock)
			commentStore := db.NewReviewCommentStore(mock)
			handler := NewReviewPatternHandler(patternStore, commentStore)

			path := tt.setupPath(patternID.String())
			req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(tt.reqBody))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", patternID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			tt.call(handler, w, req)
			require.Equal(t, http.StatusOK, w.Code, "update endpoint should return status 200")

			var resp models.SingleResponse[models.ReviewPattern]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "update endpoint should return a single review pattern response body")
			require.Equal(t, insertedID, resp.Data.ID, "update endpoint should return the newly inserted pattern ID")
			require.Equal(t, orgID, resp.Data.OrgID, "update endpoint should return the same org ID")
			require.True(t, resp.Data.Active, "update endpoint should return the new active pattern version")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
