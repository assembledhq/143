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

func TestLifecycleWriterRetentionSharedDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var firstDeadline time.Time
	calls := 0
	deleted, err := sweepExpiredLifecycleWriters(ctx, func(batchCtx context.Context) (int64, error) {
		deadline, ok := batchCtx.Deadline()
		require.True(t, ok, "every batch must inherit the same bounded job budget")
		require.LessOrEqual(t, time.Until(deadline), 5*time.Second, "the production writer sweep must never grant a fresh five-second budget per batch")
		if calls == 0 {
			firstDeadline = deadline
		}
		require.Equal(t, firstDeadline, deadline, "later batches must retain the original absolute deadline")
		calls++
		if calls == 1 {
			return 1000, nil
		}
		return 7, nil
	}, zerolog.Nop())
	require.NoError(t, err, "a short batch should finish normal bounded progress")
	require.Equal(t, int64(1007), deleted, "the sweep must retain both confirmed batch counts")
	require.Equal(t, 2, calls, "a short batch must stop the sweep before its count limit")
}

func TestLifecycleWriterRetentionWallBudget(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	calls := 0
	started := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	now := func() time.Time {
		if calls > 0 {
			return started.Add(lifecycleWriterRetentionSweepBudget)
		}
		return started
	}
	deleteBatch := func(ctx context.Context, days int) (int64, error) {
		require.NoError(t, ctx.Err(), "the first batch should receive a live shared context")
		require.Equal(t, 7, days, "budget accounting must retain the store's fixed retention policy")
		calls++
		return 1000, nil
	}
	deleted, err := sweepExpiredRecordsWithBudget(context.Background(), 7, deleteBatch, zerolog.New(&logs), "passive lifecycle writer", lifecycleWriterRetentionBatchSize, lifecycleWriterRetentionSweepBatches, lifecycleWriterRetentionSweepBudget, now)
	require.NoError(t, err, "own-budget exhaustion must leave normal partial progress for a later scheduled job")
	require.Equal(t, int64(1000), deleted, "the time cap must retain exactly the first committed batch")
	require.Equal(t, 1, calls, "five elapsed seconds must prevent a second SQL call even when the row cap permits it")
	require.Contains(t, logs.String(), "passive lifecycle writer retention sweep budget reached", "partial progress must not be confused with an empty table")
}

func TestLifecycleWriterRetentionInFlightCancellation(t *testing.T) {
	t.Parallel()
	dbErr := errors.New("synthetic database failure at deadline")
	tests := []struct {
		name         string
		parentCancel bool
		failure      error
		wantErr      error
	}{
		{name: "own deadline retains partial progress"},
		{name: "parent cancellation remains an error", parentCancel: true, wantErr: context.Canceled},
		{name: "deadline must not hide unrelated database error", failure: dbErr, wantErr: dbErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var logs bytes.Buffer
			calls := 0
			deleteBatch := func(batchCtx context.Context, _ int) (int64, error) {
				calls++
				if calls == 1 {
					return 1000, nil
				}
				if tt.parentCancel {
					cancel()
				}
				<-batchCtx.Done()
				if tt.failure != nil {
					return 0, tt.failure
				}
				return 0, batchCtx.Err()
			}
			deleted, err := sweepExpiredRecordsWithBudget(ctx, 7, deleteBatch, zerolog.New(&logs), "passive lifecycle writer", lifecycleWriterRetentionBatchSize, lifecycleWriterRetentionSweepBatches, 100*time.Millisecond, time.Now)
			require.Equal(t, int64(1000), deleted, "a canceled later batch must not erase or invent confirmed deletion progress")
			require.Equal(t, 2, calls, "canceling an in-flight second batch must prevent all later attempts")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "ambient cancellation and independent database errors must propagate")
				require.Empty(t, logs.String(), "real failures must not be reported as normal budget exhaustion")
			} else {
				require.NoError(t, err, "the sweep's own deadline should complete with partial progress")
				require.Contains(t, logs.String(), "retention sweep budget reached", "in-flight budget exhaustion must be logged for later maintenance")
			}
		})
	}
}
