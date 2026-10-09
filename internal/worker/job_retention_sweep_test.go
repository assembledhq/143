package worker

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestJobRetentionSweepBudgetsAndErrors(t *testing.T) {
	t.Parallel()
	dbErr := errors.New("foreign-key failure")
	tests := []struct {
		name          string
		counts        []int64
		errorAt       int
		cancelAt      int
		wallAt        int
		maxBatches    int
		expectedCalls int
		expectedCount int64
		expectedError error
		budgetLog     bool
	}{
		{name: "empty queue", counts: []int64{0}, maxBatches: 10, expectedCalls: 1},
		{name: "short batch", counts: []int64{42}, maxBatches: 10, expectedCalls: 1, expectedCount: 42},
		{name: "drains several independently committed batches", counts: []int64{10000, 10000, 5}, maxBatches: 10, expectedCalls: 3, expectedCount: 20005},
		{name: "batch budget leaves later work", counts: []int64{10000, 10000, 10000}, maxBatches: 2, expectedCalls: 2, expectedCount: 20000, budgetLog: true},
		{name: "wall budget stops between batches", counts: []int64{10000, 10000}, maxBatches: 10, wallAt: 1, expectedCalls: 1, expectedCount: 10000, budgetLog: true},
		{name: "database error preserves committed count", counts: []int64{10000, 0}, maxBatches: 10, errorAt: 2, expectedCalls: 2, expectedCount: 10000, expectedError: dbErr},
		{name: "cancellation before work", counts: []int64{10000}, maxBatches: 10, cancelAt: -1, expectedError: context.Canceled},
		{name: "parent cancellation after short committed batch", counts: []int64{42}, maxBatches: 10, cancelAt: 1, expectedCalls: 1, expectedCount: 42, expectedError: context.Canceled},
		{name: "parent cancellation preserves committed count", counts: []int64{10000, 10000}, maxBatches: 10, cancelAt: 1, expectedCalls: 1, expectedCount: 10000, expectedError: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelAt == -1 {
				cancel()
			}
			var logs bytes.Buffer
			logger := zerolog.New(&logs)
			calls := 0
			started := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
			now := func() time.Time {
				if tt.wallAt > 0 && calls >= tt.wallAt {
					return started.Add(time.Minute)
				}
				return started
			}
			batch := func(batchCtx context.Context, days int) (int64, error) {
				require.Equal(t, 30, days, "every batch should retain the same retention period")
				require.NoError(t, batchCtx.Err(), "callback should receive a live budget context")
				calls++
				if calls == tt.errorAt {
					return 0, dbErr
				}
				if calls == tt.cancelAt {
					cancel()
				}
				return tt.counts[calls-1], nil
			}
			actual, err := sweepExpiredRecordsWithBudget(ctx, 30, batch, logger, "job", tt.maxBatches, 30*time.Second, now)
			require.Equal(t, tt.expectedCount, actual, "sweep should report only confirmed committed batch counts")
			require.Equal(t, tt.expectedCalls, calls, "sweep should stop at exhaustion, error or budget")
			if tt.expectedError != nil {
				require.ErrorIs(t, err, tt.expectedError, "sweep should propagate cancellation and database failures")
			} else {
				require.NoError(t, err, "exhaustion and budget-limited partial progress should succeed")
			}
			if tt.budgetLog {
				require.Contains(t, logs.String(), "job retention sweep budget reached", "normal budget exit should explicitly report later progress")
			} else {
				require.Empty(t, logs.String(), "exhaustion and errors should not claim a normal budget exit")
			}
		})
	}
}

func TestJobRetentionSweepInFlightDeadline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		unrelatedErr  bool
		expectedError bool
	}{
		{name: "budget cancellation is normal partial progress"},
		{name: "SQL error remains visible at deadline", unrelatedErr: true, expectedError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dbErr := errors.New("foreign-key failure")
			var logs bytes.Buffer
			batch := func(ctx context.Context, _ int) (int64, error) {
				<-ctx.Done()
				if tt.unrelatedErr {
					return 0, dbErr
				}
				return 0, ctx.Err()
			}
			deleted, err := sweepExpiredRecordsWithBudget(context.Background(), 30, batch, zerolog.New(&logs), "webhook delivery", 10, 100*time.Millisecond, time.Now)
			require.Equal(t, int64(0), deleted, "in-flight failing batch should not invent a committed count")
			if tt.expectedError {
				require.ErrorIs(t, err, dbErr, "deadline must not hide an unrelated SQL error")
				require.Empty(t, logs.String(), "SQL failure must not be reported as successful budget exhaustion")
			} else {
				require.NoError(t, err, "own budget cancellation should leave normal partial progress")
				require.Contains(t, logs.String(), "webhook delivery retention sweep budget reached", "in-flight budget expiry should be observable")
			}
		})
	}
}
