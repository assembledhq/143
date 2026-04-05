package handlers

import (
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
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// pmPlanColumnsWithContext includes the new context count columns.
var pmPlanColumnsWithContext = []string{
	"id", "org_id", "status", "analysis", "tasks", "clusters", "skipped_issues",
	"issues_reviewed", "in_flight_runs_checked", "past_outcomes_reviewed",
	"recent_prs_checked", "past_decisions_reviewed", "commits_analyzed",
	"product_context_snapshot", "token_usage", "triggered_by",
	"created_at", "completed_at",
}

func TestPMHandler_AnalyzeEnqueuesJob(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobStore := db.NewJobStore(mock)
	planStore := db.NewPMPlanStore(mock)
	decisionLogStore := db.NewPMDecisionLogStore(mock)
	handler := NewPMHandler(planStore, decisionLogStore, jobStore, nil)

	orgID := uuid.New()
	jobID := uuid.New()

	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pm/analyze", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Analyze(rr, req)

	require.Equal(t, http.StatusAccepted, rr.Code, "should return accepted")
	require.Contains(t, rr.Body.String(), jobID.String(), "response should include job ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPMHandler_ListPlans(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobStore := db.NewJobStore(mock)
	planStore := db.NewPMPlanStore(mock)
	decisionLogStore := db.NewPMDecisionLogStore(mock)
	handler := NewPMHandler(planStore, decisionLogStore, jobStore, nil)

	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmPlanColumnsWithContext).
				AddRow(uuid.New(), orgID, "completed", "analysis",
					json.RawMessage(`[]`), json.RawMessage(`[]`), json.RawMessage(`[]`),
					2, 3, 5, 1, 8, 20,
					json.RawMessage(`{}`), json.RawMessage(`{}`), "cron",
					now, nil,
				),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/plans", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "should return ok")
	require.Contains(t, rr.Body.String(), `"analysis"`, "response should include plan data")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPMHandler_Decisions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	decisionLogStore := db.NewPMDecisionLogStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewPMHandler(planStore, decisionLogStore, jobStore, nil)

	orgID := uuid.New()
	planID := uuid.New()
	issueID := uuid.New()
	projectID := uuid.New()
	now := time.Now()

	// Mock ListDecisionViews query.
	mock.ExpectQuery("SELECT d.id, d.plan_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "plan_id", "issue_id",
				"issue_title", "project_id", "project_title",
				"decision", "reasoning", "outcome", "created_at",
			}).
				AddRow(uuid.New(), planID, &issueID,
					strPtr("Auth bug"), &projectID, strPtr("Auth Overhaul"),
					"delegate", "High impact", "succeeded", now),
		)

	// Mock GetDecisionSummary query.
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"total_delegated", "succeeded", "failed", "still_open"}).
				AddRow(15, 11, 2, 2),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/decisions", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Decisions(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "should return OK")

	var resp decisionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Len(t, resp.Data, 1, "should return one decision")
	require.Equal(t, models.PMDecisionType("delegate"), resp.Data[0].Decision, "decision type should be delegate")
	require.Equal(t, 15, resp.Summary.TotalDelegated, "summary should show 15 total delegated")
	require.Equal(t, 11, resp.Summary.Succeeded, "summary should show 11 succeeded")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPMHandler_Status(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	decisionLogStore := db.NewPMDecisionLogStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewPMHandler(planStore, decisionLogStore, jobStore, nil)

	orgID := uuid.New()
	now := time.Now()

	// Mock GetLatestByOrg query.
	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmPlanColumnsWithContext).
				AddRow(uuid.New(), orgID, "completed", "analysis",
					json.RawMessage(`[]`), json.RawMessage(`[]`), json.RawMessage(`[]`),
					14, 3, 8, 5, 12, 20,
					json.RawMessage(`{}`), json.RawMessage(`{}`), "cron",
					now, &now,
				),
		)

	// Mock GetLatestFailedByType — no recent failures.
	mock.ExpectQuery("SELECT id, last_error, updated_at FROM jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "last_error", "updated_at"}))

	// Mock GetDecisionSummary query.
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"total_delegated", "succeeded", "failed", "still_open"}).
				AddRow(15, 11, 2, 2),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/status", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Status(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "should return OK")

	var resp models.SingleResponse[models.PMStatus]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.False(t, resp.Data.IsRunning, "PM should not be running")
	require.Equal(t, 14, resp.Data.IssuesReviewed, "should show issues reviewed from last plan")
	require.Equal(t, 11, resp.Data.SuccessCount, "should show success count")
	require.Equal(t, 15, resp.Data.TotalDelegated, "should show total delegated")
	require.Nil(t, resp.Data.LastError, "should have no error when no recent failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPMHandler_StatusWithJobError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	decisionLogStore := db.NewPMDecisionLogStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewPMHandler(planStore, decisionLogStore, jobStore, nil)

	orgID := uuid.New()
	now := time.Now()
	failedAt := now.Add(5 * time.Minute) // Failed after the last plan

	// Mock GetLatestByOrg — has a previous successful plan.
	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmPlanColumnsWithContext).
				AddRow(uuid.New(), orgID, "completed", "analysis",
					json.RawMessage(`[]`), json.RawMessage(`[]`), json.RawMessage(`[]`),
					5, 0, 0, 0, 0, 0,
					json.RawMessage(`{}`), json.RawMessage(`{}`), "cron",
					now, &now,
				),
		)

	// Mock GetLatestFailedByType — has a recent failure newer than the plan.
	mock.ExpectQuery("SELECT id, last_error, updated_at FROM jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "last_error", "updated_at"}).
				AddRow(uuid.New(), "no repositories configured for org", failedAt),
		)

	// Mock GetDecisionSummary query.
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"total_delegated", "succeeded", "failed", "still_open"}).
				AddRow(0, 0, 0, 0),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/status", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Status(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "should return OK")

	var resp models.SingleResponse[models.PMStatus]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.NotNil(t, resp.Data.LastError, "should include job error")
	require.Equal(t, "no repositories configured for org", *resp.Data.LastError, "should contain the error message")
	require.NotNil(t, resp.Data.LastFailedAt, "should include failure timestamp")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// pmDocColumns matches the column order in db/pm_documents.go scanPMDoc.
var pmDocTestColumns = []string{
	"id", "org_id", "title", "content", "doc_type", "sort_order",
	"source_type", "source_url", "source_id", "source_meta", "last_synced_at",
	"active", "logical_id", "content_hash",
	"created_by", "created_at", "updated_at",
}

func TestPMHandler_BootstrapEnqueuesJob(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	jobStore := db.NewJobStore(mock)
	handler := NewPMHandler(db.NewPMPlanStore(mock), db.NewPMDecisionLogStore(mock), jobStore, nil)

	orgID := uuid.New()
	jobID := uuid.New()

	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pm/bootstrap", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Bootstrap(rr, req)

	require.Equal(t, http.StatusAccepted, rr.Code)
	require.Contains(t, rr.Body.String(), jobID.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMHandler_RefreshEnqueuesJob(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	jobStore := db.NewJobStore(mock)
	handler := NewPMHandler(db.NewPMPlanStore(mock), db.NewPMDecisionLogStore(mock), jobStore, nil)

	orgID := uuid.New()
	jobID := uuid.New()

	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pm/refresh", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Refresh(rr, req)

	require.Equal(t, http.StatusAccepted, rr.Code)
	require.Contains(t, rr.Body.String(), jobID.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMHandler_ListPendingRefreshes(t *testing.T) {
	t.Parallel()

	t.Run("returns pending refresh docs", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := NewPMHandler(db.NewPMPlanStore(mock), db.NewPMDecisionLogStore(mock), db.NewJobStore(mock), nil)
		handler.SetPMDocumentStore(db.NewPMDocumentStore(mock))

		orgID := uuid.New()
		docID := uuid.New()
		now := time.Now()

		mock.ExpectQuery("SELECT .+ FROM pm_documents").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(pmDocTestColumns).
					AddRow(docID, orgID, "Refresh doc", "content", "context", -1,
						"refresh", nil, nil, nil, &now,
						true, docID, "",
						nil, now, now),
			)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/context/pending", nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		rr := httptest.NewRecorder()

		handler.ListPendingRefreshes(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), `"refresh"`)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns empty array when store is nil", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := NewPMHandler(db.NewPMPlanStore(mock), db.NewPMDecisionLogStore(mock), db.NewJobStore(mock), nil)
		// Deliberately NOT setting pmDocStore.

		req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/context/pending", nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
		rr := httptest.NewRecorder()

		handler.ListPendingRefreshes(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), `"data":[]`)
	})
}

func TestPMHandler_AcceptRefresh(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewPMHandler(db.NewPMPlanStore(mock), db.NewPMDecisionLogStore(mock), db.NewJobStore(mock), nil)
	handler.SetPMDocumentStore(db.NewPMDocumentStore(mock))

	orgID := uuid.New()
	refreshID := uuid.New()
	activeID := uuid.New()
	now := time.Now()

	// Mock GetByID — returns the refresh doc.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(refreshID, orgID, "Refresh", "new content", "context", -1,
					"refresh", nil, nil, nil, &now,
					true, refreshID, "",
					nil, now, now),
		)

	// Mock GetByOrgAndSourceType — returns the active autogenerated doc.
	mock.ExpectQuery("SELECT .+ FROM pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(activeID, orgID, "PM Context", "old content", "context", -1,
					"autogenerated", nil, nil, nil, &now,
					true, activeID, "",
					nil, now, now),
		)

	// Mock outer transaction (AcceptRefresh wraps Update+Delete in a tx).
	mock.ExpectBegin()

	// Update now uses insert-only versioning: inner savepoint + SELECT FOR UPDATE + deactivate + insert.
	mock.ExpectBegin() // savepoint
	// Fetch current row with row lock (SELECT ... FOR UPDATE).
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id .+ AND active = true FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(activeID, orgID, "PM Context", "old content", "context", -1,
					"autogenerated", nil, nil, nil, &now,
					true, activeID, "",
					nil, now, now),
		)
	mock.ExpectExec("UPDATE pm_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(uuid.New(), orgID, "PM Context", "new content", "context", -1,
					"autogenerated", nil, nil, nil, &now,
					true, activeID, "",
					nil, now, now),
		)
	mock.ExpectCommit()  // savepoint release
	mock.ExpectRollback() // deferred rollback (no-op after commit)

	// Delete is now soft-delete (UPDATE SET active = false).
	mock.ExpectExec("UPDATE pm_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	mock.ExpectCommit()  // outer tx commit
	mock.ExpectRollback() // deferred rollback (no-op)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", refreshID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pm/context/"+refreshID.String()+"/accept", nil)
	req = req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.AcceptRefresh(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	require.Contains(t, rr.Body.String(), "new content", "should contain the promoted content")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMHandler_AcceptRefresh_NotRefreshDoc(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewPMHandler(db.NewPMPlanStore(mock), db.NewPMDecisionLogStore(mock), db.NewJobStore(mock), nil)
	handler.SetPMDocumentStore(db.NewPMDocumentStore(mock))

	orgID := uuid.New()
	docID := uuid.New()
	now := time.Now()

	// Mock GetByID — returns a manual doc (not a refresh).
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(docID, orgID, "Manual doc", "content", "context", 0,
					"manual", nil, nil, nil, &now,
					true, docID, "",
					nil, now, now),
		)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", docID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pm/context/"+docID.String()+"/accept", nil)
	req = req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.AcceptRefresh(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "NOT_REFRESH")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMHandler_RejectRefresh(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewPMHandler(db.NewPMPlanStore(mock), db.NewPMDecisionLogStore(mock), db.NewJobStore(mock), nil)
	handler.SetPMDocumentStore(db.NewPMDocumentStore(mock))

	orgID := uuid.New()
	refreshID := uuid.New()
	now := time.Now()

	// Mock GetByID — returns a refresh doc.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(refreshID, orgID, "Refresh", "content", "context", -1,
					"refresh", nil, nil, nil, &now,
					true, refreshID, "",
					nil, now, now),
		)

	// Mock Delete (now soft-delete: UPDATE SET active = false).
	mock.ExpectExec("UPDATE pm_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", refreshID.String())
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pm/context/"+refreshID.String()+"/reject", nil)
	req = req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.RejectRefresh(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMHandler_RejectRefresh_NotRefreshDoc(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewPMHandler(db.NewPMPlanStore(mock), db.NewPMDecisionLogStore(mock), db.NewJobStore(mock), nil)
	handler.SetPMDocumentStore(db.NewPMDocumentStore(mock))

	orgID := uuid.New()
	docID := uuid.New()
	now := time.Now()

	// Mock GetByID — returns a manual doc.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(docID, orgID, "Manual", "content", "context", 0,
					"manual", nil, nil, nil, &now,
					true, docID, "",
					nil, now, now),
		)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", docID.String())
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pm/context/"+docID.String()+"/reject", nil)
	req = req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.RejectRefresh(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "NOT_REFRESH")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMHandler_Current(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	decisionLogStore := db.NewPMDecisionLogStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewPMHandler(planStore, decisionLogStore, jobStore, nil)

	orgID := uuid.New()
	now := time.Now()

	// Mock GetLatestByOrg query.
	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmPlanColumnsWithContext).
				AddRow(uuid.New(), orgID, "completed", "Payment cluster found",
					json.RawMessage(`[{"rank":1,"title":"Fix auth"}]`),
					json.RawMessage(`[]`),
					json.RawMessage(`[{"issue_id":"abc","reason":"duplicate","detail":"Already fixed"}]`),
					14, 3, 8, 5, 12, 20,
					json.RawMessage(`{}`), json.RawMessage(`{}`), "manual",
					now, &now,
				),
		)

	// Mock GetDecisionSummary query.
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"total_delegated", "succeeded", "failed", "still_open"}).
				AddRow(15, 12, 1, 2),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/current", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Current(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "should return OK")

	var resp models.SingleResponse[models.PMCurrentRecommendation]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Equal(t, "Payment cluster found", resp.Data.Analysis, "should include analysis")
	require.Equal(t, 14, resp.Data.ContextStats.IssuesReviewed, "should include context stats")
	require.Equal(t, 12, resp.Data.ContextStats.PastDecisionsReviewed)
	require.Equal(t, 15, resp.Data.DecisionSummary.TotalDelegated, "should include decision summary")
	require.Equal(t, 12, resp.Data.DecisionSummary.Succeeded)
	require.Equal(t, "completed", resp.Data.Status, "should include plan status")
	require.Equal(t, "manual", resp.Data.TriggeredBy)
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPMHandler_CurrentNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	decisionLogStore := db.NewPMDecisionLogStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewPMHandler(planStore, decisionLogStore, jobStore, nil)

	orgID := uuid.New()

	// Mock GetLatestByOrg — no rows.
	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/current", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Current(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "should return 404 when no plan exists")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func strPtr(s string) *string { return &s }
