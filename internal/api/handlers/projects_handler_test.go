package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// -- column helpers --

func projectColumns() []string {
	return []string{
		"id", "org_id", "repository_id", "title", "goal", "scope", "completion_criteria",
		"status", "priority", "execution_mode", "max_concurrent", "auto_merge", "base_branch",
		"current_phase", "lessons_learned", "approach_history",
		"total_tasks", "completed_tasks", "failed_tasks",
		"proposed_by_pm", "source_issue_ids", "proposal_reasoning", "similar_projects",
		"agent_type", "model_override",
		"created_by", "deleted_at", "created_at", "updated_at", "completed_at", "archived_at",
	}
}

func newProjectRow(id, orgID, repoID uuid.UUID, status models.ProjectStatus, now time.Time) []interface{} {
	createdBy := uuid.New()
	return []interface{}{
		id, orgID, &repoID, "Test Project", "Test Goal", nil, nil,
		status, 50, models.ProjectExecModeSequential, 1, false, "main",
		nil, []byte("[]"), []byte("[]"),
		0, 0, 0,
		false, []uuid.UUID{}, nil, json.RawMessage("[]"),
		nil, nil, // agent_type, model_override
		&createdBy, (*time.Time)(nil), now, now, nil, nil,
	}
}

func withProjectRouteParam(ctx context.Context, projectID uuid.UUID) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID.String())
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}

// --- validStatusTransition tests ---

func TestValidStatusTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		from  models.ProjectStatus
		to    models.ProjectStatus
		valid bool
	}{
		{models.ProjectStatusDraft, models.ProjectStatusActive, true},
		{models.ProjectStatusDraft, models.ProjectStatusCompleted, true},
		{models.ProjectStatusActive, models.ProjectStatusCompleted, true},
		{models.ProjectStatusActive, models.ProjectStatusDraft, false},
		{models.ProjectStatusCompleted, models.ProjectStatusActive, false},
		{models.ProjectStatusCompleted, models.ProjectStatusDraft, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"->"+string(tt.to), func(t *testing.T) {
			t.Parallel()
			got := validStatusTransition(tt.from, tt.to)
			require.Equal(t, tt.valid, got)
		})
	}
}

// --- List handler tests ---

func TestProjectHandler_List(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"data":[]`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_List_WithRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	repoID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ AND repository_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?repository_id="+repoID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"data":[]`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_List_InvalidRepositoryID(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)
	orgID := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?repository_id=not-a-uuid", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_REPOSITORY_ID")
}

func TestProjectHandler_List_WithCreatedBy(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ AND created_by").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?created_by="+userID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "List should accept created_by filter")
	require.Contains(t, rr.Body.String(), `"data":[]`, "List should return an empty list response")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectHandler_List_WithCreatedByIDs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	userID1 := uuid.New()
	userID2 := uuid.New()

	mock.ExpectQuery(`SELECT .+ FROM projects WHERE org_id .+ AND created_by = ANY`).
		WithArgs(pgxmock.AnyArg(), []uuid.UUID{userID1, userID2}).
		WillReturnRows(pgxmock.NewRows(projectColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?created_by_ids="+userID1.String()+","+userID2.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "List should accept created_by_ids filter")
	require.Contains(t, rr.Body.String(), `"data":[]`, "List should return an empty list response")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectHandler_List_InvalidCreatedBy(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)
	orgID := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?created_by=not-a-uuid", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "List should reject an invalid created_by filter")
	require.Contains(t, rr.Body.String(), "INVALID_USER_ID", "List should surface the created_by validation error")
}

func TestProjectHandler_List_InvalidCreatedByIDs(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)
	orgID := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?created_by_ids=not-a-uuid", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "List should reject an invalid created_by_ids filter")
	require.Contains(t, rr.Body.String(), "INVALID_USER_ID", "List should surface the created_by_ids validation error")
}

func TestProjectHandler_List_BlankCreatedByIDs(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)
	orgID := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?created_by_ids=,,,", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "List should reject blank created_by_ids filter")
	require.Contains(t, rr.Body.String(), "INVALID_USER_ID", "List should surface the blank created_by_ids validation error")
}

func TestProjectHandler_List_EmptyCreatedByIDs(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)
	orgID := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?created_by_ids=", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "List should reject empty created_by_ids filter")
	require.Contains(t, rr.Body.String(), "INVALID_USER_ID", "List should surface the empty created_by_ids validation error")
}

func TestProjectHandler_List_WhitespaceCreatedByIDs(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)
	orgID := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?created_by_ids=%20", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "List should reject whitespace created_by_ids filter")
	require.Contains(t, rr.Body.String(), "INVALID_USER_ID", "List should surface the whitespace created_by_ids validation error")
}

func TestProjectHandler_List_WithOnlyArchived(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ archived_at IS NOT NULL").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?only_archived=true", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "List should accept only_archived")
	require.Contains(t, rr.Body.String(), `"data":[]`, "List should return an empty list response")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectHandler_Archive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusActive, now)...))
	mock.ExpectExec("UPDATE projects SET archived_at = now\\(\\), updated_at = now\\(\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/archive", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Archive(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Archive should succeed")
	require.Contains(t, rr.Body.String(), `"status":"archived"`, "Archive should return archived status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectHandler_Unarchive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	row := newProjectRow(projectID, orgID, repoID, models.ProjectStatusCompleted, now)
	row[len(row)-1] = &now
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(row...))
	mock.ExpectExec("UPDATE projects SET archived_at = NULL, updated_at = now\\(\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/unarchive", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Unarchive(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Unarchive should succeed")
	require.Contains(t, rr.Body.String(), `"status":"completed"`, "Unarchive should preserve the project's lifecycle status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// --- Get handler tests ---

func TestProjectHandler_Get_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()

	// GetByID returns no rows
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "NOT_FOUND")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- Update handler tests ---

func TestProjectHandler_Update(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID returns a draft project
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...))

	// Update (20 named args)
	mock.ExpectExec("UPDATE projects SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	body, _ := json.Marshal(map[string]string{"title": "Updated Title"})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+projectID.String(), bytes.NewBuffer(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Updated Title")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_Update_InvalidTransition(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID returns a completed project
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusCompleted, now)...))

	// Try to transition from completed -> draft (invalid)
	body, _ := json.Marshal(map[string]string{"status": "draft"})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+projectID.String(), bytes.NewBuffer(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_TRANSITION")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- UpdateTask success handler tests ---

func TestProjectHandler_UpdateTask_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	taskID := uuid.New()
	now := time.Now()

	// GetByID returns task matching project
	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTaskHandlerColumns).
				AddRow(newProjectTaskHandlerRow(taskID, projectID, orgID, models.ProjectTaskStatusPending, now)...),
		)

	// GetByID for parent project (seed field guard: title is a seed field)
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, uuid.New(), models.ProjectStatusDraft, now)...))

	// Update task (wrapped in transaction)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE project_tasks SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("DELETE FROM project_task_dependencies WHERE task_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectCommit()

	// UpdateProgress
	mock.ExpectExec("UPDATE projects SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	body, _ := json.Marshal(map[string]string{"title": "Updated Task Title"})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+projectID.String()+"/tasks/"+taskID.String(), bytes.NewBuffer(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectTaskRouteParams(req.Context(), projectID, taskID))
	rr := httptest.NewRecorder()

	handler.UpdateTask(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Updated Task Title")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- DeleteTask success handler tests ---

func TestProjectHandler_DeleteTask_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	taskID := uuid.New()
	now := time.Now()

	// GetByID returns task matching project
	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTaskHandlerColumns).
				AddRow(newProjectTaskHandlerRow(taskID, projectID, orgID, models.ProjectTaskStatusPending, now)...),
		)

	// Delete task
	mock.ExpectExec("DELETE FROM project_tasks WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	// UpdateProgress
	mock.ExpectExec("UPDATE projects SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+projectID.String()+"/tasks/"+taskID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectTaskRouteParams(req.Context(), projectID, taskID))
	rr := httptest.NewRecorder()

	handler.DeleteTask(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_Get_InvalidID(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/not-a-uuid", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_ID")
}

// --- Create handler tests ---

func TestProjectHandler_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	handler.SetRepositoryStore(db.NewRepositoryStore(mock))
	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, integrationID, int64(1001), "test-org/repo1", "main",
				false, nil, nil, "https://github.com/test-org/repo1.git", int64(12345), "active",
				nil, nil, json.RawMessage(`{}`), now, now,
			),
		)
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO projects").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectCommit()

	body, _ := json.Marshal(map[string]string{
		"title":         "New Project",
		"goal":          "Build something",
		"repository_id": repoID.String(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_Create_MissingFields(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)
	orgID := uuid.New()

	body, _ := json.Marshal(map[string]string{"title": "No goal"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New()})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "MISSING_FIELD")
}

func TestProjectHandler_Create_InvalidJSON(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString("{bad json"))
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New()})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_JSON")
}

func TestProjectHandler_Create_InvalidRepoID(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)

	body, _ := json.Marshal(map[string]string{
		"title":         "Test",
		"goal":          "Build",
		"repository_id": "not-a-uuid",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New()})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_REPOSITORY_ID")
}

func TestProjectHandler_Create_RejectsDisconnectedRepo(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	handler.SetRepositoryStore(db.NewRepositoryStore(mock))
	orgID := uuid.New()
	userID := uuid.New()
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

	body, _ := json.Marshal(map[string]string{
		"title":         "New Project",
		"goal":          "Build something",
		"repository_id": repoID.String(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "REPO_DISCONNECTED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_Create_RepoNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	handler.SetRepositoryStore(db.NewRepositoryStore(mock))

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	body, _ := json.Marshal(map[string]string{
		"title":         "New Project",
		"goal":          "Build something",
		"repository_id": uuid.New().String(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New()})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_REPOSITORY_ID")
}

func TestProjectHandler_Create_RepoStoreUnconfigured(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)

	body, _ := json.Marshal(map[string]string{
		"title":         "New Project",
		"goal":          "Build something",
		"repository_id": uuid.New().String(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New()})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Contains(t, rr.Body.String(), "REPO_STORE_UNCONFIGURED")
}

// --- Delete handler tests ---

func TestProjectHandler_Delete(t *testing.T) {
	t.Parallel()

	t.Run("returns no content when project exists", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
		orgID := uuid.New()
		projectID := uuid.New()
		repoID := uuid.New()
		now := time.Now()

		mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusActive, now)...))

		mock.ExpectExec("UPDATE projects SET status").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+projectID.String(), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
		rr := httptest.NewRecorder()

		handler.Delete(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code, "delete should return no content when project exists")
		require.NoError(t, mock.ExpectationsWereMet(), "delete should satisfy all database expectations")
	})

	t.Run("returns not found when project does not exist", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
		orgID := uuid.New()
		projectID := uuid.New()

		mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(pgx.ErrNoRows)

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+projectID.String(), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
		rr := httptest.NewRecorder()

		handler.Delete(rr, req)

		require.Equal(t, http.StatusNotFound, rr.Code, "delete should return not found when project is missing")
		require.Contains(t, rr.Body.String(), "NOT_FOUND", "delete should return the not found error code")
		require.NoError(t, mock.ExpectationsWereMet(), "delete should satisfy all database expectations")
	})
}

// --- TransitionStatus / Start / Pause / Resume tests ---

func TestProjectHandler_Start(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID returns a draft project
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...))

	// UpdateStatus
	mock.ExpectExec("UPDATE projects SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/start", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Start(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"status":"active"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_Start_InvalidTransition(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID returns a completed project (can't start)
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusCompleted, now)...))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/start", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Start(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_TRANSITION")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- CreateTask handler tests ---

func TestProjectHandler_CreateTask(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// Verify project exists
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusActive, now)...))

	// GetMaxBatchNumber
	mock.ExpectQuery("SELECT max\\(batch_number\\) FROM project_tasks").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(1))

	// Create task (wrapped in transaction)
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO project_tasks").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectExec("DELETE FROM project_task_dependencies WHERE task_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectCommit()

	// UpdateProgress
	mock.ExpectExec("UPDATE projects SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	body, _ := json.Marshal(map[string]string{"title": "New Task"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/tasks", bytes.NewBuffer(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.CreateTask(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	require.Contains(t, rr.Body.String(), "New Task")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_CreateTask_MissingTitle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusActive, now)...))

	body, _ := json.Marshal(map[string]string{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/tasks", bytes.NewBuffer(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.CreateTask(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "MISSING_FIELD")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- RetryTask handler tests ---

func TestProjectHandler_RetryTask_OnlyFailedTasksCanRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), nil, nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()
	taskID := uuid.New()
	now := time.Now()

	// Return a pending task (not failed)
	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTaskHandlerColumns).
				AddRow(newProjectTaskHandlerRow(taskID, projectID, orgID, models.ProjectTaskStatusPending, now)...),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/tasks/"+taskID.String()+"/retry", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectTaskRouteParams(req.Context(), projectID, taskID))
	rr := httptest.NewRecorder()

	handler.RetryTask(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_STATUS")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- ListCycles handler tests ---

func TestProjectHandler_ListCycles_InvalidID(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/not-a-uuid/cycles", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.ListCycles(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_ID")
}

func TestProjectHandler_ListCycles_DBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(nil, nil, db.NewProjectCycleStore(mock), nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("db error"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/cycles", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.ListCycles(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Contains(t, rr.Body.String(), "LIST_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- GetCycle handler tests ---

func TestProjectHandler_GetCycle_InvalidID(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.New().String())
	rctx.URLParams.Add("cycleId", "not-a-uuid")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/abc/cycles/not-a-uuid", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.GetCycle(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_ID")
}

// --- RunNow handler tests ---

func TestProjectHandler_RunNow_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	handler.SetJobStore(db.NewJobStore(mock))
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID returns an active project
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusActive, now)...))

	// Enqueue job
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/run", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.RunNow(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "job_id")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_RunNow_NotActive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil, nil, nil)
	handler.SetJobStore(db.NewJobStore(mock))
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID returns a draft project (not active)
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/run", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.RunNow(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_STATUS")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_RunNow_NoJobStore(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)
	// Do NOT call SetJobStore — leave it nil.

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+uuid.New().String()+"/run", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	req = req.WithContext(withProjectRouteParam(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.RunNow(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	require.Contains(t, rr.Body.String(), "NOT_CONFIGURED")
}

func TestProjectHandler_RunNow_InvalidID(t *testing.T) {
	t.Parallel()

	invalidMock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer invalidMock.Close()

	handler := NewProjectHandler(nil, nil, nil, nil, nil)
	handler.SetJobStore(db.NewJobStore(invalidMock))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/not-a-uuid/run", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.RunNow(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_ID")
}
