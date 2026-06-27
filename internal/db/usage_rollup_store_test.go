package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
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
	mock.ExpectExec("DELETE FROM usage_hourly_execution").
		WithArgs(pgx.NamedArgs{"cutoff": cutoff}).
		WillReturnResult(pgxmock.NewResult("DELETE", 21))

	count, err := store.DeleteOlderThan(context.Background(), cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(63), count)
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

func usageTimeseriesTestColumns() []string {
	return []string{
		"hour_utc", "user_id", "user_name", "capacity_tier",
		"agent_type", "model_used", "reasoning_effort", "series_key", "series_label",
		"total_container_minutes", "total_sessions", "total_container_starts",
		"peak_concurrent", "avg_duration_sec", "p95_duration_sec",
		"total_input_tokens", "total_output_tokens", "total_tokens", "total_llm_cost_usd",
	}
}

func testStringPtr(value string) *string {
	return &value
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

	rows, err := store.GetExportRows(context.Background(), orgID, start, end, "none", UsageExecutionFilters{})
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

	rows, err := store.GetExportRows(context.Background(), orgID, start, end, "user", UsageExecutionFilters{})
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

	mock.ExpectQuery("SELECT uhe.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "all_capacity": usageAllCapacityKey}).
		WillReturnRows(pgxmock.NewRows([]string{
			"hour_utc", "user_email", "capacity_tier",
			"total_container_minutes", "total_sessions", "total_container_starts",
			"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
		}))

	rows, err := store.GetExportRows(context.Background(), orgID, start, end, "capacity", UsageExecutionFilters{})
	require.NoError(t, err)
	defer rows.Close()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetExportRows_Capacity_WithFilters(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	agent := "codex"
	reasoning := "high"

	mock.ExpectQuery("FROM usage_hourly_execution uhe").
		WithArgs(pgx.NamedArgs{
			"org_id":       orgID,
			"start":        start,
			"end":          end,
			"agent":        agent,
			"reasoning":    reasoning,
			"all_capacity": usageAllCapacityKey,
		}).
		WillReturnRows(pgxmock.NewRows([]string{
			"hour_utc", "user_email", "capacity_tier",
			"total_container_minutes", "total_sessions", "total_container_starts",
			"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_llm_cost_usd",
		}))

	rows, err := store.GetExportRows(context.Background(), orgID, start, end, "capacity", UsageExecutionFilters{
		Agent:     &agent,
		Reasoning: &reasoning,
	})
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

	counts, err := store.GetDailySessionCounts(context.Background(), orgID, start, end, "none", "UTC", UsageExecutionFilters{})
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

	counts, err := store.GetDailySessionCounts(context.Background(), orgID, start, end, "user", "UTC", UsageExecutionFilters{})
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

	// Regression guard: capacity tiers include CPU, memory, and disk.
	mock.ExpectQuery(`format\('%scpu_%smb_%sdiskmb', round\(e\.cpu_limit\)::int, e\.memory_limit_mb, e\.disk_limit_mb\)`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "tz": "UTC"}).
		WillReturnRows(pgxmock.NewRows([]string{"local_date", "user_email", "capacity_tier", "sessions"}).
			AddRow("2026-04-01", "", "2cpu_4096mb_10240diskmb", 2))

	counts, err := store.GetDailySessionCounts(context.Background(), orgID, start, end, "capacity", "UTC", UsageExecutionFilters{})
	require.NoError(t, err)
	require.Len(t, counts, 1)
	require.Equal(t, "2cpu_4096mb_10240diskmb", counts[0].CapacityTier)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetDailySessionCounts_Capacity_WithFilters(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	agent := "codex"

	mock.ExpectQuery("JOIN sessions s").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "tz": "UTC", "agent": agent}).
		WillReturnRows(pgxmock.NewRows([]string{"local_date", "user_email", "capacity_tier", "sessions"}).
			AddRow("2026-04-01", "", "2cpu_4096mb_10240diskmb", 2))

	counts, err := store.GetDailySessionCounts(context.Background(), orgID, start, end, "capacity", "UTC", UsageExecutionFilters{Agent: &agent})
	require.NoError(t, err)
	require.Len(t, counts, 1)
	require.Equal(t, 2, counts[0].Sessions)
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

	_, err = store.GetDailySessionCounts(context.Background(), orgID, start, end, "none", "UTC", UsageExecutionFilters{})
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

	mock.ExpectQuery("FROM session_messages").
		WithArgs(pgx.NamedArgs{"start": hour, "end": hour.Add(time.Hour)}).
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

	mock.ExpectQuery("FROM session_messages").
		WithArgs(pgx.NamedArgs{"start": hour, "end": hour.Add(time.Hour)}).
		WillReturnError(pgx.ErrTxClosed)

	err = store.RollupAllOrgs(context.Background(), hour)
	require.Error(t, err)
	require.Contains(t, err.Error(), "list active orgs")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRollupHour_QueryError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	hour := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg()}).
		WillReturnError(pgx.ErrTxClosed)

	err = store.RollupHour(context.Background(), orgID, hour)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rollup query container events")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRollupHour_NoEvents(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	hour := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	// First query: container events — returns empty
	eventCols := []string{"id", "session_id", "user_id", "agent_type", "model_used", "reasoning_effort", "token_usage", "cpu_limit", "memory_limit_mb", "disk_limit_mb", "started_at", "stopped_at", "container_minutes", "duration_ms"}
	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg()}).
		WillReturnRows(pgxmock.NewRows(eventCols))

	// Second query: message token usage — returns empty
	tokenCols := []string{"user_id", "agent_type", "model_used", "reasoning_effort", "token_usage", "capacity_key", "input_tokens", "output_tokens", "cost_usd"}
	mock.ExpectQuery("FROM session_messages sm").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour": hour, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg(), "unknown_capacity": usageUnknownCapacityKey}).
		WillReturnRows(pgxmock.NewRows(tokenCols))

	// Transaction: batch upsert wrapped in begin/commit
	mock.ExpectBegin()
	anyArgs13 := []any{
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(),
	}
	eb := mock.ExpectBatch()
	eb.ExpectExec("INSERT INTO usage_hourly").WithArgs(anyArgs13...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = store.RollupHour(context.Background(), orgID, hour)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRollupHour_UsesSessionModelOverrideColumn(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	hour := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	eventCols := []string{"id", "session_id", "user_id", "agent_type", "model_used", "reasoning_effort", "token_usage", "cpu_limit", "memory_limit_mb", "disk_limit_mb", "started_at", "stopped_at", "container_minutes", "duration_ms"}
	mock.ExpectQuery("s\\.model_override").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg()}).
		WillReturnRows(pgxmock.NewRows(eventCols))

	tokenCols := []string{"user_id", "agent_type", "model_used", "reasoning_effort", "token_usage", "capacity_key", "input_tokens", "output_tokens", "cost_usd"}
	mock.ExpectQuery("FROM session_messages sm").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour": hour, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg(), "unknown_capacity": usageUnknownCapacityKey}).
		WillReturnRows(pgxmock.NewRows(tokenCols))

	mock.ExpectBegin()
	eb := mock.ExpectBatch()
	eb.ExpectExec("INSERT INTO usage_hourly").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = store.RollupHour(context.Background(), orgID, hour)
	require.NoError(t, err, "RollupHour should query the real sessions model column")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRollupHour_TokenQueryError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	hour := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	// First query: container events — returns empty
	eventCols := []string{"id", "session_id", "user_id", "agent_type", "model_used", "reasoning_effort", "token_usage", "cpu_limit", "memory_limit_mb", "disk_limit_mb", "started_at", "stopped_at", "container_minutes", "duration_ms"}
	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg()}).
		WillReturnRows(pgxmock.NewRows(eventCols))

	// Second query: message token usage — error
	mock.ExpectQuery("FROM session_messages sm").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour": hour, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg(), "unknown_capacity": usageUnknownCapacityKey}).
		WillReturnError(pgx.ErrTxClosed)

	err = store.RollupHour(context.Background(), orgID, hour)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rollup query token usage")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestRollupHour_UnattributedUser exercises the uuid.Nil skip branches for
// sessions without an attributed user (e.g. automation-triggered). Such rows
// must roll up at the per-tier and org-total levels, but never emit a per-user
// upsert because usage_hourly.user_id has a FK to users(id).
func TestRollupHour_UnattributedUser(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	hour := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	sessionID := uuid.New()
	eventID := uuid.New()

	// Container event with uuid.Nil user (unattributed session).
	eventCols := []string{"id", "session_id", "user_id", "agent_type", "model_used", "reasoning_effort", "token_usage", "cpu_limit", "memory_limit_mb", "disk_limit_mb", "started_at", "stopped_at", "container_minutes", "duration_ms"}
	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg()}).
		WillReturnRows(pgxmock.NewRows(eventCols).AddRow(
			eventID, sessionID, uuid.Nil, "codex", nil, nil, nil, 2.0, 4096, 10240,
			hour, hour.Add(30*time.Minute), 30.0, 1800000.0,
		))

	// Message token row also unattributed — must not emit a per-user token upsert.
	tokenCols := []string{"user_id", "agent_type", "model_used", "reasoning_effort", "token_usage", "capacity_key", "input_tokens", "output_tokens", "cost_usd"}
	mock.ExpectQuery("FROM session_messages sm").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour": hour, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg(), "unknown_capacity": usageUnknownCapacityKey}).
		WillReturnRows(pgxmock.NewRows(tokenCols).AddRow(
			uuid.Nil, "codex", nil, nil, nil, "2cpu_4096mb_10240diskmb", int64(1000), int64(500), 0.25,
		))

	// Only 2 upserts expected: Level 3 (per-tier) + Level 4 (org-total).
	// Level 1 (per-user-tier) and Level 2 (per-user) are skipped for uuid.Nil.
	mock.ExpectBegin()
	anyArgs := []any{
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(),
	}
	eb := mock.ExpectBatch()
	eb.ExpectExec("INSERT INTO usage_hourly").WithArgs(anyArgs...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	eb.ExpectExec("INSERT INTO usage_hourly").WithArgs(anyArgs...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	eb.ExpectExec("INSERT INTO usage_hourly_execution").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	eb.ExpectExec("INSERT INTO usage_hourly_execution").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = store.RollupHour(context.Background(), orgID, hour)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRollupHour_WithEvents(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	userID := uuid.New()
	hour := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	sessionID := uuid.New()
	eventID := uuid.New()
	modelUsed := "gpt-5.4"

	// First query: container events — one event
	eventCols := []string{"id", "session_id", "user_id", "agent_type", "model_used", "reasoning_effort", "token_usage", "cpu_limit", "memory_limit_mb", "disk_limit_mb", "started_at", "stopped_at", "container_minutes", "duration_ms"}
	mock.ExpectQuery("SELECT").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg()}).
		WillReturnRows(pgxmock.NewRows(eventCols).AddRow(
			eventID, sessionID, userID, "codex", &modelUsed, nil, []byte(`{"native_usage":{"model":"gpt-5.4"}}`), 2.0, 4096, 10240,
			hour, hour.Add(30*time.Minute), 30.0, 1800000.0,
		))

	// Second query: message token usage — one row. This row represents an
	// ordinary turn on a session that may still be idle rather than completed.
	tokenCols := []string{"user_id", "agent_type", "model_used", "reasoning_effort", "token_usage", "capacity_key", "input_tokens", "output_tokens", "cost_usd"}
	mock.ExpectQuery("FROM session_messages sm").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "hour": hour, "hour_start": hour, "hour_end": hour.Add(time.Hour), "now": pgxmock.AnyArg(), "unknown_capacity": usageUnknownCapacityKey}).
		WillReturnRows(pgxmock.NewRows(tokenCols).AddRow(
			userID, "codex", &modelUsed, nil, []byte(`{"native_usage":{"model":"gpt-5.4"}}`), "2cpu_4096mb_10240diskmb", int64(1000), int64(500), 0.25,
		))

	// Transaction: batch upsert wrapped in begin/commit
	// 4 rows: per-user-tier, per-user, per-tier, org-total
	mock.ExpectBegin()
	anyArgs := []any{
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(),
	}
	eb2 := mock.ExpectBatch()
	eb2.ExpectExec("INSERT INTO usage_hourly").WithArgs(anyArgs...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	eb2.ExpectExec("INSERT INTO usage_hourly").WithArgs(anyArgs...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	eb2.ExpectExec("INSERT INTO usage_hourly").WithArgs(anyArgs...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	eb2.ExpectExec("INSERT INTO usage_hourly").WithArgs(anyArgs...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	eb2.ExpectExec("INSERT INTO usage_hourly_execution").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	eb2.ExpectExec("INSERT INTO usage_hourly_execution").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = store.RollupHour(context.Background(), orgID, hour)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNormalizeUsageModel_PrefersNativeUsageModelOverOverride(t *testing.T) {
	t.Parallel()

	override := "gpt-4.1"
	model := normalizeUsageModel(&override, []byte(`{"native_usage":{"model":"gpt-5.4"}}`))

	require.Equal(t, "gpt-5.4", model)
}

func TestNormalizedSessionModelSQL_PrefersNativeUsageBeforeOverride(t *testing.T) {
	t.Parallel()

	sql := normalizedSessionModelSQL("s")

	require.Equal(t, "COALESCE(NULLIF(s.token_usage->'native_usage'->>'model', ''), NULLIF(s.model_override, ''), 'unknown')", sql)
}

func TestRollupRange_Empty(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	// start == end → no hours to process
	hour := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	err = store.RollupRange(context.Background(), orgID, hour, hour)
	require.NoError(t, err)
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

	cols := usageTimeseriesTestColumns()
	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnRows(pgxmock.NewRows(cols).AddRow(
			start, nil, "", (*string)(nil), nil, nil, nil, nil, nil,
			60.0, 5, 5, 2, 120.0, 300.0,
			int64(1000), int64(500), int64(1500), 0.50,
		))

	buckets, err := store.GetTimeseries(context.Background(), orgID, start, end, "hour", "", nil, nil, UsageExecutionFilters{})
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.Equal(t, 60.0, buckets[0].TotalContainerMinutes)
	require.Equal(t, 5, buckets[0].TotalSessions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTimeseries_ExecutionFiltersUseAllCapacityRollups(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	agent := "codex"

	mock.ExpectQuery("FROM usage_hourly_execution uhe").
		WithArgs(pgx.NamedArgs{
			"org_id":       orgID,
			"start":        start,
			"end":          end,
			"agent":        agent,
			"all_capacity": usageAllCapacityKey,
		}).
		WillReturnRows(pgxmock.NewRows(usageTimeseriesTestColumns()).AddRow(
			start, nil, "", (*string)(nil), testStringPtr("codex"), testStringPtr("gpt-5.4"), testStringPtr("default"), nil, nil,
			60.0, 1, 1, 1, 0.0, 0.0,
			int64(1000), int64(500), int64(1500), 0.50,
		))

	buckets, err := store.GetTimeseries(context.Background(), orgID, start, end, "hour", "", nil, nil, UsageExecutionFilters{Agent: &agent})
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.Equal(t, 1, buckets[0].TotalSessions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTimeseries_CapacityFilterWithExecutionFiltersUsesExecutionRollups(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	capacity := "2cpu_4096mb_10240diskmb"
	agent := "codex"

	mock.ExpectQuery("FROM usage_hourly_execution uhe").
		WithArgs(pgx.NamedArgs{
			"org_id":           orgID,
			"start":            start,
			"end":              end,
			"agent":            agent,
			"capacity":         capacity,
			"all_capacity":     usageAllCapacityKey,
			"unknown_capacity": usageUnknownCapacityKey,
		}).
		WillReturnRows(pgxmock.NewRows(usageTimeseriesTestColumns()).AddRow(
			start, nil, "", testStringPtr(capacity), nil, nil, nil, nil, nil,
			45.0, 2, 2, 1, 0.0, 0.0,
			int64(700), int64(300), int64(1000), 0.40,
		))

	buckets, err := store.GetTimeseries(
		context.Background(),
		orgID,
		start,
		end,
		"hour",
		"",
		nil,
		&capacity,
		UsageExecutionFilters{Agent: &agent},
	)
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.Equal(t, 45.0, buckets[0].TotalContainerMinutes)
	require.Equal(t, capacity, *buckets[0].CapacityTier)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTimeseries_CapacityGroupByWithExecutionFiltersUsesCapacityExecutionRollups(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	agent := "codex"
	capacity := "2cpu_4096mb_10240diskmb"

	mock.ExpectQuery("NULLIF\\(uhe\\.capacity_key, @unknown_capacity\\) AS capacity_tier").
		WithArgs(pgx.NamedArgs{
			"org_id":           orgID,
			"start":            start,
			"end":              end,
			"agent":            agent,
			"all_capacity":     usageAllCapacityKey,
			"unknown_capacity": usageUnknownCapacityKey,
		}).
		WillReturnRows(pgxmock.NewRows(usageTimeseriesTestColumns()).AddRow(
			start, nil, "", &capacity, nil, nil, nil, testStringPtr(capacity), testStringPtr(capacity),
			45.0, 2, 2, 1, 0.0, 0.0,
			int64(700), int64(300), int64(1000), 0.40,
		))

	buckets, err := store.GetTimeseries(
		context.Background(),
		orgID,
		start,
		end,
		"capacity",
		"",
		nil,
		nil,
		UsageExecutionFilters{Agent: &agent},
	)
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.Equal(t, capacity, *buckets[0].CapacityTier)
	require.Equal(t, capacity, *buckets[0].SeriesKey)
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

	_, err = store.GetTimeseries(context.Background(), orgID, start, end, "hour", "", nil, nil, UsageExecutionFilters{})
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

	cols := usageTimeseriesTestColumns()
	uid := uuid.New()
	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnRows(pgxmock.NewRows(cols).AddRow(
			start, &uid, "alice", (*string)(nil), nil, nil, nil, nil, nil,
			30.0, 2, 2, 1, 60.0, 120.0,
			int64(500), int64(250), int64(750), 0.25,
		))

	buckets, err := store.GetTimeseries(context.Background(), orgID, start, end, "user", "", nil, nil, UsageExecutionFilters{})
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

	args := pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "limit": 50}

	// Grand total query
	mock.ExpectQuery("SELECT COALESCE\\(SUM").
		WithArgs(args).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_tokens", "total_cost"}).AddRow(100.0, 2700.0, 0.80))

	cols := []string{
		"key", "label",
		"total_container_minutes", "total_sessions", "total_container_starts",
		"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_tokens", "total_llm_cost_usd",
	}
	mock.ExpectQuery("SELECT\\s+uh.user_id::text AS key").
		WithArgs(args).
		WillReturnRows(pgxmock.NewRows(cols).
			AddRow("u1", "alice@test.com", 60.0, 3, 3, 1, int64(1000), int64(500), int64(1500), 0.50).
			AddRow("u2", "bob@test.com", 40.0, 2, 2, 1, int64(800), int64(400), int64(1200), 0.30))

	rows, err := store.GetBreakdown(context.Background(), orgID, start, end, "user", "minutes_desc", 50, UsageExecutionFilters{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "alice@test.com", rows[0].Label)
	require.Equal(t, 60.0, rows[0].Percentage) // 60/100 * 100 = 60.0%
	require.Equal(t, 55.6, rows[0].ShareOfTokens)
	require.Equal(t, 62.5, rows[0].ShareOfTokenCost)
	require.Equal(t, 40.0, rows[1].Percentage) // 40/100 * 100 = 40.0%
	require.Equal(t, 44.4, rows[1].ShareOfTokens)
	require.Equal(t, 37.5, rows[1].ShareOfTokenCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageTokenCostSQL_UsesUSDObjectFallbacks(t *testing.T) {
	t.Parallel()

	sql := usageTokenCostSQL("s")

	require.Contains(t, sql, "s.token_usage->>'total_cost_usd'", "cost extraction should keep the canonical total_cost_usd field")
	require.Contains(t, sql, "s.token_usage->'cost'->>'amount'", "cost extraction should support persisted USD cost objects")
	require.Contains(t, sql, "s.token_usage->'native_cost'->>'amount'", "cost extraction should support persisted native USD cost objects")
	require.Contains(t, sql, "lower(s.token_usage->'cost'->>'unit') = 'usd'", "cost object fallback should only use USD costs")
	require.Contains(t, sql, "lower(s.token_usage->'native_cost'->>'unit') = 'usd'", "native cost object fallback should only use USD costs")
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

	args := pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "limit": 10, "all_capacity": usageAllCapacityKey}

	// Grand total query
	mock.ExpectQuery("SELECT\\s+COALESCE\\(SUM\\(uhe.total_container_minutes\\), 0\\),").
		WithArgs(args).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_tokens", "total_cost"}).AddRow(100.0, 3000.0, 1.0))

	cols := []string{
		"key", "label",
		"total_container_minutes", "total_sessions", "total_container_starts",
		"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_tokens", "total_llm_cost_usd",
	}
	// Regression guard: Postgres format() only accepts %s/%I/%L specifiers, so
	// the capacity_tier CTE must use CPU, memory, and disk. This
	// regex only matches the fixed form; if someone reintroduces %f the test
	// will fail with an unexpected-query error from pgxmock.
	mock.ExpectQuery("FROM usage_hourly_execution uhe").
		WithArgs(args).
		WillReturnRows(pgxmock.NewRows(cols).
			AddRow("2cpu_4096mb_10240diskmb", "2cpu_4096mb_10240diskmb", 100.0, 5, 5, 2, int64(2000), int64(1000), int64(3000), 1.0))

	rows, err := store.GetBreakdown(context.Background(), orgID, start, end, "capacity", "sessions_desc", 10, UsageExecutionFilters{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 100.0, rows[0].Percentage)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetBreakdown_AgentDimension_UsesAllCapacityRollups(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	agent := "codex"

	mock.ExpectQuery("SELECT\\s+COALESCE\\(SUM\\(uhe.total_container_minutes\\), 0\\),").
		WithArgs(pgx.NamedArgs{
			"org_id":       orgID,
			"start":        start,
			"end":          end,
			"limit":        10,
			"agent":        agent,
			"all_capacity": usageAllCapacityKey,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_tokens", "total_cost"}).AddRow(100.0, 3000.0, 1.0))

	mock.ExpectQuery("session_counts").
		WithArgs(pgx.NamedArgs{
			"org_id":       orgID,
			"start":        start,
			"end":          end,
			"limit":        10,
			"agent":        agent,
			"all_capacity": usageAllCapacityKey,
		}).
		WillReturnRows(pgxmock.NewRows([]string{
			"key", "label",
			"total_container_minutes", "total_sessions", "total_container_starts",
			"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_tokens", "total_llm_cost_usd",
		}).AddRow("codex", "Codex", 100.0, 2, 3, 2, int64(2000), int64(1000), int64(3000), 1.0))

	rows, err := store.GetBreakdown(context.Background(), orgID, start, end, "agent", "sessions_desc", 10, UsageExecutionFilters{Agent: &agent})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].TotalSessions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetBreakdown_ModelDimension_UsesSessionModelOverrideColumn(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT\\s+COALESCE\\(SUM\\(uhe.total_container_minutes\\), 0\\),").
		WithArgs(pgx.NamedArgs{
			"org_id":       orgID,
			"start":        start,
			"end":          end,
			"limit":        10,
			"all_capacity": usageAllCapacityKey,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_tokens", "total_cost"}).AddRow(100.0, 3000.0, 1.0))

	mock.ExpectQuery("NULLIF\\(s\\.model_override").
		WithArgs(pgx.NamedArgs{
			"org_id":       orgID,
			"start":        start,
			"end":          end,
			"limit":        10,
			"all_capacity": usageAllCapacityKey,
		}).
		WillReturnRows(pgxmock.NewRows([]string{
			"key", "label",
			"total_container_minutes", "total_sessions", "total_container_starts",
			"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_tokens", "total_llm_cost_usd",
		}).AddRow("gpt-5.4", "gpt-5.4", 100.0, 2, 3, 2, int64(2000), int64(1000), int64(3000), 1.0))

	rows, err := store.GetBreakdown(context.Background(), orgID, start, end, "model", "tokens_desc", 10, UsageExecutionFilters{})
	require.NoError(t, err, "model breakdown should query the real sessions model column")
	require.Equal(t, []models.UsageBreakdownRow{
		{
			Key:                   "gpt-5.4",
			Label:                 "gpt-5.4",
			TotalContainerMinutes: 100,
			TotalSessions:         2,
			TotalContainerStarts:  3,
			PeakConcurrent:        2,
			TotalInputTokens:      2000,
			TotalOutputTokens:     1000,
			TotalTokens:           3000,
			TotalLLMCostUSD:       1,
			Percentage:            100,
			ShareOfTokenCost:      100,
			ShareOfTokens:         100,
		},
	}, rows, "model breakdown should return normalized model rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBreakdownLabelSQL_AgentLabelsIncludeOpenCode(t *testing.T) {
	t.Parallel()

	sql := breakdownLabelSQL("agent", "uhe.agent_type")

	require.Contains(t, sql, "WHEN 'opencode' THEN 'OpenCode'", "agent breakdown labels should render OpenCode with the product label")
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

	args := pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "limit": 50}

	// Grand total query succeeds
	mock.ExpectQuery("SELECT COALESCE\\(SUM").
		WithArgs(args).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_tokens", "total_cost"}).AddRow(100.0, 0.0, 0.0))

	// Main breakdown query fails
	mock.ExpectQuery("SELECT\\s+uh.user_id::text AS key").
		WithArgs(args).
		WillReturnError(pgx.ErrTxClosed)

	_, err = store.GetBreakdown(context.Background(), orgID, start, end, "user", "minutes_desc", 50, UsageExecutionFilters{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "query breakdown")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTimeseries_WithUserIDFilter(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	uid := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	cols := usageTimeseriesTestColumns()
	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "user_id": uid}).
		WillReturnRows(pgxmock.NewRows(cols).AddRow(
			start, &uid, "alice", (*string)(nil), nil, nil, nil, nil, nil,
			30.0, 2, 2, 1, 60.0, 120.0,
			int64(500), int64(250), int64(750), 0.25,
		))

	buckets, err := store.GetTimeseries(context.Background(), orgID, start, end, "hour", "", &uid, nil, UsageExecutionFilters{})
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTimeseries_WithCapacityFilter(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	cap := "2cpu_4096mb_10240diskmb"

	cols := usageTimeseriesTestColumns()
	mock.ExpectQuery("NULLIF\\(uhe\\.capacity_key, @unknown_capacity\\) AS capacity_tier").
		WithArgs(pgx.NamedArgs{
			"org_id":           orgID,
			"start":            start,
			"end":              end,
			"capacity":         cap,
			"all_capacity":     usageAllCapacityKey,
			"unknown_capacity": usageUnknownCapacityKey,
		}).
		WillReturnRows(pgxmock.NewRows(cols).AddRow(
			start, nil, "", &cap, nil, nil, nil, nil, nil,
			45.0, 3, 3, 2, 90.0, 200.0,
			int64(800), int64(400), int64(1200), 0.40,
		))

	buckets, err := store.GetTimeseries(context.Background(), orgID, start, end, "hour", "", nil, &cap, UsageExecutionFilters{})
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.Equal(t, &cap, buckets[0].CapacityTier)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetTimeseries_CapacityGroupBy(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUsageRollupStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	cols := usageTimeseriesTestColumns()
	cap := "2cpu_4096mb_10240diskmb"
	mock.ExpectQuery("SELECT uh.hour_utc").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "start": start, "end": end}).
		WillReturnRows(pgxmock.NewRows(cols).AddRow(
			start, nil, "", &cap, nil, nil, nil, nil, nil,
			50.0, 4, 4, 2, 100.0, 250.0,
			int64(900), int64(450), int64(1350), 0.45,
		))

	buckets, err := store.GetTimeseries(context.Background(), orgID, start, end, "capacity", "", nil, nil, UsageExecutionFilters{})
	require.NoError(t, err)
	require.Len(t, buckets, 1)
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

	args := pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "limit": 50}

	// Grand total query
	mock.ExpectQuery("SELECT COALESCE\\(SUM").
		WithArgs(args).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_tokens", "total_cost"}).AddRow(0.0, 0.0, 0.0))

	cols := []string{
		"key", "label",
		"total_container_minutes", "total_sessions", "total_container_starts",
		"peak_concurrent", "total_input_tokens", "total_output_tokens", "total_tokens", "total_llm_cost_usd",
	}
	mock.ExpectQuery("SELECT\\s+uh.user_id::text AS key").
		WithArgs(args).
		WillReturnRows(pgxmock.NewRows(cols))

	rows, err := store.GetBreakdown(context.Background(), orgID, start, end, "user", "tokens_desc", 50, UsageExecutionFilters{})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}
