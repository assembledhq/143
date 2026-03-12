package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
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
	handler := NewPMHandler(planStore, decisionLogStore, jobStore)

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
	handler := NewPMHandler(planStore, decisionLogStore, jobStore)

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
	handler := NewPMHandler(planStore, decisionLogStore, jobStore)

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
	handler := NewPMHandler(planStore, decisionLogStore, jobStore)

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
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func strPtr(s string) *string { return &s }
