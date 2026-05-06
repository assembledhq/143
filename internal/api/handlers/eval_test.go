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

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
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
	"snapshot_broken",
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
		false,
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

// --- Bootstrap ---

var bootstrapRunColumns = []string{
	"id", "org_id", "repo_id", "status", "candidates", "session_id",
	"created_by", "created_at", "completed_at", "error_message",
}

func newTestBootstrapRunRow(runID, orgID, repoID uuid.UUID, userID *uuid.UUID, status string, candidates json.RawMessage, now time.Time) []interface{} {
	return []interface{}{
		runID, orgID, repoID, status, candidates, nil,
		userID, now, nil, nil,
	}
}

func TestEvalHandler_Bootstrap(t *testing.T) {
	t.Parallel()

	t.Run("creates bootstrap run and enqueues job", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("INSERT INTO eval_bootstrap_runs").
			WithArgs(anyArgs(4)...).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

		mock.ExpectQuery("INSERT INTO jobs").
			WithArgs(anyArgs(6)...).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

		body := fmt.Sprintf(`{"repo_id": %q}`, repoID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.Bootstrap(w, req)
		require.Equal(t, http.StatusAccepted, w.Code)

		var resp models.SingleResponse[models.EvalBootstrapRun]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, runID, resp.Data.ID)
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

		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap", strings.NewReader(`{bad json`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.Bootstrap(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_BODY")
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

		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.Bootstrap(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on bootstrap store create failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("INSERT INTO eval_bootstrap_runs").
			WithArgs(anyArgs(4)...).
			WillReturnError(fmt.Errorf("db error"))

		body := fmt.Sprintf(`{"repo_id": %q}`, repoID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.Bootstrap(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "CREATE_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on enqueue failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("INSERT INTO eval_bootstrap_runs").
			WithArgs(anyArgs(4)...).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

		mock.ExpectQuery("INSERT INTO jobs").
			WithArgs(anyArgs(6)...).
			WillReturnError(fmt.Errorf("queue full"))

		body := fmt.Sprintf(`{"repo_id": %q}`, repoID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.Bootstrap(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "ENQUEUE_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestEvalHandler_GetBootstrapCandidates(t *testing.T) {
	t.Parallel()

	t.Run("returns run by bootstrap_run_id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(bootstrapRunColumns).
				AddRow(newTestBootstrapRunRow(runID, orgID, repoID, &userID, "completed", json.RawMessage(`[]`), now)...))

		url := fmt.Sprintf("/api/v1/evals/bootstrap/candidates?bootstrap_run_id=%s", runID.String())
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.GetBootstrapCandidates(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.SingleResponse[models.EvalBootstrapRun]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, runID, resp.Data.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for invalid bootstrap_run_id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/evals/bootstrap/candidates?bootstrap_run_id=not-a-uuid", nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.GetBootstrapCandidates(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 404 when bootstrap_run_id not found", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		runID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnError(pgx.ErrNoRows)

		url := fmt.Sprintf("/api/v1/evals/bootstrap/candidates?bootstrap_run_id=%s", runID.String())
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.GetBootstrapCandidates(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Body.String(), "NOT_FOUND")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on db failure for bootstrap_run_id lookup", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		runID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnError(fmt.Errorf("connection reset"))

		url := fmt.Sprintf("/api/v1/evals/bootstrap/candidates?bootstrap_run_id=%s", runID.String())
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.GetBootstrapCandidates(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "INTERNAL_ERROR")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns latest run by repo_id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE org_id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(bootstrapRunColumns).
				AddRow(newTestBootstrapRunRow(runID, orgID, repoID, &userID, "completed", json.RawMessage(`[]`), now)...))

		url := fmt.Sprintf("/api/v1/evals/bootstrap/candidates?repo_id=%s", repoID.String())
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.GetBootstrapCandidates(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.SingleResponse[models.EvalBootstrapRun]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Equal(t, runID, resp.Data.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error when neither param provided", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/evals/bootstrap/candidates", nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.GetBootstrapCandidates(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_PARAM")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for invalid repo_id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/evals/bootstrap/candidates?repo_id=not-valid", nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.GetBootstrapCandidates(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 404 when no runs for repo_id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE org_id").
			WithArgs(anyArgs(2)...).
			WillReturnError(pgx.ErrNoRows)

		url := fmt.Sprintf("/api/v1/evals/bootstrap/candidates?repo_id=%s", repoID.String())
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.GetBootstrapCandidates(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Body.String(), "NOT_FOUND")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on db failure for repo_id lookup", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE org_id").
			WithArgs(anyArgs(2)...).
			WillReturnError(fmt.Errorf("timeout"))

		url := fmt.Sprintf("/api/v1/evals/bootstrap/candidates?repo_id=%s", repoID.String())
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.GetBootstrapCandidates(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "INTERNAL_ERROR")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestEvalHandler_AcceptBootstrapCandidates(t *testing.T) {
	t.Parallel()

	t.Run("creates tasks from selected candidates", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		runID := uuid.New()
		taskID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		candidates := json.RawMessage(`[{"pr_number":42,"pr_title":"Fix auth","base_commit_sha":"aaa","solution_commit_sha":"bbb","solution_diff":"diff","issue_description":"Auth broken","scoring_criteria":[],"complexity":"moderate","fitness_score":0.8,"fitness_reasoning":"good"}]`)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(bootstrapRunColumns).
				AddRow(newTestBootstrapRunRow(runID, orgID, repoID, &userID, "completed", candidates, now)...))

		mock.ExpectBegin()

		mock.ExpectQuery("INSERT INTO eval_tasks").
			WithArgs(anyArgs(22)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		mock.ExpectCommit()

		body := fmt.Sprintf(`{"bootstrap_run_id": %q, "candidate_indices": [0]}`, runID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap/accept", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.AcceptBootstrapCandidates(w, req)
		require.Equal(t, http.StatusCreated, w.Code)

		var resp models.ListResponse[models.EvalTask]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Len(t, resp.Data, 1)
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

		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap/accept", strings.NewReader(`{bad`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.AcceptBootstrapCandidates(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_BODY")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error for missing bootstrap_run_id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap/accept", strings.NewReader(`{"candidate_indices": [0]}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.AcceptBootstrapCandidates(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 404 when bootstrap run not found", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		runID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnError(pgx.ErrNoRows)

		body := fmt.Sprintf(`{"bootstrap_run_id": %q, "candidate_indices": [0]}`, runID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap/accept", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.AcceptBootstrapCandidates(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Contains(t, w.Body.String(), "NOT_FOUND")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on db failure fetching bootstrap run", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		runID := uuid.New()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnError(fmt.Errorf("connection refused"))

		body := fmt.Sprintf(`{"bootstrap_run_id": %q, "candidate_indices": [0]}`, runID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap/accept", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.AcceptBootstrapCandidates(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "INTERNAL_ERROR")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error when bootstrap run not completed", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(bootstrapRunColumns).
				AddRow(newTestBootstrapRunRow(runID, orgID, repoID, &userID, "running", nil, now)...))

		body := fmt.Sprintf(`{"bootstrap_run_id": %q, "candidate_indices": [0]}`, runID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap/accept", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.AcceptBootstrapCandidates(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "NOT_READY")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects out-of-range candidate indices", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		candidates := json.RawMessage(`[{"pr_number":1,"pr_title":"PR1","base_commit_sha":"aaa","solution_commit_sha":"bbb","solution_diff":"diff","issue_description":"desc","scoring_criteria":[],"complexity":"simple","fitness_score":0.9,"fitness_reasoning":"ok"}]`)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(bootstrapRunColumns).
				AddRow(newTestBootstrapRunRow(runID, orgID, repoID, &userID, "completed", candidates, now)...))

		// Out-of-range index 5 (only 1 candidate at index 0) — now returns error
		body := fmt.Sprintf(`{"bootstrap_run_id": %q, "candidate_indices": [5]}`, runID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap/accept", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.AcceptBootstrapCandidates(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_INDEX")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects empty candidate_indices", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		runID := uuid.New()
		handler := newEvalHandler(mock)

		body := fmt.Sprintf(`{"bootstrap_run_id": %q, "candidate_indices": []}`, runID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap/accept", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.AcceptBootstrapCandidates(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_FIELD")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error on task create failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		repoID := uuid.New()
		runID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		candidates := json.RawMessage(`[{"pr_number":42,"pr_title":"Fix auth","base_commit_sha":"aaa","solution_commit_sha":"bbb","solution_diff":"diff","issue_description":"Auth broken","scoring_criteria":[],"complexity":"moderate","fitness_score":0.8,"fitness_reasoning":"good"}]`)

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(bootstrapRunColumns).
				AddRow(newTestBootstrapRunRow(runID, orgID, repoID, &userID, "completed", candidates, now)...))

		mock.ExpectBegin()

		mock.ExpectQuery("INSERT INTO eval_tasks").
			WithArgs(anyArgs(22)...).
			WillReturnError(fmt.Errorf("unique constraint violation"))

		mock.ExpectRollback()

		body := fmt.Sprintf(`{"bootstrap_run_id": %q, "candidate_indices": [0]}`, runID.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/bootstrap/accept", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.AcceptBootstrapCandidates(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "CREATE_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- ListBatches ---

func TestEvalHandler_ListBatches(t *testing.T) {
	t.Parallel()

	t.Run("returns batches successfully", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		batchID := uuid.New()
		now := time.Now()
		handler := newEvalHandler(mock)

		mock.ExpectQuery("SELECT .+ FROM eval_batches WHERE org_id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(
				pgxmock.NewRows(evalBatchColumns).
					AddRow(batchID, orgID, "Test Batch", "completed", 3, 6, &userID, now, &now),
			)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/evals/batch", nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.ListBatches(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.EvalBatch]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Len(t, resp.Data, 1)
		require.Equal(t, batchID, resp.Data[0].ID)
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

		mock.ExpectQuery("SELECT .+ FROM eval_batches WHERE org_id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(evalBatchColumns))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/evals/batch", nil)
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.ListBatches(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.EvalBatch]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Len(t, resp.Data, 0)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// --- SHA validation ---

func TestEvalHandler_CreateTask_SHAValidation(t *testing.T) {
	t.Parallel()

	t.Run("rejects invalid base_commit_sha", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newEvalHandler(mock)
		orgID := uuid.New()
		userID := uuid.New()

		body := fmt.Sprintf(`{"repo_id":"%s","name":"test","base_commit_sha":"not-a-sha!","issue_description":"fix it"}`, uuid.New())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_SHA")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects too-short SHA", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newEvalHandler(mock)
		orgID := uuid.New()
		userID := uuid.New()

		body := fmt.Sprintf(`{"repo_id":"%s","name":"test","base_commit_sha":"abc","issue_description":"fix it"}`, uuid.New())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_SHA")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("accepts valid SHA", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newEvalHandler(mock)
		orgID := uuid.New()
		userID := uuid.New()
		taskID := uuid.New()
		repoID := uuid.New()
		now := time.Now()

		mock.ExpectQuery("INSERT INTO eval_tasks").
			WithArgs(anyArgs(22)...).
			WillReturnRows(pgxmock.NewRows(evalTaskColumns).AddRow(newTestEvalTaskRow(taskID, orgID, repoID, now)...))

		body := fmt.Sprintf(`{"repo_id":"%s","name":"test","base_commit_sha":"abcd1234","issue_description":"fix it"}`, repoID)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusCreated, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestEvalHandler_CreateTask_ThresholdValidation(t *testing.T) {
	t.Parallel()

	t.Run("rejects negative pass_threshold", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newEvalHandler(mock)
		orgID := uuid.New()
		userID := uuid.New()

		body := fmt.Sprintf(`{"repo_id":"%s","name":"test","base_commit_sha":"abcd1234","issue_description":"fix it","pass_threshold":-0.5}`, uuid.New())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_THRESHOLD")
	})

	t.Run("rejects pass_threshold above 1.0", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newEvalHandler(mock)
		orgID := uuid.New()
		userID := uuid.New()

		body := fmt.Sprintf(`{"repo_id":"%s","name":"test","base_commit_sha":"abcd1234","issue_description":"fix it","pass_threshold":1.5}`, uuid.New())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evals/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.CreateTask(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_THRESHOLD")
	})
}

func TestEvalHandler_StartRun_ModelValidation(t *testing.T) {
	t.Parallel()

	t.Run("rejects invalid model", func(t *testing.T) {
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

		body := `{"model": "gpt-4o"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks/"+taskID.String()+"/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.StartRun(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_MODEL")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects invalid config_ref", func(t *testing.T) {
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

		body := `{"model": "claude-sonnet-4-6", "config_ref": "main; rm -rf /"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/tasks/"+taskID.String()+"/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"id": taskID.String()}))
		w := httptest.NewRecorder()

		handler.StartRun(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_CONFIG_REF")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestEvalHandler_StartBatch_Validation(t *testing.T) {
	t.Parallel()

	t.Run("rejects batch exceeding max total runs", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		handler := newEvalHandler(mock)

		// 51 tasks × 2 configs = 102 runs > 100 max
		taskIDs := make([]string, 51)
		for i := range taskIDs {
			taskIDs[i] = fmt.Sprintf("%q", uuid.New().String())
		}
		body := fmt.Sprintf(`{
			"name": "Big Batch",
			"task_ids": [%s],
			"configs": [{"model": "claude-sonnet-4-6"}, {"model": "claude-opus-4-6"}]
		}`, strings.Join(taskIDs, ","))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/batches", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.StartBatch(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "BATCH_TOO_LARGE")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects config with invalid model", func(t *testing.T) {
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
			"configs": [{"model": "bad-model"}]
		}`, taskID.String())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/eval/batches", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(evalCtx(orgID, userID))
		w := httptest.NewRecorder()

		handler.StartBatch(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_MODEL")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects config with empty model", func(t *testing.T) {
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
			"configs": [{"model": ""}]
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
}

// --- SSE: StreamBatchUpdates / StreamBootstrapUpdates ---

// stubEvalMembershipStore lets the SSE handler tests exercise cross-org
// validation without bringing up a real OrganizationMembershipStore. Mirrors
// stubPullRequestMembershipStore in pull_requests_test.go.
type stubEvalMembershipStore struct {
	getFunc func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error)
}

func (s *stubEvalMembershipStore) Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	return s.getFunc(ctx, userID, orgID)
}

func newTestEvalStreams(t *testing.T) (*cache.EvalBatchStreams, *cache.EvalBootstrapStreams) {
	t.Helper()
	mr := miniredis.RunT(t)
	metrics, err := cache.NewMetrics()
	require.NoError(t, err, "Redis metrics should initialize for SSE tests")
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), metrics)
	require.NotNil(t, client, "Redis client should initialize for SSE tests")
	t.Cleanup(func() {
		closeErr := client.Close()
		if closeErr != nil && !strings.Contains(closeErr.Error(), "client is closed") {
			require.NoError(t, closeErr, "eval stream test client should close cleanly")
		}
	})
	return cache.NewEvalBatchStreams(client, zerolog.Nop()), cache.NewEvalBootstrapStreams(client, zerolog.Nop())
}

func TestEvalHandler_StreamBatchUpdates_AuthAndAvailability(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	otherOrgID := uuid.New()

	tests := []struct {
		name           string
		setup          func(*EvalHandler, pgxmock.PgxPoolIface, uuid.UUID)
		batchID        string
		query          string
		expectedCode   int
		expectedSubstr string
	}{
		{
			name:           "503 when streams missing",
			setup:          func(h *EvalHandler, _ pgxmock.PgxPoolIface, _ uuid.UUID) {},
			batchID:        uuid.New().String(),
			expectedCode:   http.StatusServiceUnavailable,
			expectedSubstr: "eval batch streams unavailable",
		},
		{
			name: "400 invalid batch ID",
			setup: func(h *EvalHandler, _ pgxmock.PgxPoolIface, _ uuid.UUID) {
				batchStreams, _ := newTestEvalStreams(t)
				h.SetBatchStreams(batchStreams)
			},
			batchID:        "not-a-uuid",
			expectedCode:   http.StatusBadRequest,
			expectedSubstr: "invalid batch ID",
		},
		{
			name: "400 invalid org_id query string",
			setup: func(h *EvalHandler, _ pgxmock.PgxPoolIface, _ uuid.UUID) {
				batchStreams, _ := newTestEvalStreams(t)
				h.SetBatchStreams(batchStreams)
			},
			batchID:        uuid.New().String(),
			query:          "?org_id=not-a-uuid",
			expectedCode:   http.StatusBadRequest,
			expectedSubstr: "invalid eval stream org",
		},
		{
			name: "403 when explicit cross-org membership rejected",
			setup: func(h *EvalHandler, _ pgxmock.PgxPoolIface, _ uuid.UUID) {
				batchStreams, _ := newTestEvalStreams(t)
				h.SetBatchStreams(batchStreams)
				h.SetMembershipStore(&stubEvalMembershipStore{
					getFunc: func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
						return models.OrganizationMembership{}, pgx.ErrNoRows
					},
				})
			},
			batchID:        uuid.New().String(),
			query:          "?org_id=" + otherOrgID.String(),
			expectedCode:   http.StatusForbidden,
			expectedSubstr: "forbidden eval stream org",
		},
		{
			name: "404 when batch not in resolved org",
			setup: func(h *EvalHandler, mock pgxmock.PgxPoolIface, _ uuid.UUID) {
				batchStreams, _ := newTestEvalStreams(t)
				h.SetBatchStreams(batchStreams)
				mock.ExpectQuery("SELECT .+ FROM eval_batches WHERE id").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(evalBatchColumns))
			},
			batchID:        uuid.New().String(),
			expectedCode:   http.StatusNotFound,
			expectedSubstr: "eval batch not found",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()

			handler := newEvalHandler(mock)
			tt.setup(handler, mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/evals/batch/"+tt.batchID+"/stream"+tt.query, nil)
			req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"batchId": tt.batchID}))
			w := httptest.NewRecorder()

			handler.StreamBatchUpdates(w, req)

			require.Equal(t, tt.expectedCode, w.Code, "StreamBatchUpdates should return the expected status")
			require.Contains(t, w.Body.String(), tt.expectedSubstr, "StreamBatchUpdates should explain the failure mode in the body")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestEvalHandler_StreamBatchUpdates_DeliversPublishedEvent(t *testing.T) {
	t.Parallel()

	// End-to-end: subscribe via the SSE handler, publish via the cache helper,
	// confirm the event reaches the response writer with the documented event
	// type. Locks in the per-batch channel scoping by also publishing for an
	// unrelated batch and asserting it doesn't leak into the response.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	batchID := uuid.New()
	otherBatchID := uuid.New()
	now := time.Now()

	handler := newEvalHandler(mock)
	batchStreams, _ := newTestEvalStreams(t)
	handler.SetBatchStreams(batchStreams)

	mock.ExpectQuery("SELECT .+ FROM eval_batches WHERE id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(evalBatchColumns).AddRow(newTestEvalBatchRow(batchID, orgID, &userID, now)...))

	ctx, cancel := context.WithCancel(evalCtxWithChi(orgID, userID, map[string]string{"batchId": batchID.String()}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/evals/batch/"+batchID.String()+"/stream", nil).WithContext(ctx)
	rr := newLockedRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.StreamBatchUpdates(rr, req)
	}()

	event := models.EvalBatchUpdatedEvent{
		BatchID:   batchID,
		OrgID:     orgID,
		Status:    models.EvalBatchStatusRunning,
		UpdatedAt: time.Now().UTC(),
	}
	otherEvent := models.EvalBatchUpdatedEvent{
		BatchID:   otherBatchID,
		OrgID:     orgID,
		Status:    models.EvalBatchStatusRunning,
		UpdatedAt: time.Now().UTC(),
	}

	require.Eventually(t, func() bool {
		// Publish the cross-batch event first so we'd see it ahead of ours
		// if the per-batch channel scoping ever regressed.
		_ = batchStreams.PublishUpdated(context.Background(), otherEvent)
		_ = batchStreams.PublishUpdated(context.Background(), event)
		return strings.Contains(rr.BodyString(), "eval_batch.updated")
	}, 2*time.Second, 20*time.Millisecond, "StreamBatchUpdates should write the per-batch event to the SSE response")

	cancel()
	<-done

	body := rr.BodyString()
	require.Contains(t, body, batchID.String(), "SSE response should include the published batch ID")
	require.NotContains(t, body, otherBatchID.String(), "SSE response must not leak events scoped to other batches")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalHandler_StreamBootstrapUpdates_AuthAndAvailability(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name           string
		setup          func(*EvalHandler, pgxmock.PgxPoolIface)
		runID          string
		query          string
		expectedCode   int
		expectedSubstr string
	}{
		{
			name:           "503 when streams missing",
			setup:          func(*EvalHandler, pgxmock.PgxPoolIface) {},
			runID:          uuid.New().String(),
			expectedCode:   http.StatusServiceUnavailable,
			expectedSubstr: "eval bootstrap streams unavailable",
		},
		{
			name: "400 invalid run ID",
			setup: func(h *EvalHandler, _ pgxmock.PgxPoolIface) {
				_, bs := newTestEvalStreams(t)
				h.SetBootstrapStreams(bs)
			},
			runID:          "not-a-uuid",
			expectedCode:   http.StatusBadRequest,
			expectedSubstr: "invalid bootstrap run ID",
		},
		{
			name: "404 when run not in resolved org",
			setup: func(h *EvalHandler, mock pgxmock.PgxPoolIface) {
				_, bs := newTestEvalStreams(t)
				h.SetBootstrapStreams(bs)
				mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(bootstrapRunColumns))
			},
			runID:          uuid.New().String(),
			expectedCode:   http.StatusNotFound,
			expectedSubstr: "bootstrap run not found",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			handler := newEvalHandler(mock)
			tt.setup(handler, mock)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/evals/bootstrap/"+tt.runID+"/stream"+tt.query, nil)
			req = req.WithContext(evalCtxWithChi(orgID, userID, map[string]string{"runId": tt.runID}))
			w := httptest.NewRecorder()

			handler.StreamBootstrapUpdates(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			require.Contains(t, w.Body.String(), tt.expectedSubstr)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestEvalHandler_StreamBootstrapUpdates_DeliversPublishedEvent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	handler := newEvalHandler(mock)
	_, bootstrapStreams := newTestEvalStreams(t)
	handler.SetBootstrapStreams(bootstrapStreams)

	mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(bootstrapRunColumns).
			AddRow(newTestBootstrapRunRow(runID, orgID, repoID, &userID, "running", json.RawMessage(`null`), now)...))

	ctx, cancel := context.WithCancel(evalCtxWithChi(orgID, userID, map[string]string{"runId": runID.String()}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/evals/bootstrap/"+runID.String()+"/stream", nil).WithContext(ctx)
	rr := newLockedRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.StreamBootstrapUpdates(rr, req)
	}()

	event := models.EvalBootstrapUpdatedEvent{
		BootstrapRunID: runID,
		OrgID:          orgID,
		Status:         models.EvalBootstrapStatusCompleted,
		UpdatedAt:      time.Now().UTC(),
	}

	require.Eventually(t, func() bool {
		_ = bootstrapStreams.PublishUpdated(context.Background(), event)
		return strings.Contains(rr.BodyString(), "eval_bootstrap.updated")
	}, 2*time.Second, 20*time.Millisecond, "StreamBootstrapUpdates should write the per-run event to the SSE response")

	cancel()
	<-done

	require.Contains(t, rr.BodyString(), runID.String(), "SSE response should include the published bootstrap run ID")
	require.NoError(t, mock.ExpectationsWereMet())
}
