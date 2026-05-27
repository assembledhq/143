package main

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

type activePreviewCounter interface {
	CountActivePreviewsByWorker(ctx context.Context, workerNodeID string) (int, error)
}

type previewRuntimeHeartbeatStore interface {
	HeartbeatPreviewRuntimesByWorker(ctx context.Context, workerNodeID string, leaseExpiresAt time.Time) (int64, error)
}

func runPreviewRuntimeHeartbeat(ctx context.Context, previews previewRuntimeHeartbeatStore, workerNodeID string, logger zerolog.Logger, interval, leaseTTL time.Duration) {
	if previews == nil || workerNodeID == "" {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if leaseTTL <= interval {
		leaseTTL = 3 * interval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := previews.HeartbeatPreviewRuntimesByWorker(ctx, workerNodeID, time.Now().Add(leaseTTL)); err != nil {
			logger.Warn().Err(err).Str("worker_node_id", workerNodeID).Msg("failed to heartbeat preview runtimes")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func waitForActivePreviewsToDrain(ctx context.Context, previews activePreviewCounter, workerNodeID string, logger zerolog.Logger, timeout time.Duration, pollInterval time.Duration) bool {
	if previews == nil || workerNodeID == "" {
		return true
	}
	if timeout <= 0 {
		logger.Info().Str("worker_node_id", workerNodeID).Msg("worker preview drain disabled; continuing worker shutdown")
		return false
	}
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	lastLoggedCount := -1
	for {
		count, err := previews.CountActivePreviewsByWorker(waitCtx, workerNodeID)
		if err != nil {
			logger.Warn().Err(err).Str("worker_node_id", workerNodeID).Msg("failed to count active previews during worker drain")
		} else if count == 0 {
			logger.Info().Str("worker_node_id", workerNodeID).Msg("active previews drained; continuing worker shutdown")
			return true
		} else if count != lastLoggedCount {
			logger.Info().
				Str("worker_node_id", workerNodeID).
				Int("active_previews", count).
				Dur("timeout", timeout).
				Msg("waiting for active previews to drain before worker shutdown")
			lastLoggedCount = count
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			logger.Warn().
				Str("worker_node_id", workerNodeID).
				Dur("timeout", timeout).
				Msg("active previews did not drain before worker preview drain timeout")
			return false
		case <-timer.C:
		}
	}
}
