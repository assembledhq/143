package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/cluster"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/version"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newWorkerSandboxPresencePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for startup observer and heartbeat admission composition")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	t.Cleanup(func() {
		require.NoError(t, admin.Close(context.Background()), "close fixture admin connection")
	})
	schema := "worker_sandbox_presence_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create a unique schema for this startup composition fixture")
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "drop only this fixture schema")
	})
	poolConfig, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse disposable database configuration")
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	// Preserve the normal typed protocol for application calls, including the
	// NodeManager's JSON metadata. Only multi-statement fixture setup uses simple protocol.
	poolConfig.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	require.NoError(t, err, "open the isolated startup composition pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
CREATE TABLE nodes(id text PRIMARY KEY,host text,mode text DEFAULT 'worker',status text DEFAULT 'active',drain_intent text DEFAULT 'none',started_at timestamptz DEFAULT now(),last_heartbeat_at timestamptz DEFAULT now(),metadata jsonb DEFAULT '{}',lifecycle_writer_version integer NOT NULL DEFAULT 0);
CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid NOT NULL,origin text DEFAULT 'code_review',status text DEFAULT 'idle',container_id text,worker_node_id text,turn_holding_container boolean DEFAULT false,recovery_state text DEFAULT '',recovery_queued_at timestamptz,snapshot_key text,sandbox_state text DEFAULT 'running',created_at timestamptz DEFAULT now()-interval '1 day',last_activity_at timestamptz DEFAULT now()-interval '1 day');
CREATE TABLE code_review_session_metadata(org_id uuid NOT NULL,session_id uuid NOT NULL,pull_request_id uuid,review_output_key text DEFAULT 'output',review_output jsonb DEFAULT '{"result":"preserve"}',status text DEFAULT 'completed',created_at timestamptz DEFAULT now()-interval '1 day',completed_at timestamptz);
CREATE TABLE jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL,payload jsonb,status text,job_type text,lock_token uuid,lease_expires_at timestamptz,run_at timestamptz DEFAULT now(),attempts integer DEFAULT 0,max_attempts integer DEFAULT 8,last_error text,updated_at timestamptz DEFAULT now());
CREATE TABLE session_threads(org_id uuid NOT NULL,session_id uuid NOT NULL,status text);
CREATE TABLE session_executors(org_id uuid NOT NULL,session_id uuid NOT NULL,status text);
CREATE TABLE thread_runtimes(org_id uuid NOT NULL,session_id uuid NOT NULL,status text);
CREATE TABLE preview_instances(org_id uuid NOT NULL,session_id uuid NOT NULL,preview_holding_container boolean);
CREATE TABLE session_sandbox_holders(org_id uuid NOT NULL,session_id uuid NOT NULL,status text,expires_at timestamptz);
CREATE TABLE code_review_revision_assessments(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),result_origin text,org_id uuid NOT NULL,session_id uuid NOT NULL,status text,publication_state text DEFAULT 'not_started',publication_receipt jsonb);
CREATE TABLE code_review_recheck_dispatches(org_id uuid NOT NULL,session_id uuid NOT NULL,status text,assessment_id uuid,job_id uuid);`, pgx.QueryExecModeSimpleProtocol)
	require.NoError(t, err, "create synthetic heartbeat, admission and every protected ownership table")
	return pool
}

func workerSandboxPresenceProtectedState(t *testing.T, pool *pgxpool.Pool) json.RawMessage {
	t.Helper()
	var state json.RawMessage
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
'nodes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY n.id) FROM nodes n WHERE id<>'replacement'),
'replacement',(SELECT to_jsonb(n)-'metadata'-'last_heartbeat_at' FROM nodes n WHERE id='replacement'),
'sessions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM sessions s),
'metadata',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.session_id) FROM code_review_session_metadata m),
'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j),
'threads',(SELECT jsonb_agg(to_jsonb(t) ORDER BY t.session_id) FROM session_threads t),
'executors',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.session_id) FROM session_executors e),
'runtimes',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.session_id) FROM thread_runtimes r),
'previews',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.session_id) FROM preview_instances p),
'holders',(SELECT jsonb_agg(to_jsonb(h) ORDER BY h.session_id) FROM session_sandbox_holders h),
'assessments',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM code_review_revision_assessments a),
'rechecks',(SELECT jsonb_agg(to_jsonb(d) ORDER BY d.session_id) FROM code_review_recheck_dispatches d))`).Scan(&state)
	require.NoError(t, err, "capture every durable field except the replacement's permitted heartbeat sample and clock")
	return state
}

func TestWorkerSandboxPresenceHeartbeatAdmissionPostgres(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"worker", "all"} {
		for _, tt := range sandboxPresenceHostCases() {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				pool := newWorkerSandboxPresencePool(t)
				ctx := context.Background()
				cfg := &config.Config{Mode: mode, WorkerPrivateIP: tt.input}
				org, session := uuid.New(), uuid.New()
				a, b, foreign := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
				_, err := pool.Exec(ctx, `
INSERT INTO nodes(id,host,mode,status,started_at,last_heartbeat_at,metadata) VALUES
('old-worker','historical-host',$1,'dead',now()-interval '1 day',now()-interval '1 day',jsonb_build_object('sandbox_host_id',$2::text)),
('replacement','replacement-host',$1,'active',now()-interval '1 minute',now()-interval '1 second',jsonb_build_object('sandbox_host_id',$2::text,'max_active_sandboxes',2,'live_sandbox_count',0,'reserved_sandbox_count',0)),
('foreign-worker','foreign-host',$1,'dead',now()-interval '1 day',now()-interval '1 day','{"sandbox_host_id":"192.0.2.99"}');
INSERT INTO sessions(id,org_id,status,recovery_state,recovery_queued_at,container_id,worker_node_id,turn_holding_container,snapshot_key) VALUES
($3,$4,'running','queued',now()-interval '2 hours',$5,'old-worker',true,'first-saved-snapshot'),
(gen_random_uuid(),gen_random_uuid(),'running','recovering',now()-interval '1 hour',$6,'old-worker',true,'second-saved-snapshot'),
(gen_random_uuid(),gen_random_uuid(),'idle','queued',now()-interval '3 hours',$7,'foreign-worker',true,'foreign-saved-snapshot');
INSERT INTO code_review_session_metadata(org_id,session_id,pull_request_id,status,completed_at) SELECT org_id,id,gen_random_uuid(),'completed',now()-interval '1 hour' FROM sessions;
INSERT INTO code_review_revision_assessments(org_id,session_id,status,publication_state,publication_receipt) SELECT org_id,id,'failed','uncertain','{"review_id":42,"delivery":"preserve"}' FROM sessions;
INSERT INTO jobs(org_id,payload,status,job_type,lock_token,lease_expires_at,run_at,attempts,max_attempts,last_error) SELECT org_id,jsonb_build_object('session_id',id,'review_output_key','output','receipt',jsonb_build_object('review_id',42)),'pending','sync_code_review_status_comment',gen_random_uuid(),now()+interval '1 hour',now()+interval '2 hours',3,8,'publication retry pending' FROM sessions;
INSERT INTO code_review_recheck_dispatches SELECT a.org_id,a.session_id,'pending',a.id,j.id FROM code_review_revision_assessments a JOIN jobs j ON j.org_id=a.org_id AND j.payload->>'session_id'=a.session_id::text;
INSERT INTO session_executors SELECT org_id,id,'running' FROM sessions;
INSERT INTO thread_runtimes SELECT org_id,id,'paused' FROM sessions;
INSERT INTO session_threads SELECT org_id,id,'awaiting_input' FROM sessions;
INSERT INTO preview_instances SELECT org_id,id,true FROM sessions;
INSERT INTO session_sandbox_holders SELECT org_id,id,'active',now()+interval '1 hour' FROM sessions`, pgx.QueryExecModeSimpleProtocol, mode, tt.canonical, session, org, a, b, foreign)
				require.NoError(t, err, "seed dead owner and fresh canonical replacement with protected missing references across tenants and a foreign host")
				available := func() bool {
					var available bool
					err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
						var err error
						available, err = db.CodeReviewSandboxAvailable(ctx, tx, org, uuid.Nil)
						return err
					})
					require.NoError(t, err, "query real serialized admission without mutating protected ownership")
					return available
				}
				before := workerSandboxPresenceProtectedState(t, pool)
				require.False(t, available(), "unproven historical references must fill the host before physical observation")
				var priorHeartbeat time.Time
				require.NoError(t, pool.QueryRow(ctx, `SELECT last_heartbeat_at FROM nodes WHERE id='replacement'`).Scan(&priorHeartbeat), "read replacement heartbeat before actual NodeManager collection")
				inspector := &heartbeatMissingInspector{missing: map[string]bool{a: true, b: true, foreign: true}}
				gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{Counter: &heartbeatPresenceCounter{ids: []string{}}, MaxActive: 2, Logger: zerolog.Nop()})
				installSandboxReferenceObserver(cfg, gate, inspector, db.NewSessionStore(pool))
				nodeManager := cluster.NewNodeManager(pool, zerolog.Nop(), "replacement", cfg.Mode)
				nodeManager.SetMetadataProvider(buildWorkerMetadataProvider(nil, false, "", "", nil, gate, agent.StaticEgressRuntimeConfig{}, cfg.SandboxHostID()))
				require.NoError(t, nodeManager.HeartbeatOnce(ctx), "persist real startup observer evidence through the worker metadata provider and NodeManager heartbeat")
				require.Equal(t, []string{a, b}, inspector.checked, "real host query must probe both exact full IDs across tenants and exclude foreign-host references")
				var metadata json.RawMessage
				var nextHeartbeat time.Time
				require.NoError(t, pool.QueryRow(ctx, `SELECT metadata,last_heartbeat_at FROM nodes WHERE id='replacement'`).Scan(&metadata, &nextHeartbeat), "read actual persisted heartbeat proof")
				expectedMetadata, err := json.Marshal(map[string]any{
					"build_sha": version.BuildSHA, "sandbox_host_id": tt.canonical,
					"active_job_count": 0, "active_run_agent_count": 0,
					"live_sandbox_count": 0, "reserved_sandbox_count": 0, "max_active_sandboxes": 2,
					"live_sandbox_count_error": "", "missing_sandbox_reference_error": "",
					"missing_sandbox_references": []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "old-worker"}, {ContainerID: b, OwnerNodeID: "old-worker"}},
				})
				require.NoError(t, err, "serialize literal canonical host and exact owner-bound missing container proof")
				require.JSONEq(t, string(expectedMetadata), string(metadata), "the persisted heartbeat must carry the complete exact canonical-host capacity sample")
				require.True(t, nextHeartbeat.After(priorHeartbeat), "the actual heartbeat must refresh only the replacement generation's sample clock")
				require.True(t, available(), "real admission must discount definitively missing references after canonical startup observation")
				require.JSONEq(t, string(before), string(workerSandboxPresenceProtectedState(t, pool)), "presence observation, heartbeat and admission must preserve all tenant references, lifecycle state, snapshots, retry payloads, leases, clocks and uncertain receipts")
			})
		}
	}
}
