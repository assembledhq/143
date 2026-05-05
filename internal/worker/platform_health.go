package worker

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
)

type queueHealthStore interface {
	QueueHealthSamples(ctx context.Context) ([]db.JobQueueHealthSample, error)
}

// RunQueueHealthSampler emits low-volume structured logs that feed the
// platform health dashboard. It is worker-local but queries the shared job
// table for platform-wide queue pressure.
func RunQueueHealthSampler(ctx context.Context, store queueHealthStore, logger zerolog.Logger, interval time.Duration) {
	if store == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	emitQueueHealthSample(ctx, store, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emitQueueHealthSample(ctx, store, logger)
		}
	}
}

func emitQueueHealthSample(ctx context.Context, store queueHealthStore, logger zerolog.Logger) {
	samples, err := store.QueueHealthSamples(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("platform health: failed to sample job queue")
		return
	}
	for _, sample := range samples {
		logger.Info().
			Str("queue", sample.Queue).
			Str("job_type", sample.JobType).
			Int64("pending_runnable", sample.PendingRunnable).
			Int64("pending_deferred", sample.PendingDeferred).
			Int64("running", sample.Running).
			Int64("dead_letter", sample.DeadLetter).
			Float64("oldest_runnable_age_seconds", sample.OldestRunnableAgeSeconds).
			Msg("platform health: job queue sample")
	}
}
