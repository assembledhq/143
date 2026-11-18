package main

import (
	"context"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/version"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Two short-lived connections let a heartbeat proceed alongside lease renewal.
// Ordinary work, advisory-lock leadership and session executors never use this
// reserve. These process-local overrides do not change inherited configuration.
func workerControlPlanePoolConfig(cfg config.Config) (*pgxpool.Config, error) {
	if cfg.Mode != "worker" && cfg.Mode != "all" {
		return nil, nil
	}
	poolConfig, err := db.NewPoolConfig(cfg.DatabaseURL, db.PoolOptions{
		MaxConns:        2,
		MaxConnIdleTime: cfg.DatabaseMaxConnIdleTime,
		ApplicationName: serverLifecycleConnectionApplicationName(cfg.Mode, version.BuildSHA),
	})
	if err != nil {
		return nil, err
	}
	// Explicitly override URL pool minimums as well as its maximum: the
	// reserve can idle at zero and never expands every executor's normal pool.
	poolConfig.MinConns = 0
	poolConfig.MinIdleConns = 0
	// A client deadline does not guarantee PostgreSQL has stopped its row-lock
	// wait. Bound the server too, so lost cancellation packets cannot
	// accumulate abandoned liveness writes behind a locked row.
	poolConfig.ConnConfig.RuntimeParams["lock_timeout"] = "4s"
	poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = "5s"
	poolConfig.ConnConfig.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{
			Conn: conn, DeadlineDelay: time.Second,
		}
	}
	return poolConfig, nil
}

func newWorkerControlPlanePool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolConfig, err := workerControlPlanePoolConfig(cfg)
	if err != nil || poolConfig == nil {
		return nil, err
	}
	startupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(startupCtx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create worker control-plane pool: %w", err)
	}
	if err := pool.Ping(startupCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping worker control-plane database: %w", err)
	}
	return pool, nil
}
