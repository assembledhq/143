package preview

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// DefaultMaxUptime is the maximum time a preview can run before being recycled.
// After this duration, the recycler gracefully restarts all preview processes
// to avoid resource leaks and stale state.
const DefaultMaxUptime = 60 * time.Minute

// RecycleWorker periodically checks for long-running previews and restarts
// them. It runs as a background goroutine and can be stopped via its Stop
// method.
//
// The restart sequence is:
//  1. SIGTERM all services
//  2. Wait 10s for graceful shutdown
//  3. SIGKILL stragglers
//  4. Tear down infrastructure
//  5. Re-provision infrastructure
//  6. Re-run init scripts
//  7. Restart services
//  8. Wait for readiness
//
// The preview transitions briefly to "starting" during recycle, then back to
// "ready". The last_path is preserved so the user returns to where they were.
type RecycleWorker struct {
	manager   *Manager
	logger    zerolog.Logger
	interval  time.Duration
	maxUptime time.Duration
	stopCh    chan struct{}
	doneCh    chan struct{}
	stopOnce  sync.Once
}

// RecycleWorkerConfig holds initialization options.
type RecycleWorkerConfig struct {
	Manager   *Manager
	Logger    zerolog.Logger
	Interval  time.Duration // default 1 minute
	MaxUptime time.Duration // default 60 minutes
}

// NewRecycleWorker creates a new recycle worker.
func NewRecycleWorker(cfg RecycleWorkerConfig) *RecycleWorker {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	maxUptime := cfg.MaxUptime
	if maxUptime <= 0 {
		maxUptime = DefaultMaxUptime
	}
	return &RecycleWorker{
		manager:   cfg.Manager,
		logger:    cfg.Logger,
		interval:  interval,
		maxUptime: maxUptime,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// Start launches the recycle loop in a background goroutine.
func (w *RecycleWorker) Start() {
	go w.run()
}

func (w *RecycleWorker) run() {
	defer close(w.doneCh)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.recycle()
		}
	}
}

func (w *RecycleWorker) recycle() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	now := time.Now()
	uptimeCutoff := now.Add(-w.maxUptime)

	// List active previews that have exceeded the max uptime since their last
	// successful start/recycle, so long-lived previews are not recycled on every sweep.
	candidates, err := w.manager.Store().ListActivePreviewsRecycledBefore(ctx, w.manager.WorkerNodeID(), uptimeCutoff)
	if err != nil {
		w.logger.Warn().Err(err).Msg("recycle: failed to list previews")
		return
	}

	var recycledCount int
	for _, p := range candidates {
		w.logger.Info().
			Str("preview_id", p.ID.String()).
			Str("org_id", p.OrgID.String()).
			Time("recycled_at", p.RecycledAt).
			Dur("uptime", now.Sub(p.RecycledAt)).
			Msg("recycle: preview exceeded max uptime, recycling")

		// Use an independent per-preview context so each recycle gets the full
		// 90 seconds regardless of how many candidates there are (the parent
		// ctx has a 2-minute timeout that could cut later previews short).
		previewCtx, previewCancel := context.WithTimeout(context.Background(), 90*time.Second)
		err := w.manager.RecyclePreview(previewCtx, p.OrgID, p.ID)
		previewCancel()
		if err != nil {
			w.logger.Warn().Err(err).
				Str("preview_id", p.ID.String()).
				Msg("recycle: failed to recycle preview")
			continue
		}

		recycledCount++
	}

	if recycledCount > 0 {
		w.logger.Info().
			Int("recycled", recycledCount).
			Msg("recycle: recycled long-running previews")
	}
}

// Stop signals the worker to stop and waits for completion. Safe to call
// multiple times.
func (w *RecycleWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
	})
	<-w.doneCh
}
