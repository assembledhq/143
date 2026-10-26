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
	if pool == nil {
		return nil, errors.New("thread send transactions: pool is required")
	}
	return &ThreadSendTransactions{pool: pool}, nil
}

// Run delegates the entire unit, including its budget, setup, settlement and
// narrowly bounded replay policy, to RunLifecycleTransaction. The callback must
// obey that runner's DB-only contract, reset captured values on every attempt,
// and discard all results/effects on failure or uncertain commit. This wrapper
// adds no transaction settings, retry policy or lifecycle/admission authority.
func (s *ThreadSendTransactions) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	if s == nil || s.pool == nil {
		return errors.New("thread send transactions: pool is required")
	}
	return RunLifecycleTransaction(ctx, s.pool, work)
}
