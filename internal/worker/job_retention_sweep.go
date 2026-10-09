package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

const (
	retentionBatchSize    int64 = 10000
	retentionSweepBatches       = 10
	retentionSweepBudget        = 30 * time.Second
)

// Each callback executes one autocommit batch through the production pool.
// The SQL claims rows with SKIP LOCKED, so concurrent org maintenance jobs
// advance separate global batches without a pooled session advisory lock.
func sweepExpiredCompletedJobs(ctx context.Context, retentionDays int, deleteBatch func(context.Context, int) (int64, error), logger zerolog.Logger) (int64, error) {
	return sweepExpiredRecordsWithBudget(ctx, retentionDays, deleteBatch, logger, "job", retentionSweepBatches, retentionSweepBudget, time.Now)
}

func sweepExpiredWebhookDeliveries(ctx context.Context, retentionDays int, deleteBatch func(context.Context, int) (int64, error), logger zerolog.Logger) (int64, error) {
	return sweepExpiredRecordsWithBudget(ctx, retentionDays, deleteBatch, logger, "webhook delivery", retentionSweepBatches, retentionSweepBudget, time.Now)
}

func sweepExpiredRecordsWithBudget(ctx context.Context, retentionDays int, deleteBatch func(context.Context, int) (int64, error), logger zerolog.Logger, kind string, maxBatches int, budget time.Duration, now func() time.Time) (int64, error) {
	started := now()
	budgetCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var total int64
	for batches := 0; batches < maxBatches; batches++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if now().Sub(started) >= budget || budgetCtx.Err() != nil {
			logRetentionBudget(logger, kind, total, batches, retentionDays)
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
				logRetentionBudget(logger, kind, total, batches, retentionDays)
				return total, nil
			}
			return total, fmt.Errorf("delete expired %s batch: %w", kind, err)
		}
		total += deleted
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if deleted < retentionBatchSize {
			return total, nil
		}
	}
	logRetentionBudget(logger, kind, total, maxBatches, retentionDays)
	return total, nil
}

func logRetentionBudget(logger zerolog.Logger, kind string, deleted int64, batches, retentionDays int) {
	logger.Info().Int64("deleted", deleted).Int("batches", batches).
		Int("retention_days", retentionDays).Msg(kind + " retention sweep budget reached; later sweeps continue progress")
}
