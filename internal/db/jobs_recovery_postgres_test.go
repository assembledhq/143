package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestReclaimLostRunningJobsSkipsLockedPublisher(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL recovery lock proof")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	schema := "test_job_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated recovery schema")
	cfg, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "parse disposable PostgreSQL settings")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "connect to isolated recovery schema")
	t.Cleanup(func() {
		pool.Close()
		_, cleanupErr := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "remove isolated recovery schema")
		admin.Close()
	})
	_, err = pool.Exec(ctx, `
		CREATE TABLE nodes (id text PRIMARY KEY, status text NOT NULL, last_heartbeat_at timestamptz NOT NULL);
		CREATE TABLE sessions (id uuid PRIMARY KEY, org_id uuid NOT NULL, snapshot_key text, recovery_state text, recovery_queued_at timestamptz, recovery_started_at timestamptz, runtime_stop_reason text);
		CREATE TABLE jobs (id uuid PRIMARY KEY, org_id uuid NOT NULL, job_type text NOT NULL, payload jsonb NOT NULL, status text NOT NULL, locked_by_node_id text, locked_at timestamptz, lease_expires_at timestamptz, last_error text, run_owner_id text, owner_kind text, lock_token uuid, run_at timestamptz, updated_at timestamptz);
	`)
	require.NoError(t, err, "create recovery query dependencies")
	orgID, lockedID, freeID := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO nodes VALUES ('dead-worker','dead',now()-interval '10 minutes')`)
	require.NoError(t, err, "seed dead worker")
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,job_type,payload,status,locked_by_node_id,locked_at,lease_expires_at,owner_kind) VALUES ($1,$3,'run_code_review','{}','running','dead-worker',now()-interval '10 minutes',now()-interval '5 minutes','worker'),($2,$3,'run_code_review','{}','running','dead-worker',now()-interval '9 minutes',now()-interval '5 minutes','worker')`, lockedID, freeID, orgID)
	require.NoError(t, err, "seed two expired review jobs")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin simulated publisher transaction")
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, `SELECT id FROM jobs WHERE id=$1 FOR UPDATE`, lockedID)
	require.NoError(t, err, "hold first expired job row")

	recoverCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	reclaimed, err := NewJobStore(pool).ReclaimLostRunningJobs(recoverCtx, time.Now().Add(-90*time.Second), 1)
	require.NoError(t, err, "recovery should not block behind the publisher")
	require.Equal(t, int64(1), reclaimed, "recovery should requeue the unlocked expired job")
	var lockedStatus, freeStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id=$1`, lockedID).Scan(&lockedStatus), "read locked job status")
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id=$1`, freeID).Scan(&freeStatus), "read unlocked job status")
	require.Equal(t, "running", lockedStatus, "publisher-owned job should remain running")
	require.Equal(t, "pending", freeStatus, "unlocked expired job should be recovered")
	_, err = tx.Exec(ctx, `UPDATE jobs SET lock_token=$2,lease_expires_at=now()+interval '5 minutes' WHERE id=$1`, lockedID, uuid.New())
	require.NoError(t, err, "simulate the publisher renewing its lease before releasing the row")
	require.NoError(t, tx.Commit(ctx), "release simulated publisher lock")
	_, err = pool.Exec(ctx, `UPDATE nodes SET status='active',last_heartbeat_at=now() WHERE id='dead-worker'`)
	require.NoError(t, err, "simulate the worker becoming healthy with its renewed lease")
	reclaimed, err = NewJobStore(pool).ReclaimLostRunningJobs(ctx, time.Now().Add(-90*time.Second), 1)
	require.NoError(t, err, "recovery should tolerate the publisher renewing its lease")
	require.Equal(t, int64(0), reclaimed, "renewed lease must not be revoked")
	_, err = pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=now()-interval '1 minute' WHERE id=$1`, lockedID)
	require.NoError(t, err, "expire the renewed lease for a later sweep")
	reclaimed, err = NewJobStore(pool).ReclaimLostRunningJobs(ctx, time.Now().Add(-90*time.Second), 1)
	require.NoError(t, err, "recovery should resume after the new lease expires")
	require.Equal(t, int64(1), reclaimed, "previously locked job should be requeued after its renewed lease expires")
}
