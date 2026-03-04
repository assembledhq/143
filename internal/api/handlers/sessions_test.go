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
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var agentRunColumns = []string{
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_run_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "pm_approach", "pm_reasoning",
	"created_at",
}

var pmPlanColumns = []string{
	"id", "org_id", "status", "analysis", "tasks", "clusters", "skipped_issues",
	"issues_reviewed", "product_context_snapshot", "token_usage", "triggered_by",
	"created_at", "completed_at",
}

func TestSessionHandler_List(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	planStore := db.NewPMPlanStore(mock)
	runStore := db.NewAgentRunStore(mock)
	handler := NewSessionHandler(planStore, runStore)

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
					5, json.RawMessage(`{}`), json.RawMessage(`{}`), "cron",
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
					earlier,
				),
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
	handler := NewSessionHandler(planStore, runStore)

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
					3, json.RawMessage(`{}`), json.RawMessage(`{}`), "manual",
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
					now,
				),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+planID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", planID.String())
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(chi.NewRouteContext().WithValue(req.Context(), chi.RouteCtxKey, rctx))
	// chi URL params: build context properly
	ctx := req.Context()
	ctx = chi.WithRouteContext(ctx, rctx)
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
	handler := NewSessionHandler(planStore, runStore)

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
					now,
				),
		)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String(), nil)
	ctx := chi.WithRouteContext(req.Context(), rctx)
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
	handler := NewSessionHandler(planStore, runStore)

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
	ctx := chi.WithRouteContext(req.Context(), rctx)
	req = req.WithContext(middleware.WithOrgID(ctx, orgID))

	rr := httptest.NewRecorder()
	handler.Get(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "should return 404")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func float64Ptr(f float64) *float64 { return &f }
