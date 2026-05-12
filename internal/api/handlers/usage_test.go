package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type stubUsageRollupStore struct {
	exportRows          pgx.Rows
	dailySessionCounts  []db.ExportDailySessionCountRow
	exportErr           error
	dailyCountsErr      error
	lastTimeseriesCall  usageTimeseriesCall
	lastBreakdownCall   usageBreakdownCall
	lastExportCall      usageExportCall
	lastDailyCountsCall usageDailyCountsCall
}

type usageTimeseriesCall struct {
	groupBy  string
	stackBy  string
	userID   *uuid.UUID
	capacity *string
	filters  db.UsageExecutionFilters
}

type usageBreakdownCall struct {
	dimension string
	sortBy    string
	limit     int
	filters   db.UsageExecutionFilters
}

type usageExportCall struct {
	dimension string
	filters   db.UsageExecutionFilters
}

type usageDailyCountsCall struct {
	dimension string
	tzName    string
	filters   db.UsageExecutionFilters
}

func (s *stubUsageRollupStore) GetRollupSummary(context.Context, uuid.UUID, time.Time, time.Time) (db.RollupSummary, error) {
	return db.RollupSummary{}, nil
}

func (s *stubUsageRollupStore) GetTokenTotals(context.Context, uuid.UUID, time.Time, time.Time) (db.TokenTotals, error) {
	return db.TokenTotals{}, nil
}

func (s *stubUsageRollupStore) GetTimeseries(_ context.Context, _ uuid.UUID, _, _ time.Time, groupBy, stackBy string, userID *uuid.UUID, capacity *string, filters db.UsageExecutionFilters) ([]models.UsageTimeseriesBucket, error) {
	s.lastTimeseriesCall = usageTimeseriesCall{
		groupBy:  groupBy,
		stackBy:  stackBy,
		userID:   userID,
		capacity: capacity,
		filters:  filters,
	}
	return nil, nil
}

func (s *stubUsageRollupStore) GetBreakdown(_ context.Context, _ uuid.UUID, _, _ time.Time, dimension, sortBy string, limit int, filters db.UsageExecutionFilters) ([]models.UsageBreakdownRow, error) {
	s.lastBreakdownCall = usageBreakdownCall{
		dimension: dimension,
		sortBy:    sortBy,
		limit:     limit,
		filters:   filters,
	}
	return nil, nil
}

func (s *stubUsageRollupStore) GetExportRows(_ context.Context, _ uuid.UUID, _, _ time.Time, dimension string, filters db.UsageExecutionFilters) (pgx.Rows, error) {
	s.lastExportCall = usageExportCall{dimension: dimension, filters: filters}
	return s.exportRows, s.exportErr
}

func (s *stubUsageRollupStore) GetDailySessionCounts(_ context.Context, _ uuid.UUID, _, _ time.Time, dimension, tzName string, filters db.UsageExecutionFilters) ([]db.ExportDailySessionCountRow, error) {
	s.lastDailyCountsCall = usageDailyCountsCall{dimension: dimension, tzName: tzName, filters: filters}
	return s.dailySessionCounts, s.dailyCountsErr
}

type stubRows struct {
	rows   [][]any
	index  int
	closed bool
}

func (s *stubRows) Close() { s.closed = true }

func (s *stubRows) Err() error { return nil }

func (s *stubRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }

func (s *stubRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (s *stubRows) Next() bool {
	if s.index >= len(s.rows) {
		s.closed = true
		return false
	}
	s.index++
	return true
}

func (s *stubRows) Scan(dest ...any) error {
	row := s.rows[s.index-1]
	for i := range dest {
		switch d := dest[i].(type) {
		case *time.Time:
			*d = row[i].(time.Time)
		case *string:
			*d = row[i].(string)
		case *float64:
			*d = row[i].(float64)
		case *int:
			*d = row[i].(int)
		case *int64:
			*d = row[i].(int64)
		default:
			return nil
		}
	}
	return nil
}

func (s *stubRows) Values() ([]any, error) { return s.rows[s.index-1], nil }

func (s *stubRows) RawValues() [][]byte { return nil }

func (s *stubRows) Conn() *pgx.Conn { return nil }

func TestUsageHandler_GetSummary(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewContainerUsageStore(mock)
	handler := NewUsageHandler(store)

	orgID := uuid.New()

	// Totals
	mock.ExpectQuery("SELECT COALESCE\\(SUM").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_sessions"}).AddRow(42.5, 5))

	// Capacity breakdown
	mock.ExpectQuery("SELECT cpu_limit, memory_limit_mb").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"cpu_limit", "memory_limit_mb", "minutes", "sessions"}).
			AddRow(2.0, 4096, 42.5, 5))

	// Peak concurrent
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(concurrent\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"peak"}).AddRow(2))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage?start=2026-04-01T00:00:00Z&end=2026-05-01T00:00:00Z", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.GetSummary(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.SingleResponse[models.UsageSummary]
	err = json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, 42.5, resp.Data.TotalContainerMinutes)
	require.Equal(t, 5, resp.Data.TotalSessions)
	require.Equal(t, 3, resp.Data.PeakConcurrent) // 2 peers + 1
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageHandler_ListBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewContainerUsageStore(mock)
	handler := NewUsageHandler(store)

	orgID := uuid.New()
	sessionID := uuid.New()
	eventID := uuid.New()
	now := time.Now()
	dur := int64(60000)
	mins := 1.0
	reason := "completed"

	cols := []string{
		"id", "org_id", "session_id", "container_id", "provider",
		"cpu_limit", "memory_limit_mb", "image",
		"started_at", "stopped_at", "duration_ms", "container_minutes",
		"exit_reason", "created_at",
	}
	mock.ExpectQuery("SELECT .+ FROM container_usage_events WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(cols).AddRow(
				eventID, orgID, sessionID, "ctr-1", "docker",
				2.0, 4096, "143-sandbox:latest",
				now, &now, &dur, &mins, &reason, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/usage", nil)
	// Set up chi route context with the "id" parameter.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.ListBySession(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.ListResponse[models.ContainerUsageEvent]
	err = json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "ctr-1", resp.Data[0].ContainerID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestParseTimeRange_Defaults(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries", nil)
	rr := httptest.NewRecorder()
	start, end, ok := parseTimeRange(rr, req)
	require.True(t, ok)
	require.True(t, start.Before(end))
	require.InDelta(t, 30*24, end.Sub(start).Hours(), 1)
}

func TestParseTimeRange_InvalidStart(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=bad", nil)
	rr := httptest.NewRecorder()
	_, _, ok := parseTimeRange(rr, req)
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestParseTimeRange_InvalidEnd(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=bad", nil)
	rr := httptest.NewRecorder()
	_, _, ok := parseTimeRange(rr, req)
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestParseTimeRange_StartAfterEnd(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-05-01T00:00:00Z&end=2026-04-01T00:00:00Z", nil)
	rr := httptest.NewRecorder()
	_, _, ok := parseTimeRange(rr, req)
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestParseTimeRange_ExceedsMaxRange(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-01-01T00:00:00Z&end=2026-12-01T00:00:00Z", nil)
	rr := httptest.NewRecorder()
	_, _, ok := parseTimeRange(rr, req)
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_GetTimeseries_NoRollupStore(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	// rollupStore is nil
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetTimeseries(rr, req)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestUsageHandler_GetTimeseries_WithStore(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetTimeseries(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestUsageHandler_GetTimeseries_ExecutionFiltersAndStackBy(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	rollup := &stubUsageRollupStore{}
	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(rollup))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&stack_by=model&agent=codex&reasoning=high", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.GetTimeseries(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "GetTimeseries should accept execution filters and stack_by")
	require.Equal(t, "hour", rollup.lastTimeseriesCall.groupBy, "GetTimeseries should default group_by to hour")
	require.Equal(t, "model", rollup.lastTimeseriesCall.stackBy, "GetTimeseries should forward stack_by")
	require.Equal(t, "codex", derefString(rollup.lastTimeseriesCall.filters.Agent), "GetTimeseries should forward agent filter")
	require.Equal(t, "high", derefString(rollup.lastTimeseriesCall.filters.Reasoning), "GetTimeseries should forward reasoning filter")
}

func TestUsageHandler_GetTimeseries_RejectsUserAndExecutionFilters(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	userID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&user_id="+userID.String()+"&agent=codex", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.GetTimeseries(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "GetTimeseries should reject mixing user filters with execution filters")
}

func TestUsageHandler_GetBreakdown_NoRollupStore(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/breakdown?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetBreakdown(rr, req)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestUsageHandler_GetBreakdown_WithStore(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/breakdown?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&dimension=capacity&sort=sessions_desc&limit=10", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetBreakdown(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestUsageHandler_GetBreakdown_AgentDimensionAndFilters(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	rollup := &stubUsageRollupStore{}
	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(rollup))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/breakdown?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&dimension=agent&sort=tokens_desc&limit=25&reasoning=default", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.GetBreakdown(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "GetBreakdown should accept agent dimension")
	require.Equal(t, "agent", rollup.lastBreakdownCall.dimension, "GetBreakdown should forward the agent dimension")
	require.Equal(t, "tokens_desc", rollup.lastBreakdownCall.sortBy, "GetBreakdown should forward the sort")
	require.Equal(t, 25, rollup.lastBreakdownCall.limit, "GetBreakdown should forward the limit")
	require.Equal(t, "default", derefString(rollup.lastBreakdownCall.filters.Reasoning), "GetBreakdown should forward reasoning filters")
}

func TestUsageHandler_GetBreakdown_RejectsUserDimensionExecutionFilters(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/breakdown?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&dimension=user&agent=codex", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.GetBreakdown(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "GetBreakdown should reject execution filters on user dimension")
}

func TestUsageHandler_ExportCSV_NoRollupStore(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestUsageHandler_ExportCSV_ForwardsExecutionFilters(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	rollup := &stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "codex", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
			},
		},
	}
	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(rollup))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&dimension=agent&agent=codex&reasoning=high", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "ExportCSV should allow agent dimension")
	require.Equal(t, "agent", rollup.lastExportCall.dimension, "ExportCSV should forward the requested dimension")
	require.Equal(t, "codex", derefString(rollup.lastExportCall.filters.Agent), "ExportCSV should forward the agent filter")
	require.Equal(t, "high", derefString(rollup.lastExportCall.filters.Reasoning), "ExportCSV should forward the reasoning filter")
}

func TestUsageHandler_ExportCSV_DailyAgentIncludesDimensionColumnAndForwardsFiltersToDailyCounts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	rollup := &stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "codex", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
			},
		},
		dailySessionCounts: []db.ExportDailySessionCountRow{
			{LocalDate: "2026-04-01", CapacityTier: "codex", Sessions: 1},
		},
	}
	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(rollup))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&dimension=agent&granularity=daily&agent=codex&reasoning=high&tz=UTC", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "ExportCSV should succeed for daily agent exports")
	require.Equal(t, "agent", rollup.lastDailyCountsCall.dimension, "ExportCSV should request daily counts for the requested dimension")
	require.Equal(t, "UTC", rollup.lastDailyCountsCall.tzName, "ExportCSV should forward the timezone for daily counts")
	require.Equal(t, "codex", derefString(rollup.lastDailyCountsCall.filters.Agent), "ExportCSV should forward the agent filter to daily counts")
	require.Equal(t, "high", derefString(rollup.lastDailyCountsCall.filters.Reasoning), "ExportCSV should forward the reasoning filter to daily counts")
	require.Contains(t, rr.Body.String(), "date,agent,container_minutes,sessions,container_starts,peak_concurrent,input_tokens,output_tokens,llm_cost_usd", "ExportCSV should include the agent header column")
	require.Contains(t, rr.Body.String(), "2026-04-01,codex,30.00,1,1,1,100,50,0.25", "ExportCSV should write the agent key into daily records")
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// stubUsageMembershipStore implements usageMembershipStore for tests.
// errOverride lets a test exercise the non-ErrNoRows membership-error branch.
type stubUsageMembershipStore struct {
	allowed     map[[2]uuid.UUID]bool
	errOverride error
}

func (s *stubUsageMembershipStore) Get(_ context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	if s.errOverride != nil {
		return models.OrganizationMembership{}, s.errOverride
	}
	if s.allowed[[2]uuid.UUID{userID, orgID}] {
		return models.OrganizationMembership{UserID: userID, OrgID: orgID, Role: "member"}, nil
	}
	return models.OrganizationMembership{}, pgx.ErrNoRows
}

// Regression: window.open downloads can't send X-Active-Org-ID. Multi-org
// users whose context org (resolved from session last_org_id) differs from
// their actively-viewed org would otherwise silently get the wrong org's CSV.
// The handler must honour ?org_id= when the user has membership in that org.
func TestUsageHandler_ExportCSV_OrgIDQueryFallback(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	contextOrgID := uuid.New()   // wrong: session last_org_id
	requestedOrgID := uuid.New() // right: actively-viewed org
	userID := uuid.New()

	handler := NewUsageHandler(
		db.NewContainerUsageStore(mock),
		WithRollupStore(&stubUsageRollupStore{
			exportRows: &stubRows{
				rows: [][]any{
					{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
				},
			},
		}),
		WithMembershipStore(&stubUsageMembershipStore{
			allowed: map[[2]uuid.UUID]bool{{userID, requestedOrgID}: true},
		}),
	)

	url := "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	ctx := middleware.WithOrgID(req.Context(), contextOrgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestUsageHandler_ExportCSV_OrgIDQueryNonMember(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	requestedOrgID := uuid.New()
	userID := uuid.New()

	handler := NewUsageHandler(
		db.NewContainerUsageStore(mock),
		WithRollupStore(&stubUsageRollupStore{}),
		WithMembershipStore(&stubUsageMembershipStore{allowed: map[[2]uuid.UUID]bool{}}),
	)

	url := "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Body.String(), "FORBIDDEN")
}

// When ?org_id= matches the context-resolved org, the helper short-circuits
// without touching the membership store. Covers the equality branch in
// exportOrgID.
func TestUsageHandler_ExportCSV_OrgIDQueryMatchesContext(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	// Intentionally no WithMembershipStore: the equality short-circuit must
	// return before the membership lookup.
	handler := NewUsageHandler(
		db.NewContainerUsageStore(mock),
		WithRollupStore(&stubUsageRollupStore{
			exportRows: &stubRows{
				rows: [][]any{
					{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
				},
			},
		}),
	)

	url := "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&org_id=" + orgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestUsageHandler_ExportCSV_OrgIDQueryMalformed(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(
		db.NewContainerUsageStore(mock),
		WithRollupStore(&stubUsageRollupStore{}),
	)

	url := "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&org_id=not-a-uuid"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_ORG")
}

func TestUsageHandler_ExportCSV_OrgIDQueryMissingUser(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	requestedOrgID := uuid.New()
	handler := NewUsageHandler(
		db.NewContainerUsageStore(mock),
		WithRollupStore(&stubUsageRollupStore{}),
		WithMembershipStore(&stubUsageMembershipStore{allowed: map[[2]uuid.UUID]bool{}}),
	)

	url := "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	// No user in context — exercises the errUsageExportUnauthorized path.
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	require.Contains(t, rr.Body.String(), "UNAUTHORIZED")
}

func TestUsageHandler_ExportCSV_OrgIDQueryMembershipNotConfigured(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	requestedOrgID := uuid.New()
	userID := uuid.New()
	// No WithMembershipStore — programmer error.
	handler := NewUsageHandler(
		db.NewContainerUsageStore(mock),
		WithRollupStore(&stubUsageRollupStore{}),
	)

	url := "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Contains(t, rr.Body.String(), "INTERNAL")
}

func TestUsageHandler_ExportCSV_OrgIDQueryMembershipStoreError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	requestedOrgID := uuid.New()
	userID := uuid.New()
	handler := NewUsageHandler(
		db.NewContainerUsageStore(mock),
		WithRollupStore(&stubUsageRollupStore{}),
		WithMembershipStore(&stubUsageMembershipStore{
			allowed:     map[[2]uuid.UUID]bool{},
			errOverride: errors.New("db unreachable"),
		}),
	)

	url := "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	ctx := middleware.WithOrgID(req.Context(), uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Contains(t, rr.Body.String(), "INTERNAL")
}

func TestUsageHandler_ExportCSV_HourlyGranularity(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
			},
		},
	}))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&granularity=hourly&dimension=none&tz=UTC", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	reader := csv.NewReader(strings.NewReader(rr.Body.String()))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2) // header + 1 data row
	require.Equal(t, "hour_utc", records[0][1])
}

func TestUsageHandler_ExportCSV_UserDimension(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "user@test.com", "", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
			},
		},
		dailySessionCounts: []db.ExportDailySessionCountRow{
			{LocalDate: "2026-04-01", UserEmail: "user@test.com", Sessions: 1},
		},
	}))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&granularity=daily&dimension=user&tz=UTC", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	reader := csv.NewReader(strings.NewReader(rr.Body.String()))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "user_email", records[0][1])
}

func TestUsageHandler_ExportCSV_CapacityDimension(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "2cpu_4096mb", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
			},
		},
		dailySessionCounts: []db.ExportDailySessionCountRow{
			{LocalDate: "2026-04-01", CapacityTier: "2cpu_4096mb", Sessions: 1},
		},
	}))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&granularity=daily&dimension=capacity&tz=UTC", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	reader := csv.NewReader(strings.NewReader(rr.Body.String()))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "capacity_tier", records[0][1])
}

func TestUsageHandler_ExportCSV_InvalidTimezone(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&tz=Invalid/Zone", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_GetSummary_InvalidStart(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage?start=bad", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetSummary(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_GetSummary_InvalidEnd(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage?start=2026-04-01T00:00:00Z&end=bad", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetSummary(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_GetSummary_StartAfterEnd(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage?start=2026-05-01T00:00:00Z&end=2026-04-01T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetSummary(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_GetSummary_ExceedsMaxRange(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage?start=2026-01-01T00:00:00Z&end=2026-12-01T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetSummary(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_GetTimeseries_WithUserID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	userID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&user_id="+userID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetTimeseries(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestUsageHandler_GetTimeseries_InvalidUserID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&user_id=not-a-uuid", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetTimeseries(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_GetTimeseries_WithCapacity(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&capacity=2cpu_4096mb", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetTimeseries(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestUsageHandler_GetTimeseries_UserIDAndCapacityMutuallyExclusive(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	userID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&user_id="+userID.String()+"&capacity=2cpu_4096mb", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetTimeseries(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_GetBreakdown_DefaultParams(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{}))
	orgID := uuid.New()
	// No dimension, sort, or limit params — tests defaults
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/breakdown?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetBreakdown(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestUsageHandler_ListBySession_InvalidID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/not-a-uuid/usage", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.ListBySession(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_ExportCSV_DefaultGranularity(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock), WithRollupStore(&stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
			},
		},
	}))
	orgID := uuid.New()
	// No granularity param — defaults to "daily"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	reader := csv.NewReader(strings.NewReader(rr.Body.String()))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)
	// Default daily — no hour_utc column
	require.Equal(t, "date", records[0][0])
	require.Equal(t, "container_minutes", records[0][1])
}

func TestUsageHandler_ExportCSV_DailyDoesNotDoubleCountSessionsAcrossHours(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	usageStore := db.NewContainerUsageStore(mock)
	handler := NewUsageHandler(usageStore, WithRollupStore(&stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
				{time.Date(2026, 4, 1, 1, 0, 0, 0, time.UTC), "", "", 15.0, 1, 0, 1, int64(0), int64(0), 0.00},
			},
		},
		dailySessionCounts: []db.ExportDailySessionCountRow{
			{LocalDate: "2026-04-01", Sessions: 1},
		},
	}))

	orgID := uuid.New()
	start := "2026-04-01T00:00:00Z"
	end := "2026-04-02T00:00:00Z"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start="+start+"&end="+end+"&granularity=daily&dimension=none&tz=UTC", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.ExportCSV(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "daily export should return HTTP 200")

	reader := csv.NewReader(strings.NewReader(rr.Body.String()))
	records, err := reader.ReadAll()
	require.NoError(t, err, "daily export should return valid CSV")
	require.Len(t, records, 2, "daily export should include header plus one data row")
	require.Equal(t, "sessions", records[0][2], "daily export should include the sessions column")
	require.Equal(t, "1", records[1][2], "daily export should count a cross-hour session once per day")
}
