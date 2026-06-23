package preview

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// DefaultMaxUptime is the maximum time a preview can run before being recycled.
// After this duration, the recycler gracefully restarts all preview processes
// to avoid resource leaks and stale state.
const DefaultMaxUptime = 60 * time.Minute

// DefaultRecycleGracePeriod is how long the recycler waits after flagging a
// preview for recycle before actually restarting it. During this window, the
// frontend shows the user a warning so they can save state / finish their
// interaction. Short enough that users don't wait long; long enough to cover
// a typical save-and-reload cycle in a web app.
const DefaultRecycleGracePeriod = 45 * time.Second

// RecycleWorker periodically checks for long-running previews and restarts
// them. It runs as a background goroutine and can be stopped via its Stop
// method.
//
// Recycle proceeds in two phases so active users are not interrupted without
// warning:
//
//  1. Sweep 1 — schedule: previews whose uptime has exceeded MaxUptime have
//     their recycle_scheduled_at stamped to now + GracePeriod. The frontend
//     polls preview status and surfaces a banner when this field is set.
//  2. Sweep 2 — act: once recycle_scheduled_at is in the past, the recycler
//     actually restarts the preview in place. The preview transitions
//     briefly to "starting", then back to "ready"; last_path is preserved.
type RecycleWorker struct {
	manager     *Manager
	logger      zerolog.Logger
	interval    time.Duration
	maxUptime   time.Duration
	gracePeriod time.Duration
	stopCh      chan struct{}
	doneCh      chan struct{}
	stopOnce    sync.Once
}

// RecycleWorkerConfig holds initialization options.
type RecycleWorkerConfig struct {
	Manager     *Manager
	Logger      zerolog.Logger
	Interval    time.Duration // default 1 minute
	MaxUptime   time.Duration // default 60 minutes
	GracePeriod time.Duration // default 45 seconds (DefaultRecycleGracePeriod)
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
	gracePeriod := cfg.GracePeriod
	if gracePeriod <= 0 {
		gracePeriod = DefaultRecycleGracePeriod
	}
	return &RecycleWorker{
		manager:     cfg.Manager,
		logger:      cfg.Logger,
		interval:    interval,
		maxUptime:   maxUptime,
		gracePeriod: gracePeriod,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
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

	// Phase 1: flag previews that have exceeded max uptime but are not yet
	// scheduled for recycle. This stamps recycle_scheduled_at so the
	// frontend can show a grace-period warning to the user.
	candidates, err := w.manager.Store().ListActivePreviewsRecycledBefore(ctx, w.manager.WorkerNodeID(), uptimeCutoff)
	if err != nil {
		w.logger.Warn().Err(err).Msg("recycle: failed to list candidates")
	}
	var scheduled int
	for _, p := range candidates {
		if p.RecycleScheduledAt != nil {
			continue // already in grace window
		}
		scheduledAt := time.Now().Add(w.gracePeriod)
		set, schedErr := w.manager.Store().ScheduleRecycle(ctx, p.OrgID, p.ID, scheduledAt)
		if schedErr != nil {
			w.logger.Warn().Err(schedErr).
				Str("preview_id", p.ID.String()).
				Msg("recycle: failed to schedule grace window")
			continue
		}
		if set {
			scheduled++
			w.logger.Info().
				Str("preview_id", p.ID.String()).
				Str("org_id", p.OrgID.String()).
				Time("recycle_at", scheduledAt).
				Dur("grace", w.gracePeriod).
				Msg("recycle: scheduled grace window before restart")
		}
	}

	// Phase 2: restart previews whose grace window has elapsed.
	due, err := w.manager.Store().ListPreviewsScheduledToRecycleBefore(ctx, w.manager.WorkerNodeID(), time.Now())
	if err != nil {
		w.logger.Warn().Err(err).Msg("recycle: failed to list due previews")
		return
	}

	var recycledCount int
	for _, p := range due {
		w.logger.Info().
			Str("preview_id", p.ID.String()).
			Str("org_id", p.OrgID.String()).
			Msg("recycle: grace window elapsed, recycling")

		// Use an independent per-preview context so each recycle gets the full
		// 90 seconds regardless of how many candidates there are (the parent
		// ctx has a 2-minute timeout that could cut later previews short).
		previewCtx, previewCancel := context.WithTimeout(context.Background(), 90*time.Second)
		rErr := w.manager.RecyclePreview(previewCtx, p.OrgID, p.ID)
		previewCancel()
		if rErr != nil {
			if errors.Is(rErr, errRecycleSkipped) {
				// Intentional skip (e.g. indeterminate liveness probe) — not a
				// failure and not a completed recycle. The scheduled marker
				// persists so a later sweep retries.
				w.logger.Debug().Err(rErr).
					Str("preview_id", p.ID.String()).
					Msg("recycle: skipped this sweep; will retry")
				continue
			}
			w.logger.Warn().Err(rErr).
				Str("preview_id", p.ID.String()).
				Msg("recycle: failed to recycle preview")
			continue
		}
		recycledCount++
	}

	if scheduled > 0 || recycledCount > 0 {
		w.logger.Info().
			Int("scheduled", scheduled).
			Int("recycled", recycledCount).
			Msg("recycle: sweep complete")
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
