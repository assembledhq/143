package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var projectSpecHandlerColumns = []string{
	"id", "project_id", "org_id", "title", "content", "spec_type", "sort_order", "version", "created_by", "created_at", "updated_at",
}

func withProjectSpecRouteParams(ctx context.Context, projectID, specID uuid.UUID) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID.String())
	rctx.URLParams.Add("specId", specID.String())
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}

func TestProjectSpecHandler_GetRejectsMismatchedProjectPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectSpecHandler(db.NewProjectSpecStore(mock), nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	specProjectID := uuid.New()
	specID := uuid.New()
	createdBy := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectSpecHandlerColumns).AddRow(
				specID, specProjectID, orgID, "Spec", "Content", "prd", 1, 1, &createdBy, now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectInPath.String()+"/specs/"+specID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectSpecRouteParams(req.Context(), projectInPath, specID))
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "Get should reject spec IDs that do not belong to the project in the path")
	require.Contains(t, rr.Body.String(), "NOT_FOUND", "Get should return a not found error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectSpecHandler_UpdateRejectsMismatchedProjectPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectSpecHandler(db.NewProjectSpecStore(mock), nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	specProjectID := uuid.New()
	specID := uuid.New()
	createdBy := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectSpecHandlerColumns).AddRow(
				specID, specProjectID, orgID, "Spec", "Content", "prd", 1, 1, &createdBy, now, now,
			),
		)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+projectInPath.String()+"/specs/"+specID.String(), bytes.NewBufferString(`{"title":"new title"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectSpecRouteParams(req.Context(), projectInPath, specID))
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "Update should reject spec IDs that do not belong to the project in the path")
	require.Contains(t, rr.Body.String(), "NOT_FOUND", "Update should return a not found error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectSpecHandler_DeleteRejectsMismatchedProjectPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectSpecHandler(db.NewProjectSpecStore(mock), nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	specProjectID := uuid.New()
	specID := uuid.New()
	createdBy := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectSpecHandlerColumns).AddRow(
				specID, specProjectID, orgID, "Spec", "Content", "prd", 1, 1, &createdBy, now, now,
			),
		)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+projectInPath.String()+"/specs/"+specID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectSpecRouteParams(req.Context(), projectInPath, specID))
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "Delete should reject spec IDs that do not belong to the project in the path")
	require.Contains(t, rr.Body.String(), "NOT_FOUND", "Delete should return a not found error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
