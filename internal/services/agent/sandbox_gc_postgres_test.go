package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newSandboxGCReviewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for full GC ownership-fallback proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect disposable PostgreSQL")
	schema := "sandbox_gc_fences_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated GC schema")
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse isolated GC pool")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "open isolated GC pool")
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "drop only this fixture schema")
		require.NoError(t, admin.Close(ctx), "close fixture admin")
	})
	_, err = pool.Exec(ctx, `
CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid NOT NULL,origin text DEFAULT 'code_review',status text DEFAULT 'idle',container_id text,worker_node_id text,turn_holding_container boolean DEFAULT false,recovery_state text DEFAULT '',recovery_queued_at timestamptz,snapshot_key text,sandbox_state text DEFAULT 'running',created_at timestamptz DEFAULT now()-interval '1 day',last_activity_at timestamptz DEFAULT now()-interval '1 day');
CREATE TABLE code_review_session_metadata(org_id uuid,session_id uuid,pull_request_id uuid,review_output_key text DEFAULT 'output',review_output jsonb DEFAULT '{"result":"preserve"}',status text DEFAULT 'completed',created_at timestamptz DEFAULT now()-interval '1 day',completed_at timestamptz);
CREATE TABLE jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid,payload jsonb,status text,job_type text,lock_token uuid,lease_expires_at timestamptz,run_at timestamptz DEFAULT now(),attempts integer DEFAULT 0,max_attempts integer DEFAULT 8,last_error text,updated_at timestamptz DEFAULT now());
CREATE TABLE session_threads(org_id uuid,session_id uuid,status text);
CREATE TABLE session_executors(org_id uuid,session_id uuid,status text);
CREATE TABLE thread_runtimes(org_id uuid,session_id uuid,status text);
CREATE TABLE preview_instances(org_id uuid,session_id uuid,preview_holding_container boolean);
CREATE TABLE session_sandbox_holders(org_id uuid,session_id uuid,status text,expires_at timestamptz);
CREATE TABLE code_review_revision_assessments(org_id uuid,session_id uuid,status text,publication_state text DEFAULT 'not_started',publication_receipt jsonb);
CREATE TABLE code_review_recheck_dispatches(org_id uuid,session_id uuid,status text);`)
	require.NoError(t, err, "create every durable GC ownership fence")
	return pool
}

func TestSandboxGCAllReviewPathsProtectOwnershipPostgres(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		age        time.Duration
		assessment bool
	}{
		{"reserved assessment in ordinary idle pass", time.Hour, true},
		{"pending recheck in ordinary idle pass", time.Hour, false},
		{"reserved assessment in hard-age pass", 25 * time.Hour, true},
		{"pending recheck in hard-age pass", 25 * time.Hour, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newSandboxGCReviewPool(t)
			ctx := context.Background()
			org, session, pr := uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,container_id,snapshot_key) VALUES($1,$2,'protected-container','saved-snapshot'); INSERT INTO code_review_session_metadata(org_id,session_id,pull_request_id) VALUES($2,$1,$3)`, session, org, pr)
			require.NoError(t, err, "seed terminal review without turn or retention holds")
			if tt.assessment {
				_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(org_id,session_id,status) VALUES($1,$2,'reserved')`, org, session)
			} else {
				_, err = pool.Exec(ctx, `INSERT INTO code_review_recheck_dispatches(org_id,session_id,status) VALUES($1,$2,'pending')`, org, session)
			}
			require.NoError(t, err, "seed successor ownership that the stale helper rejects")
			now := time.Now()
			provider := &fakeSandboxGCProvider{containers: []agent.ManagedSandboxContainer{{ID: "protected-container", OrgID: org.String(), SessionID: session.String(), CreatedAt: now.Add(-tt.age)}}}
			gc := agent.NewSandboxGC(provider, db.NewSessionStore(pool), nil, agent.SandboxGCConfig{}, zerolog.Nop())
			require.NoError(t, gc.ReapOnce(ctx, now), "a full GC pass must retain all review ownership fences")
			require.Equal(t, []string{}, provider.destroyedIDs(), "ordinary and hard-age fallback must not destroy a reserved workspace")
			var container, snapshot string
			require.NoError(t, pool.QueryRow(ctx, `SELECT container_id,snapshot_key FROM sessions WHERE org_id=$1 AND id=$2`, org, session).Scan(&container, &snapshot), "read unchanged exact ownership")
			require.Equal(t, []string{"protected-container", "saved-snapshot"}, []string{container, snapshot}, "fallback must preserve both live ownership and saved snapshot")
		})
	}
}

func TestSandboxGCReviewJobFencesPostgres(t *testing.T) {
	t.Parallel()
	paths := []struct {
		name            string
		age             time.Duration
		stale, pressure bool
	}{
		{name: "idle", age: time.Hour},
		{name: "stale terminal", age: time.Hour, stale: true},
		{name: "hard age", age: 25 * time.Hour},
		{name: "capacity pressure", age: 25 * time.Hour, pressure: true},
	}
	jobs := []struct {
		name, jobType, fence string
		want                 bool
	}{
		{name: "current review controller", jobType: "run_code_review"},
		{name: "workspace preparation", jobType: "prepare_code_review_workspace"},
		{name: "reviewer turn", jobType: "run_agent"},
		{name: "continuation turn", jobType: "continue_session"},
		{name: "thread inbox delivery", jobType: "deliver_thread_inbox"},
		{name: "future rate limited status comment", jobType: "sync_code_review_status_comment", want: true},
		{name: "future decision outcome reconciliation", jobType: "reconcile_code_review_decision_outcomes", want: true},
		{name: "future Slack publication", jobType: "slack_post_final_response", want: true},
		{name: "missing metadata", fence: `DELETE FROM code_review_session_metadata`},
		{name: "live turn", fence: `UPDATE sessions SET turn_holding_container=true,last_activity_at=now()`},
		{name: "active recovery", fence: `UPDATE sessions SET recovery_state='recovering'`},
		{name: "awaiting thread", fence: `INSERT INTO session_threads SELECT org_id,id,'awaiting_input' FROM sessions`},
		{name: "draining executor", fence: `INSERT INTO session_executors SELECT org_id,id,'draining' FROM sessions`},
		{name: "paused runtime", fence: `INSERT INTO thread_runtimes SELECT org_id,id,'paused' FROM sessions`},
		{name: "preview", fence: `INSERT INTO preview_instances SELECT org_id,id,true FROM sessions`},
		{name: "retention holder", fence: `INSERT INTO session_sandbox_holders SELECT org_id,id,'active',now()+interval '1 hour' FROM sessions`},
		{name: "reserved assessment", fence: `INSERT INTO code_review_revision_assessments(org_id,session_id,status) SELECT org_id,id,'reserved' FROM sessions`},
		{name: "running assessment", fence: `INSERT INTO code_review_revision_assessments(org_id,session_id,status) SELECT org_id,id,'running' FROM sessions`},
		{name: "publishing assessment", fence: `INSERT INTO code_review_revision_assessments(org_id,session_id,status) SELECT org_id,id,'publishing' FROM sessions`},
		{name: "uncertain publication", fence: `INSERT INTO code_review_revision_assessments(org_id,session_id,status,publication_state) SELECT org_id,id,'failed','uncertain' FROM sessions`},
		{name: "pending recheck dispatch", fence: `INSERT INTO code_review_recheck_dispatches SELECT org_id,id,'pending' FROM sessions`},
		{name: "running recheck dispatch", fence: `INSERT INTO code_review_recheck_dispatches SELECT org_id,id,'running' FROM sessions`},
	}
	statuses := []string{"pending", "running"}
	for _, path := range paths {
		for _, status := range statuses {
			for _, job := range jobs {
				t.Run(path.name+"/"+status+"/"+job.name, func(t *testing.T) {
					t.Parallel()
					pool := newSandboxGCReviewPool(t)
					ctx := context.Background()
					org, session, pr := uuid.New(), uuid.New(), uuid.New()
					metadataStatus, recovery := "running", ""
					if path.stale {
						metadataStatus, recovery = "completed", "queued"
					}
					_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,container_id,worker_node_id,snapshot_key,turn_holding_container,recovery_state,recovery_queued_at) VALUES($1,$2,'finished-container','worker','saved-snapshot',$4,$5,now()-interval '1 day');
INSERT INTO code_review_session_metadata(org_id,session_id,pull_request_id,status,completed_at) VALUES($2,$1,$3,$6,now()-interval '1 day');
INSERT INTO code_review_revision_assessments(org_id,session_id,status,publication_state,publication_receipt) VALUES($2,$1,'completed','confirmed','{"review_id":42}')`, session, org, pr, path.stale, recovery, metadataStatus)
					require.NoError(t, err, "seed reviewed workspace with retained snapshot, outcome and receipt")
					kind := job.jobType
					if kind == "" {
						kind = "sync_code_review_status_comment"
					}
					_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,payload,status,job_type,run_at,attempts,max_attempts,last_error) VALUES($1,jsonb_build_object('session_id',$2::text,'review_output_key','output','publication_receipt',jsonb_build_object('id',42)),$3,$4,now()+interval '2 hours',3,8,'GitHub rate limited')`, org, session, status, kind)
					require.NoError(t, err, "persist future job retry with its exact payload and delivery evidence")
					if job.fence != "" {
						_, err = pool.Exec(ctx, job.fence)
						require.NoError(t, err, "establish independent ownership protection alongside publication retry")
					}
					read := func() json.RawMessage {
						var state json.RawMessage
						err := pool.QueryRow(ctx, `SELECT jsonb_build_object('session',to_jsonb(s),'metadata',(SELECT jsonb_agg(to_jsonb(m)) FROM code_review_session_metadata m),'assessments',(SELECT jsonb_agg(to_jsonb(a)) FROM code_review_revision_assessments a),'jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM jobs j)) FROM sessions s WHERE org_id=$1 AND id=$2`, org, session).Scan(&state)
						require.NoError(t, err, "read exact workspace, publication and retry preservation baseline")
						return state
					}
					before := read()
					now := time.Now()
					provider := &fakeSandboxGCProvider{containers: []agent.ManagedSandboxContainer{{ID: "finished-container", OrgID: org.String(), SessionID: session.String(), CreatedAt: now.Add(-path.age)}}}
					gc := agent.NewSandboxGC(provider, db.NewSessionStore(pool), nil, agent.SandboxGCConfig{}, zerolog.Nop())
					if path.pressure {
						err = gc.ReapForCapacity(ctx, now)
					} else {
						err = gc.ReapOnce(ctx, now)
					}
					require.NoError(t, err, "full GC must reconcile publication retries without losing ownership protections")
					wantDestroyed := []string{}
					var expected map[string]any
					require.NoError(t, json.Unmarshal(before, &expected), "decode exact durable preservation baseline")
					if job.want {
						wantDestroyed = []string{"finished-container"}
						session := expected["session"].(map[string]any)
						session["container_id"], session["worker_node_id"], session["recovery_queued_at"] = nil, nil, nil
						session["turn_holding_container"], session["recovery_state"], session["sandbox_state"] = false, "", "snapshotted"
					}
					require.Equal(t, wantDestroyed, provider.destroyedIDs(), "each cleanup path must release publication-only containers and retain every active owner")
					raw, err := json.Marshal(expected)
					require.NoError(t, err, "encode the exact expected GC result")
					require.JSONEq(t, string(raw), string(read()), "physical cleanup must preserve retry timing, attempts, payloads, snapshots, review output and publication receipts")
				})
			}
		}
	}
}
