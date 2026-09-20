package cluster

import (
	"context"

	"github.com/assembledhq/143/internal/db"
)

const (
	schedulerLockID = 143143143
	// automationTargetSweepLockID serializes the per-target automation
	// sweeps, which run on their own faster loop than the scheduler tick.
	automationTargetSweepLockID = 143143144
)

type SchedulerLock struct {
	pool   db.DBTX
	lockID int64
}

func NewSchedulerLock(pool db.DBTX) *SchedulerLock {
	return &SchedulerLock{pool: pool, lockID: schedulerLockID}
}

// NewAutomationTargetSweepLock returns the lock the per-target sweep loop
// holds while it runs.
func NewAutomationTargetSweepLock(pool db.DBTX) *SchedulerLock {
	return &SchedulerLock{pool: pool, lockID: automationTargetSweepLockID}
}

func (s *SchedulerLock) TryAcquire(ctx context.Context) (bool, error) {
	var acquired bool
	err := s.pool.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, s.lockID).Scan(&acquired)
	return acquired, err
}

func (s *SchedulerLock) Release(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `SELECT pg_advisory_unlock($1)`, s.lockID)
	return err
}
