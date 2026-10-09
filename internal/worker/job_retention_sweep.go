package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

const (
	jobRetentionBatchSize    int64 = 10000
	jobRetentionSweepBatches       = 10
	jobRetentionSweepBudget        = 30 * time.Second
)

// Each callback executes one autocommit batch through the production pool.
// The SQL claims rows with SKIP LOCKED, so concurrent org maintenance jobs
// advance separate global batches without a pooled session advisory lock.
func sweepExpiredCompletedJobs(ctx context.Context, retentionDays int, deleteBatch func(context.Context, int) (int64, error), logger zerolog.Logger) (int64, error) {
	return sweepExpiredCompletedJobsWithBudget(ctx, retentionDays, deleteBatch, logger, jobRetentionSweepBatches, jobRetentionSweepBudget, time.Now)
}

func sweepExpiredCompletedJobsWithBudget(ctx context.Context, retentionDays int, deleteBatch func(context.Context, int) (int64, error), logger zerolog.Logger, maxBatches int, budget time.Duration, now func() time.Time) (int64, error) {
	started := now()
	budgetCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var total int64
	for batches := 0; batches < maxBatches; batches++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if now().Sub(started) >= budget || budgetCtx.Err() != nil {
			logJobRetentionBudget(logger, total, batches, retentionDays)
			return total, nil
		}
		deleted, err := deleteBatch(budgetCtx, retentionDays)
		if err != nil {
			if ctx.Err() != nil {
				return total, ctx.Err()
			}
			if budgetCtx.Err() != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
				// The budget cancelled the current call. Earlier confirmed batches
				// remain committed and the next scheduled sweep resumes progress.
				logJobRetentionBudget(logger, total, batches, retentionDays)
				return total, nil
			}
			return total, fmt.Errorf("delete expired jobs batch: %w", err)
		}
		total += deleted
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if deleted < jobRetentionBatchSize {
			return total, nil
		}
	}
	logJobRetentionBudget(logger, total, maxBatches, retentionDays)
	return total, nil
}

func logJobRetentionBudget(logger zerolog.Logger, deleted int64, batches, retentionDays int) {
	logger.Info().Int64("deleted", deleted).Int("batches", batches).
		Int("retention_days", retentionDays).Msg("job retention sweep budget reached; later sweeps continue progress")
}
