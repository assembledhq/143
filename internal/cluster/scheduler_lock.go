package cluster

import (
	"context"

	"github.com/assembledhq/143/internal/db"
)

const schedulerLockID = 143143143

type SchedulerLock struct {
	pool db.DBTX
}

func NewSchedulerLock(pool db.DBTX) *SchedulerLock {
	return &SchedulerLock{pool: pool}
}

func (s *SchedulerLock) TryAcquire(ctx context.Context) (bool, error) {
	var acquired bool
	err := s.pool.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, schedulerLockID).Scan(&acquired)
	return acquired, err
}

func (s *SchedulerLock) Release(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `SELECT pg_advisory_unlock($1)`, schedulerLockID)
	return err
}
