package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newRetirementPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for retirement admission concurrency proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect only to disposable PostgreSQL")
	schema := "retirement_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated retirement schema")
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse disposable database configuration")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create isolated retirement pool")
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "drop only this retirement fixture")
		require.NoError(t, admin.Close(context.Background()), "close disposable database connection")
	})
	_, err = pool.Exec(ctx, `
CREATE TABLE nodes(id text PRIMARY KEY,host text DEFAULT '',mode text DEFAULT 'worker',status text DEFAULT 'draining',drain_intent text DEFAULT 'planned_rollout',last_heartbeat_at timestamptz DEFAULT clock_timestamp(),drain_requested_at timestamptz DEFAULT now(),drain_budget_expires_at timestamptz);
CREATE TABLE worker_deploy_events(id bigserial PRIMARY KEY,node_id text,deploy_id text,build_sha text,event_type text,drain_intent text,created_at timestamptz DEFAULT now());
CREATE TABLE jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL DEFAULT gen_random_uuid(),status text DEFAULT 'pending',locked_by_node_id text,lock_token uuid,run_owner_id text,owner_kind text,job_type text DEFAULT 'synthetic',lease_expires_at timestamptz);
CREATE TABLE preview_instances(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL DEFAULT gen_random_uuid(),session_id uuid,preview_target_id uuid,status text DEFAULT 'stopped',worker_node_id text DEFAULT '',preview_handle text DEFAULT '',port integer DEFAULT 0,runtime_workspace_revision text DEFAULT '',preview_holding_container boolean DEFAULT false);
CREATE TABLE preview_runtimes(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL DEFAULT gen_random_uuid(),preview_instance_id uuid,runtime_epoch integer DEFAULT 1,status text DEFAULT 'stopped',worker_node_id text DEFAULT '',preview_handle text DEFAULT '',endpoint_url text DEFAULT '',primary_port integer DEFAULT 0,lease_expires_at timestamptz DEFAULT now()+interval '1 minute');
CREATE TABLE session_executors(host_node_id text,status text,runtime_deadline_at timestamptz);
CREATE TABLE sessions(worker_node_id text,deleted_at timestamptz,turn_holding_container boolean,container_id text,status text,pending_snapshot_key text,pending_snapshot_set_at timestamptz);
CREATE TABLE session_sandbox_holders(owner_node_id text,status text,expires_at timestamptz);
INSERT INTO nodes(id) VALUES('old'),('other');
INSERT INTO worker_deploy_events(node_id,deploy_id,build_sha,event_type,drain_intent) VALUES('old','attempt','sha','node_marked_draining','planned_rollout'),('other','attempt','sha','node_marked_draining','planned_rollout');`)
	require.NoError(t, err, "create synthetic tables matching retirement ownership contracts")
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000312_worker_retirement_seals.up.sql"))
	require.NoError(t, err, "read the production retirement migration")
	_, err = pool.Exec(ctx, string(migration))
	require.NoError(t, err, "apply the exact production retirement admission triggers")
	return pool
}

func retirementParams() SealNodeRetirementParams {
	return SealNodeRetirementParams{NodeID: "old", DeployID: "attempt", BuildSHA: "sha"}
}

func requireRetirementRefusal(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "retired admission must return a database error and roll back its statement")
	require.Equal(t, "55000", pgErr.Code, "retired admission must fail closed with the documented SQLSTATE")
}

func TestNodeRetirementSealPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seed string
		busy bool
	}{
		{name: "idle seals"},
		{name: "running job blocks", seed: `INSERT INTO jobs(status,locked_by_node_id) VALUES('running','old')`, busy: true},
		{name: "executor blocks", seed: `INSERT INTO session_executors(host_node_id,status) VALUES('old','running')`, busy: true},
		{name: "preview without runtime blocks", seed: `INSERT INTO preview_instances(status,worker_node_id) VALUES('ready','old')`, busy: true},
		{name: "expired runtime blocks", seed: `INSERT INTO preview_runtimes(status,worker_node_id,lease_expires_at) VALUES('ready','old',now()-interval '1 day')`, busy: true},
		{name: "session hold blocks", seed: `INSERT INTO sessions(worker_node_id,turn_holding_container) VALUES('old',true)`, busy: true},
		{name: "sandbox holder blocks", seed: `INSERT INTO session_sandbox_holders(owner_node_id,status,expires_at) VALUES('old','active',now()+interval '1 hour')`, busy: true},
		{name: "pending upload blocks", seed: `INSERT INTO sessions(worker_node_id,pending_snapshot_key,pending_snapshot_set_at) VALUES('old','synthetic',now())`, busy: true},
		{name: "detached cleanup blocks", seed: `INSERT INTO jobs(status,locked_by_node_id,job_type) VALUES('running','old','cleanup_stale_sandbox')`, busy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newRetirementPool(t)
			ctx := context.Background()
			if tt.seed != "" {
				_, err := pool.Exec(ctx, tt.seed)
				require.NoError(t, err, "seed the exact durable retirement blocker")
			}
			seal, err := NewNodeRetirementStore(pool).Seal(ctx, retirementParams())
			if tt.busy {
				require.ErrorIs(t, err, ErrNodeRetirementBusy, "positive ownership must block sealing regardless of lease age")
				require.Equal(t, NodeRetirementSeal{}, seal, "busy must never return a retirement receipt")
			} else {
				require.NoError(t, err, "a fresh exactly drained idle generation should seal")
				require.Equal(t, retirementParams(), SealNodeRetirementParams{NodeID: seal.NodeID, DeployID: seal.DeployID, BuildSHA: seal.BuildSHA}, "the receipt binds all requested identities")
				require.True(t, seal.Sealed, "only committed retirement returns sealed")
				again, err := NewNodeRetirementStore(pool).Seal(ctx, retirementParams())
				require.NoError(t, err, "the exact same attempt may positively reconcile its immutable seal")
				require.Equal(t, seal, again, "reconciliation must preserve the original timestamp and identity")
			}
		})
	}
}

func TestNodeRetirementRejectsUnknownPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, change string }{
		{name: "missing generation", change: `DELETE FROM nodes WHERE id='old'`},
		{name: "stale heartbeat", change: `UPDATE nodes SET last_heartbeat_at=now()-interval '1 hour' WHERE id='old'`},
		{name: "future heartbeat", change: `UPDATE nodes SET last_heartbeat_at=now()+interval '1 hour' WHERE id='old'`},
		{name: "wrong intent", change: `UPDATE nodes SET drain_intent='host_maintenance' WHERE id='old'`},
		{name: "active generation", change: `UPDATE nodes SET status='active' WHERE id='old'`},
		{name: "missing drain receipt", change: `DELETE FROM worker_deploy_events WHERE node_id='old'`},
		{name: "different attempt", change: `UPDATE worker_deploy_events SET deploy_id='other-attempt' WHERE node_id='old'`},
		{name: "different build", change: `UPDATE worker_deploy_events SET build_sha='other-sha' WHERE node_id='old'`},
		{name: "receipt predates current drain", change: `UPDATE worker_deploy_events SET created_at=now()-interval '1 day' WHERE node_id='old'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newRetirementPool(t)
			_, err := pool.Exec(context.Background(), tt.change)
			require.NoError(t, err, "seed incomplete or mismatched generation evidence")
			seal, err := NewNodeRetirementStore(pool).Seal(context.Background(), retirementParams())
			require.Error(t, err, "unknown or ineligible generation must not seal")
			require.NotErrorIs(t, err, ErrNodeRetirementBusy, "failed evidence must not be mislabeled as verified busy")
			require.Equal(t, NodeRetirementSeal{}, seal, "unknown evidence must never authorize a stop")
		})
	}
}

//nolint:paralleltest // rejection subtests share the same sealed rows and must finish before the final cleanup assertions
func TestNodeRetirementAdmissionAndCleanupPostgres(t *testing.T) {
	t.Parallel()
	pool := newRetirementPool(t)
	ctx := context.Background()
	jobID, previewID, runtimeID := uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO jobs(id,locked_by_node_id) VALUES($1,'old'); INSERT INTO preview_instances(id,worker_node_id) VALUES($2,'old'); INSERT INTO preview_runtimes(id,worker_node_id) VALUES($3,'old')`, jobID, previewID, runtimeID)
	require.NoError(t, err, "seed terminal rows that a delayed writer might resurrect")
	_, err = NewNodeRetirementStore(pool).Seal(ctx, retirementParams())
	require.NoError(t, err, "seal idle generation before delayed admissions")
	tests := []struct{ name, query string }{
		{name: "new running job", query: `INSERT INTO jobs(status,locked_by_node_id) VALUES('running','old')`},
		{name: "job claim", query: `UPDATE jobs SET status='running',lock_token=gen_random_uuid() WHERE locked_by_node_id='old'`},
		{name: "new preview", query: `INSERT INTO preview_instances(status,worker_node_id) VALUES('starting','old')`},
		{name: "resurrect preview", query: `UPDATE preview_instances SET status='ready' WHERE worker_node_id='old'`},
		{name: "new runtime", query: `INSERT INTO preview_runtimes(status,worker_node_id) VALUES('starting','old')`},
		{name: "resurrect runtime", query: `UPDATE preview_runtimes SET status='ready' WHERE worker_node_id='old'`},
		{name: "old resume", query: `UPDATE nodes SET drain_intent='none' WHERE id='old'`},
		{name: "old restart", query: `UPDATE nodes SET status='active' WHERE id='old'`},
		{name: "seal delete", query: `DELETE FROM worker_retirement_seals WHERE node_id='old'`},
		{name: "seal rewrite", query: `UPDATE worker_retirement_seals SET deploy_id='new' WHERE node_id='old'`},
	}
	// These cases intentionally share the already sealed generation and run
	// sequentially to prove each rejected statement preserves its prior state.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tt.query)
			requireRetirementRefusal(t, err)
		})
	}
	_, err = pool.Exec(ctx, `INSERT INTO jobs(status,locked_by_node_id) VALUES('running','other'); INSERT INTO preview_instances(status,worker_node_id) VALUES('ready','other'); INSERT INTO preview_runtimes(status,worker_node_id) VALUES('ready','other'); UPDATE jobs SET status='succeeded',locked_by_node_id=NULL WHERE id=$1; UPDATE preview_instances SET status='stopped',preview_holding_container=false WHERE id=$2; UPDATE preview_runtimes SET status='stopped' WHERE id=$3; DELETE FROM nodes WHERE id='old'`, jobID, previewID, runtimeID)
	require.NoError(t, err, "another generation and terminal cleanup remain available")
	_, err = pool.Exec(ctx, `INSERT INTO nodes(id,status,drain_intent) VALUES('old','active','none')`)
	requireRetirementRefusal(t, err)
	afterGC, err := NewNodeRetirementStore(pool).Seal(ctx, retirementParams())
	require.NoError(t, err, "an exact committed seal remains positively reconcilable after node GC")
	require.True(t, afterGC.Sealed, "node cleanup must never remove the durable admission closure")
	params := retirementParams()
	params.DeployID = "other-attempt"
	_, err = NewNodeRetirementStore(pool).Seal(ctx, params)
	require.Error(t, err, "a different deployment cannot reuse an immutable seal after node GC")
	require.NotErrorIs(t, err, ErrNodeRetirementBusy, "identity mismatch is unknown rather than waitable busy")
}

func TestNodeRetirementRejectsReboundOwnersPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, seed, admission string }{
		{name: "job generation rebound", seed: `INSERT INTO jobs(status,locked_by_node_id,lock_token) VALUES('running','other',gen_random_uuid())`, admission: `UPDATE jobs SET locked_by_node_id='old' WHERE locked_by_node_id='other'`},
		{name: "preview generation rebound", seed: `INSERT INTO preview_instances(status,worker_node_id) VALUES('ready','other')`, admission: `UPDATE preview_instances SET worker_node_id='old' WHERE worker_node_id='other'`},
		{name: "runtime generation rebound", seed: `INSERT INTO preview_runtimes(status,worker_node_id) VALUES('ready','other')`, admission: `UPDATE preview_runtimes SET worker_node_id='old',runtime_epoch=runtime_epoch+1 WHERE worker_node_id='other'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newRetirementPool(t)
			ctx := context.Background()
			_, err := pool.Exec(ctx, tt.seed)
			require.NoError(t, err, "seed an existing owner on an unaffected generation")
			_, err = NewNodeRetirementStore(pool).Seal(ctx, retirementParams())
			require.NoError(t, err, "only the destination generation retires")
			_, err = pool.Exec(ctx, tt.admission)
			requireRetirementRefusal(t, err)
		})
	}
}

func TestNodeRetirementPreviewRefusalRollsBackStatementPostgres(t *testing.T) {
	t.Parallel()
	pool := newRetirementPool(t)
	ctx := context.Background()
	previewID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO preview_instances(id,worker_node_id) VALUES($1,'old'); INSERT INTO preview_runtimes(preview_instance_id,worker_node_id) VALUES($1,'old')`, previewID)
	require.NoError(t, err, "seed a terminal preview and runtime before sealing")
	_, err = NewNodeRetirementStore(pool).Seal(ctx, retirementParams())
	require.NoError(t, err, "seal before the delayed multi-write callback")
	_, err = pool.Exec(ctx, `WITH changed_parent AS (
		UPDATE preview_instances SET preview_handle='must-rollback' WHERE id=$1 RETURNING id
	) UPDATE preview_runtimes SET status='starting' FROM changed_parent WHERE preview_instance_id=changed_parent.id`, previewID)
	requireRetirementRefusal(t, err)
	var handle, status string
	err = pool.QueryRow(ctx, `SELECT pi.preview_handle,pr.status FROM preview_instances pi JOIN preview_runtimes pr ON pr.preview_instance_id=pi.id WHERE pi.id=$1`, previewID).Scan(&handle, &status)
	require.NoError(t, err, "read both rows after the rejected callback")
	require.Equal(t, []string{"", "stopped"}, []string{handle, status}, "an exception must roll back the entire CTE, preventing a silent cold-start fallback")
}

func TestNodeRetirementAdmissionAlwaysEnabledPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, admission string }{
		{name: "job", admission: `INSERT INTO jobs(status,locked_by_node_id) VALUES('running','old')`},
		{name: "preview", admission: `INSERT INTO preview_instances(status,worker_node_id) VALUES('starting','old')`},
		{name: "runtime", admission: `INSERT INTO preview_runtimes(status,worker_node_id) VALUES('starting','old')`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newRetirementPool(t)
			ctx := context.Background()
			_, err := NewNodeRetirementStore(pool).Seal(ctx, retirementParams())
			require.NoError(t, err, "seal before checking replica-role admission")
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin an isolated replica-role transaction")
			defer func() { _ = tx.Rollback(ctx) }()
			_, err = tx.Exec(ctx, `SET LOCAL session_replication_role=replica`)
			require.NoError(t, err, "set only this disposable transaction's replication role")
			_, err = tx.Exec(ctx, tt.admission)
			requireRetirementRefusal(t, err)
		})
	}
}

func TestNodeRetirementMigrationDownPostgres(t *testing.T) {
	t.Parallel()
	pool := newRetirementPool(t)
	ctx := context.Background()
	_, err := NewNodeRetirementStore(pool).Seal(ctx, retirementParams())
	require.NoError(t, err, "prove the up migration is active before testing explicit rollback")
	down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000312_worker_retirement_seals.down.sql"))
	require.NoError(t, err, "read the explicit rollback migration")
	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "the explicit down migration must remove all owned triggers and functions")
	_, err = pool.Exec(ctx, `INSERT INTO jobs(status,locked_by_node_id) VALUES('running','old'); INSERT INTO preview_instances(status,worker_node_id) VALUES('starting','old'); INSERT INTO preview_runtimes(status,worker_node_id) VALUES('starting','old')`)
	require.NoError(t, err, "explicit rollback restores the prior schema's admission semantics")
}

func waitForRetirementLock(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=current_setting('application_name') AND wait_event='advisory')`).Scan(&waiting)
		require.NoError(t, err, "observe this isolated fixture's blocked admission lock")
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("retirement transaction never blocked on the admission lock")
}

func TestNodeRetirementWaitsForPriorAdmissionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, admission string }{
		{name: "running claim", admission: `INSERT INTO jobs(status,locked_by_node_id) VALUES('running','old')`},
		{name: "preview startup", admission: `INSERT INTO preview_instances(status,worker_node_id) VALUES('starting','old')`},
		{name: "runtime startup", admission: `INSERT INTO preview_runtimes(status,worker_node_id) VALUES('starting','old')`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newRetirementPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			writer, err := pool.Begin(ctx)
			require.NoError(t, err, "begin admission before retirement")
			defer func() { _ = writer.Rollback(ctx) }()
			_, err = writer.Exec(ctx, tt.admission)
			require.NoError(t, err, "admit durable owner while holding the shared transaction lock")
			done := make(chan error, 1)
			go func() { _, err := NewNodeRetirementStore(pool).Seal(ctx, retirementParams()); done <- err }()
			waitForRetirementLock(t, pool)
			require.NoError(t, writer.Commit(ctx), "commit owner while the sealer waits for admission")
			require.ErrorIs(t, <-done, ErrNodeRetirementBusy, "a separate fresh snapshot after exclusive lock must observe the preceding admission")
		})
	}
}

type pausedRetirementPool struct {
	*pgxpool.Pool
	locked  chan struct{}
	release chan struct{}
}

func (p *pausedRetirementPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pausedRetirementTx{Tx: tx, locked: p.locked, release: p.release}, nil
}

type pausedRetirementTx struct {
	pgx.Tx
	locked  chan struct{}
	release chan struct{}
}

func (tx *pausedRetirementTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if err == nil && strings.Contains(sql, "SELECT pg_advisory_xact_lock(") {
		close(tx.locked)
		select {
		case <-tx.release:
		case <-ctx.Done():
			return tag, ctx.Err()
		}
	}
	return tag, err
}

func TestNodeRetirementFencesWaitingRowLockedAdmissionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, table, seed, admission string }{
		{name: "job claim", table: "jobs", seed: `INSERT INTO jobs(status,locked_by_node_id) VALUES('pending','old')`, admission: `UPDATE jobs SET status='running',lock_token=gen_random_uuid() WHERE locked_by_node_id='old'`},
		{name: "preview resurrection", table: "preview_instances", seed: `INSERT INTO preview_instances(status,worker_node_id) VALUES('stopped','old')`, admission: `UPDATE preview_instances SET status='starting' WHERE worker_node_id='old'`},
		{name: "runtime resurrection", table: "preview_runtimes", seed: `INSERT INTO preview_runtimes(status,worker_node_id) VALUES('stopped','old')`, admission: `UPDATE preview_runtimes SET status='starting' WHERE worker_node_id='old'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newRetirementPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := pool.Exec(ctx, tt.seed)
			require.NoError(t, err, "seed the terminal row before either transaction")
			paused := &pausedRetirementPool{Pool: pool, locked: make(chan struct{}), release: make(chan struct{})}
			sealed := make(chan error, 1)
			go func() { _, err := NewNodeRetirementStore(paused).Seal(ctx, retirementParams()); sealed <- err }()
			select {
			case <-paused.locked:
			case <-ctx.Done():
				t.Fatal("sealer did not acquire its exclusive advisory lock")
			}
			writer := make(chan error, 1)
			go func() { _, err := pool.Exec(ctx, tt.admission); writer <- err }()
			waitForRetirementLock(t, pool)
			_, err = pool.Exec(ctx, `SELECT 1 FROM `+tt.table+` FOR UPDATE NOWAIT`)
			var rowLockErr *pgconn.PgError
			require.ErrorAs(t, err, &rowLockErr, "the delayed writer must already own its row lock before waiting in the BEFORE trigger")
			require.Equal(t, "55P03", rowLockErr.Code, "prove the row-lock to advisory-lock ordering that must not deadlock")
			close(paused.release)
			require.NoError(t, <-sealed, "sealing uses MVCC counts and must finish without acquiring the waiting writer's row lock")
			requireRetirementRefusal(t, <-writer)
			var count int
			err = pool.QueryRow(ctx, `SELECT count(*) FROM worker_retirement_seals WHERE node_id='old'`).Scan(&count)
			require.NoError(t, err, "read the committed permanent seal")
			require.Equal(t, 1, count, "the committed seal must precede and reject the waiting admission")
		})
	}
}

func waitForRetirementNodeLock(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=current_setting('application_name') AND wait_event_type='Lock' AND wait_event IN ('transactionid','tuple'))`).Scan(&waiting)
		require.NoError(t, err, "observe only this fixture's node-control row lock wait")
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("retirement transaction never blocked on the node control row")
}

func TestNodeRetirementWaitsForEarlierNodeControlPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, change string }{
		{name: "resume", change: `UPDATE nodes SET drain_intent='none' WHERE id='old'`},
		{name: "different intent", change: `UPDATE nodes SET drain_intent='host_maintenance' WHERE id='old'`},
		{name: "new drain without receipt", change: `UPDATE nodes SET drain_requested_at=clock_timestamp() WHERE id='old'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newRetirementPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			writer, err := pool.Begin(ctx)
			require.NoError(t, err, "begin concurrent node control before sealing")
			defer func() { _ = writer.Rollback(ctx) }()
			_, err = writer.Exec(ctx, tt.change)
			require.NoError(t, err, "node control takes the row before the sealer")
			sealed := make(chan error, 1)
			go func() { _, err := NewNodeRetirementStore(pool).Seal(ctx, retirementParams()); sealed <- err }()
			waitForRetirementNodeLock(t, pool)
			require.NoError(t, writer.Commit(ctx), "commit the changed node state while retirement waits")
			err = <-sealed
			require.Error(t, err, "retirement must reobserve and reject changed node eligibility")
			require.NotErrorIs(t, err, ErrNodeRetirementBusy, "changed node intent is unknown rather than positive work evidence")
		})
	}
}

func TestNodeRetirementRejectsResumeWaitingOnSealPostgres(t *testing.T) {
	t.Parallel()
	pool := newRetirementPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	paused := &pausedRetirementPool{Pool: pool, locked: make(chan struct{}), release: make(chan struct{})}
	sealed := make(chan error, 1)
	go func() { _, err := NewNodeRetirementStore(paused).Seal(ctx, retirementParams()); sealed <- err }()
	select {
	case <-paused.locked:
	case <-ctx.Done():
		t.Fatal("sealer did not acquire node and advisory locks")
	}
	resumed := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, `UPDATE nodes SET drain_intent='none' WHERE id='old'`)
		resumed <- err
	}()
	waitForRetirementNodeLock(t, pool)
	close(paused.release)
	require.NoError(t, <-sealed, "the stable exact node may seal while resume waits behind it")
	requireRetirementRefusal(t, <-resumed)
}

func TestNodeRetirementAllowsPriorAdmissionFKWhileNodeControlWaitsPostgres(t *testing.T) {
	t.Parallel()
	pool := newRetirementPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `CREATE TABLE retirement_node_refs(node_id text REFERENCES nodes(id))`)
	require.NoError(t, err, "create a synthetic FK that takes KEY SHARE on the exact node")
	writer, err := pool.Begin(ctx)
	require.NoError(t, err, "begin a durable admission before the retirement attempt")
	defer func() { _ = writer.Rollback(ctx) }()
	_, err = writer.Exec(ctx, `INSERT INTO jobs(status,locked_by_node_id) VALUES('running','old')`)
	require.NoError(t, err, "the admission holds its shared advisory lock through commit")
	sealed := make(chan error, 1)
	go func() { _, err := NewNodeRetirementStore(pool).Seal(ctx, retirementParams()); sealed <- err }()
	waitForRetirementLock(t, pool)
	// The sealer owns the node row but is waiting on this earlier admission.
	// Node-control writes must wait, while an FK check by that same admission
	// must finish: otherwise a row-lock/advisory-lock cycle would deadlock.
	controlled := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, `UPDATE nodes SET drain_intent='none' WHERE id='old'`)
		controlled <- err
	}()
	waitForRetirementNodeLock(t, pool)
	_, err = writer.Exec(ctx, `SET LOCAL lock_timeout='1s'; INSERT INTO retirement_node_refs(node_id) VALUES('old')`)
	require.NoError(t, err, "the prior admission's real FK KEY SHARE must proceed while retirement owns NO KEY UPDATE")
	require.NoError(t, writer.Commit(ctx), "the admitted owner can commit and release shared admission without a lock cycle")
	require.ErrorIs(t, <-sealed, ErrNodeRetirementBusy, "retirement must observe that owner after the exclusive advisory lock becomes available")
	require.NoError(t, <-controlled, "node control proceeds only after the retirement observation releases its row lock")
	var intent string
	err = pool.QueryRow(ctx, `SELECT drain_intent FROM nodes WHERE id='old'`).Scan(&intent)
	require.NoError(t, err, "observe the serialized node-control result")
	require.Equal(t, "none", intent, "the completed control write follows the aborted busy seal attempt")
}
