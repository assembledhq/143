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

// -- column helpers --

func projectColumns() []string {
	return []string{
		"id", "org_id", "repository_id", "title", "goal", "scope", "completion_criteria",
		"status", "priority", "execution_mode", "max_concurrent", "auto_merge", "base_branch",
		"current_phase", "lessons_learned", "approach_history",
		"total_tasks", "completed_tasks", "failed_tasks",
		"proposed_by_pm", "source_issue_ids", "proposal_reasoning",
		"created_by", "created_at", "updated_at", "completed_at",
	}
}

func projectCycleColumns() []string {
	return []string{
		"id", "project_id", "org_id", "pm_plan_id", "cycle_number", "analysis", "decisions", "progress_pct",
		"tasks_completed_this_cycle", "tasks_failed_this_cycle", "tasks_created_this_cycle", "created_at",
	}
}

func newProjectRow(id, orgID, repoID uuid.UUID, status models.ProjectStatus, now time.Time) []interface{} {
	createdBy := uuid.New()
	return []interface{}{
		id, orgID, repoID, "Test Project", "Test Goal", nil, nil,
		status, 50, models.ProjectExecModeSequential, 1, false, "main",
		nil, []byte("[]"), []byte("[]"),
		0, 0, 0,
		false, []uuid.UUID{}, nil,
		&createdBy, now, now, nil,
	}
}

func withProjectRouteParam(ctx context.Context, projectID uuid.UUID) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID.String())
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}

func withCycleRouteParam(ctx context.Context, projectID, cycleID uuid.UUID) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID.String())
	rctx.URLParams.Add("cycleId", cycleID.String())
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
		{models.ProjectStatusProposed, models.ProjectStatusDraft, true},
		{models.ProjectStatusProposed, models.ProjectStatusCancelled, true},
		{models.ProjectStatusProposed, models.ProjectStatusActive, false},
		{models.ProjectStatusDraft, models.ProjectStatusPlanning, true},
		{models.ProjectStatusDraft, models.ProjectStatusActive, true},
		{models.ProjectStatusDraft, models.ProjectStatusCancelled, true},
		{models.ProjectStatusDraft, models.ProjectStatusCompleted, false},
		{models.ProjectStatusPlanning, models.ProjectStatusActive, true},
		{models.ProjectStatusPlanning, models.ProjectStatusCancelled, true},
		{models.ProjectStatusPlanning, models.ProjectStatusPaused, false},
		{models.ProjectStatusActive, models.ProjectStatusPaused, true},
		{models.ProjectStatusActive, models.ProjectStatusCompleted, true},
		{models.ProjectStatusActive, models.ProjectStatusCancelled, true},
		{models.ProjectStatusActive, models.ProjectStatusDraft, false},
		{models.ProjectStatusPaused, models.ProjectStatusActive, true},
		{models.ProjectStatusPaused, models.ProjectStatusCancelled, true},
		{models.ProjectStatusPaused, models.ProjectStatusCompleted, false},
		{models.ProjectStatusCompleted, models.ProjectStatusActive, false},
		{models.ProjectStatusCancelled, models.ProjectStatusActive, false},
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

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil)
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

// --- Get handler tests ---

func TestProjectHandler_Get(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), db.NewProjectCycleStore(mock))
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...))

	// ListByProject tasks
	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectTaskHandlerColumns))

	// ListByProject cycles
	mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectCycleColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Test Project")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectHandler_Get_InvalidID(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil)

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

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil)
	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("INSERT INTO projects").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))

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

	handler := NewProjectHandler(nil, nil, nil)
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

	handler := NewProjectHandler(nil, nil, nil)

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

	handler := NewProjectHandler(nil, nil, nil)

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

// --- Delete handler tests ---

func TestProjectHandler_Delete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil)
	orgID := uuid.New()
	projectID := uuid.New()

	mock.ExpectExec("UPDATE projects SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+projectID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- TransitionStatus / Start / Pause / Resume tests ---

func TestProjectHandler_Start(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil)
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

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil)
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

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), nil)
	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// Verify project exists
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectColumns()).AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusActive, now)...))

	// GetMaxBatchNumber
	mock.ExpectQuery("SELECT COALESCE.+FROM project_tasks").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(1))

	// Create task
	mock.ExpectQuery("INSERT INTO project_tasks").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))

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

	handler := NewProjectHandler(db.NewProjectStore(mock), nil, nil)
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

	handler := NewProjectHandler(db.NewProjectStore(mock), db.NewProjectTaskStore(mock), nil)
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

func TestProjectHandler_ListCycles(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectHandler(nil, nil, db.NewProjectCycleStore(mock))
	orgID := uuid.New()
	projectID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectCycleColumns()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/cycles", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.ListCycles(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"data":[]`)
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- GetCycle handler tests ---

func TestProjectHandler_GetCycle_InvalidID(t *testing.T) {
	t.Parallel()

	handler := NewProjectHandler(nil, nil, nil)

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
