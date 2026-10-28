package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ThreadSendTransactions is the sealed top-level transaction boundary for a
// thread send. Its only dependency is an owned pool; it exposes neither that
// pool nor a raw connection or alternate transaction starter.
type ThreadSendTransactions struct {
	pool *pgxpool.Pool
}

func NewThreadSendTransactions(pool *pgxpool.Pool) (*ThreadSendTransactions, error) {
	runner := &ThreadSendTransactions{pool: pool}
	if err := runner.Validate(); err != nil {
		return nil, err
	}
	return runner, nil
}

// Validate checks the owned dependency without acquiring a connection. It is a
// construction check, not database-health evidence or lifecycle authority.
func (s *ThreadSendTransactions) Validate() error {
	if s == nil || s.pool == nil {
		return errors.New("thread send transactions: pool is required")
	}
	return nil
}

// Run delegates the entire unit, including its budget, setup, settlement and
// narrowly bounded replay policy, to RunLifecycleTransaction. The callback must
// obey that runner's DB-only contract, reset captured values on every attempt,
// and discard all results/effects on failure or uncertain commit. This wrapper
// adds no transaction settings, retry policy or lifecycle/admission authority.
func (s *ThreadSendTransactions) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	if err := s.Validate(); err != nil {
		return err
	}
	return RunLifecycleTransaction(ctx, s.pool, work)
}
