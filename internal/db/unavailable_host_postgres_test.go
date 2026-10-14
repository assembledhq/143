package db

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type unavailableReviewHost struct {
	name string
	sql  string
}

func unavailableReviewHosts() []unavailableReviewHost {
	return []unavailableReviewHost{
		{name: "dead", sql: `UPDATE nodes SET status='dead' WHERE id='source'`},
		{name: "stale", sql: `UPDATE nodes SET last_heartbeat_at=clock_timestamp()-interval '91 seconds' WHERE id='source'`},
		{name: "draining only", sql: `UPDATE nodes SET status='draining' WHERE id='source'`},
		{name: "active drain intent", sql: `UPDATE nodes SET drain_intent='deploy' WHERE id='source'`},
	}
}

type unavailableReviewCommitment struct {
	name          string
	jobType       string
	jobStatus     string
	metadataState string
	assessment    bool
}

func seedUnavailableReviewCommitment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, host unavailableReviewHost, commitment unavailableReviewCommitment, capacity int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	org, session, assessment := uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `
INSERT INTO nodes(id,host,metadata) VALUES
 ('source','source-container-host','{"sandbox_host_id":"192.0.2.1","max_active_sandboxes":2,"live_sandbox_count":1,"reserved_sandbox_count":0}'),
 ('destination','destination-container-host',jsonb_build_object('sandbox_host_id','192.0.2.2','max_active_sandboxes',$4::integer,'live_sandbox_count',0,'reserved_sandbox_count',0));
INSERT INTO sessions(id,org_id,status,container_id,worker_node_id,turn_holding_container,recovery_state,recovery_queued_at,snapshot_key)
 VALUES($1,$2,'running',repeat('a',64),'source',true,'queued',clock_timestamp()-interval '2 hours','preserved-snapshot');
INSERT INTO code_review_session_metadata(org_id,session_id,status,completed_at)
 VALUES($2,$1,$5,clock_timestamp()-interval '3 hours');
INSERT INTO code_review_revision_assessments(id,org_id,session_id,status,publication_state,publication_receipt)
 VALUES($3,$2,$1,'failed','uncertain','{"review_id":42}');
INSERT INTO session_sandbox_holders VALUES($2,$1,'active',clock_timestamp()+interval '1 hour');
INSERT INTO thread_runtimes VALUES($2,$1,'paused');
INSERT INTO session_executors VALUES($2,$1,'running');
INSERT INTO session_threads VALUES($2,$1,'awaiting_input');
INSERT INTO preview_instances VALUES($2,$1,true);`, session, org, assessment, capacity, commitment.metadataState)
	require.NoError(t, err, "seed retained ownership and protected durable state on a distinct source host")
	_, err = pool.Exec(ctx, host.sql)
	require.NoError(t, err, "make only the source physical host unavailable")
	if commitment.assessment {
		state := "reserved"
		if commitment.jobStatus == "running" {
			state = "running"
		}
		_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status=$3 WHERE org_id=$1 AND id=$2;
INSERT INTO jobs(org_id,payload,status,job_type,run_at)
 VALUES($1,jsonb_build_object('assessment_id',$2::text),$4,$5,clock_timestamp()+interval '1 hour');`, org, assessment, state, commitment.jobStatus, commitment.jobType)
	} else {
		_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,payload,status,job_type,run_at)
 VALUES($1,jsonb_build_object('session_id',$2::text,'review_output_key','output'),$3,$4,clock_timestamp()+interval '1 hour');`, org, session, commitment.jobStatus, commitment.jobType)
	}
	require.NoError(t, err, "persist the exact runnable review commitment without changing its existing clocks")
	return org, session
}

func readUnavailableReviewState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org uuid.UUID) json.RawMessage {
	t.Helper()
	var state json.RawMessage
	err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'nodes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY n.id) FROM nodes n),
 'sessions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM sessions s WHERE org_id=$1),
 'metadata',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM code_review_session_metadata m WHERE org_id=$1),
 'assessments',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM code_review_revision_assessments a WHERE org_id=$1),
 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE org_id=$1),
 'holders',(SELECT jsonb_agg(to_jsonb(h)) FROM session_sandbox_holders h WHERE org_id=$1),
 'runtimes',(SELECT jsonb_agg(to_jsonb(r)) FROM thread_runtimes r WHERE org_id=$1),
 'executors',(SELECT jsonb_agg(to_jsonb(e)) FROM session_executors e WHERE org_id=$1),
 'threads',(SELECT jsonb_agg(to_jsonb(t)) FROM session_threads t WHERE org_id=$1),
 'previews',(SELECT jsonb_agg(to_jsonb(p)) FROM preview_instances p WHERE org_id=$1))`, org).Scan(&state)
	require.NoError(t, err, "capture all original ownership, jobs, clocks, holds, snapshots and receipts")
	return state
}

func TestCodeReviewUnavailableHostCommitmentPostgres(t *testing.T) {
	t.Parallel()
	commitments := []unavailableReviewCommitment{
		{name: "pending controller", jobType: "run_code_review", jobStatus: "pending", metadataState: "queued"},
		{name: "running controller", jobType: "run_code_review", jobStatus: "running", metadataState: "running"},
		{name: "pending workspace", jobType: "prepare_code_review_workspace", jobStatus: "pending", metadataState: "queued"},
		{name: "running execution", jobType: "run_agent", jobStatus: "running", metadataState: "running"},
		{name: "pending assessment", jobType: "run_code_review_recheck", jobStatus: "pending", metadataState: "completed", assessment: true},
		{name: "running assessment", jobType: "run_code_review_recheck", jobStatus: "running", metadataState: "completed", assessment: true},
	}
	for _, host := range unavailableReviewHosts() {
		for _, commitment := range commitments {
			t.Run(host.name+"/"+commitment.name, func(t *testing.T) {
				t.Parallel()
				pool := newReviewRecoveryPool(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				org, session := seedUnavailableReviewCommitment(t, ctx, pool, host, commitment, 1)
				before := readUnavailableReviewState(t, ctx, pool, org)
				for _, warm := range []uuid.UUID{uuid.Nil, session} {
					var available bool
					err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
						var err error
						available, err = CodeReviewSandboxAvailable(ctx, tx, org, warm)
						return err
					})
					require.NoError(t, err, "read fleet admission with retained ownership on an unavailable physical host")
					require.False(t, available, "the existing runnable commitment must reserve the healthy destination's only cold slot")
				}
				require.JSONEq(t, string(before), string(readUnavailableReviewState(t, ctx, pool, org)), "cold reservation and rejected warm reuse must preserve every existing durable field")
			})
		}
	}
}

func TestCodeReviewUnavailableHostConcurrentAdmissionAndRecoveryPostgres(t *testing.T) {
	t.Parallel()
	for _, host := range unavailableReviewHosts() {
		t.Run(host.name, func(t *testing.T) {
			t.Parallel()
			pool := newReviewRecoveryPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			originalOrg, session := seedUnavailableReviewCommitment(t, ctx, pool, host, unavailableReviewCommitment{jobType: "run_code_review", jobStatus: "pending", metadataState: "queued"}, 3)
			before := readUnavailableReviewState(t, ctx, pool, originalOrg)
			newOrg := uuid.New()
			const burst = 8
			ids := make([]uuid.UUID, burst)
			for i := range ids {
				ids[i] = uuid.New()
				_, err := pool.Exec(ctx, `INSERT INTO pending_intents(id,org_id) VALUES($1,$2)`, ids[i], newOrg)
				require.NoError(t, err, "persist new intake before any attempt is admitted")
			}
			type admissionResult struct {
				available bool
				err       error
			}
			results := make(chan admissionResult, burst)
			var wg sync.WaitGroup
			for _, id := range ids {
				wg.Add(1)
				go func(id uuid.UUID) {
					defer wg.Done()
					var available bool
					err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
						var err error
						available, err = CodeReviewSandboxAvailable(ctx, tx, newOrg, uuid.Nil)
						if err != nil || !available {
							return err
						}
						_, err = tx.Exec(ctx, `INSERT INTO sessions(id,org_id) VALUES($1,$2);
INSERT INTO code_review_session_metadata(org_id,session_id,created_at) VALUES($2,$1,clock_timestamp());
INSERT INTO jobs(org_id,payload,status,job_type) VALUES($2,jsonb_build_object('session_id',$1::text,'review_output_key','output'),'pending','run_code_review');
UPDATE pending_intents SET status='started' WHERE org_id=$2 AND id=$1;`, id, newOrg)
						return err
					})
					results <- admissionResult{available, err}
				}(id)
			}
			wg.Wait()
			close(results)
			admitted := 0
			for got := range results {
				require.NoError(t, got.err, "each serialized admission must preserve intake without a partial attempt")
				if got.available {
					admitted++
				}
			}
			require.Equal(t, 2, admitted, "one unavailable retained commitment leaves exactly two of three healthy slots for new requests")
			var started, pending, clocks int
			err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE status='started'),COUNT(*) FILTER(WHERE status='pending') FROM pending_intents WHERE org_id=$1`, newOrg).Scan(&started, &pending)
			require.NoError(t, err, "read durable intake after concurrent admission")
			err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_session_metadata WHERE org_id=$1`, newOrg).Scan(&clocks)
			require.NoError(t, err, "read attempt clocks created only after successful admission")
			require.Equal(t, []int{2, 6, 2}, []int{started, pending, clocks}, "deferred requests must keep pending intake without starting review clocks")
			require.JSONEq(t, string(before), string(readUnavailableReviewState(t, ctx, pool, originalOrg)), "new tenant admission must leave the retained review's ownership and protected clocks exactly intact")

			_, err = pool.Exec(ctx, `UPDATE sessions SET container_id=NULL,worker_node_id=NULL WHERE org_id=$1 AND id=$2`, originalOrg, session)
			require.NoError(t, err, "simulate later recovery relinquishing only the unavailable workspace reference")
			var available bool
			err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				var err error
				available, err = CodeReviewSandboxAvailable(ctx, tx, newOrg, uuid.Nil)
				return err
			})
			require.NoError(t, err, "read admission after the retained review becomes explicitly cold")
			require.False(t, available, "later cold recovery must not reveal an unreserved gap or admit a fourth commitment")
			var expected map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(before, &expected), "decode original protected durable rows")
			var sessions []map[string]any
			require.NoError(t, json.Unmarshal(expected["sessions"], &sessions), "decode the exact original session ownership")
			sessions[0]["container_id"], sessions[0]["worker_node_id"] = nil, nil
			expected["sessions"], err = json.Marshal(sessions)
			require.NoError(t, err, "change only the simulated workspace reference in the expected snapshot")
			expectedJSON, err := json.Marshal(expected)
			require.NoError(t, err, "encode the exact post-recovery durable state")
			require.JSONEq(t, string(expectedJSON), string(readUnavailableReviewState(t, ctx, pool, originalOrg)), "recovery and subsequent admission must preserve every other owner, hold, receipt, job and clock")
		})
	}
}

func TestCodeReviewVerifiedReplacementHostWarmCommitmentPostgres(t *testing.T) {
	t.Parallel()
	for _, host := range unavailableReviewHosts() {
		t.Run(host.name, func(t *testing.T) {
			t.Parallel()
			pool := newReviewRecoveryPool(t)
			ctx := context.Background()
			org, session := seedUnavailableReviewCommitment(t, ctx, pool, host, unavailableReviewCommitment{jobType: "run_code_review", jobStatus: "pending", metadataState: "queued"}, 2)
			_, err := pool.Exec(ctx, `UPDATE nodes SET started_at=clock_timestamp()+interval '1 second',metadata=metadata||'{"sandbox_host_id":"192.0.2.1","live_sandbox_count":2}' WHERE id='destination'`)
			require.NoError(t, err, "make a verified replacement generation on the predecessor's saturated physical host")
			before := readUnavailableReviewState(t, ctx, pool, org)
			var available bool
			err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				var err error
				available, err = CodeReviewSandboxAvailable(ctx, tx, org, session)
				return err
			})
			require.NoError(t, err, "read exact warm predecessor reuse on verified replacement capacity")
			require.True(t, available, "a predecessor's retained workspace may reuse its verified physical host even while full")
			require.JSONEq(t, string(before), string(readUnavailableReviewState(t, ctx, pool, org)), "warm admission must preserve predecessor ownership and every durable clock")
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,metadata) VALUES('spare','{"sandbox_host_id":"192.0.2.2","max_active_sandboxes":1,"live_sandbox_count":0,"reserved_sandbox_count":0}')`)
			require.NoError(t, err, "leave exactly one spare slot on a distinct healthy physical host")
			before = readUnavailableReviewState(t, ctx, pool, org)
			err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				var err error
				available, err = CodeReviewSandboxAvailable(ctx, tx, org, uuid.Nil)
				return err
			})
			require.NoError(t, err, "read cold admission beside a warm commitment on a verified replacement host")
			require.True(t, available, "retained predecessor demand on a full verified host must not also reserve the only cold slot elsewhere")
			require.JSONEq(t, string(before), string(readUnavailableReviewState(t, ctx, pool, org)), "cold admission must preserve predecessor ownership and every durable clock")
		})
	}
}
