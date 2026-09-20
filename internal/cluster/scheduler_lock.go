package cluster

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assembledhq/143/internal/db"
)

const (
	schedulerLockID = 143143143
	// automationTargetSweepLockID serializes the per-target automation
	// sweeps, which run on their own faster loop than the scheduler tick.
	automationTargetSweepLockID = 143143144
)

// A session-level advisory lock belongs to one connection: taking it on a
// pooled connection and releasing it on whichever connection the pool hands
// out next unlocks nothing and leaves the lock held until that backend goes
// away, which would stop every other replica from ever running the loop
// again. The lock therefore pins one connection for its whole lifetime.
// Only a real pool can pin one; a test double falls back to the pooled
// statements, which is harmless because nothing else contends for the lock
// in a test.

type SchedulerLock struct {
	pool   db.DBTX
	lockID int64

	mu   sync.Mutex
	conn *pgxpool.Conn
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
	pool, ok := s.pool.(*pgxpool.Pool)
	if !ok {
		var acquired bool
		err := s.pool.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, s.lockID).Scan(&acquired)
		return acquired, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return false, fmt.Errorf("advisory lock %d is already held by this process", s.lockID)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire connection for advisory lock: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, s.lockID).Scan(&acquired); err != nil {
		conn.Release()
		return false, fmt.Errorf("take advisory lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return false, nil
	}
	s.conn = conn
	return true, nil
}

func (s *SchedulerLock) Release(ctx context.Context) error {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	if conn == nil {
		_, err := s.pool.Exec(ctx, `SELECT pg_advisory_unlock($1)`, s.lockID)
		return err
	}
	// The connection goes back to the pool either way: a connection closed
	// with the lock still held releases it when the backend ends, which is
	// the worst case if the unlock statement itself fails.
	defer conn.Release()
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, s.lockID).Scan(&unlocked); err != nil {
		return fmt.Errorf("release advisory lock: %w", err)
	}
	if !unlocked {
		return fmt.Errorf("advisory lock %d was not held by this connection", s.lockID)
	}
	return nil
}
