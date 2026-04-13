package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestComputePeakConcurrent_Empty(t *testing.T) {
	t.Parallel()
	require.Equal(t, 0, computePeakConcurrent(nil))
	require.Equal(t, 0, computePeakConcurrent([]timeInterval{}))
}

func TestComputePeakConcurrent_Single(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	ivs := []timeInterval{{start: base, stop: base.Add(10 * time.Minute)}}
	require.Equal(t, 1, computePeakConcurrent(ivs))
}

func TestComputePeakConcurrent_NonOverlapping(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	ivs := []timeInterval{
		{start: base, stop: base.Add(10 * time.Minute)},
		{start: base.Add(20 * time.Minute), stop: base.Add(30 * time.Minute)},
	}
	require.Equal(t, 1, computePeakConcurrent(ivs))
}

func TestComputePeakConcurrent_FullyOverlapping(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	ivs := []timeInterval{
		{start: base, stop: base.Add(30 * time.Minute)},
		{start: base.Add(5 * time.Minute), stop: base.Add(25 * time.Minute)},
		{start: base.Add(10 * time.Minute), stop: base.Add(20 * time.Minute)},
	}
	require.Equal(t, 3, computePeakConcurrent(ivs))
}

func TestComputePeakConcurrent_PartialOverlap(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	ivs := []timeInterval{
		{start: base, stop: base.Add(15 * time.Minute)},
		{start: base.Add(10 * time.Minute), stop: base.Add(25 * time.Minute)},
		{start: base.Add(20 * time.Minute), stop: base.Add(35 * time.Minute)},
	}
	// Only 2 overlap at any point (first+second, then second+third).
	require.Equal(t, 2, computePeakConcurrent(ivs))
}

func TestComputePeakConcurrent_SameStartTime(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	ivs := []timeInterval{
		{start: base, stop: base.Add(10 * time.Minute)},
		{start: base, stop: base.Add(20 * time.Minute)},
		{start: base, stop: base.Add(5 * time.Minute)},
	}
	require.Equal(t, 3, computePeakConcurrent(ivs))
}

func TestComputePeakConcurrent_StartEqualsStop(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	// One ends exactly when the other starts — starts are processed before stops at same time.
	ivs := []timeInterval{
		{start: base, stop: base.Add(10 * time.Minute)},
		{start: base.Add(10 * time.Minute), stop: base.Add(20 * time.Minute)},
	}
	require.Equal(t, 2, computePeakConcurrent(ivs))
}

func TestComputeDurationStats_Empty(t *testing.T) {
	t.Parallel()
	avg, p95 := computeDurationStats(nil)
	require.Equal(t, 0.0, avg)
	require.Equal(t, 0.0, p95)
}

func TestComputeDurationStats_Single(t *testing.T) {
	t.Parallel()
	avg, p95 := computeDurationStats([]float64{42.0})
	require.Equal(t, 42.0, avg)
	require.Equal(t, 42.0, p95)
}

func TestComputeDurationStats_Two(t *testing.T) {
	t.Parallel()
	avg, p95 := computeDurationStats([]float64{10.0, 20.0})
	require.Equal(t, 15.0, avg)
	require.Equal(t, 20.0, p95)
}

func TestComputeDurationStats_Twenty(t *testing.T) {
	t.Parallel()
	// 20 values: 1..20
	durations := make([]float64, 20)
	for i := range durations {
		durations[i] = float64(i + 1)
	}
	avg, p95 := computeDurationStats(durations)
	require.Equal(t, 10.5, avg)
	// p95 index: ceil(20*0.95)-1 = ceil(19)-1 = 18 → sorted[18] = 19
	require.Equal(t, 19.0, p95)
}

func TestComputeDurationStats_DoesNotMutateInput(t *testing.T) {
	t.Parallel()
	original := []float64{3.0, 1.0, 2.0}
	computeDurationStats(original)
	require.Equal(t, []float64{3.0, 1.0, 2.0}, original)
}

func TestNewUsageRollupStore(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	require.NotNil(t, store)
}

func TestGetTokenTotals(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnRows(pgxmock.NewRows([]string{"input", "output", "cost"}).
			AddRow(int64(5000), int64(2000), 1.25))

	totals, err := store.GetTokenTotals(context.Background(), orgID, start, end)
	require.NoError(t, err)
	require.Equal(t, int64(5000), totals.InputTokens)
	require.Equal(t, int64(2000), totals.OutputTokens)
	require.Equal(t, 1.25, totals.CostUSD)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTokenTotals_Error(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnError(pgx.ErrNoRows)

	_, err = store.GetTokenTotals(context.Background(), orgID, start, end)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteOlderThan(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectExec("DELETE FROM usage_hourly").
		WithArgs(pgx.NamedArgs{"cutoff": cutoff}).
		WillReturnResult(pgxmock.NewResult("DELETE", 42))

	count, err := store.DeleteOlderThan(context.Background(), cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(42), count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteOlderThan_Error(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectExec("DELETE FROM usage_hourly").
		WithArgs(pgx.NamedArgs{"cutoff": cutoff}).
		WillReturnError(pgx.ErrTxClosed)

	_, err = store.DeleteOlderThan(context.Background(), cutoff)
	require.Error(t, err)
	require.Contains(t, err.Error(), "delete old usage_hourly")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetExportRows_None(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnRows(pgxmock.NewRows([]string{
			"hour_utc", "user_email", "capacity_tier",
			"total_container_minutes", "total_sessions", "total_container_starts",
			"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
		}))

	rows, err := store.GetExportRows(context.Background(), orgID, start, end, "none")
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetExportRows_User(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnRows(pgxmock.NewRows([]string{
			"hour_utc", "user_email", "capacity_tier",
			"total_container_minutes", "total_sessions", "total_container_starts",
			"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
		}).AddRow(
			time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "alice@test.com", "",
			30.0, 1, 1, 1, int64(100), int64(50), 0.25,
		))

	rows, err := store.GetExportRows(context.Background(), orgID, start, end, "user")
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetExportRows_Capacity(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnRows(pgxmock.NewRows([]string{
			"hour_utc", "user_email", "capacity_tier",
			"total_container_minutes", "total_sessions", "total_container_starts",
			"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
		}))

	rows, err := store.GetExportRows(context.Background(), orgID, start, end, "capacity")
	require.NoError(t, err)
	defer rows.Close()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetDailySessionCounts_Default(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("WITH days AS").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "tz": "UTC"}).
		WillReturnRows(pgxmock.NewRows([]string{"local_date", "user_email", "capacity_tier", "sessions"}).
			AddRow("2026-04-01", "", "", 5))

	counts, err := store.GetDailySessionCounts(context.Background(), orgID, start, end, "none", "UTC")
	require.NoError(t, err)
	require.Len(t, counts, 1)
	require.Equal(t, "2026-04-01", counts[0].LocalDate)
	require.Equal(t, 5, counts[0].Sessions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetDailySessionCounts_User(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("WITH days AS").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "tz": "UTC"}).
		WillReturnRows(pgxmock.NewRows([]string{"local_date", "user_email", "capacity_tier", "sessions"}).
			AddRow("2026-04-01", "alice@test.com", "", 3))

	counts, err := store.GetDailySessionCounts(context.Background(), orgID, start, end, "user", "UTC")
	require.NoError(t, err)
	require.Len(t, counts, 1)
	require.Equal(t, "alice@test.com", counts[0].UserEmail)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetDailySessionCounts_Capacity(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("WITH days AS").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "tz": "UTC"}).
		WillReturnRows(pgxmock.NewRows([]string{"local_date", "user_email", "capacity_tier", "sessions"}).
			AddRow("2026-04-01", "", "2cpu_4096mb", 2))

	counts, err := store.GetDailySessionCounts(context.Background(), orgID, start, end, "capacity", "UTC")
	require.NoError(t, err)
	require.Len(t, counts, 1)
	require.Equal(t, "2cpu_4096mb", counts[0].CapacityTier)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetDailySessionCounts_QueryError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("WITH days AS").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "tz": "UTC"}).
		WillReturnError(pgx.ErrTxClosed)

	_, err = store.GetDailySessionCounts(context.Background(), orgID, start, end, "none", "UTC")
	require.Error(t, err)
	require.Contains(t, err.Error(), "query daily session counts")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRollupAllOrgs_NoActiveOrgs(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	hour := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT DISTINCT org_id").
		WithArgs(pgx.NamedArgs{"hour_start": hour, "hour_end": hour.Add(time.Hour)}).
		WillReturnRows(pgxmock.NewRows([]string{"org_id"}))

	err = store.RollupAllOrgs(context.Background(), hour)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRollupAllOrgs_QueryError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	hour := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT DISTINCT org_id").
		WithArgs(pgx.NamedArgs{"hour_start": hour, "hour_end": hour.Add(time.Hour)}).
		WillReturnError(pgx.ErrTxClosed)

	err = store.RollupAllOrgs(context.Background(), hour)
	require.Error(t, err)
	require.Contains(t, err.Error(), "list active orgs")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTimeseries_Default(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	cols := []string{
		"hour_utc", "user_id", "user_name", "capacity_tier",
		"total_container_minutes", "total_sessions", "total_container_starts",
		"peak_concurrent", "avg_duration_sec", "p95_duration_sec",
		"total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
	}
	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnRows(pgxmock.NewRows(cols).AddRow(
			start, nil, "", (*string)(nil),
			60.0, 5, 5, 2, 120.0, 300.0,
			int64(1000), int64(500), 0.50,
		))

	buckets, err := store.GetTimeseries(context.Background(), orgID, start, end, "hour", nil, nil)
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.Equal(t, 60.0, buckets[0].TotalContainerMinutes)
	require.Equal(t, 5, buckets[0].TotalSessions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTimeseries_QueryError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnError(pgx.ErrTxClosed)

	_, err = store.GetTimeseries(context.Background(), orgID, start, end, "hour", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "query timeseries")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTimeseries_UserGroupBy(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	cols := []string{
		"hour_utc", "user_id", "user_name", "capacity_tier",
		"total_container_minutes", "total_sessions", "total_container_starts",
		"peak_concurrent", "avg_duration_sec", "p95_duration_sec",
		"total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
	}
	uid := uuid.New()
	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnRows(pgxmock.NewRows(cols).AddRow(
			start, &uid, "alice", (*string)(nil),
			30.0, 2, 2, 1, 60.0, 120.0,
			int64(500), int64(250), 0.25,
		))

	buckets, err := store.GetTimeseries(context.Background(), orgID, start, end, "user", nil, nil)
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.Equal(t, "alice", buckets[0].UserName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetBreakdown_UserDimension(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	cols := []string{
		"key", "label",
		"total_container_minutes", "total_sessions", "total_container_starts",
		"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
	}
	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "limit": 50}).
		WillReturnRows(pgxmock.NewRows(cols).
			AddRow("u1", "alice@test.com", 60.0, 3, 3, 1, int64(1000), int64(500), 0.50).
			AddRow("u2", "bob@test.com", 40.0, 2, 2, 1, int64(800), int64(400), 0.30))

	rows, err := store.GetBreakdown(context.Background(), orgID, start, end, "user", "minutes_desc", 50)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "alice@test.com", rows[0].Label)
	require.Equal(t, 60.0, rows[0].Percentage)  // 60/100 * 100 = 60.0%
	require.Equal(t, 40.0, rows[1].Percentage)  // 40/100 * 100 = 40.0%
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetBreakdown_CapacityDimension(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	cols := []string{
		"key", "label",
		"total_container_minutes", "total_sessions", "total_container_starts",
		"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
	}
	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "limit": 10}).
		WillReturnRows(pgxmock.NewRows(cols).
			AddRow("2cpu_4096mb", "2cpu_4096mb", 100.0, 5, 5, 2, int64(2000), int64(1000), 1.0))

	rows, err := store.GetBreakdown(context.Background(), orgID, start, end, "capacity", "sessions_desc", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 100.0, rows[0].Percentage)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetBreakdown_QueryError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "limit": 50}).
		WillReturnError(pgx.ErrTxClosed)

	_, err = store.GetBreakdown(context.Background(), orgID, start, end, "user", "minutes_desc", 50)
	require.Error(t, err)
	require.Contains(t, err.Error(), "query breakdown")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetBreakdown_EmptyResult(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	cols := []string{
		"key", "label",
		"total_container_minutes", "total_sessions", "total_container_starts",
		"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
	}
	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "limit": 50}).
		WillReturnRows(pgxmock.NewRows(cols))

	rows, err := store.GetBreakdown(context.Background(), orgID, start, end, "user", "tokens_desc", 50)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}
