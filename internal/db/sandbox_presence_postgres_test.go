package db

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewMissingContainerCapacityPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, sql  string
		warm, want bool
	}{
		{name: "definitive absence releases physical floor despite running recovery ownership", want: true},
		{name: "unknown proof retains floor", sql: `UPDATE nodes SET metadata=metadata-'missing_sandbox_references' WHERE id='new-worker'`},
		{name: "null proof retains floor", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references}','null') WHERE id='new-worker'`},
		{name: "object proof retains floor", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references}','{}') WHERE id='new-worker'`},
		{name: "inspection failure invalidates old proof", sql: `UPDATE nodes SET metadata=metadata||'{"missing_sandbox_reference_error":"inspection unavailable"}' WHERE id='new-worker'`},
		{name: "malformed array member invalidates complete proof", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references}',(metadata->'missing_sandbox_references')||'[1]') WHERE id='new-worker'`},
		{name: "short reusable identity invalidates proof", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references,0,container_id}','"aaaaaaaaaaaa"') WHERE id='new-worker'`},
		{name: "numeric identity invalidates proof", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references,0,container_id}','42') WHERE id='new-worker'`},
		{name: "missing owner invalidates proof", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references,0}',(metadata->'missing_sandbox_references'->0)-'owner_node_id') WHERE id='new-worker'`},
		{name: "numeric owner invalidates proof", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references,0,owner_node_id}','42') WHERE id='new-worker'`},
		{name: "whitespace owner invalidates proof", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references,0,owner_node_id}','" old-worker "') WHERE id='new-worker'`},
		{name: "unexpected proof fields invalidate complete sample", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references,0}',(metadata->'missing_sandbox_references'->0)||'{"unverified":true}') WHERE id='new-worker'`},
		{name: "failed physical count cannot renew absence proof", sql: `UPDATE nodes SET metadata=metadata||'{"live_sandbox_count_error":"inventory unavailable"}' WHERE id='new-worker'`},
		{name: "empty proof retains every unknown reference", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{missing_sandbox_references}','[]') WHERE id='new-worker'`},
		{name: "stale generation proof cannot discount current floor", sql: `UPDATE nodes SET last_heartbeat_at=clock_timestamp()-interval '91 seconds' WHERE id='new-worker'; INSERT INTO nodes(id,metadata) VALUES('replacement','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":2,"live_sandbox_count":0,"reserved_sandbox_count":0}')`},
		{name: "proof on different host cannot discount ownership", sql: `UPDATE nodes SET metadata=metadata-'missing_sandbox_references' WHERE id='new-worker'; INSERT INTO nodes(id,metadata) SELECT 'other-host',metadata||jsonb_build_object('sandbox_host_id','192.0.2.2','max_active_sandboxes',0,'missing_sandbox_references',jsonb_build_array(jsonb_build_object('container_id',repeat('a',64),'owner_node_id','old-worker'),jsonb_build_object('container_id',repeat('b',64),'owner_node_id','old-worker'))) FROM nodes WHERE id='new-worker'`},
		{name: "replacement assignment invalidates exact missing identity", sql: `UPDATE sessions SET container_id=repeat('c',64) WHERE container_id=repeat('a',64); UPDATE nodes SET metadata=jsonb_set(metadata,'{max_active_sandboxes}','1') WHERE id='new-worker'`},
		{name: "owner transfer invalidates missing proof", sql: `UPDATE sessions SET worker_node_id='new-worker' WHERE container_id=repeat('a',64); UPDATE nodes SET metadata=jsonb_set(metadata,'{max_active_sandboxes}','1') WHERE id='new-worker'`},
		{name: "partial proof retains unconfirmed reference", sql: `UPDATE nodes SET metadata=jsonb_set(jsonb_set(metadata,'{missing_sandbox_references}',jsonb_build_array(metadata->'missing_sandbox_references'->0)),'{max_active_sandboxes}','1') WHERE id='new-worker'`},
		{name: "real physical count still fills host", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{live_sandbox_count}','2') WHERE id='new-worker'`},
		{name: "missing source receives no warm exception on full host", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{live_sandbox_count}','2') WHERE id='new-worker'`, warm: true},
		{name: "present source retains warm exception", sql: `UPDATE nodes SET metadata=metadata-'missing_sandbox_references'||'{"live_sandbox_count":2}' WHERE id='new-worker'`, warm: true, want: true},
		{name: "all fresh generation reservations remain charged", sql: `UPDATE nodes SET status='draining',metadata=metadata||'{"max_active_sandboxes":2,"live_sandbox_count":0,"reserved_sandbox_count":1}' WHERE id='old-worker'; UPDATE nodes SET metadata=jsonb_set(metadata,'{reserved_sandbox_count}','1') WHERE id='new-worker'`},
		{name: "every generation physical sample remains charged", sql: `UPDATE nodes SET status='draining',metadata=metadata||'{"max_active_sandboxes":2,"live_sandbox_count":2,"reserved_sandbox_count":0}' WHERE id='old-worker'`},
	}
	for _, kind := range []string{"run_code_review", "prepare_code_review_workspace", "run_agent", "continue_session", "deliver_thread_inbox"} {
		for _, status := range []string{"pending", "running"} {
			tests = append(tests, struct {
				name, sql  string
				warm, want bool
			}{name: status + " " + kind + " missing workspace remains a cold commitment", sql: `UPDATE nodes SET metadata=jsonb_set(metadata,'{max_active_sandboxes}','1') WHERE id='new-worker'; UPDATE code_review_session_metadata SET status='running' WHERE session_id=(SELECT id FROM sessions WHERE container_id=repeat('a',64)); INSERT INTO jobs(org_id,payload,status,job_type,run_at) SELECT org_id,jsonb_build_object('session_id',session_id,'review_output_key',review_output_key),'` + status + `','` + kind + `',clock_timestamp()+interval '1 hour' FROM code_review_session_metadata WHERE status='running'`})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newReviewRecoveryPool(t)
			ctx := context.Background()
			org, session := uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO nodes(id,status,metadata) VALUES('old-worker','stopped','{"sandbox_host_id":"192.0.2.1"}'),('new-worker','active',jsonb_build_object('sandbox_host_id','192.0.2.1','max_active_sandboxes',2,'live_sandbox_count',0,'reserved_sandbox_count',0,'missing_sandbox_references',jsonb_build_array(jsonb_build_object('container_id',repeat('a',64),'owner_node_id','old-worker'),jsonb_build_object('container_id',repeat('b',64),'owner_node_id','old-worker'))));
INSERT INTO sessions(id,org_id,status,container_id,worker_node_id,turn_holding_container,recovery_state,snapshot_key) VALUES($1,$2,'running',repeat('a',64),'old-worker',true,'queued','preserved-snapshot'),(gen_random_uuid(),gen_random_uuid(),'running',repeat('b',64),'old-worker',true,'recovering','other-snapshot');
INSERT INTO code_review_session_metadata(org_id,session_id,status) SELECT org_id,id,'completed' FROM sessions;
INSERT INTO code_review_revision_assessments(org_id,session_id,status,publication_state,publication_receipt) SELECT org_id,id,'failed','uncertain','{"review_id":42}' FROM sessions;
INSERT INTO session_sandbox_holders SELECT org_id,id,'active',now()+interval '1 hour' FROM sessions;
INSERT INTO thread_runtimes SELECT org_id,id,'paused' FROM sessions;
INSERT INTO session_executors SELECT org_id,id,'running' FROM sessions;
INSERT INTO session_threads SELECT org_id,id,'awaiting_input' FROM sessions;
INSERT INTO preview_instances SELECT org_id,id,true FROM sessions`, session, org)
			require.NoError(t, err, "seed two tenant references whose physical absence must not change protected ownership")
			if tt.sql != "" {
				_, err = pool.Exec(ctx, tt.sql)
				require.NoError(t, err, "establish exact physical evidence or conservative negative")
			}
			read := func() json.RawMessage {
				var value json.RawMessage
				err := pool.QueryRow(ctx, `SELECT jsonb_build_object('nodes',(SELECT jsonb_agg(to_jsonb(n)) FROM nodes n),'sessions',(SELECT jsonb_agg(to_jsonb(s)) FROM sessions s),'metadata',(SELECT jsonb_agg(to_jsonb(m)) FROM code_review_session_metadata m),'assessments',(SELECT jsonb_agg(to_jsonb(a)) FROM code_review_revision_assessments a),'jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM jobs j),'holders',(SELECT jsonb_agg(to_jsonb(h)) FROM session_sandbox_holders h),'runtimes',(SELECT jsonb_agg(to_jsonb(r)) FROM thread_runtimes r),'executors',(SELECT jsonb_agg(to_jsonb(e)) FROM session_executors e),'threads',(SELECT jsonb_agg(to_jsonb(t)) FROM session_threads t),'previews',(SELECT jsonb_agg(to_jsonb(p)) FROM preview_instances p))`).Scan(&value)
				require.NoError(t, err, "capture every protected durable field before physical capacity read")
				return value
			}
			before := read()
			warm := uuid.Nil
			if tt.warm {
				warm = session
			}
			var available bool
			err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				var err error
				available, err = CodeReviewSandboxAvailable(ctx, tx, org, warm)
				return err
			})
			require.NoError(t, err, "malformed evidence must fail closed without unsafe JSON casts")
			require.Equal(t, tt.want, available, "only exact fresh physical absence may discount the reference floor")
			require.JSONEq(t, string(before), string(read()), "capacity evidence must preserve lifecycle, tenants, holds, clocks, snapshots, jobs and publication receipts exactly")
		})
	}
}
