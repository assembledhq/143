package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/requestctx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PoolOptions struct {
	MaxConns        int32
	MaxConnIdleTime time.Duration
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
	if opts.MaxConnIdleTime > 0 {
		config.MaxConnIdleTime = opts.MaxConnIdleTime
	}
	previousPrepareConn := config.PrepareConn
	previousAfterRelease := config.AfterRelease
	var mutationConnections sync.Map
	config.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		if previousPrepareConn != nil {
			valid, prepareErr := previousPrepareConn(ctx, conn)
			if !valid || prepareErr != nil {
				return valid, prepareErr
			}
		}
		mutationID := requestctx.MutationID(ctx)
		if mutationID == uuid.Nil {
			return true, nil
		}
		_, err := conn.Exec(ctx, `SELECT set_config('app.client_mutation_id', $1, false)`, mutationID.String())
		if err == nil {
			mutationConnections.Store(conn.PgConn().PID(), struct{}{})
		}
		return err == nil, err
	}
	config.AfterRelease = func(conn *pgx.Conn) bool {
		if _, dirty := mutationConnections.LoadAndDelete(conn.PgConn().PID()); dirty {
			if _, err := conn.Exec(context.Background(), `SELECT set_config('app.client_mutation_id', '', false)`); err != nil {
				return false
			}
		}
		return previousAfterRelease == nil || previousAfterRelease(conn)
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

// isUniqueViolation reports a postgres unique_violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	type sqlStateErr interface{ SQLState() string }
	var s sqlStateErr
	if errors.As(err, &s) {
		return s.SQLState() == "23505"
	}
	return false
}
