package main

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/cluster"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestWorkerControlPlanePoolRoleAndConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode     string
		reserved bool
	}{{"worker", true}, {"all", true}, {"api", false}, {"", false}}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			cfg := config.Config{Mode: tt.mode, DatabaseURL: "postgres://test:synthetic@localhost/test?pool_max_conns=17&pool_min_conns=7&pool_min_idle_conns=5&lock_timeout=30s&statement_timeout=60s", DatabaseMaxConns: 4, DatabaseMaxConnIdleTime: time.Minute}
			before := cfg
			reserve, err := workerControlPlanePoolConfig(cfg)
			require.NoError(t, err, "role-specific pool configuration must parse")
			if tt.reserved {
				require.Equal(t, [3]int32{2, 0, 0}, [3]int32{reserve.MaxConns, reserve.MinConns, reserve.MinIdleConns}, "only the worker reserve must override URL pool budgets")
				require.Equal(t, time.Minute, reserve.MaxConnIdleTime, "reserved connections must retain idle cleanup")
				require.Equal(t, "4s", reserve.ConnConfig.RuntimeParams["lock_timeout"], "server lock waits must end before a normal renewal deadline")
				require.Equal(t, "5s", reserve.ConnConfig.RuntimeParams["statement_timeout"], "server statements must remain bounded if client cancellation cannot arrive")
				connection := &pgconn.PgConn{}
				watcher, ok := reserve.ConnConfig.BuildContextWatcherHandler(connection).(*pgconn.CancelRequestContextWatcherHandler)
				require.True(t, ok, "shorter context deadlines must cancel the server query instead of only closing its socket")
				require.Equal(t, connection, watcher.Conn, "cancellation must target the same reserved connection")
				require.Equal(t, time.Duration(0), watcher.CancelRequestDelay, "server cancellation must start immediately at the caller deadline")
				require.Equal(t, time.Second, watcher.DeadlineDelay, "a lost cancellation request must retain a bounded socket fallback")
			} else {
				require.Nil(t, reserve, "API-only processes must not open the worker reserve")
			}
			normal, err := db.NewPoolConfig(cfg.DatabaseURL, db.PoolOptions{MaxConns: cfg.DatabaseMaxConns})
			require.NoError(t, err, "ordinary worker and executor pool configuration must still parse")
			require.Equal(t, [3]int32{4, 7, 5}, [3]int32{normal.MaxConns, normal.MinConns, normal.MinIdleConns}, "control-plane settings must not change ordinary or inherited executor budgets")
			require.Equal(t, "30s", normal.ConnConfig.RuntimeParams["lock_timeout"], "the reserve must not change ordinary or inherited executor lock policy")
			require.Equal(t, "60s", normal.ConnConfig.RuntimeParams["statement_timeout"], "the reserve must not change ordinary or inherited executor statement policy")
			require.Equal(t, before, cfg, "creating the reserve must not mutate inherited process configuration")
		})
	}
}

func TestWorkerControlPlanePoolPostgres(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for real saturated-pool liveness proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	t.Cleanup(func() { require.NoError(t, admin.Close(context.Background()), "close fixture connection") })
	schema := "worker_control_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err, "create isolated control-plane fixture")
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err, "remove only the fixture schema")
	})
	_, err = admin.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err, "select isolated fixture")
	_, err = admin.Exec(ctx, `CREATE TABLE nodes(id text PRIMARY KEY,status text,drain_intent text DEFAULT 'none',last_heartbeat_at timestamptz,metadata jsonb,drain_requested_at timestamptz);
CREATE TABLE sessions(id uuid,org_id uuid,status text);
CREATE TABLE jobs(id uuid PRIMARY KEY,org_id uuid,job_type text,payload jsonb,status text,lock_token uuid,lease_expires_at timestamptz,updated_at timestamptz);`, pgx.QueryExecModeSimpleProtocol)
	require.NoError(t, err, "create synthetic liveness tables")
	connectionConfig, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse test database")
	connectionConfig.ConnConfig.RuntimeParams["search_path"] = schema
	connectionConfig.MaxConns, connectionConfig.MinConns, connectionConfig.MinIdleConns = 1, 0, 0
	ordinary, err := pgxpool.NewWithConfig(ctx, connectionConfig)
	require.NoError(t, err, "open deliberately saturatable ordinary pool")
	t.Cleanup(ordinary.Close)
	// Pass the same isolated database URL through the production role helper.
	controlURL, err := url.Parse(dsn)
	require.NoError(t, err, "parse isolated test URL")
	query := controlURL.Query()
	query.Set("search_path", schema)
	query.Set("application_name", schema)
	controlURL.RawQuery = query.Encode()
	control, err := newWorkerControlPlanePool(ctx, config.Config{Mode: "worker", DatabaseURL: controlURL.String()})
	require.NoError(t, err, "open production bounded control-plane reserve")
	t.Cleanup(control.Close)
	jobID, orgID, token := uuid.New(), uuid.New(), uuid.New()
	_, err = admin.Exec(ctx, `INSERT INTO nodes VALUES('test-worker','active','none',now()-interval '5 minutes','{}',NULL); INSERT INTO jobs VALUES($1,$2,'test','{}','running',$3,now()+interval '10 seconds',now())`, pgx.QueryExecModeSimpleProtocol, jobID, orgID, token)
	require.NoError(t, err, "seed stale heartbeat and running fenced job")
	held, err := ordinary.Acquire(ctx)
	require.NoError(t, err, "occupy the entire ordinary pool like a long transaction")
	t.Cleanup(held.Release)
	node := cluster.NewNodeManager(ordinary, zerolog.Nop(), "test-worker", "worker", cluster.WithControlPlaneDB(control))
	require.NoError(t, node.HeartbeatOnce(ctx), "heartbeat must proceed while ordinary work owns every connection")
	job, owned, err := db.NewJobStore(control).RenewLease(ctx, jobID, token, time.Minute)
	require.NoError(t, err, "fenced renewal must proceed while the ordinary pool is exhausted")
	require.True(t, owned, "the exact lock token must retain ownership")
	require.Equal(t, token, *job.LockToken, "reserved renewal must preserve fencing identity")
	require.Greater(t, time.Until(*job.LeaseExpiresAt), 50*time.Second, "a real database renewal must extend the lease")
	require.NoError(t, node.RequestDrain(ctx, time.Now()), "shutdown drain must use the reserve too")
	require.NoError(t, node.HeartbeatOnce(ctx), "reserved heartbeat must preserve local drain")
	var status string
	var fresh bool
	require.NoError(t, admin.QueryRow(ctx, `SELECT status,last_heartbeat_at > now()-interval '5 seconds' FROM nodes WHERE id='test-worker'`).Scan(&status, &fresh), "read actual persisted liveness")
	require.Equal(t, "draining", status, "reserved heartbeats must not reactivate drained admission")
	require.True(t, fresh, "heartbeat timestamp must advance on the reserved connection")
	require.Equal(t, int32(1), ordinary.Stat().AcquiredConns(), "ordinary work must remain occupied throughout liveness writes")
	// Check both cancellation paths before releasing the row lock. Otherwise a
	// disconnected backend could still apply its abandoned renewal after unlock.
	tests := []struct {
		name       string
		timeout    time.Duration
		wantCode   string
		wantCtxErr error
	}{
		{name: "caller deadline cancels server lock wait", timeout: 150 * time.Millisecond, wantCode: "57014", wantCtxErr: context.DeadlineExceeded},
		{name: "server bounds lock wait before caller deadline", timeout: 6 * time.Second, wantCode: "55P03"},
	}
	//nolint:paralleltest // The cases share a locked row and the fully occupied ordinary pool.
	for _, tt := range tests {
		// These cases intentionally share and sequentially lock one synthetic
		// row while the only ordinary connection remains occupied.
		t.Run(tt.name, func(t *testing.T) {
			var beforeExpiry time.Time
			require.NoError(t, admin.QueryRow(ctx, `SELECT lease_expires_at FROM jobs WHERE id=$1`, jobID).Scan(&beforeExpiry), "save the last confirmed lease before contention")
			locker, err := admin.Begin(ctx)
			require.NoError(t, err, "open independent row-lock transaction")
			t.Cleanup(func() {
				err := locker.Rollback(context.Background())
				require.True(t, err == nil || err == pgx.ErrTxClosed, "fixture row lock must be released")
			})
			_, err = locker.Exec(ctx, `SELECT 1 FROM jobs WHERE id=$1 FOR UPDATE`, jobID)
			require.NoError(t, err, "hold the lease row independently of both pools")
			renewCtx, renewCancel := context.WithTimeout(ctx, tt.timeout)
			defer renewCancel()
			renewResult := make(chan error, 1)
			go func() {
				_, _, err := db.NewJobStore(control).RenewLease(renewCtx, jobID, token, time.Minute)
				renewResult <- err
			}()
			require.Eventually(t, func() bool {
				var waiting int
				err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock'`, schema).Scan(&waiting)
				return err == nil && waiting == 1
			}, time.Second, time.Millisecond, "the actual PostgreSQL backend must be waiting on the locked lease")
			require.NoError(t, node.HeartbeatOnce(ctx), "one locked renewal must leave room for heartbeat")
			select {
			case err := <-renewResult:
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "PostgreSQL must cancel the blocked statement before the row is unlocked")
				require.Equal(t, tt.wantCode, pgErr.Code, "the configured client or server bound must cancel the lock wait")
				require.ErrorIs(t, renewCtx.Err(), tt.wantCtxErr, "the server lock bound must precede the caller deadline unless testing caller cancellation")
			case <-ctx.Done():
				t.Fatal("locked renewal must not wait indefinitely")
			}
			var waiting, connected int
			require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE wait_event_type='Lock'), count(*) FROM pg_stat_activity WHERE application_name=$1`, schema).Scan(&waiting, &connected), "read all real reserve backends before releasing the fixture lock")
			require.Equal(t, 0, waiting, "cancelled renewals must leave no server backend waiting to apply an abandoned write")
			require.LessOrEqual(t, connected, 2, "cancelled statements must reuse the bounded reserve instead of accumulating abandoned backends")
			require.NoError(t, locker.Rollback(ctx), "release the fixture lock only after server cancellation is proven")
			var afterExpiry time.Time
			require.NoError(t, admin.QueryRow(ctx, `SELECT lease_expires_at FROM jobs WHERE id=$1`, jobID).Scan(&afterExpiry), "read the lease after cancellation and unlock")
			require.Equal(t, beforeExpiry, afterExpiry, "an abandoned renewal must not extend the persisted lease after unlock")
		})
	}
	_, owned, err = db.NewJobStore(control).RenewLease(ctx, jobID, token, time.Minute)
	require.NoError(t, err, "a later lease renewal must recover after lock contention")
	require.True(t, owned, "bounded lock failure must not discard the fencing token")
	blockedCtx, blockedCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer blockedCancel()
	_, err = ordinary.Acquire(blockedCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded, "normal operations must still obey the unchanged ordinary connection budget")
}

func TestNewWorkerControlPlanePoolCanceledStartup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool, err := newWorkerControlPlanePool(ctx, config.Config{Mode: "worker", DatabaseURL: "postgres://test:synthetic@localhost/test"})
	require.ErrorIs(t, err, context.Canceled, "cancelled startup must close the constructed reserve and return promptly")
	require.Nil(t, pool, "failed startup must not return a live pool requiring cleanup")
	pool, err = newWorkerControlPlanePool(context.Background(), config.Config{Mode: "api", DatabaseURL: "invalid"})
	require.NoError(t, err, "API startup must not even parse a separate worker database")
	require.Nil(t, pool, "API processes must not create a worker control-plane pool")
}
