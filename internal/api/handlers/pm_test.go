package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPMHandler_AnalyzeEnqueuesJob(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobStore := db.NewJobStore(mock)
	planStore := db.NewPMPlanStore(mock)
	handler := NewPMHandler(planStore, jobStore)

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
	handler := NewPMHandler(planStore, jobStore)

	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "status", "analysis", "tasks", "clusters", "skipped_issues",
				"issues_reviewed", "product_context_snapshot", "token_usage", "triggered_by",
				"created_at", "completed_at",
			}).
				AddRow(uuid.New(), orgID, "completed", "analysis",
					json.RawMessage(`[]`), json.RawMessage(`[]`), json.RawMessage(`[]`),
					2, json.RawMessage(`{}`), json.RawMessage(`{}`), "cron",
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
