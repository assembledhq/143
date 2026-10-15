package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSandboxPresenceObserverPreservesProtectedMissingReferencesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, status, recovery string }{
		{"running turn", "running", ""},
		{"queued recovery", "idle", "queued"},
		{"recovering turn", "running", "recovering"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newSandboxGCReviewPool(t)
			ctx := context.Background()
			org, session := uuid.New(), uuid.New()
			a, b, c := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
			_, err := pool.Exec(ctx, `CREATE TABLE nodes(id text PRIMARY KEY,host text,mode text DEFAULT 'worker',status text DEFAULT 'active',drain_intent text DEFAULT 'none',started_at timestamptz DEFAULT now(),last_heartbeat_at timestamptz DEFAULT now(),metadata jsonb DEFAULT '{}');
ALTER TABLE code_review_revision_assessments ADD COLUMN result_origin text;
ALTER TABLE code_review_recheck_dispatches ADD COLUMN job_id uuid;
INSERT INTO nodes(id,status,metadata) VALUES('old-worker','stopped','{"sandbox_host_id":"192.0.2.1"}'),('observer','active','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":2,"live_sandbox_count":0,"reserved_sandbox_count":0}'),('other-host','stopped','{"sandbox_host_id":"192.0.2.2"}');
INSERT INTO sessions(id,org_id,status,recovery_state,container_id,worker_node_id,turn_holding_container,snapshot_key) VALUES($1,$2,$3,$4,$5,'old-worker',true,'saved-snapshot'),(gen_random_uuid(),gen_random_uuid(),$3,$4,$6,'old-worker',true,'other-snapshot'),(gen_random_uuid(),gen_random_uuid(),$3,$4,$7,'other-host',true,'foreign-snapshot');
INSERT INTO code_review_session_metadata(org_id,session_id,pull_request_id,status) SELECT org_id,id,gen_random_uuid(),'completed' FROM sessions;
INSERT INTO code_review_revision_assessments(org_id,session_id,status,publication_state,publication_receipt) SELECT org_id,id,'failed','uncertain','{"review_id":42}' FROM sessions;
INSERT INTO session_executors SELECT org_id,id,'running' FROM sessions;
INSERT INTO thread_runtimes SELECT org_id,id,'paused' FROM sessions;
INSERT INTO session_threads SELECT org_id,id,'awaiting_input' FROM sessions;
INSERT INTO preview_instances SELECT org_id,id,true FROM sessions;
INSERT INTO session_sandbox_holders SELECT org_id,id,'active',now()+interval '1 hour' FROM sessions`, session, org, tt.status, tt.recovery, a, b, c)
			require.NoError(t, err, "seed missing physical references with every protected owner across tenants and hosts")
			read := func() json.RawMessage {
				var value json.RawMessage
				err := pool.QueryRow(ctx, `SELECT jsonb_build_object('sessions',(SELECT jsonb_agg(to_jsonb(s)) FROM sessions s),'metadata',(SELECT jsonb_agg(to_jsonb(m)) FROM code_review_session_metadata m),'assessments',(SELECT jsonb_agg(to_jsonb(a)) FROM code_review_revision_assessments a),'executors',(SELECT jsonb_agg(to_jsonb(e)) FROM session_executors e),'runtimes',(SELECT jsonb_agg(to_jsonb(r)) FROM thread_runtimes r),'threads',(SELECT jsonb_agg(to_jsonb(t)) FROM session_threads t),'previews',(SELECT jsonb_agg(to_jsonb(p)) FROM preview_instances p),'holders',(SELECT jsonb_agg(to_jsonb(h)) FROM session_sandbox_holders h))`).Scan(&value)
				require.NoError(t, err, "capture exact protected lifecycle state")
				return value
			}
			available := func() bool {
				var got bool
				err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
					var err error
					got, err = db.CodeReviewSandboxAvailable(ctx, tx, org, uuid.Nil)
					return err
				})
				require.NoError(t, err, "read global headroom without changing protected state")
				return got
			}
			before := read()
			require.False(t, available(), "unproven ghost references retain conservative physical floor")
			inspector := &presenceInspector{missing: map[string]bool{a: true, b: true, c: true}}
			observer := agent.NewSandboxReferenceObserver(inspector, db.NewSessionStore(pool), "192.0.2.1")
			gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{Counter: &atomicPresenceCounter{ids: []string{}}, MaxActive: 2, Logger: zerolog.Nop()})
			gate.SetReferenceObserver(observer)
			snapshot := gate.Snapshot(ctx)
			require.Equal(t, agent.SandboxCapacitySnapshot{MaxActive: 2, MissingReferences: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "old-worker"}, {ContainerID: b, OwnerNodeID: "old-worker"}}}, snapshot, "host inventory must include every local generation and tenant while excluding other hosts")
			require.Equal(t, []string{a, b}, inspector.checked, "only exact host-owned IDs may receive definitive absence probes")
			raw, err := json.Marshal(map[string]any{"sandbox_host_id": "192.0.2.1", "max_active_sandboxes": snapshot.MaxActive, "live_sandbox_count": snapshot.Live, "reserved_sandbox_count": snapshot.Reserved, "missing_sandbox_references": snapshot.MissingReferences, "missing_sandbox_reference_error": snapshot.MissingReferenceError})
			require.NoError(t, err, "serialize the actual heartbeat proof schema")
			_, err = pool.Exec(ctx, `UPDATE nodes SET metadata=$1::jsonb WHERE id='observer'`, string(raw))
			require.NoError(t, err, "publish only the observer heartbeat evidence")
			require.True(t, available(), "missing running or recovery references must stop permanently consuming physical headroom")
			require.JSONEq(t, string(before), string(read()), "presence and admission must preserve every lifecycle field, tenant, runtime, lease, holder, preview, snapshot, clock, output and uncertain receipt")
		})
	}
}
