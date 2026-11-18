package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These are query-pattern probes, not tests of an implemented compatibility
// release. They exercise the competing lock edges identified in M0 on separate
// connections. Actual callers and both rolling-deploy transitions need 0a tests.
func TestSessionLifecycleLockOrderProbePostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		child string
	}{
		{name: "request first answer versus session first send", child: "session_human_input_requests"},
		{name: "automation run first dispatch versus session first grant", child: "automation_runs"},
		{name: "thread first admission versus session first admission", child: "session_threads"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID, childID := newSessionLifecycleLockProbe(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			childTable := pgx.Identifier{tt.child}.Sanitize()
			_, err := pool.Exec(ctx, "INSERT INTO "+childTable+" (id, org_id, session_id) VALUES ($1,$2,$3)", childID, orgID, sessionID)
			require.NoError(t, err, "seed the exact competing child in this test's isolated schema")

			oldTx := beginSessionLifecycleProbe(t, ctx, pool)
			_, err = oldTx.Exec(ctx, "SELECT id FROM "+childTable+" WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, childID)
			require.NoError(t, err, "pre-floor query pattern should hold the child before requesting the parent")
			floorTx := beginSessionLifecycleProbe(t, ctx, pool)
			_, err = floorTx.Exec(ctx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, sessionID)
			require.NoError(t, err, "compatibility query pattern should acquire the parent first")
			_, err = floorTx.Exec(ctx, "INSERT INTO probe_effects(org_id, label) VALUES ($1,'aborted')", orgID)
			require.NoError(t, err, "stage a transaction-local effect to prove that abort removes earlier writes")

			oldPID := backendPID(t, ctx, oldTx)
			oldDone := make(chan error, 1)
			go func() {
				_, lockErr := oldTx.Exec(ctx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, sessionID)
				if lockErr == nil {
					_, lockErr = oldTx.Exec(ctx, "INSERT INTO probe_effects(org_id, label) VALUES ($1,'old')", orgID)
				}
				if lockErr == nil {
					lockErr = oldTx.Commit(ctx)
				}
				oldDone <- lockErr
			}()
			// Observe the server's actual wait graph, not an assumed sleep order.
			waitForBlockedBackend(t, ctx, floorTx, oldPID)
			_, err = floorTx.Exec(ctx, "SELECT id FROM "+childTable+" WHERE org_id=$1 AND id=$2 FOR UPDATE NOWAIT", orgID, childID)
			requireSessionLifecycleSQLState(t, err, "55P03", "NOWAIT should break the observed parent/child cycle before waiting on the second edge")
			require.NoError(t, floorTx.Rollback(ctx), "a confirmed whole-transaction abort must release the parent and staged writes")
			require.NoError(t, waitForResult(t, ctx, oldDone), "the pre-floor contender should progress after the compatibility transaction aborts")

			// A fresh transaction revalidates and reacquires every row; retrying only
			// the failed statement in PostgreSQL's aborted transaction cannot work.
			retryTx := beginSessionLifecycleProbe(t, ctx, pool)
			_, err = retryTx.Exec(ctx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, sessionID)
			require.NoError(t, err, "retry should reacquire the parent in a fresh transaction")
			_, err = retryTx.Exec(ctx, "SELECT id FROM "+childTable+" WHERE org_id=$1 AND id=$2 FOR UPDATE NOWAIT", orgID, childID)
			require.NoError(t, err, "retry should acquire the child after the old transaction drains")
			_, err = retryTx.Exec(ctx, "INSERT INTO probe_effects(org_id, label) VALUES ($1,'floor')", orgID)
			require.NoError(t, err, "retry should stage only its own accepted effect")
			require.NoError(t, retryTx.Commit(ctx), "retry should commit after acquiring the complete lock set")
			rows, err := pool.Query(ctx, "SELECT label FROM probe_effects WHERE org_id=$1 ORDER BY label", orgID)
			require.NoError(t, err, "inspect only this tenant's committed query-probe effects")
			actual, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err, "collect the committed query-probe effects")
			require.Equal(t, []string{"floor", "old"}, actual, "aborted attempts must leave no durable effect and each successful transaction should commit once")
		})
	}
}

func TestSessionLifecycleImplicitFKLockProbePostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, sessionID, childID := newSessionLifecycleLockProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner := beginSessionLifecycleProbe(t, ctx, pool)
	_, err := owner.Exec(ctx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, sessionID)
	require.NoError(t, err, "hold the referenced session row with a conflicting lock")
	contender := beginSessionLifecycleProbe(t, ctx, pool)
	_, err = contender.Exec(ctx, "SET LOCAL lock_timeout = '250ms'")
	require.NoError(t, err, "bound implicit foreign-key waits separately from explicit NOWAIT locks")
	_, err = contender.Exec(ctx, "INSERT INTO probe_effects(org_id, label) VALUES ($1,'aborted')", orgID)
	require.NoError(t, err, "stage an effect before the implicit foreign-key wait")
	done := make(chan error, 1)
	go func() {
		_, insertErr := contender.Exec(ctx, "INSERT INTO session_human_input_requests(id,org_id,session_id) VALUES ($1,$2,$3)", childID, orgID, sessionID)
		done <- insertErr
	}()
	// Keep the owner locked until the insert returns. The 55P03 result proves
	// the implicit FK conflict without requiring a sampler to catch a 250ms
	// wait window on a busy CI host.
	requireSessionLifecycleSQLState(t, waitForResult(t, ctx, done), "55P03", "lock_timeout should bound a foreign-key wait even though the insert has no NOWAIT clause")
	_, err = contender.Exec(ctx, "SELECT 1")
	requireSessionLifecycleSQLState(t, err, "25P02", "a failed lock attempt must not permit statement-only retry in the aborted transaction")
	require.NoError(t, contender.Rollback(ctx), "confirm rollback before attempting any future transaction")
	require.NoError(t, owner.Rollback(ctx), "release the referenced parent after observing the bounded wait")
	var actual []string
	rows, err := pool.Query(ctx, "SELECT label FROM probe_effects WHERE org_id=$1 ORDER BY label", orgID)
	require.NoError(t, err, "inspect effects after the foreign-key timeout")
	actual, err = pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect effects after the confirmed abort")
	require.Equal(t, []string{}, actual, "the timed-out foreign-key transaction must commit none of its staged effects")
}

func requireSessionLifecycleSQLState(t *testing.T, err error, code, message string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, message)
	require.Equal(t, code, pgErr.Code, message)
}

func beginSessionLifecycleProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin an independent connection's query-probe transaction")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := tx.Rollback(cleanupCtx)
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			require.NoError(t, err, "cleanup should rollback only this probe's unfinished transaction")
		}
	})
	return tx
}

func newSessionLifecycleLockProbe(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL database for M0 query-pattern probes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err, "connect to the explicitly configured test database")
	schema := "lifecycle_lock_probe_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	require.NoError(t, err, "create an owned isolated schema for each parallel lock probe")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		require.NoError(t, err, "remove only this test's owned schema")
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "parse the isolated query-probe pool configuration")
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "open the isolated query-probe pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE organizations (id uuid PRIMARY KEY);
		CREATE TABLE sessions (id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id));
		CREATE TABLE session_human_input_requests (id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id), session_id uuid NOT NULL REFERENCES sessions(id));
		CREATE TABLE automation_runs (id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id), session_id uuid NOT NULL REFERENCES sessions(id));
		CREATE TABLE session_threads (id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id), session_id uuid NOT NULL REFERENCES sessions(id));
		CREATE TABLE probe_effects (org_id uuid NOT NULL REFERENCES organizations(id), label text NOT NULL);
	`)
	require.NoError(t, err, "create minimal lock-bearing rows rather than simulate an unimplemented lifecycle schema")
	orgID, sessionID, childID := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO organizations(id) VALUES ($1)", orgID)
	require.NoError(t, err, "seed the probe tenant")
	_, err = pool.Exec(ctx, "INSERT INTO sessions(id,org_id) VALUES ($1,$2)", sessionID, orgID)
	require.NoError(t, err, "seed the probe session")
	return pool, orgID, sessionID, childID
}
