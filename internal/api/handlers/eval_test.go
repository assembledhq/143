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
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// anyArgs creates a slice of pgxmock.AnyArg() matchers for use with WithArgs.
func anyArgs(n int) []interface{} {
	args := make([]interface{}, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

var evalTaskColumns = []string{
	"id", "org_id", "repo_id", "name", "description",
	"base_commit_sha", "solution_commit_sha", "solution_diff",
	"issue_description", "issue_context",
	"server_deploy_sha", "pm_document_set_pin_id", "org_settings_version_id",
	"memory_snapshot", "sandbox_image_digest", "context_overrides",
	"scoring_criteria", "pass_threshold",
	"source", "source_pr_number", "complexity", "tags",
	"created_by", "created_at", "updated_at", "archived_at",
}

var evalRunColumns = []string{
	"id", "task_id", "org_id", "batch_id",
	"input_manifest", "model", "server_deploy_sha", "pm_document_set_pin_id",
	"config_ref", "context_overrides",
	"agent_diff", "agent_trace", "token_usage",
	"criterion_results", "final_score", "passed",
	"status", "duration_seconds", "sandbox_id",
	"started_at", "completed_at", "error_message", "created_at",
}

var evalBatchColumns = []string{
	"id", "org_id", "name", "status", "task_count", "run_count", "created_by", "created_at", "completed_at",
}

func newTestEvalTaskRow(taskID, orgID, repoID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		taskID, orgID, repoID, "Test Task", "description",
		"abc123", nil, nil,
		"Fix the bug", json.RawMessage(`{}`),
		nil, nil, nil,
		nil, nil, json.RawMessage(`{}`),
		json.RawMessage(`[]`), 0.7,
		"manual", nil, "moderate", []string{"test"},
		nil, now, now, nil,
	}
}

func newTestEvalRunRow(runID, taskID, orgID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		runID, taskID, orgID, nil,
		nil, "claude-sonnet-4-6", nil, nil,
		nil, json.RawMessage(`{}`),
		nil, nil, nil,
		nil, nil, nil,
		"pending", nil, nil,
		nil, nil, nil, now,
	}
}

func newTestEvalBatchRow(batchID, orgID uuid.UUID, userID *uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		batchID, orgID, "Test Batch", "running", 1, 1, userID, now, nil,
	}
}

func newEvalHandler(mock pgxmock.PgxPoolIface) *EvalHandler {
	return NewEvalHandler(
		db.NewEvalTaskStore(mock),
		db.NewEvalRunStore(mock),
		db.NewEvalBatchStore(mock),
		db.NewEvalBootstrapStore(mock),
		db.NewJobStore(mock),
		mock,
	)
}

func evalCtx(orgID uuid.UUID, userID uuid.UUID) context.Context {
	ctx := context.Background()
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	return ctx
}

func evalCtxWithChi(orgID uuid.UUID, userID uuid.UUID, params map[string]string) context.Context {
	ctx := evalCtx(orgID, userID)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}

// --- ListTasks ---

func TestEvalHandler_ListTasks(t *testing.T) {
	t.Parallel()

	t.Run("returns tasks successfully", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		repoID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE org_id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(
				pgxmock.NewRows(evalTaskColumns).
					AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...),
			)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks", nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.ListTasks(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.EvalTask]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Len(t, resp.Data, 1)
		require.Equal(t, taskID, resp.Data[0].ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns empty list", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE org_id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks", nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.ListTasks(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.EvalTask]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Len(t, resp.Data, 0)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on db failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE org_id").
			WithArgs(anyArgs(2)...).
			WillReturnError(fmt.Errorf("db connection lost"))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks", nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.ListTasks(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "LIST_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- CreateTask ---

func TestEvalHandler_CreateTask(t *testing.T) {
	t.Parallel()

	t.Run("creates task successfully", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		taskID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("INSERT INTO eval_tasks").
			WithArgs(anyArgs(22)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		body := fmt.Sprintf(`{
			"name": "Test Task",
			"base_commit_sha": "abc123",
			"issue_description": "Fix the bug",
			"repo_id": %q
		}`, repoID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		var resp models.SingleResponse[models.EvalTask]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, taskID, resp.Data.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for missing name", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		body := fmt.Sprintf(`{
			"base_commit_sha": "abc123",
			"issue_description": "Fix the bug",
			"repo_id": %q
		}`, repoID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for missing base_commit_sha", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		body := fmt.Sprintf(`{
			"name": "Test Task",
			"issue_description": "Fix the bug",
			"repo_id": %q
		}`, repoID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for missing issue_description", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		body := fmt.Sprintf(`{
			"name": "Test Task",
			"base_commit_sha": "abc123",
			"repo_id": %q
		}`, repoID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for missing repo_id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		body := `{
			"name": "Test Task",
			"base_commit_sha": "abc123",
			"issue_description": "Fix the bug"
		}`

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for invalid JSON body", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(`{invalid json`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_JSON")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for invalid scoring_criteria", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		body := fmt.Sprintf(`{
			"name": "Test Task",
			"base_commit_sha": "abc123",
			"issue_description": "Fix the bug",
			"repo_id": %q,
			"scoring_criteria": [{"name":"c1","grader_type":"bad_grader","weight":1.0}]
		}`, repoID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_GRADER_TYPE")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for invalid source", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		body := fmt.Sprintf(`{
			"name": "Test Task",
			"base_commit_sha": "abc123",
			"issue_description": "Fix the bug",
			"repo_id": %q,
			"source": "invalid_source"
		}`, repoID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_SOURCE")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for invalid complexity", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		body := fmt.Sprintf(`{
			"name": "Test Task",
			"base_commit_sha": "abc123",
			"issue_description": "Fix the bug",
			"repo_id": %q,
			"complexity": "impossible"
		}`, repoID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_COMPLEXITY")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on db insert failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("INSERT INTO eval_tasks").
			WithArgs(anyArgs(22)...).
			WillReturnError(fmt.Errorf("unique violation"))

		body := fmt.Sprintf(`{
			"name": "Test Task",
			"base_commit_sha": "abc123",
			"issue_description": "Fix the bug",
			"repo_id": %q
		}`, repoID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "CREATE_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- GetTask ---

func TestEvalHandler_GetTask(t *testing.T) {
	t.Parallel()

	t.Run("returns task by ID", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		repoID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks/"+taskID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.GetTask(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.SingleResponse[models.EvalTask]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, taskID, resp.Data.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns not found for missing task", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks/"+taskID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.GetTask(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Body.String(), "NOT_FOUND")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid ID", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks/not-a-uuid", nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": "not-a-uuid"}))
		w := httptest.NewRecorder()

		handler.GetTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns internal error on db failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnError(fmt.Errorf("connection reset"))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks/"+taskID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.GetTask(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "INTERNAL_ERROR")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- UpdateTask ---

func TestEvalHandler_UpdateTask(t *testing.T) {
	t.Parallel()

	t.Run("updates task successfully", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		repoID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		// GetByID
		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		// Update
		updatedRow := newTestEvalTaskRow(taskID, orgID, repoID, now)
		updatedRow[3] = "Updated Name" // name field
		mock.ExpectQuery("UPDATE eval_tasks SET").
			WithArgs(anyArgs(11)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(updatedRow...))

		body := `{"name": "Updated Name"}`
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/eval/tasks/"+taskID.String(), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.UpdateTask(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.SingleResponse[models.EvalTask]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, "Updated Name", resp.Data.Name)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns not found when task does not exist", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns))

		body := `{"name": "Updated Name"}`
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/eval/tasks/"+taskID.String(), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.UpdateTask(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Body.String(), "NOT_FOUND")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid ID", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		body := `{"name": "Updated"}`
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/eval/tasks/bad-id", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": "bad-id"}))
		w := httptest.NewRecorder()

		handler.UpdateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid JSON", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		repoID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		// GetByID succeeds
		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/eval/tasks/"+taskID.String(), strings.NewReader(`{bad`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.UpdateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_JSON")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid scoring_criteria in update", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		repoID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		body := `{"scoring_criteria": [{"name":"c1","grader_type":"bad_type","weight":1.0}]}`
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/eval/tasks/"+taskID.String(), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.UpdateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_GRADER_TYPE")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- ArchiveTask ---

func TestEvalHandler_ArchiveTask(t *testing.T) {
	t.Parallel()

	t.Run("archives task successfully", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		// Archive (returns 1 row affected)
		mock.ExpectExec("UPDATE eval_tasks SET archived_at").
			WithArgs(anyArgs(2)...).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/eval/tasks/"+taskID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.ArchiveTask(w, req)
		require.Equal(t, http.StatusNoContent, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns not found when task does not exist", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		// Archive returns 0 rows affected → ErrNoRows
		mock.ExpectExec("UPDATE eval_tasks SET archived_at").
			WithArgs(anyArgs(2)...).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/eval/tasks/"+taskID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.ArchiveTask(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Body.String(), "NOT_FOUND")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid ID", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/eval/tasks/not-valid", nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": "not-valid"}))
		w := httptest.NewRecorder()

		handler.ArchiveTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on archive db failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectExec("UPDATE eval_tasks SET archived_at").
			WithArgs(anyArgs(2)...).
			WillReturnError(fmt.Errorf("db error"))

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/eval/tasks/"+taskID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.ArchiveTask(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "ARCHIVE_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- StartRun ---

func TestEvalHandler_StartRun(t *testing.T) {
	t.Parallel()

	t.Run("starts run successfully", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		repoID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		// GetByID for task validation
		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		// Transaction
		mock.ExpectBegin()

		// Create run
		mock.ExpectQuery("INSERT INTO eval_runs").
			WithArgs(anyArgs(9)...).
			WillReturnRows(pgxmock.NewRows(evalRunColumns).AddRow(newTestEvalRunRow(runID, taskID, orgID, now)...))

		// Enqueue job
		mock.ExpectQuery("INSERT INTO jobs").
			WithArgs(anyArgs(6)...).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

		mock.ExpectCommit()

		body := `{"model": "claude-sonnet-4-6"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks/"+taskID.String()+"/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.StartRun(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		var resp models.SingleResponse[models.EvalRun]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, runID, resp.Data.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns not found when task does not exist", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns))

		body := `{"model": "claude-sonnet-4-6"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks/"+taskID.String()+"/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.StartRun(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Body.String(), "NOT_FOUND")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for missing model", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		repoID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		body := `{}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks/"+taskID.String()+"/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.StartRun(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid task ID", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		body := `{"model": "claude-sonnet-4-6"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks/bad-id/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": "bad-id"}))
		w := httptest.NewRecorder()

		handler.StartRun(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on transaction begin failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		repoID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		mock.ExpectBegin().WillReturnError(fmt.Errorf("cannot begin tx"))

		body := `{"model": "claude-sonnet-4-6"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks/"+taskID.String()+"/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.StartRun(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "TX_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- ListRuns ---

func TestEvalHandler_ListRuns(t *testing.T) {
	t.Parallel()

	t.Run("returns runs for task", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_runs").
			WithArgs(anyArgs(3)...).
			WillReturnRows(
				pgxmock.NewRows(evalRunColumns).
					AddRow(newTestEvalRunRow(runID, taskID, orgID, now)...),
			)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks/"+taskID.String()+"/runs", nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.ListRuns(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.EvalRun]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Len(t, resp.Data, 1)
		require.Equal(t, runID, resp.Data[0].ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns empty list", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_runs").
			WithArgs(anyArgs(3)...).
			WillReturnRows(pgxmock.NewRows(evalRunColumns))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks/"+taskID.String()+"/runs", nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.ListRuns(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.EvalRun]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Len(t, resp.Data, 0)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid task ID", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks/bad-id/runs", nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": "bad-id"}))
		w := httptest.NewRecorder()

		handler.ListRuns(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on db failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_runs").
			WithArgs(anyArgs(3)...).
			WillReturnError(fmt.Errorf("db error"))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/tasks/"+taskID.String()+"/runs", nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.ListRuns(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "LIST_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- GetRun ---

func TestEvalHandler_GetRun(t *testing.T) {
	t.Parallel()

	t.Run("returns run by ID", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalRunColumns).AddRow(newTestEvalRunRow(runID, taskID, orgID, now)...))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/runs/"+runID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"runId": runID.String()}))
		w := httptest.NewRecorder()

		handler.GetRun(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.SingleResponse[models.EvalRun]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, runID, resp.Data.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns not found for missing run", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		runID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalRunColumns))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/runs/"+runID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"runId": runID.String()}))
		w := httptest.NewRecorder()

		handler.GetRun(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Body.String(), "NOT_FOUND")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid run ID", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/runs/not-uuid", nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"runId": "not-uuid"}))
		w := httptest.NewRecorder()

		handler.GetRun(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- GetBatch ---

func TestEvalHandler_GetBatch(t *testing.T) {
	t.Parallel()

	t.Run("returns batch with runs", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		batchID := uuid.New()
		taskID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		// GetByID for batch
		mock.ExpectQuery("SELECT .+ FROM eval_batches WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalBatchColumns).AddRow(newTestEvalBatchRow(batchID, orgID, &userID, now)...))

		// ListByBatch for runs
		mock.ExpectQuery("SELECT .+ FROM eval_runs").
			WithArgs(anyArgs(2)...).
			WillReturnRows(
				pgxmock.NewRows(evalRunColumns).
					AddRow(newTestEvalRunRow(runID, taskID, orgID, now)...),
			)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/batches/"+batchID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"batchId": batchID.String()}))
		w := httptest.NewRecorder()

		handler.GetBatch(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.SingleResponse[models.EvalBatchDetail]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, batchID, resp.Data.ID)
		require.Len(t, resp.Data.Runs, 1)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns not found for missing batch", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		batchID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_batches WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalBatchColumns))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/batches/"+batchID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"batchId": batchID.String()}))
		w := httptest.NewRecorder()

		handler.GetBatch(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Body.String(), "NOT_FOUND")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid batch ID", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/batches/bad-id", nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"batchId": "bad-id"}))
		w := httptest.NewRecorder()

		handler.GetBatch(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on list runs db failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		batchID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_batches WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalBatchColumns).AddRow(newTestEvalBatchRow(batchID, orgID, &userID, now)...))

		mock.ExpectQuery("SELECT .+ FROM eval_runs").
			WithArgs(anyArgs(2)...).
			WillReturnError(fmt.Errorf("db error"))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/eval/batches/"+batchID.String(), nil)
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"batchId": batchID.String()}))
		w := httptest.NewRecorder()

		handler.GetBatch(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "LIST_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- StartBatch ---

func TestEvalHandler_StartBatch(t *testing.T) {
	t.Parallel()

	t.Run("starts batch successfully", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		batchID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		// Validate task IDs via CountByIDs
		mock.ExpectQuery("SELECT COUNT").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

		// Transaction
		mock.ExpectBegin()

		// Create batch
		mock.ExpectQuery("INSERT INTO eval_batches").
			WithArgs(anyArgs(6)...).
			WillReturnRows(pgxmock.NewRows(evalBatchColumns).AddRow(newTestEvalBatchRow(batchID, orgID, &userID, now)...))

		// Create run (1 task x 1 config = 1 run)
		mock.ExpectQuery("INSERT INTO eval_runs").
			WithArgs(anyArgs(9)...).
			WillReturnRows(pgxmock.NewRows(evalRunColumns).AddRow(newTestEvalRunRow(runID, taskID, orgID, now)...))

		// Enqueue job
		mock.ExpectQuery("INSERT INTO jobs").
			WithArgs(anyArgs(6)...).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

		mock.ExpectCommit()

		body := fmt.Sprintf(`{
			"name": "Test Batch",
			"task_ids": [%q],
			"configs": [{"model": "claude-sonnet-4-6"}]
		}`, taskID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/batches", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.StartBatch(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		var resp models.SingleResponse[models.EvalBatch]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, batchID, resp.Data.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for missing task_ids", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		body := `{
			"name": "Test Batch",
			"task_ids": [],
			"configs": [{"model": "claude-sonnet-4-6"}]
		}`

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/batches", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.StartBatch(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for missing configs", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		body := fmt.Sprintf(`{
			"name": "Test Batch",
			"task_ids": [%q],
			"configs": []
		}`, taskID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/batches", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.StartBatch(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns bad request for invalid task_id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		// CountByIDs returns 0 — task not found
		mock.ExpectQuery("SELECT COUNT").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

		body := fmt.Sprintf(`{
			"name": "Test Batch",
			"task_ids": [%q],
			"configs": [{"model": "claude-sonnet-4-6"}]
		}`, taskID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/batches", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.StartBatch(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_TASK_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for invalid JSON body", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/batches", strings.NewReader(`{bad json`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.StartBatch(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_JSON")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on transaction begin failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT COUNT").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

		mock.ExpectBegin().WillReturnError(fmt.Errorf("cannot begin tx"))

		body := fmt.Sprintf(`{
			"name": "Test Batch",
			"task_ids": [%q],
			"configs": [{"model": "claude-sonnet-4-6"}]
		}`, taskID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/batches", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.StartBatch(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "TX_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
