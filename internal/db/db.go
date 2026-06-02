package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PoolOptions struct {
	MaxConns int32
}

// DBTX is the interface satisfied by pgxpool.Pool, pgx.Tx, and pgxmock.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// TxStarter is the interface for starting transactions. Satisfied by pgxpool.Pool and pgxmock.
type TxStarter interface {
	DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

func NewPoolConfig(databaseURL string, opts PoolOptions) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	if opts.MaxConns > 0 {
		config.MaxConns = opts.MaxConns
	}
	return config, nil
}

func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return NewPoolWithOptions(ctx, databaseURL, PoolOptions{})
}

func NewPoolWithOptions(ctx context.Context, databaseURL string, opts PoolOptions) (*pgxpool.Pool, error) {
	config, err := NewPoolConfig(databaseURL, opts)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
