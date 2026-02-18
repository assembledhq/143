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
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunHandler_List_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	store := db.NewAgentRunStore(mock)
	handler := NewRunHandler(store)

	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
				"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
				"container_id", "started_at", "completed_at", "token_usage",
				"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
				"parent_run_id", "revision_context", "error", "result_summary", "diff", "created_at",
			}).AddRow(
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, nil,
				nil, nil, nil, nil, nil, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.AgentRun]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp.Data, 1)
	assert.Equal(t, "completed", resp.Data[0].Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRunHandler_List_Empty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewAgentRunStore(mock)
	handler := NewRunHandler(store)

	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
				"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
				"container_id", "started_at", "completed_at", "token_usage",
				"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
				"parent_run_id", "revision_context", "error", "result_summary", "diff", "created_at",
			}),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.AgentRun]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp.Data, 0)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRunHandler_Get_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	store := db.NewAgentRunStore(mock)
	handler := NewRunHandler(store)

	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
				"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
				"container_id", "started_at", "completed_at", "token_usage",
				"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
				"parent_run_id", "revision_context", "error", "result_summary", "diff", "created_at",
			}).AddRow(
				runID, issueID, orgID, "claude-code", "running", "supervised", "standard",
				nil, nil, nil, nil,
				nil, &now, nil, nil,
				nil, nil, nil, nil,
				nil, nil, nil, nil, nil, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Get(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.AgentRun]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "running", resp.Data.Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRunHandler_Get_InvalidID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewAgentRunStore(mock)
	handler := NewRunHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/invalid", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "invalid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Get(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_ID")
}
