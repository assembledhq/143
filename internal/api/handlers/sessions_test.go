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

// agentRunColumns is defined in runs_test.go (same package).

var pmPlanColumns = []string{
	"id", "org_id", "status", "analysis", "tasks", "clusters", "skipped_issues",
	"issues_reviewed", "in_flight_runs_checked", "past_outcomes_reviewed",
	"recent_prs_checked", "past_decisions_reviewed", "commits_analyzed",
	"product_context_snapshot", "token_usage", "triggered_by",
	"created_at", "completed_at",
}

func TestSessionHandler_List(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	runStore := db.NewAgentRunStore(mock)
	issueStore := db.NewIssueStore(mock)
	orgStore := db.NewOrganizationStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewSessionHandler(planStore, runStore, issueStore, orgStore, jobStore)

	orgID := uuid.New()
	planID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)

	// Mock pm_plans query.
	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmPlanColumns).
				AddRow(planID, orgID, "completed", "Full analysis of issues",
					json.RawMessage(`[{"rank":1,"title":"Fix auth","issue_ids":["i1"],"reasoning":"r","approach":"a","risk":"low","complexity":"simple","confidence":"high","status":"delegated"}]`),
					json.RawMessage(`[]`), json.RawMessage(`[]`),
					5, 0, 0, 0, 0, 0,
					json.RawMessage(`{}`), json.RawMessage(`{}`), "cron",
					now, nil,
				),
		)

	// Mock ad-hoc agent_runs query (AdHocOnly).
	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(agentRunColumns).
				AddRow(runID, issueID, orgID, "claude_code", "completed", "full", "standard",
					nil, float64Ptr(0.85), nil, nil,
					nil, &earlier, &now, nil,
					nil, nil, nil, nil,
					nil, nil, nil, strPtr("Fixed payment bug"), nil,
					nil, nil, nil,
					nil, // project_task_id
				nil, // model_override
					earlier,
				),
		)

	mock.ExpectQuery("SELECT id, org_id, external_id, source").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at",
			}).
				AddRow(issueID, orgID, "ISSUE-1", "sentry", nil, nil, "Checkout timeout", nil, json.RawMessage(`{}`), "open", now, now, 1, 1, "high", []string{}, "fp-1", now, now),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "should return OK")

	var resp models.ListResponse[models.AgentSession]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Len(t, resp.Data, 2, "should return both plan and manual sessions")

	// Plan session should be first (more recent).
	require.Equal(t, models.AgentSessionTypePlan, resp.Data[0].Type, "first session should be plan type")
	require.Equal(t, models.AgentSessionTypeManual, resp.Data[1].Type, "second session should be manual type")
	require.Equal(t, "Fixed payment bug", resp.Data[1].Title, "manual session title should come from run result_summary")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Get_PlanSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	runStore := db.NewAgentRunStore(mock)
	issueStore := db.NewIssueStore(mock)
	orgStore := db.NewOrganizationStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewSessionHandler(planStore, runStore, issueStore, orgStore, jobStore)

	orgID := uuid.New()
	planID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	// Mock pm_plans query.
	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmPlanColumns).
				AddRow(planID, orgID, "completed", "Analysis text",
					json.RawMessage(`[{"rank":1,"title":"Fix auth","issue_ids":["i1"],"reasoning":"r","approach":"a","risk":"low","complexity":"simple","confidence":"high","status":"delegated","agent_run_id":"`+runID.String()+`"}]`),
					json.RawMessage(`[]`), json.RawMessage(`[]`),
					3, 0, 0, 0, 0, 0,
					json.RawMessage(`{}`), json.RawMessage(`{}`), "manual",
					now, nil,
				),
		)

	// Mock agent runs batch fetch for enrichment.
	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(agentRunColumns).
				AddRow(runID, uuid.New(), orgID, "claude_code", "completed", "full", "standard",
					nil, float64Ptr(0.9), nil, nil,
					nil, &now, &now, nil,
					nil, nil, nil, nil,
					nil, nil, nil, strPtr("Fixed auth issue"), nil,
					&planID, nil, nil,
					nil, // project_task_id
				nil, // model_override
					now,
				),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+planID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", planID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	req = req.WithContext(middleware.WithOrgID(ctx, orgID))

	rr := httptest.NewRecorder()
	handler.Get(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "should return OK")

	var resp models.SingleResponse[models.AgentSession]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Equal(t, models.AgentSessionTypePlan, resp.Data.Type, "session type should be plan")
	require.Len(t, resp.Data.Tasks, 1, "should have one task")
	require.NotNil(t, resp.Data.Tasks[0].RunStatus, "task should have enriched run status")
	require.Equal(t, models.AgentRunStatusCompleted, *resp.Data.Tasks[0].RunStatus, "run status should be completed")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Get_ManualSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	runStore := db.NewAgentRunStore(mock)
	issueStore := db.NewIssueStore(mock)
	orgStore := db.NewOrganizationStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewSessionHandler(planStore, runStore, issueStore, orgStore, jobStore)

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	// Mock pm_plans query — not found.
	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(pmPlanColumns))

	// Mock agent_runs query.
	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(agentRunColumns).
				AddRow(runID, issueID, orgID, "codex", "failed", "supervised", "standard",
					nil, nil, nil, nil,
					nil, &now, &now, nil,
					strPtr("Build failed"), strPtr("ci_failure"), nil, nil,
					nil, nil, nil, nil, nil,
					nil, nil, nil,
					nil, // project_task_id
				nil, // model_override
					now,
				),
		)

	mock.ExpectQuery("SELECT id, org_id, external_id, source").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at",
			}).
				AddRow(issueID, orgID, "ISSUE-2", "sentry", nil, nil, "Fix issue", nil, json.RawMessage(`{}`), "open", now, now, 1, 1, "medium", []string{}, "fp-2", now, now),
		)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String(), nil)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	req = req.WithContext(middleware.WithOrgID(ctx, orgID))

	rr := httptest.NewRecorder()
	handler.Get(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "should return OK")

	var resp models.SingleResponse[models.AgentSession]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Equal(t, models.AgentSessionTypeManual, resp.Data.Type, "session type should be manual")
	require.Equal(t, models.AgentSessionStatusFailed, resp.Data.Status, "session status should be failed")
	require.Equal(t, models.AgentSessionTriggeredByFixThis, resp.Data.TriggeredBy, "triggered_by should be fix_this")
	require.Len(t, resp.Data.Tasks, 1, "should have one task")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Get_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	runStore := db.NewAgentRunStore(mock)
	issueStore := db.NewIssueStore(mock)
	orgStore := db.NewOrganizationStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewSessionHandler(planStore, runStore, issueStore, orgStore, jobStore)

	orgID := uuid.New()
	sessionID := uuid.New()

	// Mock pm_plans — not found.
	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(pmPlanColumns))

	// Mock agent_runs — not found.
	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(agentRunColumns))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String(), nil)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	req = req.WithContext(middleware.WithOrgID(ctx, orgID))

	rr := httptest.NewRecorder()
	handler.Get(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "should return 404")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRunStatusToSessionStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		runStatus     string
		sessionStatus models.AgentSessionStatus
	}{
		{"pending", models.AgentSessionStatusActive},
		{"running", models.AgentSessionStatusActive},
		{"awaiting_input", models.AgentSessionStatusActive},
		{"needs_human_guidance", models.AgentSessionStatusActive},
		{"completed", models.AgentSessionStatusCompleted},
		{"pr_created", models.AgentSessionStatusCompleted},
		{"skipped", models.AgentSessionStatusCompleted},
		{"failed", models.AgentSessionStatusFailed},
		{"cancelled", models.AgentSessionStatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.runStatus, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.sessionStatus, runStatusToSessionStatus(tt.runStatus),
				"run status %q should map to session status %q", tt.runStatus, tt.sessionStatus)
		})
	}
}

func TestPlanToSession_TypedTaskFields(t *testing.T) {
	t.Parallel()

	plan := models.PMPlan{
		ID:             uuid.New(),
		OrgID:          uuid.New(),
		Status:         models.PMPlanStatusCompleted,
		Analysis:       "Analysis text",
		Tasks:          json.RawMessage(`[{"rank":1,"title":"Fix auth","issue_ids":["i1"],"complexity":"moderate","confidence":"high","status":"delegated","agent_run_id":"00000000-0000-0000-0000-000000000001"}]`),
		Clusters:       json.RawMessage(`[]`),
		SkippedIssues:  json.RawMessage(`[]`),
		IssuesReviewed: 3,
		TriggeredBy:    models.PMTriggerCron,
		CreatedAt:      time.Now(),
	}

	session := planToSession(plan)

	require.Equal(t, models.AgentSessionTypePlan, session.Type, "session type should be plan")
	require.Equal(t, models.AgentSessionStatusCompleted, session.Status, "session status should match plan status")
	require.Equal(t, models.AgentSessionTriggeredByScheduled, session.TriggeredBy, "cron trigger should map to scheduled")
	require.Len(t, session.Tasks, 1, "should have one task")

	task := session.Tasks[0]
	require.Equal(t, models.PMTaskComplexity("moderate"), task.Complexity, "task complexity should be typed PMTaskComplexity")
	require.Equal(t, models.PMTaskConfidence("high"), task.Confidence, "task confidence should be typed PMTaskConfidence")
	require.Equal(t, models.PMTaskStatus("delegated"), task.Status, "task status should be typed PMTaskStatus")
}

func TestRunToSession_TypedTaskFields(t *testing.T) {
	t.Parallel()

	runID := uuid.New()
	issueID := uuid.New()
	summary := "Fixed the bug"
	score := 0.88
	now := time.Now()

	run := models.AgentRun{
		ID:              runID,
		IssueID:         issueID,
		OrgID:           uuid.New(),
		Status:          "completed",
		ResultSummary:   &summary,
		ConfidenceScore: &score,
		StartedAt:       &now,
		CompletedAt:     &now,
		CreatedAt:       now,
	}

	session := runToSession(run)

	require.Equal(t, models.AgentSessionTypeManual, session.Type, "session type should be manual")
	require.Equal(t, models.AgentSessionStatusCompleted, session.Status, "completed run should map to completed session")
	require.Equal(t, models.AgentSessionTriggeredByFixThis, session.TriggeredBy, "ad-hoc run should be fix_this")
	require.Len(t, session.Tasks, 1, "should have one task")

	task := session.Tasks[0]
	require.Equal(t, models.PMTaskStatusDelegated, task.Status, "task status should be PMTaskStatusDelegated")
	require.NotNil(t, task.RunStatus, "run status should be set")
	require.Equal(t, models.AgentRunStatusCompleted, *task.RunStatus, "run status should be typed AgentRunStatusCompleted")
	require.NotNil(t, task.RunConfidenceScore, "confidence score should be set")
	require.Equal(t, 0.88, *task.RunConfidenceScore, "confidence score should match")
	require.NotNil(t, task.RunStartedAt, "started_at should be set")
	require.NotNil(t, task.RunCompletedAt, "completed_at should be set")
}

func TestSessionHandler_CreateManual(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	runStore := db.NewAgentRunStore(mock)
	issueStore := db.NewIssueStore(mock)
	orgStore := db.NewOrganizationStore(mock)
	jobStore := db.NewJobStore(mock)
	handler := NewSessionHandler(planStore, runStore, issueStore, orgStore, jobStore)

	orgID := uuid.New()
	issueID := uuid.New()
	runID := uuid.New()
	jobID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`(?s)INSERT INTO issues`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(issueID, now, now),
		)

	mock.ExpectQuery(`(?s)INSERT INTO agent_runs`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(runID, now),
		)

	mock.ExpectQuery(`(?s)INSERT INTO jobs`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).
				AddRow(jobID),
		)

	body := `{"message":"Please investigate checkout timeout and include a fix.","images":["https://example.com/error.png"],"agent_type":"codex"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(body))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.CreateManual(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "should create a manual session: %s", rr.Body.String())

	var resp models.SingleResponse[models.AgentSession]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Equal(t, models.AgentSessionTypeManual, resp.Data.Type, "session type should be manual")
	require.Equal(t, models.AgentSessionTriggeredByManual, resp.Data.TriggeredBy, "manual session should be triggered manually")
	require.Len(t, resp.Data.Tasks, 1, "manual session should include one task")
	require.Equal(t, runID, resp.Data.ID, "session id should match created run id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func float64Ptr(f float64) *float64 { return &f }
