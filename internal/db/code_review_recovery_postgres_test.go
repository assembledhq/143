package db

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newReviewRecoveryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for admission and abandoned sandbox ownership proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, admin.Close(cleanupCtx), "close disposable admin connection")
	})
	schema := "review_recovery_causes_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated recovery schema")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(cleanupCtx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "drop only this test's schema")
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse disposable database configuration")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	// Admission still races across goroutines, with a bounded connection budget.
	cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = 4, 0, 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create isolated recovery pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
CREATE TABLE nodes(id text PRIMARY KEY,host text,mode text DEFAULT 'worker',status text DEFAULT 'active',drain_intent text DEFAULT 'none',started_at timestamptz DEFAULT now(),last_heartbeat_at timestamptz DEFAULT now(),metadata jsonb DEFAULT '{}');
CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid NOT NULL,origin text DEFAULT 'code_review',status text DEFAULT 'idle',container_id text,worker_node_id text,turn_holding_container boolean DEFAULT false,recovery_state text DEFAULT '',recovery_queued_at timestamptz,snapshot_key text,sandbox_state text DEFAULT 'running',created_at timestamptz DEFAULT now()-interval '1 day',last_activity_at timestamptz DEFAULT now()-interval '1 day');
CREATE TABLE code_review_session_metadata(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL,session_id uuid NOT NULL,review_output_key text DEFAULT 'output',pull_request_id uuid DEFAULT gen_random_uuid(),status text DEFAULT 'queued',created_at timestamptz DEFAULT now()-interval '1 day',completed_at timestamptz,review_output jsonb DEFAULT '{"result":"preserve"}');
CREATE TABLE code_review_revision_assessments(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),result_origin text,org_id uuid NOT NULL,session_id uuid NOT NULL,review_scope text DEFAULT 'full',status text,publication_state text DEFAULT 'not_started',publication_receipt jsonb);
CREATE TABLE jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL,payload jsonb,status text,job_type text,run_at timestamptz DEFAULT now(),attempts integer DEFAULT 0,max_attempts integer DEFAULT 8,last_error text,updated_at timestamptz DEFAULT now());
CREATE TABLE session_threads(org_id uuid NOT NULL,session_id uuid NOT NULL,status text);
CREATE TABLE session_executors(org_id uuid NOT NULL,session_id uuid NOT NULL,status text);
CREATE TABLE thread_runtimes(org_id uuid NOT NULL,session_id uuid NOT NULL,status text);
CREATE TABLE preview_instances(org_id uuid NOT NULL,session_id uuid NOT NULL,preview_holding_container boolean);
CREATE TABLE session_sandbox_holders(org_id uuid NOT NULL,session_id uuid NOT NULL,status text,expires_at timestamptz);
CREATE TABLE code_review_recheck_dispatches(job_id uuid,assessment_id uuid,org_id uuid NOT NULL,session_id uuid NOT NULL,status text);
CREATE TABLE pending_intents(id uuid PRIMARY KEY,org_id uuid NOT NULL,status text DEFAULT 'pending');`)
	require.NoError(t, err, "create synthetic capacity and ownership tables")
	return pool
}

func TestCodeReviewSandboxAdmissionConcurrentBurstPostgres(t *testing.T) {
	t.Parallel()
	pool := newReviewRecoveryPool(t)
	ctx := context.Background()
	org, other := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO nodes(id,host,metadata) VALUES('worker','shared-host','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":7,"live_sandbox_count":1,"reserved_sandbox_count":0}');
INSERT INTO sessions(id,org_id,container_id,worker_node_id) VALUES(gen_random_uuid(),$1,'one','worker'),(gen_random_uuid(),$1,'two','worker');
WITH s AS(INSERT INTO sessions(id,org_id) VALUES(gen_random_uuid(),$2) RETURNING id,org_id)
INSERT INTO code_review_revision_assessments(org_id,session_id,status) SELECT org_id,id,'reserved' FROM s;
INSERT INTO jobs(org_id,payload,status,job_type) SELECT org_id,jsonb_build_object('assessment_id',id),'pending','run_code_review_recheck' FROM code_review_revision_assessments;
WITH parked AS(INSERT INTO sessions(id,org_id) SELECT gen_random_uuid(),$2 FROM generate_series(1,128) RETURNING id,org_id), metadata AS(INSERT INTO code_review_session_metadata(org_id,session_id) SELECT org_id,id FROM parked RETURNING *) INSERT INTO jobs(org_id,payload,status,job_type,run_at) SELECT org_id,jsonb_build_object('session_id',session_id,'review_output_key','output'),'pending','run_code_review','infinity' FROM metadata;`, org, other)
	require.NoError(t, err, "seed heartbeat lag and another tenant's cold evidence reservation")
	const burst = 124
	ids := make([]uuid.UUID, burst)
	for i := range ids {
		ids[i] = uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO pending_intents(id,org_id) VALUES($1,$2)`, ids[i], org)
		require.NoError(t, err, "persist each request independently of attempt admission")
	}
	results := make(chan bool, burst)
	errors := make(chan error, burst)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				available, err := CodeReviewSandboxAvailable(ctx, tx, org, uuid.Nil)
				if err != nil {
					return err
				}
				if available {
					_, err = tx.Exec(ctx, `INSERT INTO sessions(id,org_id) VALUES($1,$2);
INSERT INTO code_review_session_metadata(org_id,session_id,created_at) VALUES($2,$1,now());
INSERT INTO jobs(org_id,payload,status,job_type) VALUES($2,jsonb_build_object('session_id',$1::text,'review_output_key','output'),'pending','run_code_review');
UPDATE pending_intents SET status='started' WHERE org_id=$2 AND id=$1;`, id, org)
				}
				results <- available
				return err
			})
			errors <- err
		}(id)
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err, "every concurrent admission should finish without losing intake")
	}
	var admitted int
	for ok := range results {
		if ok {
			admitted++
		}
	}
	require.Equal(t, 4, admitted, "two assigned containers and another tenant's reservation leave exactly four of seven slots")
	var started, pending, clocks int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE status='started'),COUNT(*) FILTER(WHERE status='pending') FROM pending_intents WHERE org_id=$1`, org).Scan(&started, &pending)
	require.NoError(t, err, "read durable intake after burst")
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_session_metadata WHERE org_id=$1`, org).Scan(&clocks)
	require.NoError(t, err, "read only admitted attempt clocks")
	require.Equal(t, []int{4, 120, 4}, []int{started, pending, clocks}, "unadmitted requests must remain pending without an attempt clock")
}

func TestCodeReviewCapacityWaitMigrationPostgres(t *testing.T) {
	t.Parallel()
	pool := newReviewRecoveryPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE TABLE code_review_pr_state(org_id uuid NOT NULL,wait_reason text NOT NULL CHECK(wait_reason IN ('','quiet_period','minimum_interval','draft','manual_pause','policy_disabled','already_approved','active_review','context_unavailable')),pending_input jsonb NOT NULL DEFAULT '{"identity":"preserve"}');`)
	require.NoError(t, err, "create historical wait constraint")
	up, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000310_code_review_capacity_wait.up.sql"))
	require.NoError(t, err, "read capacity wait migration")
	down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000310_code_review_capacity_wait.down.sql"))
	require.NoError(t, err, "read rollback compatibility migration")
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "extend historical constraint without modifying old migration")
	org := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_pr_state(org_id,wait_reason) VALUES($1,'sandbox_capacity')`, org)
	require.NoError(t, err, "persist the new capacity wait")
	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err, "rollback maps only the unsupported wait label")
	var reason string
	var pending json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT wait_reason,pending_input FROM code_review_pr_state WHERE org_id=$1`, org).Scan(&reason, &pending), "read preserved pending intent after rollback")
	require.Equal(t, "context_unavailable", reason, "older readers should see a supported wait label")
	require.JSONEq(t, `{"identity":"preserve"}`, string(pending), "rollback must retain pending request provenance")
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "reapplying the migration should restore capacity wait support")
}

func TestCodeReviewSandboxAdmissionInventoryPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sql  string
		warm bool
		want bool
	}{
		{name: "verified free slot", want: true},
		{name: "full", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{live_sandbox_count}','3')`},
		{name: "stale inventory", sql: `UPDATE nodes SET last_heartbeat_at=clock_timestamp()-interval '91 seconds'`},
		{name: "unknown physical host", sql: `UPDATE nodes SET metadata=metadata-'sandbox_host_id'`},
		{name: "draining generation with unknown identity", sql: `UPDATE nodes SET status='draining',metadata=metadata-'sandbox_host_id'; INSERT INTO nodes(id,host,started_at,metadata) VALUES('replacement','different-container',now()+interval '1 second','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":3,"live_sandbox_count":0,"reserved_sandbox_count":0}')`},
		{name: "overlapping unknown reservation fails closed", sql: `UPDATE nodes SET status='draining',metadata=metadata-'reserved_sandbox_count'; INSERT INTO nodes(id,host,started_at,metadata) VALUES('replacement','different-container',now()+interval '1 second','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":3,"live_sandbox_count":0,"reserved_sandbox_count":0}')`},
		{name: "unknown live count", sql: `UPDATE nodes SET metadata=metadata-'live_sandbox_count'`},
		{name: "malformed capacity", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{max_active_sandboxes}','"unknown"')`},
		{name: "count error", sql: `UPDATE nodes SET metadata=metadata||'{"live_sandbox_count_error":"unavailable"}'`},
		{name: "draining worker", sql: `UPDATE nodes SET drain_intent='deploy'`},
		{name: "newer stale active generation cannot hide fresh predecessor", sql: `INSERT INTO nodes(id,host,started_at,last_heartbeat_at,metadata) VALUES('replacement','new-container-host',clock_timestamp()+interval '1 second',clock_timestamp()-interval '91 seconds','{"sandbox_host_id":"192.0.2.1"}')`, want: true},
		{name: "newer dead generation cannot hide fresh predecessor", sql: `INSERT INTO nodes(id,host,started_at,status,metadata) VALUES('replacement','new-container-host',clock_timestamp()+interval '1 second','dead','{"sandbox_host_id":"192.0.2.1"}')`, want: true},
		{name: "newer stopped generation cannot hide fresh predecessor", sql: `INSERT INTO nodes(id,host,started_at,status,metadata) VALUES('replacement','new-container-host',clock_timestamp()+interval '1 second','stopped','{"sandbox_host_id":"192.0.2.1"}')`, want: true},
		{name: "newer fresh draining generation still blocks older active budget", sql: `INSERT INTO nodes(id,host,started_at,status,metadata) VALUES('replacement','new-container-host',clock_timestamp()+interval '1 second','draining','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":3,"live_sandbox_count":0,"reserved_sandbox_count":0}')`},
		{name: "stopped predecessor containers still occupy fresh physical host", sql: `UPDATE nodes SET status='stopped'; INSERT INTO nodes(id,host,started_at,metadata) VALUES('replacement','new-container-host',clock_timestamp()+interval '1 second','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":3,"live_sandbox_count":0,"reserved_sandbox_count":0}'); INSERT INTO sessions(id,org_id,container_id,worker_node_id) VALUES(gen_random_uuid(),gen_random_uuid(),'stopped-two','worker'),(gen_random_uuid(),gen_random_uuid(),'stopped-three','worker')`},
		{name: "replacement suppresses old heartbeat", sql: `INSERT INTO nodes(id,host,started_at,metadata) VALUES('replacement','new-container-host',now()+interval '1 second','{"sandbox_host_id":"192.0.2.1"}')`},
		{name: "overlapping process reservations consume shared headroom", sql: `UPDATE nodes SET status='draining',metadata=jsonb_set(metadata,'{reserved_sandbox_count}','2'); INSERT INTO nodes(id,host,started_at,metadata) VALUES('replacement','new-container-host',now()+interval '1 second','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":3,"live_sandbox_count":0,"reserved_sandbox_count":0}')`},
		{name: "finite future controllers retain reservations", sql: `WITH s AS(INSERT INTO sessions(id,org_id) SELECT gen_random_uuid(),gen_random_uuid() FROM generate_series(1,3) RETURNING *),m AS(INSERT INTO code_review_session_metadata(org_id,session_id) SELECT org_id,id FROM s RETURNING *) INSERT INTO jobs(org_id,payload,status,job_type,run_at) SELECT org_id,jsonb_build_object('session_id',session_id,'review_output_key',review_output_key),'pending','run_code_review',clock_timestamp()+interval '1 hour' FROM m`},
		{name: "parked controllers consume no runnable reservation", sql: `WITH s AS(INSERT INTO sessions(id,org_id) SELECT gen_random_uuid(),gen_random_uuid() FROM generate_series(1,5) RETURNING *),m AS(INSERT INTO code_review_session_metadata(org_id,session_id) SELECT org_id,id FROM s RETURNING *) INSERT INTO jobs(org_id,payload,status,job_type,run_at) SELECT org_id,jsonb_build_object('session_id',session_id,'review_output_key',review_output_key),'pending','run_code_review','infinity' FROM m`, want: true},
		{name: "dead-letter controllers consume no reservation", sql: `WITH s AS(INSERT INTO sessions(id,org_id) SELECT gen_random_uuid(),gen_random_uuid() FROM generate_series(1,5) RETURNING *),m AS(INSERT INTO code_review_session_metadata(org_id,session_id) SELECT org_id,id FROM s RETURNING *) INSERT INTO jobs(org_id,payload,status,job_type) SELECT org_id,jsonb_build_object('session_id',session_id,'review_output_key',review_output_key),'dead_letter','run_code_review' FROM m`, want: true},
		{name: "cold evidence supervisor reserves workspace", sql: `WITH s AS(INSERT INTO sessions(id,org_id) SELECT gen_random_uuid(),gen_random_uuid() FROM generate_series(1,3) RETURNING *),a AS(INSERT INTO code_review_revision_assessments(org_id,session_id,status) SELECT org_id,id,'reserved' FROM s RETURNING *) INSERT INTO jobs(org_id,payload,status,job_type) SELECT org_id,jsonb_build_object('assessment_id',id),'pending','run_code_review_recheck' FROM a`},
		{name: "saved publishing outcomes consume no cold slot", sql: `WITH s AS(INSERT INTO sessions(id,org_id) SELECT gen_random_uuid(),gen_random_uuid() FROM generate_series(1,3) RETURNING *),a AS(INSERT INTO code_review_revision_assessments(org_id,session_id,status,result_origin) SELECT org_id,id,'publishing','executed' FROM s RETURNING *) INSERT INTO jobs(org_id,payload,status,job_type) SELECT org_id,jsonb_build_object('assessment_id',id),'pending','run_code_review_recheck' FROM a`, want: true},
		{name: "predecessor containers occupy replacement host", sql: `UPDATE nodes SET status='draining'; INSERT INTO nodes(id,host,started_at,metadata) VALUES('replacement','new-container-host',now()+interval '1 second','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":3,"live_sandbox_count":0,"reserved_sandbox_count":0}'); INSERT INTO sessions(id,org_id,container_id,worker_node_id) VALUES(gen_random_uuid(),gen_random_uuid(),'old-two','worker'),(gen_random_uuid(),gen_random_uuid(),'old-three','worker')`},
		{name: "warm draining predecessor reuses replacement physical budget", sql: `UPDATE nodes SET status='draining'; INSERT INTO nodes(id,host,started_at,metadata) VALUES('replacement','new-container-host',now()+interval '1 second','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":3,"live_sandbox_count":3,"reserved_sandbox_count":0}')`, warm: true, want: true},
		{name: "warm owner can reuse a full fleet", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{live_sandbox_count}','3')`, warm: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newReviewRecoveryPool(t)
			ctx := context.Background()
			org, sessionID := uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO nodes(id,host,metadata) VALUES('worker','shared-host','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":3,"live_sandbox_count":0,"reserved_sandbox_count":0}'); INSERT INTO sessions(id,org_id,container_id,worker_node_id) VALUES($1,$2,'warm','worker');`, sessionID, org)
			require.NoError(t, err, "seed synthetic worker and exact warm ownership")
			if tt.sql != "" {
				_, err := pool.Exec(ctx, tt.sql)
				require.NoError(t, err, "set inventory condition")
			}
			warm := uuid.Nil
			if tt.warm {
				warm = sessionID
			}
			var available bool
			err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				var err error
				available, err = CodeReviewSandboxAvailable(ctx, tx, org, warm)
				return err
			})
			require.NoError(t, err, "inventory parsing should fail closed without cast failures")
			require.Equal(t, tt.want, available, "admission should match verified inventory and exact warm-session scope")
			if tt.warm {
				err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
					available, err = CodeReviewSandboxAvailable(ctx, tx, uuid.New(), warm)
					return err
				})
				require.NoError(t, err, "read another tenant's warm-session exception safely")
				require.False(t, available, "a warm session in another tenant must not bypass capacity")
			}
		})
	}
}

func TestCodeReviewAdmissionFreshnessUsesWallClockPostgres(t *testing.T) {
	t.Parallel()
	pool := newReviewRecoveryPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	org := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO nodes(id,metadata) VALUES('worker','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":3,"live_sandbox_count":0,"reserved_sandbox_count":0}')`)
	require.NoError(t, err, "seed verified inventory")
	blocker, err := pool.Begin(ctx)
	require.NoError(t, err, "hold the fleet admission lock before the next transaction")
	defer func() { _ = blocker.Rollback(ctx) }()
	_, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('code-review-sandbox-admission',0))`)
	require.NoError(t, err, "serialize behind a competing startup")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin admission before inventory expires")
	defer func() { _ = tx.Rollback(ctx) }()
	var start time.Time
	require.NoError(t, tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&start), "capture the old transaction clock")
	_, err = pool.Exec(ctx, `UPDATE nodes SET last_heartbeat_at=$1`, start.Add(-90*time.Second+100*time.Millisecond))
	require.NoError(t, err, "make heartbeat fresh only before the lock wait")
	type result struct {
		available bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		available, err := CodeReviewSandboxAvailable(ctx, tx, org, uuid.Nil)
		done <- result{available, err}
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)`, tx.Conn().PgConn().PID()).Scan(&waiting)
		return err == nil && waiting
	}, time.Second, 10*time.Millisecond, "actual admission must wait behind the fleet lock")
	_, err = blocker.Exec(ctx, `SELECT pg_sleep(0.2)`)
	require.NoError(t, err, "let inventory expire while admission waits")
	require.NoError(t, blocker.Commit(ctx), "release admission after heartbeat expiry")
	got := <-done
	require.NoError(t, got.err, "read inventory after acquiring the lock")
	require.False(t, got.available, "transaction-stable time cannot revive inventory that expired while waiting for admission")
}

func TestStaleTerminalCodeReviewContainerConcurrentOwnershipPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		admit bool
	}{
		{name: "only one GC wins exact container"},
		{name: "recheck admission wins PR lock", admit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newReviewRecoveryPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			org, sessionID, pr := uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,container_id,turn_holding_container,recovery_state) VALUES($1,$2,'container',true,'queued'); INSERT INTO code_review_session_metadata(org_id,session_id,pull_request_id,status) VALUES($2,$1,$3,'completed')`, sessionID, org, pr)
			require.NoError(t, err, "seed quiet terminal session with abandoned ownership")
			var admission pgx.Tx
			if tt.admit {
				admission, err = pool.Begin(ctx)
				require.NoError(t, err, "begin successor admission")
				defer func() { _ = admission.Rollback(context.Background()) }()
				_, err = admission.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+org.String()+":"+pr.String())
				require.NoError(t, err, "own shared PR admission lock before cleanup")
			}
			results := make(chan bool, 2)
			errors := make(chan error, 2)
			for range 2 {
				go func() {
					cleared, err := NewSessionStore(pool).FinalizeStaleTerminalCodeReviewContainer(ctx, org, sessionID, "container")
					results <- cleared
					errors <- err
				}()
			}
			if tt.admit {
				_, err = admission.Exec(ctx, `INSERT INTO code_review_revision_assessments(org_id,session_id,status) VALUES($1,$2,'reserved')`, org, sessionID)
				require.NoError(t, err, "reserve successor ownership before releasing PR lock")
				require.NoError(t, admission.Commit(ctx), "publish successor reservation atomically")
			}
			wins := 0
			for range 2 {
				require.NoError(t, <-errors, "both reconciliations must finish under the bounded context")
				if <-results {
					wins++
				}
			}
			want := 1
			if tt.admit {
				want = 0
			}
			require.Equal(t, want, wins, "container destruction must converge on one owner or yield to admitted recheck")
		})
	}
}

func TestStaleTerminalCodeReviewContainerFencesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{name: "abandoned terminal flags", want: true},
		{name: "recent session activity", sql: `UPDATE sessions SET last_activity_at=now()`},
		{name: "recent recovery request", sql: `UPDATE sessions SET recovery_queued_at=now()`},
		{name: "recent terminal outcome", sql: `UPDATE code_review_session_metadata SET completed_at=now()`},
		{name: "active metadata", sql: `UPDATE code_review_session_metadata SET status='running'`},
		{name: "missing metadata", sql: `DELETE FROM code_review_session_metadata`},
		{name: "another session origin", sql: `UPDATE sessions SET origin='manual'`},
		{name: "recovering session", sql: `UPDATE sessions SET recovery_state='recovering'`},
		{name: "running session", sql: `UPDATE sessions SET status='running'`},
		{name: "pending preparation", sql: `INSERT INTO jobs(org_id,payload,status,job_type) SELECT org_id,jsonb_build_object('session_id',id),'pending','prepare_code_review_workspace' FROM sessions`},
		{name: "active thread", sql: `INSERT INTO session_threads SELECT org_id,id,'awaiting_input' FROM sessions`},
		{name: "active executor", sql: `INSERT INTO session_executors SELECT org_id,id,'draining' FROM sessions`},
		{name: "paused runtime", sql: `INSERT INTO thread_runtimes SELECT org_id,id,'paused' FROM sessions`},
		{name: "preview owner", sql: `INSERT INTO preview_instances SELECT org_id,id,true FROM sessions`},
		{name: "retention holder", sql: `INSERT INTO session_sandbox_holders SELECT org_id,id,'active',now()+interval '1 hour' FROM sessions`},
		{name: "expired retention holder", sql: `INSERT INTO session_sandbox_holders SELECT org_id,id,'active',now()-interval '1 hour' FROM sessions`, want: true},
		{name: "publication uncertainty", sql: `INSERT INTO code_review_revision_assessments(org_id,session_id,status,publication_state) SELECT org_id,id,'failed','uncertain' FROM sessions`},
		{name: "reserved assessment", sql: `INSERT INTO code_review_revision_assessments(org_id,session_id,status) SELECT org_id,id,'reserved' FROM sessions`},
		{name: "pending recheck", sql: `INSERT INTO code_review_recheck_dispatches(org_id,session_id,status) SELECT org_id,id,'pending' FROM sessions`},
		{name: "changed container identity", sql: `UPDATE sessions SET container_id='new-container'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newReviewRecoveryPool(t)
			ctx := context.Background()
			org, sessionID := uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,container_id,worker_node_id,turn_holding_container,recovery_state,recovery_queued_at,snapshot_key) VALUES($1,$2,'old-container','worker',true,'queued',now()-interval '1 day','saved-snapshot');
INSERT INTO code_review_session_metadata(org_id,session_id,status,completed_at) VALUES($2,$1,'completed',now()-interval '1 day');
INSERT INTO code_review_revision_assessments(org_id,session_id,status,publication_state,publication_receipt) VALUES($2,$1,'completed','confirmed','{"review_id":42}');`, sessionID, org)
			require.NoError(t, err, "seed terminal outcome, immutable receipt, and abandoned flags")
			if tt.sql != "" {
				_, err := pool.Exec(ctx, tt.sql)
				require.NoError(t, err, "set durable ownership fence")
			}
			read := func() json.RawMessage {
				var value json.RawMessage
				err := pool.QueryRow(ctx, `SELECT jsonb_build_object('session',to_jsonb(s),'metadata',(SELECT jsonb_agg(to_jsonb(m)) FROM code_review_session_metadata m WHERE m.org_id=s.org_id AND m.session_id=s.id),'assessments',(SELECT jsonb_agg(to_jsonb(a)) FROM code_review_revision_assessments a WHERE a.org_id=s.org_id AND a.session_id=s.id)) FROM sessions s WHERE org_id=$1 AND id=$2`, org, sessionID).Scan(&value)
				require.NoError(t, err, "read all preserved session and outcome fields")
				return value
			}
			before := read()
			cleared, err := NewSessionStore(pool).FinalizeStaleTerminalCodeReviewContainer(ctx, org, sessionID, "old-container")
			require.NoError(t, err, "reconcile stale flags with durable fences")
			require.Equal(t, tt.want, cleared, "only quiet terminal work without an owner may transfer destruction")
			after := read()
			if tt.want {
				var expected map[string]any
				require.NoError(t, json.Unmarshal(before, &expected), "decode exact expected preservation baseline")
				session := expected["session"].(map[string]any)
				session["container_id"], session["worker_node_id"], session["recovery_queued_at"] = nil, nil, nil
				session["turn_holding_container"], session["recovery_state"], session["sandbox_state"] = false, "", "snapshotted"
				raw, err := json.Marshal(expected)
				require.NoError(t, err, "encode exact expected flag-only repair")
				require.JSONEq(t, string(raw), string(after), "snapshots, outcomes, receipt, clocks and all unrelated fields must survive repair")
			} else {
				require.JSONEq(t, string(before), string(after), "every rejected cleanup must leave all durable state unchanged")
			}
			cleared, err = NewSessionStore(pool).FinalizeStaleTerminalCodeReviewContainer(ctx, uuid.New(), sessionID, "old-container")
			require.NoError(t, err, "wrong-tenant cleanup should fail closed")
			require.False(t, cleared, "container identity does not permit another tenant to clear ownership")
		})
	}
}

func TestCodeReviewContainerUndispatchedEvidenceFencesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{name: "reserved evidence waits for cleanup", want: true},
		{name: "running evidence waits for first dispatch", sql: `UPDATE code_review_revision_assessments SET status='running'`, want: true},
		{name: "full reservation remains protected", sql: `UPDATE code_review_revision_assessments SET review_scope='full'`},
		{name: "running full assessment remains protected", sql: `UPDATE code_review_revision_assessments SET review_scope='full',status='running'`},
		{name: "unknown scope remains protected", sql: `UPDATE code_review_revision_assessments SET review_scope=NULL`},
		{name: "publishing evidence remains protected", sql: `UPDATE code_review_revision_assessments SET status='publishing'`},
		{name: "uncertain evidence remains protected", sql: `UPDATE code_review_revision_assessments SET publication_state='uncertain'`},
		{name: "unsent publication reservation remains protected", sql: `UPDATE code_review_revision_assessments SET publication_state='reserved'`},
		{name: "unknown publication remains protected", sql: `UPDATE code_review_revision_assessments SET publication_state=NULL`},
		{name: "pending dispatch remains protected", sql: `INSERT INTO code_review_recheck_dispatches(org_id,session_id,assessment_id,status) SELECT org_id,session_id,id,'pending' FROM code_review_revision_assessments`},
		{name: "running dispatch remains protected", sql: `UPDATE code_review_revision_assessments SET status='running'; INSERT INTO code_review_recheck_dispatches(org_id,session_id,assessment_id,status) SELECT org_id,session_id,id,'running' FROM code_review_revision_assessments`},
		{name: "completed dispatch still proves prior execution", sql: `INSERT INTO code_review_recheck_dispatches(org_id,session_id,assessment_id,status) SELECT org_id,session_id,id,'completed' FROM code_review_revision_assessments`},
		{name: "failed dispatch still proves prior execution", sql: `INSERT INTO code_review_recheck_dispatches(org_id,session_id,assessment_id,status) SELECT org_id,session_id,id,'failed' FROM code_review_revision_assessments`},
		{name: "cancelled dispatch still proves prior execution", sql: `INSERT INTO code_review_recheck_dispatches(org_id,session_id,assessment_id,status) SELECT org_id,session_id,id,'cancelled' FROM code_review_revision_assessments`},
		{name: "dispatch identity cannot be hidden by a different session", sql: `INSERT INTO code_review_recheck_dispatches(org_id,session_id,assessment_id,status) SELECT org_id,gen_random_uuid(),id,'completed' FROM code_review_revision_assessments`},
		{name: "another tenant dispatch cannot claim assessment", sql: `INSERT INTO code_review_recheck_dispatches(org_id,session_id,assessment_id,status) SELECT gen_random_uuid(),session_id,id,'running' FROM code_review_revision_assessments`, want: true},
		{name: "another assessment dispatch does not claim reservation", sql: `INSERT INTO code_review_recheck_dispatches(org_id,session_id,assessment_id,status) SELECT org_id,session_id,gen_random_uuid(),'completed' FROM code_review_revision_assessments`, want: true},
		{name: "another tenant full assessment cannot claim reservation", sql: `INSERT INTO code_review_revision_assessments(org_id,session_id,review_scope,status) SELECT gen_random_uuid(),id,'full','running' FROM sessions`, want: true},
		{name: "workspace job remains protected", sql: `INSERT INTO jobs(org_id,payload,status,job_type) SELECT org_id,jsonb_build_object('session_id',id),'pending','prepare_code_review_workspace' FROM sessions`},
		{name: "active thread remains protected", sql: `INSERT INTO session_threads SELECT org_id,id,'running' FROM sessions`},
		{name: "executor remains protected", sql: `INSERT INTO session_executors SELECT org_id,id,'draining' FROM sessions`},
		{name: "runtime remains protected", sql: `INSERT INTO thread_runtimes SELECT org_id,id,'paused' FROM sessions`},
		{name: "preview remains protected", sql: `INSERT INTO preview_instances SELECT org_id,id,true FROM sessions`},
		{name: "holder remains protected", sql: `INSERT INTO session_sandbox_holders SELECT org_id,id,'active',now()+interval '1 hour' FROM sessions`},
	}
	paths := []struct {
		name  string
		stale bool
	}{{name: "idle"}, {name: "stale terminal", stale: true}}
	for _, path := range paths {
		for _, tt := range tests {
			t.Run(path.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				pool := newReviewRecoveryPool(t)
				ctx := context.Background()
				org, sessionID := uuid.New(), uuid.New()
				_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,container_id,worker_node_id,turn_holding_container,recovery_state,snapshot_key) VALUES($1,$2,'retained-container','worker',$3,$4,'saved-snapshot');
INSERT INTO code_review_session_metadata(org_id,session_id,status,completed_at) VALUES($2,$1,'completed',now()-interval '1 day');
INSERT INTO code_review_revision_assessments(org_id,session_id,review_scope,status) VALUES($2,$1,'evidence_only','reserved');`, sessionID, org, path.stale, map[bool]string{false: "", true: "queued"}[path.stale])
				require.NoError(t, err, "seed retained workspace and evidence reservation before its first dispatch")
				if tt.sql != "" {
					_, err := pool.Exec(ctx, tt.sql)
					require.NoError(t, err, "seed exact assessment or live execution protection")
				}
				store := NewSessionStore(pool)
				finalize := store.FinalizeIdleCodeReviewContainer
				if path.stale {
					finalize = store.FinalizeStaleTerminalCodeReviewContainer
				}
				cleared, err := finalize(ctx, uuid.New(), sessionID, "retained-container")
				require.NoError(t, err, "another tenant cannot finalize an evidence reservation's workspace")
				require.False(t, cleared, "tenant mismatch must preserve the retained workspace")
				read := func() string {
					t.Helper()
					var state string
					err := pool.QueryRow(ctx, `SELECT jsonb_build_object('session',to_jsonb(s),'metadata',(SELECT jsonb_agg(to_jsonb(m)) FROM code_review_session_metadata m),'assessments',(SELECT jsonb_agg(to_jsonb(a)) FROM code_review_revision_assessments a),'dispatches',(SELECT jsonb_agg(to_jsonb(d)) FROM code_review_recheck_dispatches d))::text FROM sessions s WHERE org_id=$1 AND id=$2`, org, sessionID).Scan(&state)
					require.NoError(t, err, "snapshot exact workspace, assessment and publication ownership")
					return state
				}
				before := read()
				cleared, err = finalize(ctx, org, sessionID, "retained-container")
				require.NoError(t, err, "finalize retained workspace under admission's PR lock")
				require.Equal(t, tt.want, cleared, "only evidence work with no first dispatch may yield workspace cleanup")
				if !tt.want {
					require.Equal(t, before, read(), "every protected owner must retain its exact durable state")
					return
				}
				var expected map[string]any
				require.NoError(t, json.Unmarshal([]byte(before), &expected), "decode exact ownership preservation baseline")
				session := expected["session"].(map[string]any)
				session["container_id"], session["worker_node_id"], session["recovery_queued_at"] = nil, nil, nil
				session["turn_holding_container"], session["recovery_state"], session["sandbox_state"] = false, "", "snapshotted"
				raw, err := json.Marshal(expected)
				require.NoError(t, err, "encode exact expected workspace-only cleanup")
				require.JSONEq(t, string(raw), read(), "cleanup must preserve the admitted assessment, output, snapshot and dispatch history")
			})
		}
	}
}

func TestCodeReviewContainerSandboxJobFencesPostgres(t *testing.T) {
	t.Parallel()
	jobs := []struct {
		name, jobType, outputKey, sessionField string
		otherOrg, otherSession, want           bool
	}{
		{name: "current review controller", jobType: "run_code_review", outputKey: "output"},
		{name: "workspace preparation", jobType: "prepare_code_review_workspace"},
		{name: "reviewer turn", jobType: "run_agent"},
		{name: "continuation turn", jobType: "continue_session"},
		{name: "thread inbox delivery", jobType: "deliver_thread_inbox"},
		{name: "rate limited status comment", jobType: "sync_code_review_status_comment", want: true},
		{name: "decision outcome reconciliation", jobType: "reconcile_code_review_decision_outcomes", want: true},
		{name: "Slack publication", jobType: "slack_post_final_response", want: true},
		{name: "superseded review controller", jobType: "run_code_review", outputKey: "superseded-output", want: true},
		{name: "review controller without output identity", jobType: "run_code_review", want: true},
		{name: "another tenant reviewer", jobType: "run_agent", otherOrg: true, want: true},
		{name: "another session reviewer", jobType: "run_agent", otherSession: true, want: true},
		{name: "fork creates independent session", jobType: "fork_session_thread", sessionField: "source_session_id", want: true},
		{name: "revert hydrates independent snapshot", jobType: "revert_session_thread", want: true},
	}
	paths := []struct {
		name  string
		stale bool
	}{{name: "idle"}, {name: "stale terminal", stale: true}}
	statuses := []string{"pending", "running"}
	for _, path := range paths {
		for _, status := range statuses {
			for _, job := range jobs {
				t.Run(path.name+"/"+status+"/"+job.name, func(t *testing.T) {
					t.Parallel()
					pool := newReviewRecoveryPool(t)
					ctx := context.Background()
					org, sessionID := uuid.New(), uuid.New()
					_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,container_id,worker_node_id,turn_holding_container,recovery_state,recovery_queued_at,snapshot_key) VALUES($1,$2,'finished-container','worker',$3,$4,now()-interval '1 day','saved-snapshot');
INSERT INTO code_review_session_metadata(org_id,session_id,status,completed_at) VALUES($2,$1,'completed',now()-interval '1 day');
INSERT INTO code_review_revision_assessments(org_id,session_id,status,publication_state,publication_receipt) VALUES($2,$1,'completed','confirmed','{"review_id":42}');`, sessionID, org, path.stale, map[bool]string{false: "", true: "queued"}[path.stale])
					require.NoError(t, err, "seed eligible finished review with saved output and confirmed publication")
					jobOrg, jobSession := org, sessionID
					if job.otherOrg {
						jobOrg = uuid.New()
					}
					if job.otherSession {
						jobSession = uuid.New()
					}
					field := job.sessionField
					if field == "" {
						field = "session_id"
					}
					_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,payload,status,job_type,run_at,attempts,max_attempts,last_error) VALUES($1,jsonb_build_object($2::text,$3::text,'review_output_key',$4::text,'publication_receipt',jsonb_build_object('id',42)),$5,$6,now()+interval '2 hours',3,8,'GitHub rate limited')`, jobOrg, field, jobSession, job.outputKey, status, job.jobType)
					require.NoError(t, err, "seed future retry retaining its payload, attempts and delivery evidence")
					read := func() json.RawMessage {
						var state json.RawMessage
						err := pool.QueryRow(ctx, `SELECT jsonb_build_object('session',to_jsonb(s),'metadata',(SELECT jsonb_agg(to_jsonb(m)) FROM code_review_session_metadata m),'assessments',(SELECT jsonb_agg(to_jsonb(a)) FROM code_review_revision_assessments a),'jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM jobs j)) FROM sessions s WHERE org_id=$1 AND id=$2`, org, sessionID).Scan(&state)
						require.NoError(t, err, "read exact durable baseline including all publication retry fields")
						return state
					}
					before := read()
					store := NewSessionStore(pool)
					var cleared bool
					if path.stale {
						cleared, err = store.FinalizeStaleTerminalCodeReviewContainer(ctx, org, sessionID, "finished-container")
					} else {
						cleared, err = store.FinalizeIdleCodeReviewContainer(ctx, org, sessionID, "finished-container")
					}
					require.NoError(t, err, "shared cleanup predicate must reconcile actual sandbox job ownership")
					require.Equal(t, job.want, cleared, "only same-tenant current sandbox consumers may block cleanup")
					var expected map[string]any
					require.NoError(t, json.Unmarshal(before, &expected), "decode the exact preservation baseline")
					if job.want {
						session := expected["session"].(map[string]any)
						session["container_id"], session["worker_node_id"], session["recovery_queued_at"] = nil, nil, nil
						session["turn_holding_container"], session["recovery_state"], session["sandbox_state"] = false, "", "snapshotted"
					}
					raw, err := json.Marshal(expected)
					require.NoError(t, err, "encode exact expected cleanup changes")
					require.JSONEq(t, string(raw), string(read()), "cleanup must preserve every job, retry, snapshot, output and publication receipt field")
				})
			}
		}
	}
}

func TestCodeReviewWarmAdmissionPinsOwnershipPostgres(t *testing.T) {
	t.Parallel()
	pool := newReviewRecoveryPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	org, session := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO nodes(id,metadata) VALUES('worker','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":1,"live_sandbox_count":1,"reserved_sandbox_count":0}'); INSERT INTO sessions(id,org_id,container_id,worker_node_id) VALUES($1,$2,'warm-workspace','worker')`, session, org)
	require.NoError(t, err, "seed an exact warm workspace on a saturated physical machine")
	admission, err := pool.Begin(ctx)
	require.NoError(t, err, "begin warm assessment admission")
	defer func() { _ = admission.Rollback(ctx) }()
	available, err := CodeReviewSandboxAvailable(ctx, admission, org, session)
	require.NoError(t, err, "warm reuse needs no new physical slot")
	require.True(t, available, "verified warm ownership may reuse a saturated host")
	contender, err := pool.Begin(ctx)
	require.NoError(t, err, "begin competing session ownership change")
	defer func() { _ = contender.Rollback(ctx) }()
	done := make(chan error, 1)
	go func() {
		_, err := contender.Exec(ctx, `UPDATE sessions SET container_id=NULL,worker_node_id=NULL WHERE org_id=$1 AND id=$2`, org, session)
		done <- err
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted)`, contender.Conn().PgConn().PID()).Scan(&waiting)
		return err == nil && waiting
	}, time.Second, 10*time.Millisecond, "warm ownership must remain pinned until the admission transaction commits")
	assessment := uuid.New()
	_, err = admission.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,session_id,status) VALUES($1,$2,$3,'reserved'); INSERT INTO jobs(org_id,payload,status,job_type) VALUES($2,jsonb_build_object('assessment_id',$1::text),'pending','run_code_review_recheck')`, assessment, org, session)
	require.NoError(t, err, "commit a runnable assessment supervisor with its reservation")
	require.NoError(t, admission.Commit(ctx), "release warm ownership only with the durable assessment commitment")
	require.NoError(t, <-done, "competing workspace change may proceed after reservation commit")
	require.NoError(t, contender.Commit(ctx), "commit the later workspace loss")
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { available, err = CodeReviewSandboxAvailable(ctx, tx, org, uuid.Nil); return err })
	require.NoError(t, err, "read the newly cold supervisor-backed commitment")
	require.False(t, available, "workspace loss cannot expose an unreserved admission gap")
}
