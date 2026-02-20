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
	"github.com/stretchr/testify/require"
)

func newPriorityRequest(t *testing.T, method, path string, orgID uuid.UUID, issueID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	if issueID != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", issueID)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return req.WithContext(ctx)
}

func TestPriorityHandler_GetPriorityScore_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()
	scoreID := uuid.New()
	now := time.Now()

	// GetByIssueID uses 2 named args: issue_id, org_id
	mock.ExpectQuery("SELECT .+ FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "issue_id", "org_id", "score", "customer_impact_score", "severity_score",
				"recency_score", "revenue_risk_score", "direction_alignment", "factors",
				"eligible_for_agent", "computed_at",
			}).AddRow(scoreID, issueID, orgID, 85.5, 20.0, 30.0, 15.0, 10.0, 10.5, nil, true, now),
		)

	store := db.NewPriorityScoreStore(mock)
	handler := NewPriorityHandler(store, db.NewComplexityEstimateStore(mock), db.NewJobStore(mock))

	req := newPriorityRequest(t, http.MethodGet, "/api/v1/issues/"+issueID.String()+"/priority", orgID, issueID.String())
	rr := httptest.NewRecorder()

	handler.GetPriorityScore(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "should return 200 OK")

	var resp models.SingleResponse[models.PriorityScore]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Equal(t, scoreID, resp.Data.ID, "should return correct score ID")
	require.Equal(t, 85.5, resp.Data.Score, "should return correct score value")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityHandler_GetPriorityScore_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewPriorityScoreStore(mock)
	handler := NewPriorityHandler(store, db.NewComplexityEstimateStore(mock), db.NewJobStore(mock))

	req := newPriorityRequest(t, http.MethodGet, "/api/v1/issues/not-a-uuid/priority", orgID, "not-a-uuid")
	rr := httptest.NewRecorder()

	handler.GetPriorityScore(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "should return 400 for invalid UUID")
}

func TestPriorityHandler_GetPriorityScore_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "issue_id", "org_id", "score", "customer_impact_score", "severity_score",
				"recency_score", "revenue_risk_score", "direction_alignment", "factors",
				"eligible_for_agent", "computed_at",
			}),
		)

	store := db.NewPriorityScoreStore(mock)
	handler := NewPriorityHandler(store, db.NewComplexityEstimateStore(mock), db.NewJobStore(mock))

	req := newPriorityRequest(t, http.MethodGet, "/api/v1/issues/"+issueID.String()+"/priority", orgID, issueID.String())
	rr := httptest.NewRecorder()

	handler.GetPriorityScore(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code, "should return 404 when score not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityHandler_GetComplexity_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()
	estID := uuid.New()
	now := time.Now()

	// GetByIssueID uses 2 named args: issue_id, org_id
	mock.ExpectQuery("SELECT .+ FROM complexity_estimates WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "issue_id", "org_id", "tier", "label", "confidence", "issue_type",
				"reasoning", "estimated_files", "estimated_tokens", "model_used",
				"computed_at", "created_at",
			}).AddRow(estID, issueID, orgID, 2, "moderate", 0.85, nil, nil, nil, nil, nil, now, now),
		)

	store := db.NewComplexityEstimateStore(mock)
	handler := NewPriorityHandler(db.NewPriorityScoreStore(mock), store, db.NewJobStore(mock))

	req := newPriorityRequest(t, http.MethodGet, "/api/v1/issues/"+issueID.String()+"/complexity", orgID, issueID.String())
	rr := httptest.NewRecorder()

	handler.GetComplexity(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "should return 200 OK")

	var resp models.SingleResponse[models.ComplexityEstimate]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Equal(t, estID, resp.Data.ID, "should return correct estimate ID")
	require.Equal(t, "moderate", resp.Data.Label, "should return correct label")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityHandler_GetComplexity_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	handler := NewPriorityHandler(db.NewPriorityScoreStore(mock), db.NewComplexityEstimateStore(mock), db.NewJobStore(mock))

	req := newPriorityRequest(t, http.MethodGet, "/api/v1/issues/bad-id/complexity", orgID, "bad-id")
	rr := httptest.NewRecorder()

	handler.GetComplexity(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "should return 400 for invalid UUID")
}

func TestPriorityHandler_GetComplexity_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM complexity_estimates WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "issue_id", "org_id", "tier", "label", "confidence", "issue_type",
				"reasoning", "estimated_files", "estimated_tokens", "model_used",
				"computed_at", "created_at",
			}),
		)

	store := db.NewComplexityEstimateStore(mock)
	handler := NewPriorityHandler(db.NewPriorityScoreStore(mock), store, db.NewJobStore(mock))

	req := newPriorityRequest(t, http.MethodGet, "/api/v1/issues/"+issueID.String()+"/complexity", orgID, issueID.String())
	rr := httptest.NewRecorder()

	handler.GetComplexity(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code, "should return 404 when estimate not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityHandler_ListPriorityScores_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()

	// ListByOrg uses 1 named arg: org_id
	mock.ExpectQuery("SELECT .+ FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "issue_id", "org_id", "score", "customer_impact_score", "severity_score",
				"recency_score", "revenue_risk_score", "direction_alignment", "factors",
				"eligible_for_agent", "computed_at",
			}).
				AddRow(uuid.New(), uuid.New(), orgID, 90.0, 25.0, 30.0, 15.0, 10.0, 10.0, nil, true, now).
				AddRow(uuid.New(), uuid.New(), orgID, 70.0, 15.0, 20.0, 15.0, 10.0, 10.0, nil, false, now),
		)

	store := db.NewPriorityScoreStore(mock)
	handler := NewPriorityHandler(store, db.NewComplexityEstimateStore(mock), db.NewJobStore(mock))

	req := newPriorityRequest(t, http.MethodGet, "/api/v1/priority?limit=10", orgID, "")
	rr := httptest.NewRecorder()

	handler.ListPriorityScores(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "should return 200 OK")

	var resp models.ListResponse[models.PriorityScore]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Len(t, resp.Data, 2, "should return 2 scores")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityHandler_ListPriorityScores_Empty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "issue_id", "org_id", "score", "customer_impact_score", "severity_score",
				"recency_score", "revenue_risk_score", "direction_alignment", "factors",
				"eligible_for_agent", "computed_at",
			}),
		)

	store := db.NewPriorityScoreStore(mock)
	handler := NewPriorityHandler(store, db.NewComplexityEstimateStore(mock), db.NewJobStore(mock))

	req := newPriorityRequest(t, http.MethodGet, "/api/v1/priority", orgID, "")
	rr := httptest.NewRecorder()

	handler.ListPriorityScores(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "should return 200 OK for empty list")

	var resp models.ListResponse[models.PriorityScore]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
	require.Empty(t, resp.Data, "should return empty data array")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityHandler_Reprioritize_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()

	// Enqueue uses 6 named args: org_id, queue, job_type, payload, priority, dedupe_key
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	jobStore := db.NewJobStore(mock)
	handler := NewPriorityHandler(db.NewPriorityScoreStore(mock), db.NewComplexityEstimateStore(mock), jobStore)

	req := newPriorityRequest(t, http.MethodPost, "/api/v1/issues/"+issueID.String()+"/reprioritize", orgID, issueID.String())
	rr := httptest.NewRecorder()

	handler.Reprioritize(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code, "should return 202 Accepted")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityHandler_Reprioritize_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	handler := NewPriorityHandler(db.NewPriorityScoreStore(mock), db.NewComplexityEstimateStore(mock), db.NewJobStore(mock))

	req := newPriorityRequest(t, http.MethodPost, "/api/v1/issues/invalid/reprioritize", orgID, "invalid")
	rr := httptest.NewRecorder()

	handler.Reprioritize(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "should return 400 for invalid UUID")
}
