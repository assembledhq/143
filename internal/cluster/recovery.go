package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

type recoveryNodeStore interface {
	MarkStaleNodesDead(ctx context.Context, staleBefore time.Time) (int64, error)
}

type recoveryJobStore interface {
	ReclaimLostRunningJobs(ctx context.Context, staleBefore time.Time, limit int) (int64, error)
}

type recoverySessionExecutorStore interface {
	ReclaimLost(ctx context.Context, staleBefore time.Time, limit int) (int64, error)
}

type recoveryPreviewRuntimeStore interface {
	MarkExpiredPreviewRuntimesLost(ctx context.Context, cutoff time.Time, reason string) (int64, error)
}

type RecoveryLoop struct {
	nodes            recoveryNodeStore
	jobs             recoveryJobStore
	executors        recoverySessionExecutorStore
	previews         recoveryPreviewRuntimeStore
	logger           zerolog.Logger
	deadNodeTimeout  time.Duration
	reclaimBatchSize int
}

func NewRecoveryLoop(nodes recoveryNodeStore, jobs recoveryJobStore, logger zerolog.Logger, deadNodeTimeout time.Duration, reclaimBatchSize int) *RecoveryLoop {
	return &RecoveryLoop{
		nodes:            nodes,
		jobs:             jobs,
		logger:           logger,
		deadNodeTimeout:  deadNodeTimeout,
		reclaimBatchSize: reclaimBatchSize,
	}
}

func (r *RecoveryLoop) SetSessionExecutors(executors recoverySessionExecutorStore) {
	r.executors = executors
}

func (r *RecoveryLoop) SetPreviewRuntimes(previews recoveryPreviewRuntimeStore) {
	r.previews = previews
}

func (r *RecoveryLoop) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.runOnce(ctx, time.Now()); err != nil {
				r.logger.Error().Err(err).Msg("recovery loop failed")
			}
		}
	}
}

func (r *RecoveryLoop) runOnce(ctx context.Context, now time.Time) error {
	staleBefore := now.Add(-r.deadNodeTimeout)
	if _, err := r.nodes.MarkStaleNodesDead(ctx, staleBefore); err != nil {
		return fmt.Errorf("mark stale nodes dead: %w", err)
	}
	if r.executors != nil {
		if _, err := r.executors.ReclaimLost(ctx, staleBefore, r.reclaimBatchSize); err != nil {
			return fmt.Errorf("reclaim lost session executors: %w", err)
		}
	}
	if r.previews != nil {
		if _, err := r.previews.MarkExpiredPreviewRuntimesLost(ctx, now, "preview runtime lease expired"); err != nil {
			return fmt.Errorf("mark expired preview runtimes lost: %w", err)
		}
	}
	if _, err := r.jobs.ReclaimLostRunningJobs(ctx, staleBefore, r.reclaimBatchSize); err != nil {
		return fmt.Errorf("reclaim lost running jobs: %w", err)
	}
	return nil
}
