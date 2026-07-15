package preview

import (
	"context"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
)

const (
	DefaultPreviewResourceSampleRetention   = 24 * time.Hour
	DefaultPreviewResourceSampleDeleteLimit = 10000
)

// CleanupWorker periodically stops expired and idle previews.
// It runs as a background goroutine and can be stopped via its Stop method.
type CleanupWorker struct {
	manager                   *Manager
	logger                    zerolog.Logger
	interval                  time.Duration
	idleTimeout               time.Duration
	resourceSampleRetention   time.Duration
	resourceSampleDeleteLimit int
	stopCh                    chan struct{}
	doneCh                    chan struct{}
}

// CleanupWorkerConfig holds initialization options.
type CleanupWorkerConfig struct {
	Manager                   *Manager
	Logger                    zerolog.Logger
	Interval                  time.Duration // default 1 minute
	IdleTimeout               time.Duration // default 15 minutes
	ResourceSampleRetention   time.Duration // default 24 hours; set negative to disable
	ResourceSampleDeleteLimit int           // default 10000 rows per cleanup pass
}

// NewCleanupWorker creates a new cleanup worker.
func NewCleanupWorker(cfg CleanupWorkerConfig) *CleanupWorker {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	idleTimeout := cfg.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	resourceSampleRetention := cfg.ResourceSampleRetention
	if resourceSampleRetention == 0 {
		resourceSampleRetention = DefaultPreviewResourceSampleRetention
	}
	resourceSampleDeleteLimit := cfg.ResourceSampleDeleteLimit
	if resourceSampleDeleteLimit <= 0 {
		resourceSampleDeleteLimit = DefaultPreviewResourceSampleDeleteLimit
	}
	return &CleanupWorker{
		manager:                   cfg.Manager,
		logger:                    cfg.Logger,
		interval:                  interval,
		idleTimeout:               idleTimeout,
		resourceSampleRetention:   resourceSampleRetention,
		resourceSampleDeleteLimit: resourceSampleDeleteLimit,
		stopCh:                    make(chan struct{}),
		doneCh:                    make(chan struct{}),
	}
}

// Start launches the cleanup loop in a background goroutine.
func (w *CleanupWorker) Start() {
	go w.run()
}

func (w *CleanupWorker) run() {
	defer close(w.doneCh)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.cleanup()
		}
	}
}

func (w *CleanupWorker) cleanup() {
	if w == nil || w.manager == nil || w.manager.store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var expiredCount, idleCount int
	var resourceSamplesDeleted int64

	now := time.Now()

	// Stop expired previews (hard TTL exceeded).
	expired, err := w.manager.store.ListExpiredPreviewsForWorker(ctx, w.manager.WorkerNodeID(), now)
	if err != nil {
		w.logger.Warn().Err(err).Msg("cleanup: failed to list expired previews")
	} else {
		for _, p := range expired {
			if stopErr := w.manager.StopPreviewWithReason(ctx, p.OrgID, p.ID, models.PreviewStoppedReasonExpired); stopErr != nil {
				w.logger.Warn().Err(stopErr).
					Str("preview_id", p.ID.String()).
					Msg("cleanup: failed to stop expired preview")
			} else {
				expiredCount++
			}
		}
	}

	// Stop idle previews (no activity for idleTimeout).
	idleSince := now.Add(-w.idleTimeout)
	idle, err := w.manager.store.ListIdlePreviewsForWorker(ctx, w.manager.WorkerNodeID(), idleSince)
	if err != nil {
		w.logger.Warn().Err(err).Msg("cleanup: failed to list idle previews")
	} else {
		for _, p := range idle {
			if stopErr := w.manager.StopPreviewWithReason(ctx, p.OrgID, p.ID, models.PreviewStoppedReasonExpired); stopErr != nil {
				w.logger.Warn().Err(stopErr).
					Str("preview_id", p.ID.String()).
					Msg("cleanup: failed to stop idle preview")
			} else {
				idleCount++
			}
		}
	}

	if w.resourceSampleRetention > 0 {
		cutoff := now.Add(-w.resourceSampleRetention)
		deleted, err := w.manager.store.DeleteExpiredPreviewResourceSamples(ctx, cutoff, w.resourceSampleDeleteLimit)
		if err != nil {
			w.logger.Warn().Err(err).Msg("cleanup: failed to delete expired preview resource samples")
		} else {
			resourceSamplesDeleted = deleted
		}
	}

	if expiredCount > 0 || idleCount > 0 || resourceSamplesDeleted > 0 {
		w.logger.Info().
			Int("expired", expiredCount).
			Int("idle", idleCount).
			Int64("resource_samples_deleted", resourceSamplesDeleted).
			Msg("cleanup: completed preview cleanup")
	}
}

// Stop signals the worker to stop and waits for completion.
func (w *CleanupWorker) Stop() {
	close(w.stopCh)
	<-w.doneCh
}
