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
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var projectTaskHandlerColumns = []string{
	"id", "project_id", "org_id", "title", "description", "approach", "reasoning",
	"sort_order", "depends_on", "batch_number", "status", "complexity", "confidence",
	"session_id", "issue_id", "branch_name", "pr_url", "outcome_notes",
	"retry_count", "max_retries", "created_at", "updated_at", "completed_at",
}

func newProjectTaskHandlerRow(taskID, projectID, orgID uuid.UUID, status models.ProjectTaskStatus, now time.Time) []interface{} {
	return []interface{}{
		taskID, projectID, orgID, "Task title", nil, nil, nil,
		1, []uuid.UUID{}, 1, status, nil, nil,
		nil, nil, nil, nil, nil,
		0, 2, now, now, nil,
	}
}

func withProjectTaskRouteParams(ctx context.Context, projectID, taskID uuid.UUID) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID.String())
	rctx.URLParams.Add("taskId", taskID.String())
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}

func TestProjectHandler_UpdateTaskRejectsMismatchedProjectPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), nil, nil, nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	taskProjectID := uuid.New()
	taskID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTaskHandlerColumns).
				AddRow(newProjectTaskHandlerRow(taskID, taskProjectID, orgID, models.ProjectTaskStatusPending, now)...),
		)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+projectInPath.String()+"/tasks/"+taskID.String(), bytes.NewBufferString(`{}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectTaskRouteParams(req.Context(), projectInPath, taskID))
	rr := httptest.NewRecorder()

	handler.UpdateTask(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "UpdateTask should reject task IDs that do not belong to the project in the path")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectHandler_DeleteTaskRejectsMismatchedProjectPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), nil, nil, nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	taskProjectID := uuid.New()
	taskID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTaskHandlerColumns).
				AddRow(newProjectTaskHandlerRow(taskID, taskProjectID, orgID, models.ProjectTaskStatusPending, now)...),
		)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+projectInPath.String()+"/tasks/"+taskID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectTaskRouteParams(req.Context(), projectInPath, taskID))
	rr := httptest.NewRecorder()

	handler.DeleteTask(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "DeleteTask should reject task IDs that do not belong to the project in the path")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectHandler_RetryTaskRejectsMismatchedProjectPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), nil, nil, nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	taskProjectID := uuid.New()
	taskID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTaskHandlerColumns).
				AddRow(newProjectTaskHandlerRow(taskID, taskProjectID, orgID, models.ProjectTaskStatusFailed, now)...),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectInPath.String()+"/tasks/"+taskID.String()+"/retry", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectTaskRouteParams(req.Context(), projectInPath, taskID))
	rr := httptest.NewRecorder()

	handler.RetryTask(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "RetryTask should reject task IDs that do not belong to the project in the path")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
