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
