package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
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
	exportRows         pgx.Rows
	dailySessionCounts []db.ExportDailySessionCountRow
	exportErr          error
	dailyCountsErr     error
}

func (s *stubUsageRollupStore) GetTokenTotals(context.Context, uuid.UUID, time.Time, time.Time) (db.TokenTotals, error) {
	return db.TokenTotals{}, nil
}

func (s *stubUsageRollupStore) GetTimeseries(context.Context, uuid.UUID, time.Time, time.Time, string, *uuid.UUID, *string) ([]models.UsageTimeseriesBucket, error) {
	return nil, nil
}

func (s *stubUsageRollupStore) GetBreakdown(context.Context, uuid.UUID, time.Time, time.Time, string, string, int) ([]models.UsageBreakdownRow, error) {
	return nil, nil
}

func (s *stubUsageRollupStore) GetExportRows(context.Context, uuid.UUID, time.Time, time.Time, string) (pgx.Rows, error) {
	return s.exportRows, s.exportErr
}

func (s *stubUsageRollupStore) GetDailySessionCounts(context.Context, uuid.UUID, time.Time, time.Time, string, string) ([]db.ExportDailySessionCountRow, error) {
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
	start, end, ok := parseTimeRange(req, rr)
	require.True(t, ok)
	require.True(t, start.Before(end))
	require.InDelta(t, 30*24, end.Sub(start).Hours(), 1)
}

func TestParseTimeRange_InvalidStart(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=bad", nil)
	rr := httptest.NewRecorder()
	_, _, ok := parseTimeRange(req, rr)
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestParseTimeRange_InvalidEnd(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=bad", nil)
	rr := httptest.NewRecorder()
	_, _, ok := parseTimeRange(req, rr)
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestParseTimeRange_StartAfterEnd(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-05-01T00:00:00Z&end=2026-04-01T00:00:00Z", nil)
	rr := httptest.NewRecorder()
	_, _, ok := parseTimeRange(req, rr)
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestParseTimeRange_ExceedsMaxRange(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-01-01T00:00:00Z&end=2026-12-01T00:00:00Z", nil)
	rr := httptest.NewRecorder()
	_, _, ok := parseTimeRange(req, rr)
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

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	handler.SetRollupStore(&stubUsageRollupStore{})
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/timeseries?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetTimeseries(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
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

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	handler.SetRollupStore(&stubUsageRollupStore{})
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/breakdown?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&dimension=capacity&sort=sessions_desc&limit=10", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.GetBreakdown(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
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

func TestUsageHandler_ExportCSV_HourlyGranularity(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	handler.SetRollupStore(&stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
			},
		},
	})
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

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	handler.SetRollupStore(&stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "user@test.com", "", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
			},
		},
		dailySessionCounts: []db.ExportDailySessionCountRow{
			{LocalDate: "2026-04-01", UserEmail: "user@test.com", Sessions: 1},
		},
	})
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

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	handler.SetRollupStore(&stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "2cpu_4096mb", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
			},
		},
		dailySessionCounts: []db.ExportDailySessionCountRow{
			{LocalDate: "2026-04-01", CapacityTier: "2cpu_4096mb", Sessions: 1},
		},
	})
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

	handler := NewUsageHandler(db.NewContainerUsageStore(mock))
	handler.SetRollupStore(&stubUsageRollupStore{})
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?start=2026-04-01T00:00:00Z&end=2026-04-02T00:00:00Z&tz=Invalid/Zone", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()
	handler.ExportCSV(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUsageHandler_ExportCSV_DailyDoesNotDoubleCountSessionsAcrossHours(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	usageStore := db.NewContainerUsageStore(mock)
	handler := NewUsageHandler(usageStore)
	handler.SetRollupStore(&stubUsageRollupStore{
		exportRows: &stubRows{
			rows: [][]any{
				{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "", "", 30.0, 1, 1, 1, int64(100), int64(50), 0.25},
				{time.Date(2026, 4, 1, 1, 0, 0, 0, time.UTC), "", "", 15.0, 1, 0, 1, int64(0), int64(0), 0.00},
			},
		},
		dailySessionCounts: []db.ExportDailySessionCountRow{
			{LocalDate: "2026-04-01", Sessions: 1},
		},
	})

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
