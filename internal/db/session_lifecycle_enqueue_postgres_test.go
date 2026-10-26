package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLifecycleEnqueueParkedControlPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		kind      models.LifecycleDispatchKind
		allocated bool
		corrupt   bool
		want      lifecycleEnqueueDisposition
	}{
		{name: "retained answer exact replay", kind: models.LifecycleDispatchAnswer, want: lifecycleEnqueueBoundExistingConsumer},
		{name: "retained denial exact replay", kind: models.LifecycleDispatchDenial, want: lifecycleEnqueueBoundExistingConsumer},
		{name: "wrong scalar snapshot version", kind: models.LifecycleDispatchAnswer, corrupt: true, want: lifecycleEnqueuePendingReconciliation},
		{name: "runtime parked history is not a current exception", kind: models.LifecycleDispatchAnswer, allocated: true, want: lifecycleEnqueuePendingReconciliation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			fixture := seedStartControlReaderFixture(t, pool, tt.kind, true, true, tt.allocated)
			binding := fixture.expected.Binding
			var payload json.RawMessage
			opts := EnqueueOpts{}
			err := pool.QueryRow(context.Background(), "SELECT payload::text,queue,job_type,priority,dedupe_key,target_node_id FROM jobs WHERE org_id=$1 AND id=$2", binding.orgID, binding.jobID).
				Scan(&payload, &opts.Queue, &opts.JobType, &opts.Priority, &opts.DedupeKey, &opts.TargetNodeID)
			require.NoError(t, err, "retain the exact real job options and opaque domain payload without numeric conversion")
			fields, err := lifecycleJobPayloadObject(payload)
			require.NoError(t, err, "separate the original domain envelope from its immutable binding for exact retry")
			for _, key := range lifecycleJobBindingKeys {
				delete(fields, key)
			}
			opts.Payload = fields
			if tt.corrupt {
				_, err := pool.Exec(context.Background(), "UPDATE session_input_dispatches SET control_snapshot=jsonb_set(control_snapshot,'{version}',to_jsonb(control_version+1)) WHERE org_id=$1 AND id=$2", binding.orgID, binding.dispatchIDs[0])
				require.NoError(t, err, "retain structurally valid but contradictory control evidence for reconciliation")
			}
			before := lifecycleEnqueueGraph(t, pool, binding.orgID)
			ctx, cancel, tx := beginLifecycleLaneClaimOwner(t, context.Background(), pool)
			defer cancel()
			_, err = tx.Exec(ctx, "SELECT id FROM jobs WHERE org_id=$1 AND id=$2 FOR UPDATE", binding.orgID, binding.jobID)
			require.NoError(t, err, "known parked job must be locked before parent and lane")
			_, err = lockLifecycleScopeInTx(ctx, tx, binding.orgID, lifecycleScope{sessionID: binding.fence.sessionID, threadIDs: []uuid.UUID{binding.fence.threadID}})
			require.NoError(t, err, "exact parked replay retains the canonical owner scope")
			result, err := enqueueLifecycleJobInTx(ctx, tx, binding.orgID, binding, opts, nil)
			require.NoError(t, err, "a parked collision must resolve explicitly without a lease or preparation lifetime")
			id := uuid.Nil
			if tt.want == lifecycleEnqueueBoundExistingConsumer {
				id = binding.jobID
			}
			require.Equal(t, lifecycleEnqueueResult{disposition: tt.want, jobID: id}, result, "only a complete actual retained job-only control may resolve exact logical replay")
			competes, err := HasCompetingInputConsumerInTx(ctx, tx, binding.orgID, binding.fence, nil)
			require.NoError(t, err, "retained logical replay must not hide the independently competing parked control")
			require.True(t, competes, "replay is not a provider permit or permission for another input consumer")
			require.NoError(t, tx.Rollback(ctx), "the caller alone settles the unchanged parked replay transaction")
			require.Equal(t, before, lifecycleEnqueueGraph(t, pool, binding.orgID), "exact replay and reconciliation must preserve every job/input/control field")
		})
	}
}

func TestLifecycleEnqueueCanonicalPayloadBudgetPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		count int
		fits  bool
	}{
		{name: "canonical payload remains replayable", count: 2, fits: true},
		{name: "compact payload cannot create unreadable canonical job", count: 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			binding, opts := seedLifecycleEnqueueInputs(t, pool)
			opts.Payload = json.RawMessage(`{"domain_ids":[` + strings.TrimSuffix(strings.Repeat(`1e100000,`, tt.count), ",") + `]}`)
			require.Less(t, len(opts.Payload.(json.RawMessage)), legacyJobRouteMaxBytes, "the regression input must fit the original compact-byte limit")
			before := lifecycleEnqueueGraph(t, pool, binding.orgID)
			ctx, cancel, tx := beginLifecycleLaneClaimOwner(t, context.Background(), pool)
			defer cancel()
			_, err := lockLifecycleScopeInTx(ctx, tx, binding.orgID, lifecycleScope{sessionID: binding.fence.sessionID, threadIDs: []uuid.UUID{binding.fence.threadID}})
			require.NoError(t, err, "acquire the parent's known scope before selecting a new exact input job")
			actual, err := enqueueLifecycleJobInTx(ctx, tx, binding.orgID, binding, opts, nil)
			if !tt.fits {
				require.ErrorIs(t, err, ErrLifecycleConsumerDiscoveryIncomplete, "oversized canonical JSONB must fail before inserting an unprovable immutable job")
				require.Equal(t, lifecycleEnqueueResult{}, actual, "canonical size refusal must expose neither successful admission nor a partially allocated job")
			} else {
				require.NoError(t, err, "a bounded canonical expansion must still create a supported immutable envelope")
				require.Equal(t, lifecycleEnqueueResult{disposition: lifecycleEnqueueNewJob, jobID: binding.jobID}, actual, "the first bounded allocation must name its exact new job")
				_, err = tx.Exec(ctx, `UPDATE session_input_dispatches SET admission_state='bound',consumer_kind='job',job_id=$3,next_attempt_at=NULL WHERE org_id=$1 AND id=ANY($2::uuid[])`, binding.orgID, binding.dispatchIDs, binding.jobID)
				require.NoError(t, err, "complete synthetic binding in the same owner transaction before exact replay")
				actual, err = enqueueLifecycleJobInTx(ctx, tx, binding.orgID, binding, opts, nil)
				require.NoError(t, err, "an unchanged canonical payload must remain within exact own-consumer discovery budgets")
				require.Equal(t, lifecycleEnqueueResult{disposition: lifecycleEnqueueBoundExistingConsumer, jobID: binding.jobID}, actual, "opaque exponent encoding must not make an admitted bounded job impossible to replay")
			}
			var count int
			err = tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND id=$2`, binding.orgID, binding.jobID).Scan(&count)
			require.NoError(t, err, "canonical budget refusal must leave the caller's transaction usable for explicit disposition")
			want := 0
			if tt.fits {
				want = 1
			}
			require.Equal(t, want, count, "no unusable job may be staged even before owner rollback")
			require.NoError(t, tx.Rollback(ctx), "the owner alone must discard the regression's synthetic admission")
			require.Equal(t, before, lifecycleEnqueueGraph(t, pool, binding.orgID), "the entire original input/job graph must survive canonical-budget refusal or owner rollback")
		})
	}
}

func TestLifecycleEnqueueCollisionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, collision string
		want            lifecycleEnqueueDisposition
	}{
		{name: "new immutable job", want: lifecycleEnqueueNewJob},
		{name: "exact job and complete input set", collision: "exact", want: lifecycleEnqueueBoundExistingConsumer},
		{name: "foreign global dedupe collision", collision: "foreign", want: lifecycleEnqueuePendingReconciliation},
		{name: "same tenant different job", collision: "other_job", want: lifecycleEnqueuePendingReconciliation},
		{name: "missing dispatch reference", collision: "missing_ref", want: lifecycleEnqueuePendingReconciliation},
		{name: "extra dispatch reference", collision: "extra_ref", want: lifecycleEnqueuePendingReconciliation},
		{name: "different recorded epoch", collision: "epoch", want: lifecycleEnqueuePendingReconciliation},
		{name: "different opaque domain input", collision: "payload", want: lifecycleEnqueuePendingReconciliation},
		{name: "different selected target", collision: "target", want: lifecycleEnqueuePendingReconciliation},
		{name: "different input job kind", collision: "job_type", want: lifecycleEnqueuePendingReconciliation},
		{name: "terminal job is not current consumer", collision: "terminal", want: lifecycleEnqueuePendingReconciliation},
		{name: "PK collision does not change dedupe key", collision: "key", want: lifecycleEnqueuePendingReconciliation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			binding, opts := seedLifecycleEnqueueInputs(t, pool)
			var collisionOrg, collisionJob uuid.UUID
			if tt.collision != "" {
				collisionOrg, collisionJob = binding.orgID, binding.jobID
				if tt.collision == "foreign" {
					collisionOrg, _, _ = seedRuntimeSeedThread(t, pool)
					collisionJob = uuid.New()
				} else if tt.collision == "other_job" {
					collisionJob = uuid.New()
				}
				payload, err := encodeLifecycleJobBindingPayload(opts.Payload.(json.RawMessage), binding)
				require.NoError(t, err, "construct the exact synthetic immutable job payload before independent collision mutations")
				seedOpts := opts
				seedOpts.Payload = payload
				id, err := enqueueOnWithID(context.Background(), pool, collisionOrg, seedOpts, collisionJob)
				require.NoError(t, err, "seed one global queue/key competitor without invoking admission")
				require.Equal(t, collisionJob, id, "collision setup must retain the explicitly selected job identity")
				if tt.collision != "foreign" && tt.collision != "other_job" {
					_, err = pool.Exec(context.Background(), `UPDATE session_input_dispatches SET admission_state='bound',consumer_kind='job',job_id=$3,next_attempt_at=NULL WHERE org_id=$1 AND id=ANY($2::uuid[])`, binding.orgID, binding.dispatchIDs, binding.jobID)
					require.NoError(t, err, "bind the complete original input set before testing exact replay")
				}
				switch tt.collision {
				case "missing_ref":
					_, err = pool.Exec(context.Background(), `UPDATE session_input_dispatches SET admission_state='waiting',consumer_kind='none',job_id=NULL,next_attempt_at=now() WHERE org_id=$1 AND id=$2`, binding.orgID, binding.dispatchIDs[0])
				case "extra_ref":
					_, err = pool.Exec(context.Background(), `INSERT INTO session_input_dispatches(org_id,session_id,thread_id,session_epoch,thread_epoch,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_state,consumer_kind,job_id) VALUES($1,$2,$3,0,0,'message','synthetic_extra',gen_random_uuid()::text,repeat('a',64),'{}',1,'bound','job',$4)`, binding.orgID, binding.fence.sessionID, binding.fence.threadID, binding.jobID)
				case "epoch":
					_, err = pool.Exec(context.Background(), `UPDATE session_input_dispatches SET session_epoch=1 WHERE org_id=$1 AND id=$2`, binding.orgID, binding.dispatchIDs[0])
				case "payload":
					_, err = pool.Exec(context.Background(), `UPDATE jobs SET payload=payload || '{"domain_id":2}'::jsonb WHERE org_id=$1 AND id=$2`, binding.orgID, binding.jobID)
				case "target":
					_, err = pool.Exec(context.Background(), `UPDATE jobs SET target_node_id='synthetic_other_target' WHERE org_id=$1 AND id=$2`, binding.orgID, binding.jobID)
				case "job_type":
					_, err = pool.Exec(context.Background(), `UPDATE jobs SET job_type='run_agent' WHERE org_id=$1 AND id=$2`, binding.orgID, binding.jobID)
				case "terminal":
					_, err = pool.Exec(context.Background(), `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, binding.orgID, binding.jobID)
				case "key":
					_, err = pool.Exec(context.Background(), `UPDATE jobs SET dedupe_key='synthetic_other_key' WHERE org_id=$1 AND id=$2`, binding.orgID, binding.jobID)
				}
				require.NoError(t, err, "install only the intended synthetic collision mismatch")
			}
			before := lifecycleEnqueueGraph(t, pool, binding.orgID)
			var beforeCollision json.RawMessage
			if collisionJob != uuid.Nil {
				beforeCollision = lifecycleStatusJobSnapshot(t, pool, collisionOrg, collisionJob)
			}
			ctx, cancel, tx := beginLifecycleLaneClaimOwner(t, context.Background(), pool)
			defer cancel()
			_, err := tx.Exec(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND id=$2 FOR UPDATE`, binding.orgID, binding.jobID)
			require.NoError(t, err, "any known job lock must precede parent and lane acquisition")
			_, err = lockLifecycleScopeInTx(ctx, tx, binding.orgID, lifecycleScope{sessionID: binding.fence.sessionID, threadIDs: []uuid.UUID{binding.fence.threadID}})
			require.NoError(t, err, "the enqueue owner must acquire its known parent and lane before admission")
			result, err := enqueueLifecycleJobInTx(ctx, tx, binding.orgID, binding, opts, nil)
			require.NoError(t, err, "collisions must have explicit dispositions rather than implicit input-consumed success")
			wantID := uuid.Nil
			if tt.want != lifecycleEnqueuePendingReconciliation {
				wantID = binding.jobID
			}
			require.Equal(t, lifecycleEnqueueResult{disposition: tt.want, jobID: wantID}, result, "only a new exact job or fully proved immutable consumer may expose a job reference")
			var open bool
			require.NoError(t, tx.QueryRow(ctx, `SELECT true`).Scan(&open), "enqueue must not settle or abort its caller's transaction")
			require.True(t, open, "the owner must retain responsibility for binding and commit")
			if tt.want == lifecycleEnqueueNewJob {
				var payload json.RawMessage
				require.NoError(t, tx.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, binding.orgID, binding.jobID).Scan(&payload), "a new job must retain the full exact envelope inside the caller's transaction")
				actual, err := decodeLifecycleJobBindingPayload(binding.orgID, binding.jobID, payload)
				require.NoError(t, err, "the persisted new job must decode against its real tenant and identity")
				require.True(t, sameLifecycleJobBinding(binding, actual), "new allocation must preserve the complete input set and recorded fence")
			}
			require.NoError(t, tx.Rollback(ctx), "only the owner may release locks or discard the uncommitted new job")
			require.Equal(t, before, lifecycleEnqueueGraph(t, pool, binding.orgID), "collision handling must never append, bind or rewrite inputs, and a rolled-back new job must disappear")
			if collisionJob != uuid.Nil {
				require.Equal(t, beforeCollision, lifecycleStatusJobSnapshot(t, pool, collisionOrg, collisionJob), "every field of the original collision job must remain unchanged, including a foreign global-key owner")
			}
		})
	}
}

func seedLifecycleEnqueueInputs(t *testing.T, pool *pgxpool.Pool) (LifecycleJobBinding, EnqueueOpts) {
	t.Helper()
	org, session, lane := seedRuntimeSeedThread(t, pool)
	zero := int64(0)
	fence, err := NewAdmissionFence(session, &lane, 0, &zero)
	require.NoError(t, err, "create an exact tenant-owned lane fence for synthetic input jobs")
	binding, err := NewLifecycleJobBinding(org, fence, uuid.New(), []uuid.UUID{uuid.New(), uuid.New()}, models.LifecycleDispatchMessage, nil)
	require.NoError(t, err, "construct a newly selected ordinary-message batch without adopting any existing job")
	for _, id := range binding.dispatchIDs {
		_, err := pool.Exec(context.Background(), `INSERT INTO session_input_dispatches(id,org_id,session_id,thread_id,session_epoch,thread_epoch,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_state,consumer_kind,next_attempt_at) VALUES($1,$2,$3,$4,0,0,'message','synthetic_enqueue',$1::uuid::text,repeat('a',64),'{}',1,'waiting','none',now())`, id, org, session, lane)
		require.NoError(t, err, "retain each unbound input before a separate canonical admission unit")
	}
	key, target := "synthetic_enqueue:"+binding.jobID.String(), "synthetic_enqueue_target"
	runAt := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	return binding, EnqueueOpts{Queue: "agent", JobType: "continue_session", Payload: json.RawMessage(`{"domain_id":900719925474099312345}`), Priority: 7, DedupeKey: &key, TargetNodeID: &target, RunAt: &runAt, MaxAttempts: 8}
}

func lifecycleEnqueueGraph(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE j.org_id=$1),'dispatches',(SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM session_input_dispatches d WHERE d.org_id=$1))`, orgID).Scan(&raw)
	require.NoError(t, err, "capture every job and input field for this isolated synthetic tenant without numeric narrowing")
	return raw
}
