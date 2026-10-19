package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This is an executable proposed-query/policy probe for M0 contract sections
// 2/7/9, not a production readiness classifier or a migrated schema test. It
// deliberately does not install floor/activation guards, mutate classification,
// emit recovery notices, or establish concurrent checkpoint publication safety.
func TestSessionLifecycleReadinessPolicyProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	liveProducer := `UPDATE thread_runtimes SET status='running', drain_receipt_id=NULL;
		UPDATE session_activity_phases SET status='running';
		UPDATE session_human_input_requests SET readiness_proof=NULL,readiness_published_at=NULL;`
	tests := []struct {
		name               string
		change             string
		expected           string
		expectedReason     string
		deadlineOffset     time.Duration
		nowOffset          time.Duration
		pollAgain          bool
		operations         []string
		deferCompatibility bool
		expectedAdvances   []int
		afterOperations    string
	}{
		{name: "exact ready tuple and producer drain", expected: "ready", expectedReason: "exact_checkpoint"},
		{name: "immutable dispatch survives completed job token rotation", change: `UPDATE jobs SET lock_token=gen_random_uuid(), status='succeeded'`, expected: "ready", expectedReason: "exact_checkpoint"},
		{name: "compatible legacy live producer waits five minutes", change: liveProducer, expected: "waiting", expectedReason: "legacy_checkpoint_wait", deadlineOffset: 5 * time.Minute},
		{name: "polling retains the first legacy waiting deadline", change: liveProducer + `UPDATE session_human_input_requests SET readiness_deadline_at='2026-10-18T12:02:00Z'`, expected: "waiting", expectedReason: "legacy_checkpoint_wait", deadlineOffset: 2 * time.Minute, pollAgain: true},
		{name: "new postfloor live producer has no recovery deadline", change: liveProducer + `UPDATE session_human_input_requests SET legacy_request=false,request_created_at='2026-10-18T11:59:50Z'`, expected: "checkpoint_pending", expectedReason: "live_producer_checkpoint_pending"},
		{name: "new live producer remains checkpoint pending beyond five minutes", change: liveProducer + `UPDATE session_human_input_requests SET legacy_request=false,request_created_at='2026-10-18T11:59:50Z'`, nowOffset: 6 * time.Minute, expected: "checkpoint_pending", expectedReason: "live_producer_checkpoint_pending"},
		{name: "new live producer ignores an inapplicable legacy wait deadline", change: liveProducer + `UPDATE session_human_input_requests SET legacy_request=false,request_created_at='2026-10-18T11:59:50Z',readiness_deadline_at='2026-10-18T11:59:59Z'`, expected: "checkpoint_pending", expectedReason: "live_producer_checkpoint_pending"},
		{name: "paused producer without proof requires recovery", change: `UPDATE session_human_input_requests SET readiness_proof=NULL`, expected: "recovery_required"},
		{name: "orphan producer requires recovery", change: `DELETE FROM thread_runtimes`, expected: "recovery_required"},
		{name: "unknown attempt cannot wait or become ready", change: liveProducer + `UPDATE session_human_input_requests SET producer_attempt_token=NULL`, expected: "recovery_required"},
		{name: "incompatible live producer cannot wait", change: liveProducer + `UPDATE thread_runtimes SET writer_version=0`, expected: "recovery_required"},
		{name: "expired producer lease cannot wait", change: liveProducer + `UPDATE thread_runtimes SET lease_expires_at='2026-10-18T11:59:59Z'`, expected: "recovery_required"},
		{name: "expired legacy live wait requires recovery", change: liveProducer + `UPDATE session_human_input_requests SET readiness_deadline_at='2026-10-18T11:59:59Z'`, expected: "recovery_required", expectedReason: "legacy_wait_expired"},
		{name: "legacy wait expires exactly at the fixed deadline", change: liveProducer + `UPDATE session_human_input_requests SET readiness_deadline_at='2026-10-18T12:05:00Z'`, nowOffset: 5 * time.Minute, expected: "recovery_required", expectedReason: "legacy_wait_expired"},
		{name: "late exact proof cannot revive expired legacy consent", change: `UPDATE session_human_input_requests SET readiness_deadline_at='2026-10-18T11:59:59Z'`, expected: "recovery_required", expectedReason: "legacy_wait_expired"},
		{name: "recovery disposition cannot be reversed by late proof", change: `UPDATE session_human_input_requests SET readiness_state='recovery_required'`, expected: "recovery_required", expectedReason: "recovery_required"},
		{name: "different request proof is never ready", change: `UPDATE session_human_input_requests SET readiness_proof=jsonb_set(readiness_proof,'{request_id}','"another-request"')`, expected: "recovery_required"},
		{name: "different provider proof is never ready", change: `UPDATE session_human_input_requests SET readiness_proof=jsonb_set(readiness_proof,'{provider_request_id}','"another-provider-request"')`, expected: "recovery_required"},
		{name: "missing provider identity needs an explicit proof variant", change: `UPDATE session_human_input_requests SET provider_request_id=NULL,readiness_proof=jsonb_set(readiness_proof,'{provider_request_id}','null')`, expected: "recovery_required"},
		{name: "different request kind is never ready", change: `UPDATE session_human_input_requests SET request_kind='tool_approval'`, expected: "recovery_required"},
		{name: "different lane proof is never ready", change: `UPDATE session_human_input_requests SET readiness_proof=jsonb_set(readiness_proof,'{thread_id}','"another-lane"')`, expected: "recovery_required"},
		{name: "superseded session epoch is never ready", change: `UPDATE sessions SET admission_epoch=admission_epoch+1`, expected: "recovery_required", expectedReason: "successor_epoch"},
		{name: "superseded lane epoch is never ready", change: `UPDATE session_threads SET admission_epoch=admission_epoch+1`, expected: "recovery_required", expectedReason: "successor_epoch"},
		{name: "different request turn is never ready", change: `UPDATE session_human_input_requests SET turn_number=turn_number+1`, expected: "recovery_required"},
		{name: "different runtime lease is never ready", change: `UPDATE thread_runtimes SET lease_token=gen_random_uuid()`, expected: "recovery_required"},
		{name: "different producer attempt is never ready", change: `UPDATE session_human_input_requests SET producer_attempt_token=gen_random_uuid()`, expected: "recovery_required"},
		{name: "unproven replacement checkpoint key is never ready", change: `UPDATE sessions SET snapshot_key='replacement-checkpoint'`, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "successor workspace generation is never ready", change: `UPDATE sessions SET workspace_generation=workspace_generation+1`, expected: "recovery_required", expectedReason: "workspace_generation_changed"},
		{name: "unauthorized workspace revision drift is never ready", change: `UPDATE sessions SET workspace_revision=workspace_revision+1`, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "genuine successor differs from unauthorized revision drift", change: `UPDATE sessions SET admission_epoch=admission_epoch+1,workspace_revision=workspace_revision+1,checkpoint_workspace_revision=checkpoint_workspace_revision+1`, expected: "recovery_required", expectedReason: "successor_epoch"},
		{name: "raw snapshot before final diff is valid after exact metadata seal", expected: "ready", expectedReason: "exact_checkpoint"},
		{name: "seal before the final pause diff write is never ready", change: `UPDATE session_human_input_requests SET pause_finalized_revision=pause_workspace_revision+1`, expected: "recovery_required", expectedReason: "checkpoint_before_final_pause"},
		{name: "seal publication before finalization is never ready", change: `UPDATE session_human_input_requests SET pause_proof_sealed_at=pause_finalized_at-interval '1 second'`, expected: "recovery_required", expectedReason: "checkpoint_before_final_pause"},
		{name: "unauthenticated current checkpoint producer is never ready", change: `UPDATE sessions SET checkpoint_runtime_id=gen_random_uuid()`, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "unclosed input phase is never ready", change: `UPDATE session_activity_phases SET status='running'`, expected: "recovery_required"},
		{name: "missing producer drain is never ready", change: `UPDATE thread_runtimes SET drain_receipt_id=NULL`, expected: "recovery_required"},
		{name: "malformed proof values fail closed without casts", change: `UPDATE session_human_input_requests SET readiness_proof=jsonb_set(readiness_proof,'{workspace_generation}','"not-a-number"')`, expected: "recovery_required"},
		{name: "sibling checkpoint then diff preserves A immutable proof", operations: []string{"sibling_checkpoint", "sibling_diff"}, expectedAdvances: []int{1, 1}, expected: "ready", expectedReason: "exact_checkpoint"},
		{name: "explicit AcceptSplit preserves scoped provider context", operations: []string{"accept_split"}, expectedAdvances: []int{1}, expected: "ready", expectedReason: "exact_checkpoint"},
		{name: "explicit publication promotion preserves scoped provider context", operations: []string{"publication_promotion"}, expectedAdvances: []int{1}, expected: "ready", expectedReason: "exact_checkpoint"},
		{name: "explicit preview effect preserves scoped provider context", operations: []string{"preview_effect"}, expectedAdvances: []int{1}, expected: "ready", expectedReason: "exact_checkpoint"},
		{name: "metadata revision uses explicit preserving authority", operations: []string{"metadata_revision"}, expectedAdvances: []int{1}, expected: "ready", expectedReason: "exact_checkpoint"},
		{name: "authorized sibling compatibility pending is non destructive", operations: []string{"sibling_checkpoint"}, deferCompatibility: true, expectedAdvances: []int{1}, expected: "context_pending", expectedReason: "workspace_compatibility_pending"},
		{name: "inactive interpreter cannot authorize a context pending outcome", change: `UPDATE session_human_input_requests SET enforcement_version=0,proof_policy_version=0`, operations: []string{"sibling_checkpoint"}, deferCompatibility: true, expectedAdvances: []int{1}, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "same lane successor installs workspace but cannot certify A", operations: []string{"same_lane_checkpoint"}, expectedAdvances: []int{1}, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "foreign typed receipt cannot bridge an installed mutation", operations: []string{"foreign_receipt"}, expectedAdvances: []int{0}, afterOperations: `UPDATE sessions SET workspace_revision=workspace_revision+1`, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "context scope mismatch cannot certify an authorized mutation", operations: []string{"foreign_context"}, expectedAdvances: []int{1}, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "certificate gap cannot block workspace or heal lineage", change: `UPDATE session_input_workspace_compatibility SET workspace_revision=workspace_revision-1`, operations: []string{"metadata_revision"}, expectedAdvances: []int{1}, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "changed anchor digest cannot block workspace or become valid", change: `UPDATE session_input_workspace_compatibility SET anchor_digest='foreign-anchor'`, operations: []string{"metadata_revision"}, expectedAdvances: []int{1}, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "missing final metadata seal is not ready", change: `DELETE FROM probe_workspace_effect_receipts WHERE id=(SELECT pause_seal_receipt_id FROM session_human_input_requests)`, expected: "recovery_required"},
		{name: "missing request proof is not ready", change: `UPDATE session_human_input_requests SET readiness_proof=NULL`, expected: "recovery_required"},
		{name: "provider type mismatch invalidates native context identity", change: `UPDATE probe_native_context_availability SET provider_type='another-provider'`, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "provider build mismatch invalidates native context identity", change: `UPDATE probe_native_context_availability SET provider_build='synthetic-provider@2'`, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "conversation mismatch invalidates native context identity", change: `UPDATE probe_native_context_availability SET conversation_id='another-conversation'`, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "workdir mismatch invalidates native context identity", change: `UPDATE probe_native_context_availability SET workdir_scope='another-workdir'`, expected: "recovery_required", expectedReason: "workspace_lineage_unprovable"},
		{name: "expected native context missing is unavailable", change: `DELETE FROM probe_native_context_availability`, expected: "context_unavailable", expectedReason: "input_context_unavailable"},
		{name: "named context retarget requires selected verification", operations: []string{"scope_retarget"}, expectedAdvances: []int{1}, expected: "context_pending", expectedReason: "workspace_compatibility_pending"},
		{name: "named restore replacement requires selected verification", operations: []string{"restore_replacement"}, expectedAdvances: []int{1}, expected: "context_pending", expectedReason: "workspace_compatibility_pending"},
		{name: "named provider context replacement requires selected verification", operations: []string{"context_replacement"}, expectedAdvances: []int{1}, expected: "context_pending", expectedReason: "workspace_compatibility_pending"},
		{name: "snapshot expiry retains explicitly usable trusted live workspace", operations: []string{"shared_snapshot_expiry"}, expectedAdvances: []int{1}, expected: "ready", expectedReason: "exact_checkpoint"},
		{name: "snapshot expiry with unusable whole workspace is unavailable", operations: []string{"workspace_expiry"}, expectedAdvances: []int{1}, expected: "context_unavailable", expectedReason: "input_context_unavailable"},
		{name: "new live producer before first proof remains checkpoint pending", change: liveProducer + `UPDATE session_human_input_requests SET legacy_request=false,request_created_at='2026-10-18T11:59:50Z',conversation_id=NULL`, expected: "checkpoint_pending", expectedReason: "live_producer_checkpoint_pending"},
		{name: "legacy live producer before first proof retains bounded wait", change: liveProducer + `UPDATE session_human_input_requests SET conversation_id=NULL`, expected: "waiting", expectedReason: "legacy_checkpoint_wait", deadlineOffset: 5 * time.Minute},
		{name: "new live producer without first proof survives B checkpoint and diff", change: liveProducer + `UPDATE session_human_input_requests SET legacy_request=false,request_created_at='2026-10-18T11:59:50Z',conversation_id=NULL`, operations: []string{"sibling_checkpoint", "sibling_diff"}, expectedAdvances: []int{1, 1}, expected: "checkpoint_pending", expectedReason: "live_producer_checkpoint_pending"},
		{name: "legacy live producer without first proof survives B checkpoint and diff", change: liveProducer + `UPDATE session_human_input_requests SET conversation_id=NULL`, operations: []string{"sibling_checkpoint", "sibling_diff"}, expectedAdvances: []int{1, 1}, expected: "waiting", expectedReason: "legacy_checkpoint_wait", deadlineOffset: 5 * time.Minute},
		{name: "paused producer without any first proof is unprovable not lost", change: `UPDATE session_human_input_requests SET readiness_proof=NULL,conversation_id=NULL`, expected: "recovery_required"},
		{name: "new live preproof known conversation missing observation waits across B", change: liveProducer + `UPDATE session_human_input_requests SET legacy_request=false,request_created_at='2026-10-18T11:59:50Z'; DELETE FROM probe_native_context_availability`, operations: []string{"sibling_checkpoint", "sibling_diff"}, expectedAdvances: []int{1, 1}, expected: "checkpoint_pending", expectedReason: "live_producer_checkpoint_pending"},
		{name: "legacy live preproof known conversation missing observation retains fixed wait", change: liveProducer + `DELETE FROM probe_native_context_availability`, operations: []string{"sibling_checkpoint", "sibling_diff"}, expectedAdvances: []int{1, 1}, expected: "waiting", expectedReason: "legacy_checkpoint_wait", deadlineOffset: 5 * time.Minute},
		{name: "missing expected published observation remains unavailable even while live", change: liveProducer + `UPDATE session_human_input_requests SET readiness_published_at='2026-10-18T11:59:40Z'; DELETE FROM probe_native_context_availability`, expected: "context_unavailable", expectedReason: "input_context_unavailable"},
		{name: "known native loss never becomes waiting merely because producer is live", change: liveProducer + `UPDATE probe_native_context_availability SET availability='missing'`, expected: "context_unavailable", expectedReason: "input_context_unavailable"},
		{name: "stamped certificate with unknown workspace availability never ready", change: `UPDATE sessions SET physical_workspace_availability=NULL,snapshot_availability=NULL;
		 UPDATE session_input_workspace_compatibility SET workspace_state=(SELECT ` + sessionLifecycleWorkspaceStateProbe + ` FROM sessions s);
		 UPDATE probe_workspace_operation_receipts SET to_state=(SELECT ` + sessionLifecycleWorkspaceStateProbe + ` FROM sessions s)`, expected: "context_unavailable", expectedReason: "input_context_unavailable"},
		{name: "stamped certificate with invalid workspace availability never ready", change: `UPDATE sessions SET physical_workspace_availability='unknown',snapshot_availability='unknown';
		 UPDATE session_input_workspace_compatibility SET workspace_state=(SELECT ` + sessionLifecycleWorkspaceStateProbe + ` FROM sessions s);
		 UPDATE probe_workspace_operation_receipts SET to_state=(SELECT ` + sessionLifecycleWorkspaceStateProbe + ` FROM sessions s)`, expected: "context_unavailable", expectedReason: "input_context_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, requestID := newSessionLifecycleReadinessProbe(t, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if tt.change != "" {
				_, err := pool.Exec(ctx, tt.change)
				require.NoError(t, err, "the isolated fixture should represent the selected legacy evidence state")
			}
			var immutableAnchor json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object('proof',readiness_proof,'digest',pause_anchor_digest,'key',pause_snapshot_key,'revision',pause_workspace_revision) FROM session_human_input_requests WHERE org_id=$1 AND id=$2`, orgID, requestID).Scan(&immutableAnchor), "capture the exact A pause anchor before any B or domain workspace operation")
			var advances []int
			for _, operation := range tt.operations {
				advances = append(advances, advanceSessionLifecycleCompatibilityProbe(t, ctx, pool, orgID, requestID, operation, tt.deferCompatibility))
			}
			require.Equal(t, tt.expectedAdvances, advances, "workspace authorization must be independent of every pending request's eligibility or certificate health")
			if tt.afterOperations != "" {
				_, err := pool.Exec(ctx, tt.afterOperations)
				require.NoError(t, err, "represent an installed mutation whose claimed authority was rejected")
			}
			var anchorAfterOperations json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object('proof',readiness_proof,'digest',pause_anchor_digest,'key',pause_snapshot_key,'revision',pause_workspace_revision) FROM session_human_input_requests WHERE org_id=$1 AND id=$2`, orgID, requestID).Scan(&anchorAfterOperations), "inspect A pause anchor after all proposed certificate transitions")
			require.Equal(t, immutableAnchor, anchorAfterOperations, "sibling/domain progress must never rewrite A request's paused provider proof, key or sealed revision")
			var actual, reason string
			var deadline *time.Time
			queryTime := now.Add(tt.nowOffset)
			require.NoError(t, pool.QueryRow(ctx, sessionLifecycleReadinessPolicyProbe, orgID, requestID, queryTime).Scan(&actual, &deadline, &reason), "the canonical proposed query should classify the exact tenant request")
			if deadline != nil {
				utcDeadline := deadline.UTC()
				deadline = &utcDeadline
			}
			require.Equal(t, tt.expected, actual, "only exact immutable proof may be ready and only a compatible live producer may wait")
			expectedReason := tt.expectedReason
			if expectedReason == "" {
				expectedReason = "unprovable"
			}
			require.Equal(t, expectedReason, reason, "the policy must distinguish successor authority, unauthorized drift, checkpoint ordering and live producer waiting")
			var expectedDeadline *time.Time
			if tt.expected == "waiting" {
				value := now.Add(tt.deadlineOffset)
				expectedDeadline = &value
			}
			require.Equal(t, expectedDeadline, deadline, "waiting must have a fixed deadline and other dispositions must not manufacture more waiting time")
			if tt.pollAgain {
				var polledState string
				var polledDeadline *time.Time
				var polledReason string
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleReadinessPolicyProbe, orgID, requestID, queryTime.Add(time.Minute)).Scan(&polledState, &polledDeadline, &polledReason), "a later policy poll should use the recorded first deadline")
				if polledDeadline != nil {
					utcDeadline := polledDeadline.UTC()
					polledDeadline = &utcDeadline
				}
				require.Equal(t, tt.expected, polledState, "an unexpired compatible producer should remain waiting")
				require.Equal(t, expectedReason, polledReason, "polling should retain the exact waiting policy")
				require.Equal(t, expectedDeadline, polledDeadline, "polling must not extend the first recorded waiting deadline")
			}
			var status string
			var answer *string
			var grants, jobs int
			require.NoError(t, pool.QueryRow(ctx, `SELECT q.status,q.answer_text,
				(SELECT count(*) FROM session_capability_grants g WHERE g.org_id=q.org_id),
				(SELECT count(*) FROM jobs j WHERE j.org_id=q.org_id)
				FROM session_human_input_requests q WHERE q.org_id=$1 AND q.id=$2`, orgID, requestID).Scan(&status, &answer, &grants, &jobs), "inspect request and side effects after the read-only policy probe")
			require.Equal(t, "pending", status, "classification evidence must not answer or deny the original request")
			require.Equal(t, (*string)(nil), answer, "the original request must remain unanswered in every readiness outcome")
			require.Equal(t, []int{0, 1}, []int{grants, jobs}, "readiness inspection must add no grant or execution job beyond the existing producer")
		})
	}
}

// These receipts are synthetic source projections, not runtime implementation or
// proof of native files. The baseline trusts authorized same-session workspace/
// HOME writers; it does not claim byte immutability, per-request HOME isolation,
// artifact export, or weakened tenant authorization. Actual source joins, native
// VerifyResumable I/O and transaction/launch integration remain 0a gates.
// Descriptor timestamps use numeric Unix epochs, independent of connection
// TimeZone; fixture anchor digests use SHA-256 of PostgreSQL's jsonb text form.
const sessionLifecycleWorkspaceStateProbe = `jsonb_build_object(
 'generation',s.workspace_generation,'revision',s.workspace_revision,'snapshot_key',s.snapshot_key,
 'checkpoint_runtime_id',s.checkpoint_runtime_id,'checkpoint_attempt_token',s.checkpoint_attempt_token,
 'checkpoint_phase_id',s.checkpoint_phase_id,'checkpoint_revision',s.checkpoint_workspace_revision,
 'checkpoint_published_at',extract(epoch FROM s.checkpoint_published_at),'effect_receipt_id',s.workspace_effect_receipt_id,
 'snapshot_availability_version',s.snapshot_availability_version,
 'snapshot_availability',s.snapshot_availability,'physical_workspace_availability',s.physical_workspace_availability)`

// Identity and availability are not artifact content hashes or security claims.
// A real selected adapter must verify this identity on the actual live/restored
// target before launch; a synthetic availability projection cannot replace it.
const sessionLifecycleNativeIdentityProbe = `
 n.org_id=q.org_id AND n.request_id=q.id AND n.session_id=q.session_id AND n.thread_id=q.thread_id
 AND n.provider_type=q.provider_type AND n.provider_build=q.provider_build
 AND n.conversation_id=q.conversation_id AND n.workdir_scope=q.workdir_scope
 AND n.workspace_generation=s.workspace_generation`

const sessionLifecycleEffectScopeProbe = `
 a.org_id=q.org_id AND a.session_id=q.session_id AND a.request_id=q.id
 AND a.anchor_digest=q.pause_anchor_digest AND src.org_id=q.org_id AND src.session_id=q.session_id
 AND src.request_id=q.id AND src.context_scope_digest=q.context_scope_digest
 AND src.workspace_generation=q.pause_workspace_generation
 AND ((src.kind='pause_metadata_seal' AND a.id=q.pause_seal_receipt_id
       AND src.actor_thread_id=q.thread_id AND src.actor_thread_epoch=q.admission_thread_epoch AND src.actor_runtime_id=q.producer_runtime_id)
   OR (src.kind IN ('sibling_checkpoint','sibling_diff') AND src.actor_thread_id<>q.thread_id AND src.actor_runtime_id IS NOT NULL)
   OR (src.kind IN ('metadata_revision','accept_split','publication_promotion','preview_effect') AND src.actor_thread_id IS NULL))`

// Canonical M0 candidate: immutable A/provider pause proof plus an exact current
// compatibility certificate. A sibling's current snapshot/producer may differ.
// Neither numeric revision ordering nor current snapshot equality to A is proof.
const sessionLifecycleReadinessPolicyProbe = `
WITH evidence AS (
 SELECT q.*,r.status AS runtime_status,r.writer_version,r.lease_expires_at,
  r.can_publish_checkpoint,j.status AS job_status,j.lock_token AS current_job_token,
  d.job_lock_token AS dispatch_job_token,s.workspace_generation AS current_workspace_generation,
  s.admission_epoch AS current_session_epoch,t.admission_epoch AS current_thread_epoch,
  (q.provider_request_id IS NOT NULL AND q.provider_request_id<>''
   AND q.legacy_request=(q.request_created_at<s.lifecycle_activated_at)
   AND r.id IS NOT NULL AND d.id IS NOT NULL AND p.id IS NOT NULL AND t.id IS NOT NULL
   AND s.admission_epoch=q.admission_session_epoch AND t.admission_epoch=q.admission_thread_epoch
   AND d.session_epoch=q.admission_session_epoch AND d.thread_epoch=q.admission_thread_epoch
   AND r.session_epoch=q.admission_session_epoch AND r.thread_epoch=q.admission_thread_epoch
   AND p.turn_number=q.turn_number AND p.kind='human_input') AS exact_producer,
  COALESCE(p.status='completed' AND r.status IN ('closed','failed') AND r.drain_receipt_id IS NOT NULL
   AND q.pause_workspace_revision>=q.pause_finalized_revision AND q.pause_proof_sealed_at>=q.pause_finalized_at
   AND q.pause_snapshot_captured_revision<=q.pause_workspace_revision
   AND q.pause_checkpoint_published_at<=q.pause_proof_sealed_at
   AND a.status='applied' AND ` + sessionLifecycleEffectScopeProbe + `
   AND a.from_revision=q.pause_snapshot_captured_revision AND a.to_revision=q.pause_workspace_revision
   AND a.from_snapshot_key=q.pause_snapshot_key AND a.to_snapshot_key=q.pause_snapshot_key
   AND q.pause_anchor_digest=encode(sha256(convert_to(q.readiness_proof::text,'UTF8')),'hex')
   AND q.readiness_proof=jsonb_build_object(
    'proof_policy_version',1,'request_id',q.id,'provider_request_id',q.provider_request_id,
    'request_kind',q.request_kind,'org_id',q.org_id,'session_id',q.session_id,'thread_id',q.thread_id,
    'session_epoch',q.admission_session_epoch,'thread_epoch',q.admission_thread_epoch,'turn_number',q.turn_number,
    'producer_job_id',q.producer_job_id,'producer_attempt_token',q.producer_attempt_token,
    'producer_runtime_id',r.id,'runtime_lease_token',r.lease_token,'phase_id',p.id,
    'snapshot_key',q.pause_snapshot_key,'checkpoint_runtime_id',r.id,'checkpoint_attempt_token',d.job_lock_token,
    'workspace_generation',q.pause_workspace_generation,'workspace_revision',q.pause_workspace_revision,
    'checkpoint_workspace_revision',q.pause_workspace_revision,'producer_pause_finalized_revision',q.pause_finalized_revision,
    'snapshot_captured_revision',q.pause_snapshot_captured_revision,'context_scope_digest',q.context_scope_digest,
    'provider_type',q.provider_type,'provider_build',q.provider_build,'conversation_id',q.conversation_id,'workdir_scope',q.workdir_scope,
    'producer_drained',true,'drain_receipt_id',r.drain_receipt_id),false) AS exact_anchor,
  COALESCE(` + sessionLifecycleNativeIdentityProbe + ` AND n.availability='available',false) AS exact_native_context,
  ((q.conversation_id IS NOT NULL AND n.request_id IS NULL AND NOT COALESCE(r.status='running' AND q.readiness_proof IS NULL AND q.readiness_published_at IS NULL,false)) OR n.availability IN ('expired','destroyed','missing')
	   OR c.state='unavailable' OR NOT COALESCE(s.physical_workspace_availability='available' OR s.snapshot_availability='available',false)) AS context_unavailable,
  c.state AS certificate_state,
  EXISTS(SELECT 1 FROM probe_workspace_operation_receipts op
   WHERE op.id=c.last_authority_receipt_id AND c.last_authority_kind=op.kind AND op.status='applied'
    AND op.org_id=q.org_id AND op.session_id=q.session_id
    AND c.anchor_digest=q.pause_anchor_digest AND c.context_scope_digest=q.context_scope_digest
    AND c.workspace_generation=s.workspace_generation
    AND c.workspace_revision=s.workspace_revision AND c.snapshot_key=s.snapshot_key
    AND c.workspace_state=` + sessionLifecycleWorkspaceStateProbe + ` AND op.to_state=c.workspace_state) AS current_watermark
 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id
 LEFT JOIN session_threads t ON t.org_id=q.org_id AND t.session_id=q.session_id AND t.id=q.thread_id
 LEFT JOIN thread_runtimes r ON r.org_id=q.org_id AND r.session_id=q.session_id AND r.thread_id=q.thread_id AND r.id=q.producer_runtime_id
 LEFT JOIN session_input_dispatches d ON d.org_id=q.org_id AND d.session_id=q.session_id AND d.thread_id=q.thread_id
  AND d.runtime_id=r.id AND d.job_id=q.producer_job_id AND d.job_lock_token=q.producer_attempt_token AND d.runtime_lease_token=r.lease_token
 LEFT JOIN session_activity_phases p ON p.org_id=q.org_id AND p.session_id=q.session_id AND p.thread_id=q.thread_id AND p.id=q.activity_phase_id AND p.runtime_id=r.id
 LEFT JOIN jobs j ON j.org_id=q.org_id AND j.session_id=q.session_id AND j.id=q.producer_job_id
 LEFT JOIN session_input_workspace_compatibility c ON c.org_id=q.org_id AND c.request_id=q.id
 LEFT JOIN probe_native_context_availability n ON n.org_id=q.org_id AND n.request_id=q.id
 LEFT JOIN probe_workspace_effect_receipts a ON a.id=q.pause_seal_receipt_id
 LEFT JOIN probe_workspace_effect_sources src ON src.id=a.source_id
 WHERE q.org_id=$1 AND q.id=$2 AND q.status='pending'
), reasoned AS (
 SELECT *,CASE
  WHEN readiness_state='recovery_required' THEN 'recovery_required'
  WHEN current_session_epoch>admission_session_epoch OR current_thread_epoch>admission_thread_epoch THEN 'successor_epoch'
  WHEN current_workspace_generation<>pause_workspace_generation AND NOT current_watermark THEN 'workspace_generation_changed'
  WHEN legacy_request AND readiness_deadline_at <= $3::timestamptz THEN 'legacy_wait_expired'
  WHEN enforcement_version=1 AND proof_policy_version=1 AND context_unavailable THEN 'input_context_unavailable'
  WHEN enforcement_version=1 AND proof_policy_version=1 AND exact_producer AND exact_anchor
   AND exact_native_context AND current_watermark AND certificate_state='certified' THEN 'exact_checkpoint'
  WHEN enforcement_version=1 AND proof_policy_version=1 AND exact_producer
   AND runtime_status='running' AND writer_version>=1 AND lease_expires_at>$3::timestamptz
   AND can_publish_checkpoint AND job_status='running' AND current_job_token=dispatch_job_token
   THEN CASE WHEN legacy_request THEN 'legacy_checkpoint_wait' ELSE 'live_producer_checkpoint_pending' END
  WHEN enforcement_version=1 AND proof_policy_version=1 AND exact_producer AND exact_anchor
   AND current_watermark AND certificate_state='pending' THEN 'workspace_compatibility_pending'
  WHEN pause_workspace_revision<pause_finalized_revision OR pause_proof_sealed_at<pause_finalized_at THEN 'checkpoint_before_final_pause'
  WHEN exact_producer AND exact_anchor THEN 'workspace_lineage_unprovable'
  ELSE 'unprovable' END AS reason FROM evidence
)
SELECT CASE reason WHEN 'exact_checkpoint' THEN 'ready' WHEN 'legacy_checkpoint_wait' THEN 'waiting'
 WHEN 'live_producer_checkpoint_pending' THEN 'checkpoint_pending' WHEN 'workspace_compatibility_pending' THEN 'context_pending'
 WHEN 'input_context_unavailable' THEN 'context_unavailable' ELSE 'recovery_required' END,
 CASE WHEN reason='legacy_checkpoint_wait' THEN COALESCE(readiness_deadline_at,$3::timestamptz+interval '5 minutes') END,reason FROM reasoned`

// Workspace authorization is independent of request eligibility. Every pending
// request gets the latest descriptor/receipt watermark, even when its certificate
// is pending, corrupt or missing. A stale certificate becomes unprovable; it must
// neither veto B's authorized mutation nor gain invented lineage. Pending is
// sticky until selected adapter revalidation, not healed by a later B operation.
const sessionLifecycleAdvanceCompatibilityProbe = `
WITH candidate AS MATERIALIZED (
 SELECT s.id AS session_id,s.admission_epoch,op.id AS receipt_id,op.from_state,op.to_state,op.kind
 FROM sessions s JOIN probe_workspace_operation_receipts op ON op.org_id=s.org_id AND op.session_id=s.id
 WHERE s.org_id=$1 AND s.id=$2 AND op.id=$3 AND op.status='authorized'
  AND op.from_state=` + sessionLifecycleWorkspaceStateProbe + `
  AND op.to_state->>'effect_receipt_id'=op.id::text
  AND ((op.to_state->>'generation')::bigint=s.workspace_generation OR op.kind='generation_change')
  AND ((op.kind IN ('sibling_checkpoint','sibling_diff') AND EXISTS(SELECT 1 FROM session_threads actor
    WHERE actor.org_id=s.org_id AND actor.session_id=s.id AND actor.id=op.actor_thread_id AND actor.admission_epoch=op.actor_thread_epoch))
   OR (op.kind IN ('metadata_revision','accept_split','publication_promotion','preview_effect','key_only_checkpoint','shared_snapshot_expiry','workspace_expiry','scope_retarget','restore_replacement','context_replacement','generation_change') AND op.actor_thread_id IS NULL))
 FOR UPDATE OF s
), requests AS MATERIALIZED (
 SELECT q.*,c.state AS old_state,c.anchor_digest AS old_anchor,c.context_scope_digest AS old_scope,
  c.workspace_generation AS old_generation,c.workspace_revision AS old_revision,
  c.snapshot_key AS old_key,c.workspace_state AS old_descriptor,c.last_authority_receipt_id AS old_receipt,c.last_authority_kind AS old_kind,
  t.admission_epoch AS current_thread_epoch,a.*,effect.effect_kind,
  COALESCE(effect.anchor_digest=q.pause_anchor_digest AND effect.context_scope_digest=q.context_scope_digest,false) AS exact_effect,
  n.availability AS native_availability,r.status AS producer_state,
  EXISTS(SELECT 1 FROM probe_workspace_operation_receipts prior WHERE prior.org_id=q.org_id AND prior.session_id=q.session_id
   AND prior.id=c.last_authority_receipt_id AND prior.kind=c.last_authority_kind AND prior.status='applied' AND prior.to_state=c.workspace_state) AS prior_valid
 FROM candidate a JOIN session_human_input_requests q ON q.org_id=$1 AND q.session_id=a.session_id AND q.status='pending'
 LEFT JOIN session_threads t ON t.org_id=q.org_id AND t.session_id=q.session_id AND t.id=q.thread_id
 LEFT JOIN session_input_workspace_compatibility c ON c.org_id=q.org_id AND c.request_id=q.id
 LEFT JOIN probe_request_context_effects effect ON effect.org_id=q.org_id AND effect.request_id=q.id AND effect.operation_id=a.receipt_id
 LEFT JOIN probe_native_context_availability n ON n.org_id=q.org_id AND n.request_id=q.id
 LEFT JOIN thread_runtimes r ON r.org_id=q.org_id AND r.session_id=q.session_id AND r.thread_id=q.thread_id AND r.id=q.producer_runtime_id
 FOR UPDATE OF q
), moved AS (
 UPDATE sessions s SET workspace_generation=(a.to_state->>'generation')::bigint,workspace_revision=(a.to_state->>'revision')::bigint,snapshot_key=a.to_state->>'snapshot_key',
  checkpoint_runtime_id=(a.to_state->>'checkpoint_runtime_id')::uuid,
  checkpoint_attempt_token=(a.to_state->>'checkpoint_attempt_token')::uuid,
  checkpoint_phase_id=(a.to_state->>'checkpoint_phase_id')::uuid,
  checkpoint_workspace_revision=(a.to_state->>'checkpoint_revision')::bigint,
  checkpoint_published_at=to_timestamp((a.to_state->>'checkpoint_published_at')::double precision),workspace_effect_receipt_id=a.receipt_id,
  snapshot_availability_version=(a.to_state->>'snapshot_availability_version')::bigint,
  snapshot_availability=a.to_state->>'snapshot_availability',physical_workspace_availability=a.to_state->>'physical_workspace_availability'
 FROM candidate a WHERE s.org_id=$1 AND s.id=a.session_id RETURNING a.*
), watermarked AS (
 INSERT INTO session_input_workspace_compatibility AS existing(org_id,request_id,anchor_digest,context_scope_digest,state,
  workspace_generation,workspace_revision,snapshot_key,workspace_state,last_authority_receipt_id,last_authority_kind)
 SELECT q.org_id,q.id,COALESCE(q.old_anchor,q.pause_anchor_digest),COALESCE(q.old_scope,q.context_scope_digest),
 CASE
  WHEN q.old_state='unavailable' OR (q.conversation_id IS NOT NULL AND q.native_availability IS NULL
   AND NOT COALESCE(q.producer_state='running' AND q.readiness_proof IS NULL AND q.readiness_published_at IS NULL,false))
   OR q.native_availability IN ('expired','destroyed','missing')
   OR NOT COALESCE(a.to_state->>'physical_workspace_availability'='available' OR a.to_state->>'snapshot_availability'='available',false) THEN 'unavailable'
  WHEN q.old_state IS NULL OR q.old_state NOT IN ('certified','pending','unavailable') OR q.old_anchor IS DISTINCT FROM q.pause_anchor_digest
   OR q.old_scope IS DISTINCT FROM q.context_scope_digest
   OR q.pause_anchor_digest IS DISTINCT FROM encode(sha256(convert_to(q.readiness_proof::text,'UTF8')),'hex')
   OR q.admission_session_epoch<>q.admission_epoch OR q.admission_thread_epoch IS DISTINCT FROM q.current_thread_epoch
   OR q.old_generation IS DISTINCT FROM (q.from_state->>'generation')::bigint
   OR q.old_revision IS DISTINCT FROM (q.from_state->>'revision')::bigint OR q.old_key IS DISTINCT FROM q.from_state->>'snapshot_key'
   OR q.old_descriptor IS DISTINCT FROM q.from_state OR NOT q.prior_valid THEN 'unprovable'
  WHEN q.effect_kind='same_lane_successor' OR (q.effect_kind IS NOT NULL AND NOT q.exact_effect) THEN 'unprovable'
  WHEN q.old_state='pending' OR q.effect_kind='verification_pending'
   OR a.kind IN ('scope_retarget','restore_replacement','context_replacement','generation_change') THEN 'pending'
  ELSE 'certified' END,
 (a.to_state->>'generation')::bigint,(a.to_state->>'revision')::bigint,a.to_state->>'snapshot_key',a.to_state,a.receipt_id,a.kind
 FROM requests q JOIN moved a ON a.receipt_id=q.receipt_id
 ON CONFLICT(request_id) DO UPDATE SET state=EXCLUDED.state,workspace_generation=EXCLUDED.workspace_generation,
  workspace_revision=EXCLUDED.workspace_revision,snapshot_key=EXCLUDED.snapshot_key,workspace_state=EXCLUDED.workspace_state,
  last_authority_receipt_id=EXCLUDED.last_authority_receipt_id,last_authority_kind=EXCLUDED.last_authority_kind
 WHERE existing.org_id=$1 RETURNING request_id
), receipted AS (
 UPDATE probe_workspace_operation_receipts r SET status='applied'
 FROM moved a WHERE r.org_id=$1 AND r.id=a.receipt_id RETURNING r.id
) SELECT count(*) FROM moved`

func advanceSessionLifecycleCompatibilityProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, requestID uuid.UUID, operation string, deferCompatibility bool) int {
	t.Helper()
	receiptID := uuid.New()
	kind := operation
	if operation == "same_lane_checkpoint" || operation == "foreign_receipt" {
		kind = "sibling_checkpoint"
	} else if operation == "foreign_context" {
		kind = "metadata_revision"
	}
	receiptOrg := orgID
	if operation == "foreign_receipt" {
		receiptOrg = uuid.New()
	}
	var sessionID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT session_id FROM session_human_input_requests WHERE org_id=$1 AND id=$2`, orgID, requestID).Scan(&sessionID), "resolve the workspace independently of request certificate eligibility")
	_, err := pool.Exec(ctx, `INSERT INTO probe_workspace_operation_receipts
	 SELECT $3,$4,s.id,$5,
	  CASE WHEN $5 IN ('sibling_checkpoint','sibling_diff') THEN actor.id END,
	  CASE WHEN $5 IN ('sibling_checkpoint','sibling_diff') THEN actor.admission_epoch END,
	  `+sessionLifecycleWorkspaceStateProbe+`,
	  `+sessionLifecycleWorkspaceStateProbe+`||jsonb_build_object('revision',s.workspace_revision+CASE WHEN $5 IN ('key_only_checkpoint','shared_snapshot_expiry','workspace_expiry') THEN 0 ELSE 1 END,'effect_receipt_id',$3::uuid)
	  ||CASE WHEN $5 IN ('sibling_checkpoint','publication_promotion','key_only_checkpoint') THEN jsonb_build_object(
	    'snapshot_key','checkpoint-'||$3::text,'snapshot_availability_version',s.snapshot_availability_version+1,'snapshot_availability','available') ELSE '{}'::jsonb END
	  ||CASE WHEN $5='sibling_checkpoint' THEN jsonb_build_object('checkpoint_runtime_id',actor.id,'checkpoint_attempt_token',$7::uuid,
	   'checkpoint_phase_id',$8::uuid,'checkpoint_revision',s.workspace_revision+1,'checkpoint_published_at',extract(epoch FROM timestamptz '2026-10-18T12:00:00Z'))
	   WHEN $5='publication_promotion' THEN jsonb_build_object('checkpoint_runtime_id',NULL,'checkpoint_attempt_token',NULL,'checkpoint_phase_id',NULL,
	   'checkpoint_revision',s.workspace_revision+1,'checkpoint_published_at',extract(epoch FROM timestamptz '2026-10-18T12:00:00Z'))
	   WHEN $5='shared_snapshot_expiry' THEN jsonb_build_object('snapshot_availability','expired','snapshot_availability_version',s.snapshot_availability_version+1)
	   WHEN $5='workspace_expiry' THEN jsonb_build_object('snapshot_availability','expired','snapshot_availability_version',s.snapshot_availability_version+1,'physical_workspace_availability','missing')
	   WHEN $5='generation_change' THEN jsonb_build_object('generation',s.workspace_generation+1) ELSE '{}'::jsonb END,'authorized'
	 FROM sessions s JOIN LATERAL(SELECT id,admission_epoch FROM session_threads
	  WHERE org_id=s.org_id AND session_id=s.id AND (id=(SELECT thread_id FROM session_human_input_requests WHERE org_id=$1 AND id=$9))=($6='same_lane_checkpoint') ORDER BY id LIMIT 1) actor ON true
	 WHERE s.org_id=$1 AND s.id=$2`, orgID, sessionID, receiptID, receiptOrg, kind, operation, uuid.New(), uuid.New(), requestID)
	require.NoError(t, err, "record exact old/new workspace descriptors under independent synthetic operation authority, not a request compatibility boolean")
	_, err = pool.Exec(ctx, `INSERT INTO probe_request_context_effects
	 SELECT $3,q.org_id,q.id,q.pause_anchor_digest,CASE WHEN $4='foreign_context' AND q.id=$2 THEN 'foreign-context' ELSE q.context_scope_digest END,
	 CASE WHEN $4='same_lane_checkpoint' THEN 'same_lane_successor'
	  WHEN $5 AND q.id=$2 THEN 'verification_pending' ELSE 'foreign_context' END
	 FROM session_human_input_requests q WHERE q.org_id=$1 AND q.session_id=$6 AND q.status='pending'
	 AND ($4 IN ('same_lane_checkpoint','foreign_context') OR ($5 AND q.id=$2))`, orgID, requestID, receiptID, operation, deferCompatibility, sessionID)
	require.NoError(t, err, "record only named request-specific context-affecting exceptions; trusted sibling/domain progress needs no per-request manifest")
	var advanced int
	require.NoError(t, pool.QueryRow(ctx, sessionLifecycleAdvanceCompatibilityProbe, orgID, sessionID, receiptID).Scan(&advanced), "the authorized workspace CAS must install independently and watermark every pending request without healing unproved lineage")
	if advanced == 1 {
		var certificateState, installedState json.RawMessage
		var certificatePresent bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT c.request_id IS NOT NULL,COALESCE(c.workspace_state,'null'::jsonb),`+sessionLifecycleWorkspaceStateProbe+`
		 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id
		 LEFT JOIN session_input_workspace_compatibility c ON c.org_id=q.org_id AND c.request_id=q.id WHERE q.org_id=$1 AND q.id=$2`, orgID, requestID).Scan(&certificatePresent, &certificateState, &installedState), "inspect owned certificate and installed workspace without crossing a forged foreign-org row")
		if certificatePresent {
			require.Equal(t, installedState, certificateState, "every owned certificate state must watermark the exact installed descriptor, not merely a larger revision")
		}
	}
	return advanced
}

// Synthetic selected VerifyResumable evidence certifies identity/availability on
// the exact current target, not unchanged bytes or a new security boundary.
const sessionLifecycleRevalidateCompatibilityProbe = `
WITH candidate AS MATERIALIZED (
 SELECT q.org_id,q.id,v.id AS verification_id FROM session_human_input_requests q
 JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id
 JOIN session_threads t ON t.org_id=q.org_id AND t.session_id=q.session_id AND t.id=q.thread_id
 JOIN session_input_workspace_compatibility c ON c.org_id=q.org_id AND c.request_id=q.id
 JOIN probe_native_context_availability n ON n.org_id=q.org_id AND n.request_id=q.id
 JOIN probe_input_context_revalidations v ON v.org_id=q.org_id AND v.request_id=q.id
 JOIN probe_workspace_operation_receipts op ON op.org_id=q.org_id AND op.session_id=q.session_id AND op.id=c.last_authority_receipt_id
 WHERE q.org_id=$1 AND q.id=$2 AND v.id=$3 AND v.status='verified' AND q.status='pending' AND c.state='pending'
  AND q.admission_session_epoch=s.admission_epoch AND q.admission_thread_epoch=t.admission_epoch
  AND q.pause_anchor_digest=encode(sha256(convert_to(q.readiness_proof::text,'UTF8')),'hex')
  AND c.anchor_digest=q.pause_anchor_digest AND c.context_scope_digest=q.context_scope_digest
  AND c.workspace_generation=s.workspace_generation
  AND c.workspace_revision=s.workspace_revision AND c.snapshot_key=s.snapshot_key AND c.workspace_state=` + sessionLifecycleWorkspaceStateProbe + `
  AND op.status='applied' AND op.kind=c.last_authority_kind AND op.to_state=c.workspace_state
  AND v.workspace_state=c.workspace_state AND v.anchor_digest=q.pause_anchor_digest
  AND v.provider_type=q.provider_type AND v.provider_build=q.provider_build AND v.conversation_id=q.conversation_id AND v.workdir_scope=q.workdir_scope
  AND v.availability_version=n.availability_version AND v.expires_at>$4::timestamptz
  AND ` + sessionLifecycleNativeIdentityProbe + ` AND n.availability='available'
 FOR UPDATE OF s,q,c,n
), certified AS (
 UPDATE session_input_workspace_compatibility c SET state='certified' FROM candidate a
 WHERE c.org_id=a.org_id AND c.request_id=a.id RETURNING a.verification_id
), consumed AS (
 UPDATE probe_input_context_revalidations v SET status='consumed' FROM certified a WHERE v.id=a.verification_id RETURNING v.id
) SELECT count(*) FROM certified`

// RetentionAuthority installs the exact native-context tombstone before any
// hypothetical delete. No filesystem deletion occurs; identity/version CAS
// fences stale expiry work without claiming immutable context artifact bytes.
const sessionLifecycleExpireContextProbe = `
WITH candidate AS MATERIALIZED (
 SELECT n.org_id,n.request_id,e.id AS receipt_id,e.availability FROM probe_native_context_availability n
 JOIN probe_context_expiry_receipts e ON e.org_id=n.org_id AND e.request_id=n.request_id
 WHERE e.org_id=$1 AND e.id=$2 AND e.status='authorized' AND e.authority_kind='RetentionAuthority' AND n.availability='available'
  AND e.provider_type=n.provider_type AND e.provider_build=n.provider_build AND e.conversation_id=n.conversation_id
  AND e.workdir_scope=n.workdir_scope AND e.workspace_generation=n.workspace_generation AND e.availability_version=n.availability_version
  AND e.availability IN ('expired','destroyed','missing') FOR UPDATE OF n
), unavailable AS (
 UPDATE probe_native_context_availability n SET availability=a.availability,availability_version=n.availability_version+1
 FROM candidate a WHERE n.org_id=a.org_id AND n.request_id=a.request_id RETURNING a.*
), watermarked AS (
 UPDATE session_input_workspace_compatibility c SET state='unavailable' FROM unavailable a
 WHERE c.org_id=a.org_id AND c.request_id=a.request_id RETURNING c.request_id
), consumed AS (
 UPDATE probe_context_expiry_receipts e SET status='applied' FROM unavailable a WHERE e.org_id=a.org_id AND e.id=a.receipt_id RETURNING e.id
) SELECT count(*) FROM unavailable`

func revalidateSessionLifecycleCompatibilityProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, requestID uuid.UUID, now time.Time, corrupt bool) int {
	t.Helper()
	receiptID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO probe_input_context_revalidations
	 SELECT $3,q.org_id,q.id,q.pause_anchor_digest,q.provider_type,q.provider_build,
	 CASE WHEN $4 THEN 'foreign-conversation' ELSE q.conversation_id END,q.workdir_scope,n.availability_version,`+sessionLifecycleWorkspaceStateProbe+`,$5,'verified'
	 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id
	 JOIN probe_native_context_availability n ON n.org_id=q.org_id AND n.request_id=q.id WHERE q.org_id=$1 AND q.id=$2`, orgID, requestID, receiptID, corrupt, now.Add(time.Minute))
	require.NoError(t, err, "materialize selected native identity/current-target verification without claiming real provider filesystem I/O")
	var changed int
	require.NoError(t, pool.QueryRow(ctx, sessionLifecycleRevalidateCompatibilityProbe, orgID, requestID, receiptID, now).Scan(&changed), "selected revalidation must consume exact current native identity evidence, not adopt another conversation or repair corrupt lineage")
	return changed
}

func expireSessionLifecycleContextProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, requestID uuid.UUID, availability string, stale, replay bool) []int {
	t.Helper()
	receiptID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO probe_context_expiry_receipts
	 SELECT $3,n.org_id,n.request_id,n.provider_type,n.provider_build,CASE WHEN $5 THEN 'old-conversation' ELSE n.conversation_id END,
	 n.workdir_scope,n.workspace_generation,n.availability_version,$4,'RetentionAuthority','authorized'
	 FROM probe_native_context_availability n WHERE n.org_id=$1 AND n.request_id=$2`, orgID, requestID, receiptID, availability, stale)
	require.NoError(t, err, "record typed RetentionAuthority for exact native identity and availability version before physical deletion")
	results := []int{}
	count := 1
	if replay {
		count = 2
	}
	for range count {
		var changed int
		require.NoError(t, pool.QueryRow(ctx, sessionLifecycleExpireContextProbe, orgID, receiptID).Scan(&changed), "expiry must mark availability and the affected request unavailable atomically, rejecting stale refs and replay")
		results = append(results, changed)
	}
	return results
}

func cloneSessionLifecyclePendingRequestProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, requestID uuid.UUID) uuid.UUID {
	t.Helper()
	otherID, sealID, sourceID := uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO session_human_input_requests SELECT (jsonb_populate_record(NULL::session_human_input_requests,
	 to_jsonb(q)||jsonb_build_object('id',$3::uuid,'provider_request_id','provider-input-other',
	 'conversation_id','conversation-other','workdir_scope','trusted-workdir-other','context_scope_digest','trusted-scope-other','pause_seal_receipt_id',$4::uuid))).*
	 FROM session_human_input_requests q WHERE q.org_id=$1 AND q.id=$2`, orgID, requestID, otherID, sealID)
	require.NoError(t, err, "create a second independent pending request in the same quiescent trusted session without copying another native conversation identity")
	_, err = pool.Exec(ctx, `UPDATE session_human_input_requests q SET readiness_proof=readiness_proof||jsonb_build_object(
	 'request_id',q.id,'provider_request_id',q.provider_request_id,'context_scope_digest',q.context_scope_digest,
	 'conversation_id',q.conversation_id,'workdir_scope',q.workdir_scope) WHERE q.org_id=$1 AND q.id=$2`, orgID, otherID)
	require.NoError(t, err, "bind the second proof to its exact request/provider/native identity rather than borrowing the first request's anchor")
	batch := &pgx.Batch{}
	batch.Queue(`UPDATE session_human_input_requests SET pause_anchor_digest=encode(sha256(convert_to(readiness_proof::text,'UTF8')),'hex') WHERE org_id=$1 AND id=$2`, orgID, otherID)
	batch.Queue(`INSERT INTO probe_native_context_availability SELECT org_id,id,session_id,thread_id,provider_type,provider_build,conversation_id,workdir_scope,pause_workspace_generation,'available',1,$3 FROM session_human_input_requests WHERE org_id=$1 AND id=$2`, orgID, otherID, uuid.New())
	batch.Queue(`INSERT INTO probe_workspace_effect_sources SELECT $3,org_id,session_id,id,'pause_metadata_seal',thread_id,admission_thread_epoch,producer_runtime_id,context_scope_digest,pause_workspace_generation FROM session_human_input_requests WHERE org_id=$1 AND id=$2`, orgID, otherID, sourceID)
	batch.Queue(`INSERT INTO probe_workspace_effect_receipts SELECT $3,$4,q.org_id,q.session_id,q.id,q.pause_anchor_digest,11,13,q.pause_snapshot_key,q.pause_snapshot_key,'{}'::jsonb,`+sessionLifecycleWorkspaceStateProbe+`,'applied'
	 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id WHERE q.org_id=$1 AND q.id=$2`, orgID, otherID, sealID, sourceID)
	batch.Queue(`INSERT INTO session_input_workspace_compatibility SELECT q.org_id,q.id,q.pause_anchor_digest,q.context_scope_digest,'certified',s.workspace_generation,s.workspace_revision,s.snapshot_key,`+sessionLifecycleWorkspaceStateProbe+`,s.workspace_effect_receipt_id,'pause_metadata_seal'
	 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id WHERE q.org_id=$1 AND q.id=$2`, orgID, otherID)
	require.NoError(t, pool.SendBatch(ctx, batch).Close(), "seed separate second-request anchor, native identity and certificate without changing the shared workspace or first request")
	return otherID
}

func TestSessionLifecycleCompatibilityStateMachineProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	type operation struct {
		kind              string
		deferVerification bool
	}
	type outcome struct {
		certificate, disposition, reason string
	}
	ready := outcome{"certified", "ready", "exact_checkpoint"}
	pending := outcome{"pending", "context_pending", "workspace_compatibility_pending"}
	unprovable := outcome{"unprovable", "recovery_required", "workspace_lineage_unprovable"}
	unavailable := outcome{"unavailable", "context_unavailable", "input_context_unavailable"}
	tests := []struct {
		name, change, expiry, afterOperations                             string
		operations                                                        []operation
		other, revalidate, corruptVerification, staleExpiry, replayExpiry bool
		foreignCertificate                                                bool
		expected                                                          []outcome
		expectedRevision                                                  int64
		expectedRevalidations, expectedExpiry                             []int
	}{
		{name: "two deferred checkpoint and diff steps retain pending", operations: []operation{{"sibling_checkpoint", true}, {"sibling_diff", true}}, expected: []outcome{pending}, expectedRevision: 15},
		{name: "later preserving operation cannot silently revalidate pending", operations: []operation{{"sibling_checkpoint", true}, {"sibling_diff", false}, {"metadata_revision", false}}, expected: []outcome{pending}, expectedRevision: 16},
		{name: "selected exact current revalidation certifies after N steps", operations: []operation{{"sibling_checkpoint", true}, {"sibling_diff", true}, {"preview_effect", false}}, revalidate: true, expectedRevalidations: []int{1}, expected: []outcome{ready}, expectedRevision: 16},
		{name: "foreign native conversation cannot revalidate after N steps", operations: []operation{{"sibling_checkpoint", true}, {"sibling_diff", true}}, revalidate: true, corruptVerification: true, expectedRevalidations: []int{0}, expected: []outcome{pending}, expectedRevision: 15},
		{name: "pending A and certified other request advance independently", other: true, operations: []operation{{"sibling_checkpoint", true}, {"sibling_diff", true}}, expected: []outcome{pending, ready}, expectedRevision: 15},
		{name: "stale A certificate never vetoes B or other request", other: true, change: `UPDATE session_input_workspace_compatibility SET workspace_revision=12 WHERE request_id=$1`, operations: []operation{{"sibling_checkpoint", false}, {"sibling_diff", false}}, expected: []outcome{unprovable, ready}, expectedRevision: 15},
		{name: "corrupt A anchor never vetoes B or other request", other: true, change: `UPDATE session_input_workspace_compatibility SET anchor_digest='corrupt' WHERE request_id=$1`, operations: []operation{{"metadata_revision", false}}, expected: []outcome{unprovable, ready}, expectedRevision: 14},
		{name: "missing A certificate is watermarked unprovable without veto", other: true, change: `DELETE FROM session_input_workspace_compatibility WHERE request_id=$1`, operations: []operation{{"metadata_revision", false}}, expected: []outcome{unprovable, ready}, expectedRevision: 14},
		{name: "foreign org certificate collision cannot mutate foreign row or veto B", other: true, foreignCertificate: true, change: `UPDATE session_input_workspace_compatibility SET org_id=gen_random_uuid() WHERE request_id=$1`, operations: []operation{{"metadata_revision", false}}, expected: []outcome{{"missing", "recovery_required", "workspace_lineage_unprovable"}, ready}, expectedRevision: 14},
		{name: "foreign A effect evidence never vetoes B or other request", other: true, operations: []operation{{"foreign_context", false}}, expected: []outcome{unprovable, ready}, expectedRevision: 14},
		{name: "unknown A certificate state cannot acquire invented certification", other: true, change: `UPDATE session_input_workspace_compatibility SET state='invalid' WHERE request_id=$1`, operations: []operation{{"metadata_revision", false}}, expected: []outcome{unprovable, ready}, expectedRevision: 14},
		{name: "key only checkpoint change uses exact descriptor not revision increment", operations: []operation{{"key_only_checkpoint", false}}, expected: []outcome{ready}, expectedRevision: 13},
		{name: "typed native expiry same key revision generation is unavailable", expiry: "expired", expectedExpiry: []int{1}, expected: []outcome{unavailable}, expectedRevision: 13},
		{name: "typed native destruction is unavailable before any answer", expiry: "destroyed", expectedExpiry: []int{1}, expected: []outcome{unavailable}, expectedRevision: 13},
		{name: "typed missing native context is unavailable before any answer", expiry: "missing", expectedExpiry: []int{1}, expected: []outcome{unavailable}, expectedRevision: 13},
		{name: "replayed expiry is idempotent", expiry: "expired", replayExpiry: true, expectedExpiry: []int{1, 0}, expected: []outcome{unavailable}, expectedRevision: 13},
		{name: "old conversation expiry cannot remove current native context", expiry: "expired", staleExpiry: true, expectedExpiry: []int{0}, expected: []outcome{ready}, expectedRevision: 13},
		{name: "A expiry does not affect other request or B workspace", other: true, expiry: "expired", operations: []operation{{"sibling_checkpoint", false}, {"sibling_diff", false}}, expectedExpiry: []int{1}, expected: []outcome{unavailable, ready}, expectedRevision: 15},
		{name: "ordinary checkpoint expiry retains explicitly usable live native context", operations: []operation{{"sibling_checkpoint", false}, {"shared_snapshot_expiry", false}}, expected: []outcome{ready}, expectedRevision: 14},
		{name: "authorized generation change requires selected current target verification", operations: []operation{{"generation_change", false}}, expected: []outcome{pending}, expectedRevision: 14},
		{name: "selected native verification may certify authorized new generation before answer", operations: []operation{{"generation_change", false}}, afterOperations: `UPDATE probe_native_context_availability SET workspace_generation=(SELECT workspace_generation FROM sessions)`, revalidate: true, expectedRevalidations: []int{1}, expected: []outcome{ready}, expectedRevision: 14},
		{name: "named restore replacement remains pending across later sibling metadata", operations: []operation{{"restore_replacement", false}, {"metadata_revision", false}}, expected: []outcome{pending}, expectedRevision: 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, requestID := newSessionLifecycleReadinessProbe(t, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			requestIDs := []uuid.UUID{requestID}
			if tt.other {
				requestIDs = append(requestIDs, cloneSessionLifecyclePendingRequestProbe(t, ctx, pool, orgID, requestID))
			}
			if tt.change != "" {
				_, err := pool.Exec(ctx, tt.change, requestID)
				require.NoError(t, err, "represent only A certificate damage while retaining independent sibling authority")
			}
			var foreignBefore json.RawMessage
			if tt.foreignCertificate {
				require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(c) FROM session_input_workspace_compatibility c WHERE c.org_id<>$1 AND c.request_id=$2`, orgID, requestID).Scan(&foreignBefore), "capture the forged foreign-org row before the owned workspace mutation")
			}
			if tt.expiry != "" {
				var beforeExpiry, afterExpiry json.RawMessage
				require.NoError(t, pool.QueryRow(ctx, `SELECT `+sessionLifecycleWorkspaceStateProbe+` FROM sessions s WHERE s.org_id=$1`, orgID).Scan(&beforeExpiry), "capture shared snapshot key/revision/generation before named native-context expiry")
				require.Equal(t, tt.expectedExpiry, expireSessionLifecycleContextProbe(t, ctx, pool, orgID, requestID, tt.expiry, tt.staleExpiry, tt.replayExpiry), "RetentionAuthority must fence exact native identity and record unavailable before deletion")
				require.NoError(t, pool.QueryRow(ctx, `SELECT `+sessionLifecycleWorkspaceStateProbe+` FROM sessions s WHERE s.org_id=$1`, orgID).Scan(&afterExpiry), "inspect unchanged shared workspace after named native-context availability transition")
				require.Equal(t, beforeExpiry, afterExpiry, "native context loss must become unavailable even with exactly unchanged shared key/revision/generation")
			}
			for _, op := range tt.operations {
				require.Equal(t, 1, advanceSessionLifecycleCompatibilityProbe(t, ctx, pool, orgID, requestID, op.kind, op.deferVerification), "authorized workspace progress must never depend on any individual certificate state")
			}
			var revalidations []int
			if tt.afterOperations != "" {
				_, err := pool.Exec(ctx, tt.afterOperations)
				require.NoError(t, err, "represent selected adapter native identity verification on the newly authorized current target")
			}
			if tt.revalidate {
				revalidations = append(revalidations, revalidateSessionLifecycleCompatibilityProbe(t, ctx, pool, orgID, requestID, now, tt.corruptVerification))
			}
			require.Equal(t, tt.expectedRevalidations, revalidations, "only explicit exact current adapter evidence may transition pending to certified")
			actual := []outcome{}
			for _, id := range requestIDs {
				var value outcome
				var deadline *time.Time
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleReadinessPolicyProbe, orgID, id, now).Scan(&value.disposition, &deadline, &value.reason), "classify each independent request from its own immutable proof and current state")
				require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE(c.state,'missing') FROM session_human_input_requests q LEFT JOIN session_input_workspace_compatibility c ON c.org_id=q.org_id AND c.request_id=q.id WHERE q.org_id=$1 AND q.id=$2`, orgID, id).Scan(&value.certificate), "inspect each owned durable typed certificate without borrowing a foreign-org collision")
				require.Nil(t, deadline, "context pending and unavailable must not invent a legacy checkpoint deadline")
				actual = append(actual, value)
				var ownedCertificate, watermarkCurrent bool
				require.NoError(t, pool.QueryRow(ctx, `SELECT c.request_id IS NOT NULL,COALESCE(c.workspace_state=`+sessionLifecycleWorkspaceStateProbe+` AND c.last_authority_receipt_id=s.workspace_effect_receipt_id,false)
				 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id LEFT JOIN session_input_workspace_compatibility c ON c.org_id=q.org_id AND c.request_id=q.id WHERE q.org_id=$1 AND q.id=$2`, orgID, id).Scan(&ownedCertificate, &watermarkCurrent), "inspect the complete owned descriptor and receipt watermark independently of certificate eligibility")
				expectOwned := value.certificate != "missing"
				require.Equal(t, []bool{expectOwned, expectOwned}, []bool{ownedCertificate, watermarkCurrent}, "every owned pending certificate must watermark current workspace; a forged foreign row must never be reused or overwritten")
			}
			require.Equal(t, tt.expected, actual, "pending, unavailable and unprovable request states must remain isolated from other requests and authorized workspace progress")
			if tt.foreignCertificate {
				var foreignAfter json.RawMessage
				require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(c) FROM session_input_workspace_compatibility c WHERE c.org_id<>$1 AND c.request_id=$2`, orgID, requestID).Scan(&foreignAfter), "inspect the forged foreign-org row after owned workspace and other-request progress")
				require.Equal(t, foreignBefore, foreignAfter, "tenant-scoped UPSERT must leave every byte of a colliding foreign-org certificate unchanged")
			}
			var revision int64
			var pendingRequests, answers, grants, jobs int
			require.NoError(t, pool.QueryRow(ctx, `SELECT s.workspace_revision,(SELECT count(*) FROM session_human_input_requests WHERE org_id=$1 AND status='pending'),(SELECT count(*) FROM session_human_input_requests WHERE org_id=$1 AND answer_text IS NOT NULL),(SELECT count(*) FROM session_capability_grants WHERE org_id=$1),(SELECT count(*) FROM jobs WHERE org_id=$1) FROM sessions s WHERE s.org_id=$1`, orgID).Scan(&revision, &pendingRequests, &answers, &grants, &jobs), "inspect workspace progress and preserve unanswered intent without grants or continuation jobs")
			require.Equal(t, tt.expectedRevision, revision, "descriptor CAS must allow key-only changes and must not manufacture revision progress for expiry")
			require.Equal(t, []int{len(requestIDs), 0, 0, 1}, []int{pendingRequests, answers, grants, jobs}, "synthetic state transitions must preserve all pending requests and existing producer job only")
		})
	}
}

// A preparation is not a provider/runtime permit. Selected VerifyResumable I/O
// occurs after preparation commits and outside the final authorization txn. This
// synthetic projection tests exact receipt consumption, NOT a provider adapter,
// native filesystem availability, target restore, actual Execute or delivery.
// Readiness admitted the answer earlier; later launch failure never revokes it.
// The synthetic receipt enum has exactly one positive no-entry value. Missing,
// legacy, malformed or unresolved provider-entry/delivery evidence is NOT washed
// away by a NULL FIRST marker or a replacement allocation/preparation token.
const sessionLifecyclePossibleAcceptanceProbe = `(prior.first_provider_launch_marker IS NOT NULL OR prior.execution_state IS DISTINCT FROM 'known_not_launched')`

const sessionLifecycleAuthorizePreparedStartProbe = `
WITH evidence AS MATERIALIZED (
 SELECT p.token,p.org_id,p.attempt_id,c.id AS check_id,a.execution_state,a.first_provider_launch_marker,
  EXISTS(SELECT 1 FROM probe_start_attempts prior WHERE prior.org_id=d.org_id AND prior.dispatch_id=d.id AND ` + sessionLifecyclePossibleAcceptanceProbe + `) AS prior_provider_entry,
  d.id AS dispatch_id,d.binding_token,d.job_id,d.control_version,p.job_lock_token,encode(sha256(convert_to(j.payload::text,'UTF8')),'hex') AS job_payload_digest,
  p.session_epoch,p.thread_epoch,p.workspace_generation,p.provider_type,p.provider_build,p.conversation_id,p.workdir_scope,
  c.disposition AS check_disposition,n.availability,
  COALESCE(p.status='preparing' AND p.expires_at>$4::timestamptz AND c.expires_at>$4::timestamptz AND c.status='observed'
   AND c.preparation_token=p.token AND c.org_id=p.org_id AND c.attempt_id=p.attempt_id AND c.job_lock_token=p.job_lock_token
   AND d.control_state='active' AND d.control_version=p.control_version AND d.binding_token=p.binding_token AND d.job_id=p.job_id AND a.dispatch_id=d.id AND a.job_id=d.job_id
   AND a.job_lock_token=p.job_lock_token AND j.lock_token=p.job_lock_token AND j.status='running' AND j.locked_until>$4::timestamptz
   AND q.provider_request_id=d.provider_request_id AND encode(sha256(convert_to(q.answer_text,'UTF8')),'hex')=d.answer_digest
   AND q.pause_anchor_digest=d.anchor_digest AND q.pause_anchor_digest=encode(sha256(convert_to(q.readiness_proof::text,'UTF8')),'hex')
   AND ((d.input_kind='answer' AND q.status='answered' AND g.id=d.grant_id AND g.request_id=q.id)
    OR (d.input_kind='denial' AND q.status='cancelled' AND d.grant_id IS NULL AND EXISTS(SELECT 1 FROM probe_committed_denial_receipts denial
     WHERE denial.org_id=q.org_id AND denial.id=d.denial_receipt_id AND denial.request_id=q.id AND denial.provider_request_id=d.provider_request_id
      AND denial.answer_digest=d.answer_digest AND denial.payload=q.answer_payload)))
   AND inbox.id=d.inbox_id AND inbox.request_id=q.id AND inbox.dispatch_id=d.id AND inbox.delivery_state='pending'
   AND s.admission_epoch=d.session_epoch AND t.admission_epoch=d.thread_epoch
   AND p.session_epoch=d.session_epoch AND p.thread_epoch=d.thread_epoch
   AND p.workspace_generation=d.workspace_generation AND d.workspace_generation=s.workspace_generation
   AND p.workspace_state=` + sessionLifecycleWorkspaceStateProbe + ` AND c.workspace_state=p.workspace_state
   AND c.workspace_generation=p.workspace_generation
   AND target.target_id=p.target_id AND target.source_kind=p.source_kind AND target.source_ref=p.source_ref
   AND target.source_availability_version=p.source_availability_version AND target.availability='available'
   AND c.target_id=p.target_id AND c.source_kind=p.source_kind AND c.source_ref=p.source_ref AND c.source_availability_version=p.source_availability_version
   AND (p.source_kind='live' AND s.physical_workspace_availability='available'
    OR p.source_kind='restore' AND s.snapshot_availability='available' AND p.source_ref=s.snapshot_key AND p.source_availability_version=s.snapshot_availability_version)
   AND p.provider_type=d.provider_type AND p.provider_build=d.provider_build AND p.conversation_id=d.conversation_id AND p.workdir_scope=d.workdir_scope
   AND q.provider_type=d.provider_type AND q.provider_build=d.provider_build AND q.conversation_id=d.conversation_id AND q.workdir_scope=d.workdir_scope
   AND c.provider_type=p.provider_type AND c.provider_build=p.provider_build AND c.conversation_id=p.conversation_id AND c.workdir_scope=p.workdir_scope
   AND c.native_availability_version IS NOT DISTINCT FROM n.availability_version,false) AS exact_current,
  COALESCE(` + sessionLifecycleNativeIdentityProbe + ` AND n.availability='available',false) AS exact_native,
  jsonb_build_object('dispatch_id',d.id,'binding_token',d.binding_token,'request_id',q.id,'provider_request_id',d.provider_request_id,'grant_id',d.grant_id,'answer_digest',d.answer_digest,'input_kind',d.input_kind,
   'provider_type',d.provider_type,'provider_build',d.provider_build,'conversation_id',d.conversation_id,'workdir_scope',d.workdir_scope,
   'workspace_generation',d.workspace_generation,'target_id',p.target_id,'attempt_id',p.attempt_id,'job_lock_token',p.job_lock_token) AS bound_context
 FROM probe_start_preparations p
 JOIN probe_bound_dispatches d ON d.org_id=p.org_id AND d.id=p.dispatch_id
 JOIN session_human_input_requests q ON q.org_id=d.org_id AND q.id=d.request_id AND q.session_id=d.session_id AND q.thread_id=d.thread_id
 JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id
 JOIN session_threads t ON t.org_id=q.org_id AND t.session_id=q.session_id AND t.id=q.thread_id
 JOIN jobs j ON j.org_id=d.org_id AND j.session_id=d.session_id AND j.id=d.job_id
 LEFT JOIN session_capability_grants g ON g.org_id=d.org_id AND g.id=d.grant_id
 JOIN probe_pending_input_inbox inbox ON inbox.org_id=d.org_id AND inbox.id=d.inbox_id
 JOIN probe_start_attempts a ON a.org_id=p.org_id AND a.id=p.attempt_id
 JOIN probe_start_targets target ON target.org_id=p.org_id AND target.session_id=q.session_id
 LEFT JOIN probe_native_context_availability n ON n.org_id=q.org_id AND n.request_id=q.id
 JOIN probe_resumability_checks c ON c.org_id=p.org_id AND c.id=$3
 WHERE p.org_id=$1 AND p.token=$2
 FOR UPDATE OF p,s,q,j,a,target,inbox
), reasoned AS MATERIALIZED (
 SELECT *,CASE
  WHEN first_provider_launch_marker IS NOT NULL OR prior_provider_entry OR execution_state IN ('provider_launched','delivery_unknown') OR check_disposition='delivery_unknown' THEN 'delivery_unknown'
  WHEN NOT exact_current THEN 'recheck'
  WHEN execution_state IS DISTINCT FROM 'known_not_launched' THEN 'delivery_unknown'
  WHEN check_disposition='retryable_io' THEN 'start_deferred'
  WHEN check_disposition='unsupported' THEN 'unsupported'
  WHEN check_disposition='unavailable' OR NOT exact_native THEN 'input_context_unavailable'
  WHEN check_disposition='verified' THEN 'launch_permitted'
  ELSE 'recheck' END AS reason FROM evidence
), permitted AS (
 UPDATE probe_start_preparations p SET status='authorized',invocation_authority_token=p.token,invocation_purpose='resume_input' FROM reasoned e
 WHERE p.org_id=e.org_id AND p.token=e.token AND e.reason='launch_permitted' RETURNING p.token
), parked_control AS (
 INSERT INTO probe_start_controls AS prior(id,org_id,dispatch_id,binding_token,control_version,job_id,revoked_preparation_token,attempt_id,revoked_job_lock_token,
  session_epoch,thread_epoch,workspace_generation,provider_type,provider_build,conversation_id,workdir_scope,no_launch_proof_token,reason,status,job_payload_digest)
 SELECT $5,e.org_id,e.dispatch_id,e.binding_token,e.control_version+1,e.job_id,e.token,e.attempt_id,e.job_lock_token,
  e.session_epoch,e.thread_epoch,e.workspace_generation,e.provider_type,e.provider_build,e.conversation_id,e.workdir_scope,$5,e.reason,'awaiting_control',e.job_payload_digest FROM reasoned e
 WHERE e.reason IN ('input_context_unavailable','unsupported')
 ON CONFLICT(org_id,dispatch_id) DO UPDATE SET id=EXCLUDED.id,control_version=EXCLUDED.control_version,
  revoked_preparation_token=EXCLUDED.revoked_preparation_token,attempt_id=EXCLUDED.attempt_id,revoked_job_lock_token=EXCLUDED.revoked_job_lock_token,
  no_launch_proof_token=EXCLUDED.no_launch_proof_token,reason=EXCLUDED.reason,status=EXCLUDED.status,job_payload_digest=EXCLUDED.job_payload_digest
 WHERE prior.status='consumed' AND prior.control_version<EXCLUDED.control_version
  AND prior.binding_token=EXCLUDED.binding_token AND prior.job_id=EXCLUDED.job_id
  AND prior.session_epoch=EXCLUDED.session_epoch AND prior.thread_epoch=EXCLUDED.thread_epoch AND prior.workspace_generation=EXCLUDED.workspace_generation
  AND prior.provider_type=EXCLUDED.provider_type AND prior.provider_build=EXCLUDED.provider_build AND prior.conversation_id=EXCLUDED.conversation_id AND prior.workdir_scope=EXCLUDED.workdir_scope
 RETURNING org_id,dispatch_id
), parked_dispatch AS (
 UPDATE probe_bound_dispatches d SET control_state='awaiting_control',control_version=d.control_version+1 FROM reasoned e
 JOIN parked_control written ON written.org_id=e.org_id AND written.dispatch_id=e.dispatch_id
 WHERE d.org_id=e.org_id AND d.id=e.dispatch_id RETURNING e.*
), parked_job AS (
 UPDATE jobs j SET status='awaiting_start_control',lock_token=NULL,locked_until=NULL,
  attempts=j.attempts-CASE WHEN j.claim_debit_refunded THEN 0 ELSE 1 END,claim_debit_refunded=true,parked_control_id=$5
 FROM parked_dispatch e WHERE j.org_id=e.org_id AND j.id=e.job_id AND j.status='running' AND j.lock_token=e.job_lock_token RETURNING j.id
), deferred_job AS (
 UPDATE jobs j SET status='pending',lock_token=NULL,locked_until=NULL,
  attempts=j.attempts-CASE WHEN j.claim_debit_refunded THEN 0 ELSE 1 END,claim_debit_refunded=true
 FROM reasoned e WHERE j.org_id=e.org_id AND j.id=e.job_id AND e.reason='start_deferred' RETURNING j.id
), revoked_preparations AS (
 UPDATE probe_start_preparations p SET status='closed',invocation_authority_token=NULL FROM reasoned e
 WHERE p.org_id=e.org_id AND p.token=e.token AND (e.reason='start_deferred' OR EXISTS(SELECT 1 FROM parked_control written WHERE written.org_id=e.org_id AND written.dispatch_id=e.dispatch_id)) RETURNING p.token
), uncertain AS (
 UPDATE probe_start_attempts a SET execution_state='delivery_unknown' FROM reasoned e
 WHERE a.org_id=e.org_id AND a.id=e.attempt_id AND e.reason='delivery_unknown' RETURNING a.id
), consumed AS (
 UPDATE probe_resumability_checks c SET status='consumed' FROM reasoned e
 WHERE c.org_id=e.org_id AND c.id=e.check_id AND (e.reason IN ('launch_permitted','start_deferred') OR EXISTS(SELECT 1 FROM parked_control written WHERE written.org_id=e.org_id AND written.dispatch_id=e.dispatch_id)) RETURNING c.id
)
SELECT CASE WHEN reason IN ('input_context_unavailable','unsupported') AND NOT EXISTS(SELECT 1 FROM parked_control) THEN 'recheck'
 ELSE CASE reason WHEN 'input_context_unavailable' THEN 'accepted_undelivered' WHEN 'unsupported' THEN 'accepted_undelivered' ELSE reason END END,
 CASE WHEN reason IN ('input_context_unavailable','unsupported') AND NOT EXISTS(SELECT 1 FROM parked_control) THEN 'recheck'
 ELSE CASE reason WHEN 'input_context_unavailable' THEN 'INPUT_CONTEXT_UNAVAILABLE' WHEN 'unsupported' THEN 'VERIFY_RESUMABLE_UNSUPPORTED'
  WHEN 'start_deferred' THEN 'StartDeferred' ELSE reason END END,
 reason='start_deferred',CASE WHEN reason='launch_permitted' THEN bound_context END FROM reasoned`

// Answered-request control is deliberately not a pending-only recovery CAS.
// Durable parked controls outlive the revoked execution preparation/lease. Exact
// current owner/epochs/generation/control version and fenced no-launch proof,
// not a 30-second preparation TTL, authorize retry or abandon minutes later.
const sessionLifecycleAbandonAcceptedInputProbe = `
WITH candidate AS MATERIALIZED (
 SELECT d.org_id,d.id,d.inbox_id,d.job_id,control.revoked_job_lock_token AS job_lock_token,e.id AS receipt_id,control.id AS parked_control_id FROM probe_bound_dispatches d
 JOIN session_human_input_requests q ON q.org_id=d.org_id AND q.id=d.request_id
 JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id
 JOIN session_threads t ON t.org_id=d.org_id AND t.session_id=d.session_id AND t.id=d.thread_id
 LEFT JOIN session_capability_grants g ON g.org_id=d.org_id AND g.id=d.grant_id AND g.request_id=q.id
 JOIN probe_pending_input_inbox inbox ON inbox.org_id=d.org_id AND inbox.id=d.inbox_id AND inbox.dispatch_id=d.id AND inbox.request_id=q.id
 JOIN probe_start_attempts a ON a.org_id=d.org_id AND a.dispatch_id=d.id AND a.job_id=d.job_id
 JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id
 JOIN probe_accepted_input_controls e ON e.org_id=d.org_id AND e.request_id=q.id AND e.dispatch_id=d.id AND e.binding_token=d.binding_token
 JOIN probe_start_controls control ON control.org_id=d.org_id AND control.dispatch_id=d.id AND control.id=e.parked_control_id
 WHERE d.org_id=$1 AND e.id=$2 AND e.action='abandon' AND e.status='authorized'
  AND e.control_version=d.control_version AND control.control_version=d.control_version AND control.status='awaiting_control'
  AND control.binding_token=d.binding_token AND control.job_id=d.job_id AND control.no_launch_proof_token IS NOT NULL
  AND control.attempt_id=a.id AND control.revoked_job_lock_token=a.job_lock_token AND a.first_provider_launch_marker IS NULL
  AND NOT EXISTS(SELECT 1 FROM probe_start_attempts prior WHERE prior.org_id=d.org_id AND prior.dispatch_id=d.id AND ` + sessionLifecyclePossibleAcceptanceProbe + `)
  AND control.session_epoch=d.session_epoch AND control.thread_epoch=d.thread_epoch AND control.workspace_generation=d.workspace_generation
  AND control.provider_type=d.provider_type AND control.provider_build=d.provider_build AND control.conversation_id=d.conversation_id AND control.workdir_scope=d.workdir_scope
  AND s.admission_epoch=d.session_epoch AND t.admission_epoch=d.thread_epoch AND s.workspace_generation=d.workspace_generation
  AND s.execution_status NOT IN ('ended','archived') AND t.execution_status NOT IN ('ended','archived')
  AND q.admission_session_epoch=d.session_epoch AND q.admission_thread_epoch=d.thread_epoch
  AND q.provider_request_id=d.provider_request_id AND encode(sha256(convert_to(q.answer_text,'UTF8')),'hex')=d.answer_digest
  AND q.pause_anchor_digest=d.anchor_digest AND q.pause_anchor_digest=encode(sha256(convert_to(q.readiness_proof::text,'UTF8')),'hex')
  AND ((d.input_kind='answer' AND q.status='answered' AND g.id=d.grant_id)
   OR (d.input_kind='denial' AND q.status='cancelled' AND d.grant_id IS NULL AND EXISTS(SELECT 1 FROM probe_committed_denial_receipts denial
    WHERE denial.org_id=q.org_id AND denial.id=d.denial_receipt_id AND denial.request_id=q.id AND denial.provider_request_id=d.provider_request_id
     AND denial.answer_digest=d.answer_digest AND denial.payload=q.answer_payload)))
  AND d.control_state='awaiting_control' AND inbox.delivery_state='pending' AND a.execution_state='known_not_launched'
  AND j.status='awaiting_start_control' AND j.lock_token IS NULL AND j.locked_until IS NULL AND j.parked_control_id=control.id
 FOR UPDATE OF s,t,d,q,inbox,a,j,control
), abandoned AS (
 UPDATE probe_bound_dispatches d SET control_state='abandoned',control_version=d.control_version+1 FROM candidate c WHERE d.org_id=c.org_id AND d.id=c.id RETURNING c.*
), terminal_inbox AS (
 UPDATE probe_pending_input_inbox inbox SET delivery_state='abandoned' FROM abandoned c
 WHERE inbox.org_id=c.org_id AND inbox.id=c.inbox_id RETURNING inbox.id
), terminal_job AS (
 UPDATE jobs j SET status='cancelled',abandoned_lease_token=c.job_lock_token,lock_token=NULL,locked_until=NULL,
  cancellation_kind='user_abandoned_unlaunched',cancelled_control_receipt_id=c.receipt_id FROM abandoned c
 WHERE j.org_id=c.org_id AND j.id=c.job_id AND j.status='awaiting_start_control' AND j.parked_control_id=c.parked_control_id RETURNING j.id
), revoked_preparations AS (
 UPDATE probe_start_preparations p SET status='closed',invocation_authority_token=NULL FROM abandoned c WHERE p.org_id=c.org_id AND p.dispatch_id=c.id RETURNING p.token
), receipted AS (
 UPDATE probe_accepted_input_controls e SET status='applied' FROM abandoned c WHERE e.org_id=c.org_id AND e.id=c.receipt_id RETURNING e.id
), consumed_control AS (
 UPDATE probe_start_controls control SET status='consumed' FROM abandoned c WHERE control.org_id=c.org_id AND control.id=c.parked_control_id RETURNING control.id
) SELECT count(*) FROM abandoned`

// Explicit retry consumes a durable parked version, not an expired lease or
// preparation. It requeues the SAME immutable job/binding/native context without
// allocating, launching, changing consent, or charging an attempt. A later fresh
// claim and preparation must independently pass selected verification again.
const sessionLifecycleRetryParkedStartProbe = `
WITH candidate AS MATERIALIZED (
 SELECT d.org_id,d.id,d.job_id,control.id AS control_id FROM probe_bound_dispatches d
 JOIN probe_start_controls control ON control.org_id=d.org_id AND control.dispatch_id=d.id
 JOIN session_human_input_requests q ON q.org_id=d.org_id AND q.id=d.request_id
 JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id
 JOIN session_threads t ON t.org_id=d.org_id AND t.session_id=d.session_id AND t.id=d.thread_id
 JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id AND j.session_id=d.session_id
 JOIN probe_start_attempts a ON a.org_id=d.org_id AND a.id=control.attempt_id AND a.dispatch_id=d.id
 LEFT JOIN session_capability_grants g ON g.org_id=d.org_id AND g.id=d.grant_id AND g.request_id=q.id
 WHERE d.org_id=$1 AND d.id=$2 AND control.control_version=$3 AND d.control_version=$3
  AND d.control_state='awaiting_control' AND control.status='awaiting_control' AND control.binding_token=d.binding_token AND control.job_id=d.job_id
  AND control.no_launch_proof_token IS NOT NULL AND a.first_provider_launch_marker IS NULL AND a.execution_state='known_not_launched'
  AND NOT EXISTS(SELECT 1 FROM probe_start_attempts prior WHERE prior.org_id=d.org_id AND prior.dispatch_id=d.id AND ` + sessionLifecyclePossibleAcceptanceProbe + `)
  AND a.job_lock_token=control.revoked_job_lock_token AND j.parked_control_id=control.id
  AND j.status='awaiting_start_control' AND j.lock_token IS NULL AND j.locked_until IS NULL AND j.claim_debit_refunded
  AND encode(sha256(convert_to(j.payload::text,'UTF8')),'hex')=control.job_payload_digest
  AND s.admission_epoch=d.session_epoch AND t.admission_epoch=d.thread_epoch AND s.workspace_generation=d.workspace_generation
  AND s.execution_status NOT IN ('ended','archived') AND t.execution_status NOT IN ('ended','archived')
  AND control.session_epoch=d.session_epoch AND control.thread_epoch=d.thread_epoch AND control.workspace_generation=d.workspace_generation
  AND q.provider_request_id=d.provider_request_id AND q.provider_type=d.provider_type AND q.provider_build=d.provider_build
  AND q.conversation_id=d.conversation_id AND q.workdir_scope=d.workdir_scope
  AND control.provider_type=d.provider_type AND control.provider_build=d.provider_build AND control.conversation_id=d.conversation_id AND control.workdir_scope=d.workdir_scope
  AND q.pause_anchor_digest=d.anchor_digest AND q.pause_anchor_digest=encode(sha256(convert_to(q.readiness_proof::text,'UTF8')),'hex')
  AND encode(sha256(convert_to(q.answer_text,'UTF8')),'hex')=d.answer_digest
  AND ((d.input_kind='answer' AND q.status='answered' AND g.id=d.grant_id)
   OR (d.input_kind='denial' AND q.status='cancelled' AND d.grant_id IS NULL AND EXISTS(SELECT 1 FROM probe_committed_denial_receipts denial
    WHERE denial.org_id=q.org_id AND denial.id=d.denial_receipt_id AND denial.request_id=q.id AND denial.provider_request_id=d.provider_request_id
     AND denial.answer_digest=d.answer_digest AND denial.payload=q.answer_payload)))
 FOR UPDATE OF s,t,d,q,j,a,control
), requeued AS (
 UPDATE jobs j SET status='pending',parked_control_id=NULL FROM candidate c WHERE j.org_id=c.org_id AND j.id=c.job_id RETURNING c.*
), dispatch_control AS (
 UPDATE probe_bound_dispatches d SET control_state='active',control_version=d.control_version+1 FROM requeued c
 WHERE d.org_id=c.org_id AND d.id=c.id RETURNING d.id
), consumed AS (
 UPDATE probe_start_controls control SET status='consumed' FROM requeued c WHERE control.org_id=c.org_id AND control.id=c.control_id RETURNING control.id
) SELECT count(*) FROM requeued`

// Bounded fields on the existing allocation/continuation-attempt receipt, not a
// new marker ledger. Every Execute must first WIN a fresh current invocation CAS.
// AlreadyMarked or unknown commit is never permission to call Execute again.
const sessionLifecycleMarkProviderInvocationProbe = `
WITH candidate AS MATERIALIZED (
 SELECT a.org_id,a.id,p.token FROM probe_start_attempts a
 JOIN probe_bound_dispatches d ON d.org_id=a.org_id AND d.id=a.dispatch_id
 JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id
 JOIN session_threads t ON t.org_id=d.org_id AND t.session_id=d.session_id AND t.id=d.thread_id
 JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id AND j.id=a.job_id
 JOIN probe_start_preparations p ON p.org_id=d.org_id AND p.dispatch_id=d.id AND p.attempt_id=a.id
 JOIN probe_start_targets target ON target.org_id=d.org_id AND target.session_id=d.session_id
 WHERE d.org_id=$1 AND p.token=$2 AND p.status='authorized' AND p.expires_at>$7::timestamptz
  AND p.invocation_authority_token=$5 AND p.invocation_purpose=$6 AND $6 IN ('resume_input','turn_followup')
  AND (a.launch_ordinal=0 OR $6='turn_followup') AND a.launch_ordinal=$4 AND a.invocation_nonce IS DISTINCT FROM $3::uuid
  AND ((a.execution_state='known_not_launched' AND a.first_provider_launch_marker IS NULL AND a.launch_ordinal=0)
   OR (a.execution_state IN ('provider_launch_marked','provider_launched') AND a.first_provider_launch_marker IS NOT NULL AND a.launch_ordinal>0 AND $6='turn_followup'))
  AND d.control_state='active' AND d.control_version=p.control_version
  AND NOT EXISTS(SELECT 1 FROM probe_start_attempts prior WHERE prior.org_id=d.org_id AND prior.dispatch_id=d.id AND prior.id<>a.id AND ` + sessionLifecyclePossibleAcceptanceProbe + `)
  AND j.status='running' AND j.lock_token=p.job_lock_token AND j.lock_token=a.job_lock_token AND j.locked_until>$7::timestamptz
  AND s.admission_epoch=d.session_epoch AND t.admission_epoch=d.thread_epoch AND s.workspace_generation=d.workspace_generation
  AND s.execution_status NOT IN ('ended','archived') AND t.execution_status NOT IN ('ended','archived')
  AND p.workspace_state=` + sessionLifecycleWorkspaceStateProbe + ` AND p.workspace_generation=d.workspace_generation
  AND target.target_id=p.target_id AND target.source_kind=p.source_kind AND target.source_ref=p.source_ref
  AND target.source_availability_version=p.source_availability_version AND target.availability='available'
 FOR UPDATE OF s,t,d,j,p,a,target
), marked AS (
 UPDATE probe_start_attempts a SET first_provider_launch_marker=COALESCE(a.first_provider_launch_marker,$3),launch_ordinal=a.launch_ordinal+1,
  invocation_nonce=$3,invocation_purpose=$6,execution_state='provider_launch_marked'
 FROM candidate c WHERE a.org_id=c.org_id AND a.id=c.id RETURNING c.token
), consumed AS (
 UPDATE probe_start_preparations p SET invocation_authority_token=NULL FROM marked c WHERE p.org_id=$1 AND p.token=c.token RETURNING p.token
) SELECT count(*) FROM marked`

// Atomic revocation wins against the same marker CAS locks. Positive no-entry
// evidence for ALL same-binding receipts plus marker absence is required, even
// if setup allocated a target. Marker-free legacy uncertainty stays unknown;
// FIRST present also preserves the conservative crash gap before Execute itself.
const sessionLifecycleReconcileExpiredStartProbe = `
WITH candidate AS MATERIALIZED (
 SELECT d.org_id,d.id AS dispatch_id,d.binding_token,d.control_version,d.job_id,p.token,a.id AS attempt_id,a.job_lock_token,
  d.session_epoch,d.thread_epoch,d.workspace_generation,d.provider_type,d.provider_build,d.conversation_id,d.workdir_scope,
  COALESCE(a.first_provider_launch_marker,(SELECT prior.first_provider_launch_marker FROM probe_start_attempts prior WHERE prior.org_id=d.org_id AND prior.dispatch_id=d.id AND prior.first_provider_launch_marker IS NOT NULL ORDER BY prior.id LIMIT 1)) AS first_provider_launch_marker,
  EXISTS(SELECT 1 FROM probe_start_attempts prior WHERE prior.org_id=d.org_id AND prior.dispatch_id=d.id AND ` + sessionLifecyclePossibleAcceptanceProbe + `) AS possible_acceptance,
  encode(sha256(convert_to(j.payload::text,'UTF8')),'hex') AS job_payload_digest
 FROM probe_bound_dispatches d JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id
 JOIN session_threads t ON t.org_id=d.org_id AND t.session_id=d.session_id AND t.id=d.thread_id
 JOIN probe_start_preparations p ON p.org_id=d.org_id AND p.dispatch_id=d.id AND p.token=$2
 JOIN probe_start_attempts a ON a.org_id=d.org_id AND a.id=p.attempt_id
 JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id
 WHERE d.org_id=$1 AND d.control_state='active' AND p.status IN ('preparing','authorized')
  AND j.status='running' AND j.lock_token=$3 AND p.job_lock_token=$3 AND a.job_lock_token=$3 AND j.locked_until<=$4::timestamptz
  AND s.admission_epoch=d.session_epoch AND t.admission_epoch=d.thread_epoch AND s.workspace_generation=d.workspace_generation
  AND p.control_version=d.control_version FOR UPDATE OF s,t,d,p,a,j
), parked AS (
 INSERT INTO probe_start_controls AS prior(id,org_id,dispatch_id,binding_token,control_version,job_id,revoked_preparation_token,attempt_id,revoked_job_lock_token,
  session_epoch,thread_epoch,workspace_generation,provider_type,provider_build,conversation_id,workdir_scope,no_launch_proof_token,reason,status,job_payload_digest)
 SELECT $5,c.org_id,c.dispatch_id,c.binding_token,c.control_version+1,c.job_id,c.token,c.attempt_id,c.job_lock_token,
  c.session_epoch,c.thread_epoch,c.workspace_generation,c.provider_type,c.provider_build,c.conversation_id,c.workdir_scope,
  CASE WHEN NOT c.possible_acceptance THEN $5::uuid END,'expired_start_reconciled',
  CASE WHEN NOT c.possible_acceptance THEN 'awaiting_control' ELSE 'delivery_unknown' END,c.job_payload_digest FROM candidate c
 ON CONFLICT(org_id,dispatch_id) DO UPDATE SET id=EXCLUDED.id,control_version=EXCLUDED.control_version,
  revoked_preparation_token=EXCLUDED.revoked_preparation_token,attempt_id=EXCLUDED.attempt_id,revoked_job_lock_token=EXCLUDED.revoked_job_lock_token,
  no_launch_proof_token=EXCLUDED.no_launch_proof_token,reason=EXCLUDED.reason,status=EXCLUDED.status,job_payload_digest=EXCLUDED.job_payload_digest
 WHERE prior.status='consumed' AND prior.control_version<EXCLUDED.control_version
  AND prior.binding_token=EXCLUDED.binding_token AND prior.job_id=EXCLUDED.job_id
  AND prior.session_epoch=EXCLUDED.session_epoch AND prior.thread_epoch=EXCLUDED.thread_epoch AND prior.workspace_generation=EXCLUDED.workspace_generation
  AND prior.provider_type=EXCLUDED.provider_type AND prior.provider_build=EXCLUDED.provider_build AND prior.conversation_id=EXCLUDED.conversation_id AND prior.workdir_scope=EXCLUDED.workdir_scope
 RETURNING org_id,dispatch_id
), dispatch_control AS (
 UPDATE probe_bound_dispatches d SET control_state=CASE WHEN NOT c.possible_acceptance THEN 'awaiting_control' ELSE 'delivery_unknown' END,
  control_version=d.control_version+1 FROM candidate c JOIN parked written ON written.org_id=c.org_id AND written.dispatch_id=c.dispatch_id
 WHERE d.org_id=c.org_id AND d.id=c.dispatch_id RETURNING c.*
), revoked_job AS (
 UPDATE jobs j SET status='awaiting_start_control',lock_token=NULL,locked_until=NULL,parked_control_id=$5,
  attempts=j.attempts-CASE WHEN NOT c.possible_acceptance AND NOT j.claim_debit_refunded THEN 1 ELSE 0 END,
  claim_debit_refunded=j.claim_debit_refunded OR NOT c.possible_acceptance
 FROM dispatch_control c WHERE j.org_id=c.org_id AND j.id=c.job_id RETURNING j.id
), revoked_preparation AS (
 UPDATE probe_start_preparations p SET status='closed',invocation_authority_token=NULL FROM dispatch_control c WHERE p.org_id=c.org_id AND p.token=c.token RETURNING p.token
), settled_attempt AS (
 UPDATE probe_start_attempts a SET execution_state=CASE WHEN NOT c.possible_acceptance THEN 'known_not_launched' ELSE 'delivery_unknown' END
 FROM dispatch_control c WHERE a.org_id=c.org_id AND a.id=c.attempt_id RETURNING a.id
) SELECT count(*) FROM dispatch_control`

// AdmissionAuthority releases only this dedicated answer/denial job's exact
// logical claim. A physical preparation blocker or foreign/successor claim stays
// visible; no status/counter is cleared merely because consent was abandoned.
const sessionLifecycleReleaseAbandonedClaimProbe = `
WITH candidate AS MATERIALIZED (
 SELECT claim.* FROM probe_admission_claims claim
 JOIN probe_bound_dispatches d ON d.org_id=claim.org_id AND d.id=claim.dispatch_id AND d.binding_token=claim.binding_token
 JOIN probe_accepted_input_controls e ON e.org_id=d.org_id AND e.dispatch_id=d.id AND e.binding_token=d.binding_token
 JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id
 JOIN session_threads t ON t.org_id=d.org_id AND t.session_id=d.session_id AND t.id=d.thread_id
 JOIN probe_admission_capacity capacity ON capacity.org_id=d.org_id AND capacity.session_id=d.session_id
 JOIN probe_start_attempts a ON a.org_id=d.org_id AND a.dispatch_id=d.id AND a.job_id=d.job_id
 JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id
 WHERE d.org_id=$1 AND e.id=$2 AND e.status='applied' AND e.action='abandon' AND d.control_state='abandoned'
  AND claim.session_id=d.session_id AND claim.thread_id=d.thread_id AND claim.session_epoch=d.session_epoch AND claim.thread_epoch=d.thread_epoch
  AND claim.workspace_generation=d.workspace_generation AND claim.state='running' AND claim.physical_state='settled'
  AND s.admission_epoch=d.session_epoch AND t.admission_epoch=d.thread_epoch AND s.workspace_generation=d.workspace_generation
  AND a.execution_state='known_not_launched' AND j.status='cancelled' AND j.cancellation_kind='user_abandoned_unlaunched'
  AND j.cancelled_control_receipt_id=e.id AND j.abandoned_lease_token=a.job_lock_token AND j.lock_token IS NULL
  AND capacity.running_slots>0 FOR UPDATE OF s,t,d,claim,capacity,a,j
), released AS (
 UPDATE probe_admission_claims claim SET state='released' FROM candidate c
 WHERE claim.org_id=c.org_id AND claim.dispatch_id=c.dispatch_id RETURNING c.*
), freed AS (
 UPDATE probe_admission_capacity capacity SET running_slots=capacity.running_slots-1 FROM released r
 WHERE capacity.org_id=r.org_id AND capacity.session_id=r.session_id
 RETURNING capacity.running_slots,r.org_id,r.session_id,r.thread_id,r.dispatch_id,r.session_epoch,r.thread_epoch
), lane_projection AS (
 UPDATE session_threads t SET execution_status=CASE WHEN EXISTS(SELECT 1 FROM probe_admission_claims other
  WHERE other.org_id=f.org_id AND other.session_id=f.session_id AND other.thread_id=f.thread_id AND other.dispatch_id<>f.dispatch_id AND other.state='running') THEN 'running' ELSE 'idle' END
 FROM freed f WHERE t.org_id=f.org_id AND t.id=f.thread_id AND t.admission_epoch=f.thread_epoch AND t.execution_status='running' RETURNING t.id
), parent_projection AS (
 UPDATE sessions s SET execution_status=CASE WHEN f.running_slots>0 THEN 'running' ELSE 'idle' END FROM freed f
 WHERE s.org_id=f.org_id AND s.id=f.session_id AND s.admission_epoch=f.session_epoch AND s.execution_status='running' RETURNING s.id
) SELECT count(*) FROM released`

// A distinct ordinary input, not replay of abandoned consent, may claim newly
// free capacity. This is a candidate CAS probe, not SessionHandler.SendMessage.
const sessionLifecycleOrdinaryAdmissionAfterAbandonProbe = `
WITH candidate AS MATERIALIZED (
 SELECT s.org_id,s.id AS session_id,t.id AS thread_id,s.admission_epoch AS session_epoch,t.admission_epoch AS thread_epoch,s.workspace_generation
 FROM sessions s JOIN probe_bound_dispatches old ON old.org_id=s.org_id AND old.session_id=s.id
 JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id AND t.id=old.thread_id
 JOIN probe_admission_capacity capacity ON capacity.org_id=s.org_id AND capacity.session_id=s.id
 WHERE s.org_id=$1 AND old.control_state IN ('abandoned','superseded') AND s.execution_status='idle' AND t.execution_status='idle' AND capacity.running_slots=0
  AND NOT EXISTS(SELECT 1 FROM probe_admission_claims claim WHERE claim.org_id=s.org_id AND claim.session_id=s.id AND (claim.state='running' OR claim.physical_state<>'settled'))
 FOR UPDATE OF s,t,capacity
), admitted AS (
 INSERT INTO probe_admission_claims SELECT c.org_id,c.session_id,c.thread_id,$2,$3,c.session_epoch,c.thread_epoch,c.workspace_generation,'running','settled' FROM candidate c RETURNING *
), counted AS (
 UPDATE probe_admission_capacity capacity SET running_slots=capacity.running_slots+1 FROM admitted a
 WHERE capacity.org_id=a.org_id AND capacity.session_id=a.session_id RETURNING capacity.session_id
), lane_projection AS (
 UPDATE session_threads t SET execution_status='running' FROM admitted a WHERE t.org_id=a.org_id AND t.id=a.thread_id AND t.admission_epoch=a.thread_epoch RETURNING t.id
), parent_projection AS (
 UPDATE sessions s SET execution_status='running' FROM admitted a WHERE s.org_id=a.org_id AND s.id=a.session_id AND s.admission_epoch=a.session_epoch RETURNING s.id
) SELECT count(*) FROM admitted`

// Current operation authority, not stale retry/abandon authority, closes an old
// known-unlaunched dedicated intent. Physical cleanup evidence remains separate:
// releasing logical capacity never manufactures drain or provider nonacceptance.
const sessionLifecycleSupersedeUnlaunchedIntentProbe = `
WITH candidate AS MATERIALIZED (
 SELECT d.*,control.id AS control_id,authority.id AS authority_id,authority.action FROM probe_bound_dispatches d
 JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id
 JOIN session_threads t ON t.org_id=d.org_id AND t.session_id=d.session_id AND t.id=d.thread_id
 JOIN probe_supersession_authorities authority ON authority.org_id=d.org_id AND authority.dispatch_id=d.id
 JOIN probe_start_controls control ON control.org_id=d.org_id AND control.dispatch_id=d.id
 JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id AND j.session_id=d.session_id
 JOIN probe_pending_input_inbox inbox ON inbox.org_id=d.org_id AND inbox.id=d.inbox_id AND inbox.dispatch_id=d.id
 WHERE d.org_id=$1 AND authority.id=$2 AND authority.status='authorized'
  AND authority.action IN ('end_thread','end_session','successor','recover','archive','delete') AND authority.binding_token=d.binding_token
  AND (authority.action<>'end_session' OR s.execution_status='idle')
  AND authority.current_session_epoch=s.admission_epoch AND authority.current_thread_epoch=t.admission_epoch
  AND authority.current_generation=s.workspace_generation AND authority.control_version=d.control_version
  AND (authority.action<>'recover' OR (authority.acknowledged_dispatch_id=d.id AND authority.acknowledged_binding_token=d.binding_token
   AND authority.acknowledged_control_version=d.control_version AND authority.acknowledgment_digest=encode(sha256(convert_to(
    jsonb_build_object('dispatch_id',d.id,'binding_token',d.binding_token,'control_version',d.control_version)::text,'UTF8')),'hex')))
  AND d.control_state='awaiting_control' AND control.status='awaiting_control' AND control.control_version=d.control_version
  AND control.binding_token=d.binding_token AND control.job_id=d.job_id AND control.no_launch_proof_token IS NOT NULL
  AND control.session_epoch=d.session_epoch AND control.thread_epoch=d.thread_epoch AND control.workspace_generation=d.workspace_generation
  AND j.status='awaiting_start_control' AND j.parked_control_id=control.id AND j.lock_token IS NULL AND inbox.delivery_state='pending'
  AND EXISTS(SELECT 1 FROM probe_start_attempts attempt WHERE attempt.org_id=d.org_id AND attempt.id=control.attempt_id AND attempt.dispatch_id=d.id AND attempt.execution_state='known_not_launched')
  AND NOT EXISTS(SELECT 1 FROM probe_start_attempts prior WHERE prior.org_id=d.org_id AND prior.dispatch_id=d.id AND ` + sessionLifecyclePossibleAcceptanceProbe + `)
 FOR UPDATE OF s,t,d,authority,control,j,inbox
), closed AS (
 UPDATE probe_bound_dispatches d SET control_state='superseded',control_version=d.control_version+1 FROM candidate c
 WHERE d.org_id=c.org_id AND d.id=c.id RETURNING c.*
), terminal_job AS (
 UPDATE jobs j SET status='cancelled',cancellation_kind='superseded_unlaunched',cancelled_control_receipt_id=c.authority_id
 FROM closed c WHERE j.org_id=c.org_id AND j.id=c.job_id RETURNING j.id
), terminal_inbox AS (
 UPDATE probe_pending_input_inbox inbox SET delivery_state='superseded' FROM closed c WHERE inbox.org_id=c.org_id AND inbox.id=c.inbox_id RETURNING inbox.id
), terminal_control AS (
 UPDATE probe_start_controls control SET status='superseded' FROM closed c WHERE control.org_id=c.org_id AND control.id=c.control_id RETURNING control.id
), revoked AS (
 UPDATE probe_start_preparations p SET status='closed',invocation_authority_token=NULL FROM closed c WHERE p.org_id=c.org_id AND p.dispatch_id=c.id RETURNING p.token
), released AS (
 UPDATE probe_admission_claims claim SET state='released' FROM closed c WHERE claim.org_id=c.org_id AND claim.dispatch_id=c.id
  AND claim.binding_token=c.binding_token AND claim.session_epoch=c.session_epoch AND claim.thread_epoch=c.thread_epoch
  AND claim.workspace_generation=c.workspace_generation AND claim.state='running' RETURNING claim.*
), freed AS (
 UPDATE probe_admission_capacity capacity SET running_slots=capacity.running_slots-1 FROM released r
 WHERE capacity.org_id=r.org_id AND capacity.session_id=r.session_id AND capacity.running_slots>0 RETURNING capacity.*
), applied AS (
 UPDATE probe_supersession_authorities authority SET status='applied' FROM closed c WHERE authority.org_id=c.org_id AND authority.id=c.authority_id RETURNING authority.id
) SELECT count(*) FROM closed`

func TestSessionLifecycleStartControlAtomicityProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct{ name, status, change string }{
		{name: "unconsumed control", status: "awaiting_control"},
		{name: "uncertain control", status: "delivery_unknown"},
		{name: "consumed foreign binding", status: "consumed", change: `UPDATE probe_start_controls SET binding_token=gen_random_uuid()`},
		{name: "consumed foreign native identity", status: "consumed", change: `UPDATE probe_start_controls SET conversation_id='foreign-conversation'`},
		{name: "consumed newer version", status: "consumed", change: `UPDATE probe_start_controls SET control_version=99`},
	}
	actions := []string{"park", "reconcile"}
	for _, tt := range tests {
		for _, action := range actions {
			t.Run(tt.name+"/"+action, func(t *testing.T) {
				t.Parallel()
				pool, orgID, requestID := newSessionLifecycleReadinessProbe(t, now)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				token, _ := prepareSessionLifecycleStartProbe(t, ctx, pool, orgID, requestID, now, false, false)
				_, err := pool.Exec(ctx, `INSERT INTO probe_start_controls SELECT gen_random_uuid(),d.org_id,d.id,d.binding_token,0,d.job_id,p.token,p.attempt_id,p.job_lock_token,
				 d.session_epoch,d.thread_epoch,d.workspace_generation,d.provider_type,d.provider_build,d.conversation_id,d.workdir_scope,gen_random_uuid(),'prior_control',$2,
				 encode(sha256(convert_to(j.payload::text,'UTF8')),'hex') FROM probe_bound_dispatches d JOIN probe_start_preparations p ON p.org_id=d.org_id AND p.dispatch_id=d.id
				 JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id WHERE d.org_id=$1`, orgID, tt.status)
				require.NoError(t, err, "seed a prior bounded control whose failed exact upsert must block every downstream authority mutation")
				if tt.change != "" {
					_, err = pool.Exec(ctx, tt.change)
					require.NoError(t, err, "represent conflicting old control identity or version without altering current prepared execution")
				}
				checkID := uuid.New()
				_, err = pool.Exec(ctx, `INSERT INTO probe_resumability_checks SELECT $3,p.org_id,p.token,p.attempt_id,p.job_lock_token,p.provider_type,p.provider_build,p.conversation_id,p.workdir_scope,
				 p.target_id,p.source_kind,p.source_ref,p.source_availability_version,p.workspace_generation,p.workspace_state,n.availability_version,'unavailable',$4,'observed'
				 FROM probe_start_preparations p JOIN probe_bound_dispatches d ON d.org_id=p.org_id AND d.id=p.dispatch_id
				 LEFT JOIN probe_native_context_availability n ON n.org_id=d.org_id AND n.request_id=d.request_id WHERE p.org_id=$1 AND p.token=$2`, orgID, token, checkID, now.Add(time.Minute))
				require.NoError(t, err, "persist selected external check before capturing the complete transition prestate")
				before := sessionLifecycleControlStateProbe(t, ctx, pool, orgID)
				if action == "reconcile" {
					var lease uuid.UUID
					require.NoError(t, pool.QueryRow(ctx, `SELECT job_lock_token FROM probe_start_preparations WHERE org_id=$1 AND token=$2`, orgID, token).Scan(&lease), "capture exact old lease for expired-start reconciliation")
					var changed int
					require.NoError(t, pool.QueryRow(ctx, sessionLifecycleReconcileExpiredStartProbe, orgID, token, lease, now.Add(10*time.Minute), uuid.New()).Scan(&changed), "control conflict must be an explicit atomic no-op, not a partially advanced CTE")
					require.Equal(t, 0, changed, "failed parked-control write must prevent dispatch version, job, lease, preparation and attempt changes")
				} else {
					var state, reason string
					var retry bool
					var launch json.RawMessage
					require.NoError(t, pool.QueryRow(ctx, sessionLifecycleAuthorizePreparedStartProbe, orgID, token, checkID, now, uuid.New()).Scan(&state, &reason, &retry, &launch), "failed ordinary parking control upsert must also remain an atomic no-op")
					require.Equal(t, []any{"recheck", "recheck", false, json.RawMessage(nil)}, []any{state, reason, retry, launch}, "conflicting control never reports accepted parking or consumes the selected check")
				}
				require.JSONEq(t, string(before), string(sessionLifecycleControlStateProbe(t, ctx, pool, orgID)), "every request, grant, binding, inbox, control, job, preparation, allocation, lease and check field must remain exactly unchanged on control-write conflict")
			})
		}
	}
}

func sessionLifecycleControlStateProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID) json.RawMessage {
	t.Helper()
	var value json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object(
	 'dispatches',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM probe_bound_dispatches x WHERE x.org_id=$1),
	 'controls',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM probe_start_controls x WHERE x.org_id=$1),
	 'jobs',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM jobs x WHERE x.org_id=$1),
	 'preparations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.token) FROM probe_start_preparations x WHERE x.org_id=$1),
	 'attempts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM probe_start_attempts x WHERE x.org_id=$1),
	 'checks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM probe_resumability_checks x WHERE x.org_id=$1),
	 'inbox',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM probe_pending_input_inbox x WHERE x.org_id=$1),
	 'requests',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM session_human_input_requests x WHERE x.org_id=$1),
	 'grants',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM session_capability_grants x WHERE x.org_id=$1))`, orgID).Scan(&value), "capture complete synthetic control transition state rather than counts alone")
	return value
}

func TestSessionLifecycleParkedSupersessionProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, action, change, acknowledgmentChange string
		denial, physical                           bool
		expectedClosed                             int
		expectedSlots                              int
		expectedAdmission                          int
	}{
		{name: "EndThread closes old parked answer before later explicit reopen", action: "end_thread", expectedClosed: 1, expectedAdmission: 1},
		{name: "idle only EndSession rejects current running parked session atomically", action: "end_session", expectedSlots: 1},
		{name: "successor admission atomically supersedes old parked consumer", action: "successor", expectedClosed: 1, expectedAdmission: 1},
		{name: "request scoped recovery closes old dedicated parked denial", action: "recover", denial: true, expectedClosed: 1, expectedAdmission: 1},
		{name: "archive closes old parked consumer without erasing audit", action: "archive", expectedClosed: 1, expectedAdmission: 1},
		{name: "soft delete closes old parked consumer before later explicit restore", action: "delete", expectedClosed: 1, expectedAdmission: 1},
		{name: "current recovery can close an already stale epoch parked binding", action: "recover", change: `UPDATE sessions SET admission_epoch=admission_epoch+1; UPDATE session_threads SET admission_epoch=admission_epoch+1 WHERE id=(SELECT thread_id FROM probe_bound_dispatches)`, expectedClosed: 1, expectedAdmission: 1},
		{name: "recovery stale acknowledged parked control aborts whole owner transition", action: "recover", acknowledgmentChange: `UPDATE probe_supersession_authorities SET acknowledged_control_version=acknowledged_control_version-1`, expectedSlots: 1},
		{name: "logical supersession preserves physical cleanup blocker until positive settlement", action: "successor", physical: true, expectedClosed: 1, expectedAdmission: 1},
		{name: "supersession preserves foreign successor logical claim", action: "successor", change: `UPDATE probe_admission_claims SET binding_token=gen_random_uuid(),session_epoch=session_epoch+1`, expectedClosed: 1, expectedSlots: 1},
		{name: "supersession preserves unrelated sibling claim and capacity", action: "successor", change: `INSERT INTO probe_admission_claims SELECT t.org_id,t.session_id,t.id,gen_random_uuid(),gen_random_uuid(),s.admission_epoch,t.admission_epoch,s.workspace_generation,'running','settled' FROM session_threads t JOIN sessions s ON s.org_id=t.org_id AND s.id=t.session_id WHERE t.id<>(SELECT thread_id FROM probe_bound_dispatches); UPDATE probe_admission_capacity SET running_slots=2`, expectedClosed: 1, expectedSlots: 1},
		{name: "prior possible provider acceptance cannot be superseded as unsent", action: "successor", change: `UPDATE probe_start_attempts SET execution_state='delivery_unknown',first_provider_launch_marker=gen_random_uuid()`, expectedSlots: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, requestID := newSessionLifecycleReadinessProbe(t, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			token, _ := prepareSessionLifecycleStartProbe(t, ctx, pool, orgID, requestID, now, false, tt.denial)
			state, _ := observeSessionLifecycleStartProbe(t, ctx, pool, orgID, token, now, "unavailable")
			require.Equal(t, "accepted_undelivered", state, "first park the committed dedicated answer or denial rather than merely advance epochs")
			_, err := pool.Exec(ctx, `CREATE TABLE probe_supersession_authorities(id uuid PRIMARY KEY,org_id uuid,dispatch_id uuid,binding_token uuid,control_version bigint,current_session_epoch bigint,current_thread_epoch bigint,current_generation bigint,action text,status text,acknowledged_dispatch_id uuid,acknowledged_binding_token uuid,acknowledged_control_version bigint,acknowledgment_digest text)`)
			require.NoError(t, err, "project current End/successor/recovery/archive/delete authority independently of stale user retry authority")
			if tt.change != "" {
				_, err = pool.Exec(ctx, tt.change)
				require.NoError(t, err, "represent unrelated ownership or retained prior provider uncertainty before supersession")
			}
			if tt.physical {
				_, err = pool.Exec(ctx, `UPDATE probe_admission_claims SET physical_state='cleanup_pending' WHERE org_id=$1`, orgID)
				require.NoError(t, err, "retain an uncertain physical preparation independently of the known-unlaunched logical input intent")
			}
			authorityID := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO probe_supersession_authorities SELECT $2,d.org_id,d.id,d.binding_token,d.control_version,s.admission_epoch,t.admission_epoch,s.workspace_generation,$3,'authorized',d.id,d.binding_token,d.control_version,
 encode(sha256(convert_to(jsonb_build_object('dispatch_id',d.id,'binding_token',d.binding_token,'control_version',d.control_version)::text,'UTF8')),'hex') FROM probe_bound_dispatches d JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id JOIN session_threads t ON t.org_id=d.org_id AND t.id=d.thread_id WHERE d.org_id=$1`, orgID, authorityID, tt.action)
			require.NoError(t, err, "record exact current owner authority targeting the old immutable binding/control version")
			if tt.acknowledgmentChange != "" {
				_, err = pool.Exec(ctx, tt.acknowledgmentChange)
				require.NoError(t, err, "represent stale F1 acknowledgment of the exact parked intent; answered request is not pending-consent authority")
			}
			before := sessionLifecycleControlStateProbe(t, ctx, pool, orgID)
			admissionBefore := sessionLifecycleAdmissionStateProbe(t, ctx, pool, orgID)
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin one owner transition transaction containing explicit old-intent closure and exact logical release")
			defer func() { _ = tx.Rollback(ctx) }()
			var changed, replayed int
			require.NoError(t, tx.QueryRow(ctx, sessionLifecycleSupersedeUnlaunchedIntentProbe, orgID, authorityID).Scan(&changed), "current operation authority must explicitly terminalize the old parked dedicated consumer before epoch/status transition")
			require.NoError(t, tx.QueryRow(ctx, sessionLifecycleSupersedeUnlaunchedIntentProbe, orgID, authorityID).Scan(&replayed), "replaying current supersession receipt must not release or refund anything twice")
			require.Equal(t, 0, replayed, "applied authority and terminal old control must make same-operation replay a no-op")
			if changed == 1 {
				transition := &pgx.Batch{}
				transition.Queue(`UPDATE sessions s SET admission_epoch=admission_epoch+CASE WHEN $2::text='end_thread' THEN 0 ELSE 1 END,execution_status=CASE WHEN $2::text IN ('end_session','archive','delete') THEN 'ended' WHEN capacity.running_slots=0 THEN 'idle' ELSE s.execution_status END FROM probe_admission_capacity capacity WHERE s.org_id=$1 AND capacity.org_id=s.org_id AND capacity.session_id=s.id`, orgID, tt.action)
				transition.Queue(`UPDATE session_threads t SET admission_epoch=admission_epoch+1,execution_status=CASE WHEN $2::text IN ('end_session','end_thread','archive','delete') THEN 'ended' WHEN NOT EXISTS(SELECT 1 FROM probe_admission_claims c WHERE c.org_id=t.org_id AND c.thread_id=t.id AND c.state='running') THEN 'idle' ELSE t.execution_status END WHERE t.org_id=$1 AND t.id=(SELECT thread_id FROM probe_bound_dispatches WHERE org_id=$1)`, orgID, tt.action)
				err = tx.SendBatch(ctx, transition).Close()
				require.NoError(t, err, "the same canonical owner transaction must include both old-intent closure and the authorized epoch/status transition")
			}
			require.NoError(t, tx.Commit(ctx), "commit exact old dispatch/control/job/inbox closure, owned logical release and current owner transition together")
			require.Equal(t, tt.expectedClosed, changed, "only positively known-unlaunched old intent may use supersession closure; unknown acceptance remains reconciler-owned")
			var expected map[string]any
			require.NoError(t, json.Unmarshal(before, &expected), "decode complete old-intent state for exact permitted closure metadata")
			if changed == 1 {
				expected["dispatches"].([]any)[0].(map[string]any)["control_state"] = "superseded"
				expected["dispatches"].([]any)[0].(map[string]any)["control_version"] = float64(3)
				expected["controls"].([]any)[0].(map[string]any)["status"] = "superseded"
				expected["inbox"].([]any)[0].(map[string]any)["delivery_state"] = "superseded"
				for _, value := range expected["jobs"].([]any) {
					job := value.(map[string]any)
					if job["status"] == "awaiting_start_control" {
						job["status"], job["cancellation_kind"], job["cancelled_control_receipt_id"] = "cancelled", "superseded_unlaunched", authorityID.String()
					}
				}
			}
			expectedJSON, err := json.Marshal(expected)
			require.NoError(t, err, "encode terminal metadata without rewriting any consent/grant/denial/audit/native identity or payload")
			require.JSONEq(t, string(expectedJSON), string(sessionLifecycleControlStateProbe(t, ctx, pool, orgID)), "supersession must preserve exact request, grant, payload, allocation, prior uncertainty and check evidence while closing only the old dedicated consumer")
			var expectedAdmission map[string]any
			require.NoError(t, json.Unmarshal(admissionBefore, &expectedAdmission), "decode complete claims/capacity/session/lane state before conditional exact claim release")
			if changed == 1 && (tt.expectedSlots == 0 || strings.Contains(tt.name, "sibling")) {
				for _, value := range expectedAdmission["claims"].([]any) {
					claim := value.(map[string]any)
					if claim["dispatch_id"] == expected["dispatches"].([]any)[0].(map[string]any)["id"] {
						claim["state"] = "released"
					}
				}
				expectedAdmission["capacity"].(map[string]any)["running_slots"] = float64(tt.expectedSlots)
			}
			if changed == 1 {
				parent := expectedAdmission["session"].(map[string]any)
				if tt.action != "end_thread" {
					parent["admission_epoch"] = parent["admission_epoch"].(float64) + 1
				}
				ended := tt.action == "end_session" || tt.action == "archive" || tt.action == "delete"
				if ended {
					parent["execution_status"] = "ended"
				} else if tt.expectedSlots == 0 {
					parent["execution_status"] = "idle"
				}
				for _, value := range expectedAdmission["threads"].([]any) {
					thread := value.(map[string]any)
					if thread["id"] == expected["dispatches"].([]any)[0].(map[string]any)["thread_id"] {
						thread["admission_epoch"] = thread["admission_epoch"].(float64) + 1
						if ended || tt.action == "end_thread" {
							thread["execution_status"] = "ended"
						} else if !strings.Contains(tt.name, "foreign successor") {
							thread["execution_status"] = "idle"
						}
					}
				}
			}
			admissionJSON, err := json.Marshal(expectedAdmission)
			require.NoError(t, err, "encode only exact owned logical release without clearing physical evidence or foreign/sibling claims")
			require.JSONEq(t, string(admissionJSON), string(sessionLifecycleAdmissionStateProbe(t, ctx, pool, orgID)), "closing an obsolete intent cannot invent physical drain, clear successor ownership or alter sibling status/capacity")
			if changed == 1 {
				if tt.action == "end_thread" || tt.action == "end_session" || tt.action == "archive" || tt.action == "delete" {
					reopen := &pgx.Batch{}
					reopen.Queue(`UPDATE sessions SET admission_epoch=admission_epoch+1,execution_status='idle' WHERE org_id=$1`, orgID)
					reopen.Queue(`UPDATE session_threads SET admission_epoch=admission_epoch+1,execution_status='idle' WHERE org_id=$1 AND id=(SELECT thread_id FROM probe_bound_dispatches WHERE org_id=$1)`, orgID)
					err = pool.SendBatch(ctx, reopen).Close()
					require.NoError(t, err, "stand in for a later explicit reopen/restore; stale retry can never resurrect an ended or deleted admission scope")
				}
				var admitted int
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleOrdinaryAdmissionAfterAbandonProbe, orgID, uuid.New(), uuid.New()).Scan(&admitted), "ordinary input must check both logical and retained physical blockers")
				if tt.physical {
					require.Equal(t, 0, admitted, "logical supersession alone must not unblock an uncertain physical preparation")
					_, err = pool.Exec(ctx, `UPDATE probe_admission_claims SET physical_state='settled' WHERE org_id=$1 AND state='released'`, orgID)
					require.NoError(t, err, "stand in only for separate positive physical cleanup completion, not TTL expiry")
					require.NoError(t, pool.QueryRow(ctx, sessionLifecycleOrdinaryAdmissionAfterAbandonProbe, orgID, uuid.New(), uuid.New()).Scan(&admitted), "fresh ordinary input may retry after real blocker settlement")
				}
				require.Equal(t, tt.expectedAdmission, admitted, "fresh distinct ordinary input progresses after explicit obsolete-consumer closure and actual blockers settle, without replaying old answer or denial")
			}
		})
	}
}

func TestSessionLifecycleIdleSessionEndProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name                                         string
		abandon                                      bool
		expectedEnd                                  int
		expectedJob, expectedControl, expectedParent string
		expectedSlots                                int
	}{
		{name: "running parked session keeps existing idle only EndSession rejection", expectedJob: "awaiting_start_control", expectedControl: "awaiting_control", expectedParent: "running", expectedSlots: 1},
		{name: "reachable explicit abandon releases own claim then existing idle EndSession succeeds", abandon: true, expectedEnd: 1, expectedJob: "cancelled", expectedControl: "abandoned", expectedParent: "ended"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, requestID := newSessionLifecycleReadinessProbe(t, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			token, _ := prepareSessionLifecycleStartProbe(t, ctx, pool, orgID, requestID, now, false, false)
			state, _ := observeSessionLifecycleStartProbe(t, ctx, pool, orgID, token, now, "unavailable")
			require.Equal(t, "accepted_undelivered", state, "retain a committed answer with reachable durable parked controls")
			if tt.abandon {
				receiptID := uuid.New()
				_, err := pool.Exec(ctx, `INSERT INTO probe_accepted_input_controls SELECT $2,d.org_id,d.request_id,d.id,d.binding_token,control.id,d.control_version,'abandon','authorized' FROM probe_bound_dispatches d JOIN probe_start_controls control ON control.org_id=d.org_id AND control.dispatch_id=d.id WHERE d.org_id=$1`, orgID, receiptID)
				require.NoError(t, err, "record explicit user abandon before invoking idle-only session end")
				tx, err := pool.Begin(ctx)
				require.NoError(t, err, "begin exact dedicated abandonment and conditional logical claim release")
				defer func() { _ = tx.Rollback(ctx) }()
				var abandoned, released int
				require.NoError(t, tx.QueryRow(ctx, sessionLifecycleAbandonAcceptedInputProbe, orgID, receiptID).Scan(&abandoned), "explicit control may close positively known undelivered committed intent")
				require.NoError(t, tx.QueryRow(ctx, sessionLifecycleReleaseAbandonedClaimProbe, orgID, receiptID).Scan(&released), "safe exact settled claim release must make the parent idle without broadening EndSession eligibility")
				require.Equal(t, []int{1, 1}, []int{abandoned, released}, "abandon and eligible own-claim release must both occur exactly once")
				require.NoError(t, tx.Commit(ctx), "commit reachable user control and nonfailed idle projection before a separate session end")
			}
			before := sessionLifecycleControlStateProbe(t, ctx, pool, orgID)
			rows, err := pool.Query(ctx, `UPDATE sessions SET admission_epoch=admission_epoch+1,execution_status='ended' WHERE org_id=$1 AND execution_status='idle' RETURNING id`, orgID)
			require.NoError(t, err, "preserve the characterized idle-only EndSession predicate, never add parked idle-equivalent semantics")
			ended, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			require.NoError(t, err, "collect current idle-only end transition")
			require.Equal(t, tt.expectedEnd, len(ended), "a running parked claim must reject end until explicit safe abandonment actually makes parent idle")
			require.JSONEq(t, string(before), string(sessionLifecycleControlStateProbe(t, ctx, pool, orgID)), "session end must not alter committed request/grant/job/binding audit or resurrect old parked controls")
			var job, control, parent, request string
			var slots, grants int
			require.NoError(t, pool.QueryRow(ctx, `SELECT j.status,d.control_state,s.execution_status,q.status,capacity.running_slots,(SELECT count(*) FROM session_capability_grants g WHERE g.org_id=d.org_id AND g.request_id=d.request_id) FROM probe_bound_dispatches d JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id JOIN session_human_input_requests q ON q.org_id=d.org_id AND q.id=d.request_id JOIN probe_admission_capacity capacity ON capacity.org_id=d.org_id AND capacity.session_id=d.session_id WHERE d.org_id=$1`, orgID).Scan(&job, &control, &parent, &request, &slots, &grants), "inspect exact old consumer and current parent after the legal end flow")
			require.Equal(t, []any{tt.expectedJob, tt.expectedControl, tt.expectedParent, "answered", tt.expectedSlots, 1}, []any{job, control, parent, request, slots, grants}, "end preserves consent and grant while logical capacity reflects real control closure, not a new end bypass")
		})
	}
}

func observeSessionLifecycleStartProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, token uuid.UUID, now time.Time, disposition string) (string, uuid.UUID) {
	t.Helper()
	checkID, controlID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `DELETE FROM probe_resumability_checks WHERE org_id=$1 AND preparation_token=$2`, orgID, token)
	require.NoError(t, err, "replace only the bounded current selected verification projection")
	_, err = pool.Exec(ctx, `INSERT INTO probe_resumability_checks SELECT $3,p.org_id,p.token,p.attempt_id,p.job_lock_token,p.provider_type,p.provider_build,p.conversation_id,p.workdir_scope,
	 p.target_id,p.source_kind,p.source_ref,p.source_availability_version,p.workspace_generation,p.workspace_state,n.availability_version,$4,$5,'observed'
	 FROM probe_start_preparations p JOIN probe_bound_dispatches d ON d.org_id=p.org_id AND d.id=p.dispatch_id
	 LEFT JOIN probe_native_context_availability n ON n.org_id=d.org_id AND n.request_id=d.request_id WHERE p.org_id=$1 AND p.token=$2`, orgID, token, checkID, disposition, now.Add(time.Minute))
	require.NoError(t, err, "stand in for external selected native checking without actual provider or filesystem proof")
	var state, reason string
	var retry bool
	var contextJSON json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, sessionLifecycleAuthorizePreparedStartProbe, orgID, token, checkID, now, controlID).Scan(&state, &reason, &retry, &contextJSON), "consume only the current exact checked execution preparation or persist durable parked control")
	return state, controlID
}

func TestSessionLifecycleParkedStartControlProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, change, action string
		expectedCount        int
		expectedJob          string
	}{
		{name: "parked retry works minutes after revoked lease and preparation expire", action: "retry", expectedCount: 1, expectedJob: "pending"},
		{name: "parked retry does not need retained execution preparation row", change: `DELETE FROM probe_start_preparations`, action: "retry", expectedCount: 1, expectedJob: "pending"},
		{name: "parked abandon works minutes after preparation expiry", action: "abandon", expectedCount: 1, expectedJob: "cancelled"},
		{name: "parked abandon does not need old execution preparation row", change: `DELETE FROM probe_start_preparations`, action: "abandon", expectedCount: 1, expectedJob: "cancelled"},
		{name: "stale control version cannot retry", change: `UPDATE probe_bound_dispatches SET control_version=control_version+1`, action: "retry", expectedJob: "awaiting_start_control"},
		{name: "successor epoch cannot retry parked old consent", change: `UPDATE sessions SET admission_epoch=admission_epoch+1`, action: "retry", expectedJob: "awaiting_start_control"},
		{name: "changed bound generation cannot retry parked consent", change: `UPDATE sessions SET workspace_generation=workspace_generation+1`, action: "retry", expectedJob: "awaiting_start_control"},
		{name: "ended session cannot retry parked consent", change: `UPDATE sessions SET execution_status='ended'`, action: "retry", expectedJob: "awaiting_start_control"},
		{name: "foreign parked binding cannot retry", change: `UPDATE probe_start_controls SET binding_token=gen_random_uuid()`, action: "retry", expectedJob: "awaiting_start_control"},
		{name: "changed immutable job payload cannot retry", change: `UPDATE jobs SET payload='{"foreign_input":true}' WHERE id=(SELECT job_id FROM probe_bound_dispatches)`, action: "retry", expectedJob: "awaiting_start_control"},
		{name: "repeated park observation never refunds claim debit twice", action: "reobserve", expectedJob: "awaiting_start_control"},
		{name: "old lawful parked control excluded from claim reaper deadletter age failure and stale cleanup", action: "scans", expectedJob: "awaiting_start_control"},
		{name: "explicit parked retry obtains new preparation and same native launch identity", action: "retry_launch", expectedCount: 1, expectedJob: "running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, requestID := newSessionLifecycleReadinessProbe(t, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			token, launchContext := prepareSessionLifecycleStartProbe(t, ctx, pool, orgID, requestID, now, false, false)
			state, controlID := observeSessionLifecycleStartProbe(t, ctx, pool, orgID, token, now, "unavailable")
			require.Equal(t, "accepted_undelivered", state, "known unavailable context before Execute must persist durable control without changing consent")
			_, err := pool.Exec(ctx, `UPDATE probe_start_preparations SET expires_at=$2 WHERE org_id=$1`, orgID, now.Add(-10*time.Minute))
			require.NoError(t, err, "advance beyond the closed execution preparation TTL while retaining durable parked user-control authority")
			if tt.change != "" {
				_, err := pool.Exec(ctx, tt.change)
				require.NoError(t, err, "represent current parked control, source or ownership conditions at a later user action")
			}
			var dispatchID uuid.UUID
			var version int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT dispatch_id,control_version FROM probe_start_controls WHERE org_id=$1 AND id=$2`, orgID, controlID).Scan(&dispatchID, &version), "load durable parked authority independently of the expired execution preparation")
			var changed int
			switch tt.action {
			case "retry":
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleRetryParkedStartProbe, orgID, dispatchID, version).Scan(&changed), "retry must consume only current exact parked binding/native identity/generation and fenced no-launch proof")
			case "abandon":
				receiptID := uuid.New()
				_, err := pool.Exec(ctx, `INSERT INTO probe_accepted_input_controls SELECT $3,d.org_id,d.request_id,d.id,d.binding_token,control.id,control.control_version,'abandon','authorized' FROM probe_bound_dispatches d JOIN probe_start_controls control ON control.org_id=d.org_id AND control.dispatch_id=d.id WHERE d.org_id=$1 AND d.id=$2`, orgID, dispatchID, receiptID)
				require.NoError(t, err, "record explicit durable parked abandon authority after old lease/preparation expiry")
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleAbandonAcceptedInputProbe, orgID, receiptID).Scan(&changed), "abandon must remain reachable without an execution TTL or live worker lease")
			case "reobserve":
				repeated, _ := observeSessionLifecycleStartProbe(t, ctx, pool, orgID, token, now.Add(10*time.Minute), "unavailable")
				require.Equal(t, "recheck", repeated, "an already closed preparation is not fresh authority to repark or refund a debit")
			case "scans":
				var counts []int
				var jobID uuid.UUID
				require.NoError(t, pool.QueryRow(ctx, `SELECT job_id FROM probe_bound_dispatches WHERE org_id=$1 AND id=$2`, orgID, dispatchID).Scan(&jobID), "identify exact dedicated parked job for nonrunnable and logical-age predicates")
				require.NoError(t, pool.QueryRow(ctx, `WITH exact_parked AS (
				 SELECT d.session_id,d.thread_id FROM probe_bound_dispatches d JOIN probe_start_controls control ON control.org_id=d.org_id AND control.dispatch_id=d.id
				 JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id
				 JOIN session_threads t ON t.org_id=d.org_id AND t.session_id=d.session_id AND t.id=d.thread_id
				 WHERE d.org_id=$1 AND d.control_state='awaiting_control' AND control.status='awaiting_control' AND control.control_version=d.control_version
				  AND control.binding_token=d.binding_token AND j.status='awaiting_start_control' AND j.lock_token IS NULL
				  AND d.session_epoch=s.admission_epoch AND d.thread_epoch=t.admission_epoch AND d.workspace_generation=s.workspace_generation)
				 SELECT ARRAY[
				 (SELECT count(*)::int FROM jobs WHERE org_id=$1 AND status='pending'),
				 (SELECT count(*)::int FROM jobs WHERE org_id=$1 AND id=$2 AND status='running' AND locked_until<=$3),
				 (SELECT count(*)::int FROM jobs WHERE org_id=$1 AND status='failed' AND attempts>=3),
				 (SELECT count(*)::int FROM jobs WHERE org_id=$1 AND id=$2 AND status IN ('running','failed') AND locked_until<=$3),
				 (SELECT count(*)::int FROM exact_parked),
				 (SELECT count(*)::int FROM sessions s WHERE s.org_id=$1 AND s.execution_status='running' AND s.lifecycle_activated_at<$3-interval '5 minutes'),
				 (SELECT count(*)::int FROM sessions s WHERE s.org_id=$1 AND s.execution_status='running' AND s.lifecycle_activated_at<$3-interval '5 minutes' AND NOT EXISTS(SELECT 1 FROM exact_parked control WHERE control.session_id=s.id)),
				 (SELECT count(*)::int FROM session_threads t JOIN sessions s ON s.org_id=t.org_id AND s.id=t.session_id WHERE t.org_id=$1 AND t.execution_status='running' AND s.lifecycle_activated_at<$3-interval '5 minutes'),
				 (SELECT count(*)::int FROM session_threads t JOIN sessions s ON s.org_id=t.org_id AND s.id=t.session_id WHERE t.org_id=$1 AND t.execution_status='running' AND s.lifecycle_activated_at<$3-interval '5 minutes' AND NOT EXISTS(SELECT 1 FROM exact_parked control WHERE control.thread_id=t.id)),
				 (SELECT count(*)::int FROM probe_start_preparations p WHERE p.org_id=$1 AND p.status IN ('preparing','authorized') AND p.expires_at<=$3 AND NOT EXISTS(SELECT 1 FROM exact_parked control JOIN probe_bound_dispatches d ON d.org_id=p.org_id AND d.session_id=control.session_id WHERE d.id=p.dispatch_id))]`, orgID, jobID, now.Add(10*time.Minute)).Scan(&counts), "candidate generic age/cleanup scans must exclude exact lawful parked ownership; synthetic activation age stands in source updated-at age, not real caller proof")
				require.Equal(t, []int{0, 0, 0, 0, 1, 1, 0, 1, 0, 0}, counts, "old lawful park is nonrunnable but remains a competing consumer: raw status/age would match, exact park exclusions must prevent false failure and cleanup requeue")
				var ordinary int
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleOrdinaryAdmissionAfterAbandonProbe, orgID, uuid.New(), uuid.New()).Scan(&ordinary), "new ordinary intent may wait durably but cannot bypass a parked bound consumer")
				require.Equal(t, 0, ordinary, "parking alone must not admit a second input consumer or release the bound logical claim")
			case "retry_launch":
				newToken, sameContext := restartSessionLifecycleBoundStartProbe(t, ctx, pool, orgID, requestID, now.Add(10*time.Minute), launchContext)
				require.NotEqual(t, token, newToken, "explicit retry must obtain a fresh execution preparation rather than reuse old launch authority")
				verified, _ := observeSessionLifecycleStartProbe(t, ctx, pool, orgID, newToken, now.Add(10*time.Minute), "verified")
				require.Equal(t, "launch_permitted", verified, "a fresh selected check may permit only the same bound native context after durable explicit retry")
				var original, retried map[string]any
				require.NoError(t, json.Unmarshal(launchContext, &original), "decode original exact bound input/native launch identity")
				require.NoError(t, json.Unmarshal(sameContext, &retried), "decode retried launch identity with refreshed current lease")
				delete(original, "job_lock_token")
				delete(retried, "job_lock_token")
				require.Equal(t, original, retried, "retry may refresh a lease, never job payload, grant, input binding, allocation first-marker history or native conversation")
				changed = 1
			}
			require.Equal(t, tt.expectedCount, changed, "only authorized same-binding current parked controls may transition the dedicated job")
			var status string
			var attempts int
			var lockToken *uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT j.status,j.attempts,j.lock_token FROM jobs j JOIN probe_bound_dispatches d ON d.org_id=j.org_id AND d.job_id=j.id WHERE d.org_id=$1 AND d.id=$2`, orgID, dispatchID).Scan(&status, &attempts, &lockToken), "inspect durable parked execution metadata after later control action")
			require.Equal(t, tt.expectedJob, status, "control authority must not depend on an expired preparation or silently make parked jobs runnable")
			expectedAttempts := 0
			if tt.action == "retry_launch" {
				expectedAttempts = 1
			}
			require.Equal(t, expectedAttempts, attempts, "parking refunds the owned claim debit once; explicit retry does not spend failure budget")
			if tt.action != "retry_launch" {
				require.Nil(t, lockToken, "parked/pending/abandoned control must have no live execution lease")
			}
		})
	}
}

func TestSessionLifecycleProviderLaunchMarkerProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, change              string
		mark, followup, reconcile bool
		expectedMarks             []int
		expectedState             string
	}{
		{name: "prepared setup alone is not final permit", change: "no_final_permit", mark: true, expectedMarks: []int{0}, expectedState: "known_not_launched"},
		{name: "fresh final permit requires winning first marker before Execute", mark: true, expectedMarks: []int{1, 0}, expectedState: "provider_launch_marked"},
		{name: "distinct authorized followup needs next ordinal and preserves FIRST marker", mark: true, followup: true, expectedMarks: []int{1, 0, 1}, expectedState: "provider_launch_marked"},
		{name: "revoked lease rejects invocation marker", mark: true, change: `UPDATE jobs SET lock_token=gen_random_uuid() WHERE id=(SELECT job_id FROM probe_bound_dispatches)`, expectedMarks: []int{0}, expectedState: "known_not_launched"},
		{name: "successor epoch rejects invocation marker", mark: true, change: `UPDATE sessions SET admission_epoch=admission_epoch+1`, expectedMarks: []int{0}, expectedState: "known_not_launched"},
		{name: "new allocation cannot wash prior same binding FIRST marker uncertainty", mark: true, change: "prior_allocation", expectedMarks: []int{0}, expectedState: "delivery_unknown"},
		{name: "crash after allocation and final permit before FIRST marker reconciles no launch", reconcile: true, expectedState: "known_not_launched"},
		{name: "crash after FIRST marker before Execute remains unknown", mark: true, reconcile: true, expectedMarks: []int{1, 0}, expectedState: "delivery_unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, requestID := newSessionLifecycleReadinessProbe(t, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			token, _ := prepareSessionLifecycleStartProbe(t, ctx, pool, orgID, requestID, now, false, false)
			if tt.change != "no_final_permit" {
				state, _ := observeSessionLifecycleStartProbe(t, ctx, pool, orgID, token, now, "verified")
				require.Equal(t, "launch_permitted", state, "only final checked authorization may enable running effects or an invocation marker")
			}
			if tt.change == "prior_allocation" {
				_, err := pool.Exec(ctx, `INSERT INTO probe_start_attempts SELECT gen_random_uuid(),a.org_id,a.dispatch_id,a.job_id,gen_random_uuid(),'delivery_unknown',gen_random_uuid(),1,gen_random_uuid(),'resume_input' FROM probe_start_attempts a WHERE a.org_id=$1`, orgID)
				require.NoError(t, err, "retain a prior unresolved same-binding allocation receipt while a replacement allocation has no local marker")
				state, _ := observeSessionLifecycleStartProbe(t, ctx, pool, orgID, token, now, "verified")
				require.Equal(t, "delivery_unknown", state, "renewed verification or an unmarked replacement allocation must not erase possible prior bound-input acceptance")
			} else if tt.change != "" && tt.change != "no_final_permit" {
				_, err := pool.Exec(ctx, tt.change)
				require.NoError(t, err, "represent authority changing before invocation marking")
			}
			first := uuid.New()
			var marks []int
			if tt.mark {
				var marked int
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleMarkProviderInvocationProbe, orgID, token, first, 0, token, "resume_input", now).Scan(&marked), "every Execute needs a fresh exact current permit/lease/expected-ordinal marker CAS")
				marks = append(marks, marked)
				if marked == 1 {
					require.NoError(t, pool.QueryRow(ctx, sessionLifecycleMarkProviderInvocationProbe, orgID, token, first, 0, token, "resume_input", now).Scan(&marked), "replaying an already-written marker or unknown commit cannot authorize another Execute")
					marks = append(marks, marked)
				}
			}
			if tt.followup {
				authority, nonce := uuid.New(), uuid.New()
				_, err := pool.Exec(ctx, `UPDATE probe_start_preparations SET invocation_authority_token=$3,invocation_purpose='turn_followup' WHERE org_id=$1 AND token=$2`, orgID, token, authority)
				require.NoError(t, err, "stand in for a distinct trusted logical followup authorization, not replay of bound input")
				var marked int
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleMarkProviderInvocationProbe, orgID, token, nonce, 1, authority, "turn_followup", now).Scan(&marked), "a separate authorized invocation must win its own next-ordinal nonce CAS")
				marks = append(marks, marked)
			}
			require.Equal(t, tt.expectedMarks, marks, "final permit is not Execute, and every actual invocation requires a distinct winning marker")
			if tt.reconcile {
				var lease uuid.UUID
				require.NoError(t, pool.QueryRow(ctx, `SELECT job_lock_token FROM probe_start_attempts WHERE org_id=$1`, orgID).Scan(&lease), "capture exact expired claim identity to revoke before interpreting marker absence")
				var reconciled int
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleReconcileExpiredStartProbe, orgID, token, lease, now.Add(10*time.Minute), uuid.New()).Scan(&reconciled), "expired-start reconciliation must revoke the exact old permit/lease before classifying first-marker absence")
				require.Equal(t, 1, reconciled, "reconciliation must durably fence one exact expired execution preparation")
				var lateMarker int
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleMarkProviderInvocationProbe, orgID, token, uuid.New(), 0, token, "resume_input", now).Scan(&lateMarker), "a fenced old worker must not race a new marker into the known-unlaunched recovery proof")
				require.Equal(t, 0, lateMarker, "revoking the old permit/lease must make late provider entry impossible through the candidate CAS")
			}
			var execution string
			var firstMarker *uuid.UUID
			var ordinal int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT a.execution_state,a.first_provider_launch_marker,a.launch_ordinal FROM probe_start_attempts a JOIN probe_start_preparations p ON p.org_id=a.org_id AND p.attempt_id=a.id WHERE a.org_id=$1 AND p.token=$2`, orgID, token).Scan(&execution, &firstMarker, &ordinal), "inspect current allocation fields without discarding any prior same-binding FIRST receipt")
			require.Equal(t, tt.expectedState, execution, "FIRST marker presence conservatively preserves provider-attempt uncertainty even before Execute actually begins")
			var expectedFirst *uuid.UUID
			var expectedOrdinal int64
			if len(marks) > 0 && marks[0] == 1 {
				expectedFirst = &first
				expectedOrdinal = 1
				if tt.followup {
					expectedOrdinal = 2
				}
			}
			require.Equal(t, expectedFirst, firstMarker, "later invocation/reconciliation must never clear or replace FIRST provider-entry history")
			require.Equal(t, expectedOrdinal, ordinal, "only fresh winning invocation CAS operations advance the bounded launch ordinal")
		})
	}
}

func TestSessionLifecyclePossibleAcceptanceProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, evidence string
		prior          bool
	}{
		{name: "current marker-free delivery uncertainty cannot become unsent", evidence: "delivery_unknown"},
		{name: "current marker-free provider entry cannot become unsent", evidence: "provider_launched"},
		{name: "prior marker-free delivery uncertainty survives replacement allocation", evidence: "delivery_unknown", prior: true},
		{name: "prior marker-free provider entry survives replacement allocation", evidence: "provider_launched", prior: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, requestID := newSessionLifecycleReadinessProbe(t, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			token, _ := prepareSessionLifecycleStartProbe(t, ctx, pool, orgID, requestID, now, false, false)
			state, _ := observeSessionLifecycleStartProbe(t, ctx, pool, orgID, token, now, "verified")
			require.Equal(t, "launch_permitted", state, "fixture must hold a real proposed final permit before unresolved provider evidence appears")
			if tt.prior {
				_, err := pool.Exec(ctx, `INSERT INTO probe_start_attempts(id,org_id,dispatch_id,job_id,job_lock_token,execution_state,first_provider_launch_marker) SELECT gen_random_uuid(),a.org_id,a.dispatch_id,a.job_id,gen_random_uuid(),$2,NULL FROM probe_start_attempts a WHERE a.org_id=$1`, orgID, tt.evidence)
				require.NoError(t, err, "retain unresolved prior same-binding allocation evidence even though it predates FIRST markers")
			} else {
				_, err := pool.Exec(ctx, `UPDATE probe_start_attempts SET execution_state=$2,first_provider_launch_marker=NULL WHERE org_id=$1`, orgID, tt.evidence)
				require.NoError(t, err, "represent normalized legacy current provider-entry/delivery uncertainty with no marker")
			}
			var marked int
			require.NoError(t, pool.QueryRow(ctx, sessionLifecycleMarkProviderInvocationProbe, orgID, token, uuid.New(), 0, token, "resume_input", now).Scan(&marked), "invocation CAS must inspect current and all prior possible-acceptance evidence, not only FIRST UUIDs")
			require.Equal(t, 0, marked, "unresolved marker-free provider evidence must block new invocation even under a previously authorized permit")
			state, _ = observeSessionLifecycleStartProbe(t, ctx, pool, orgID, token, now, "verified")
			require.Equal(t, "delivery_unknown", state, "a fresh native check or new allocation cannot convert possible acceptance into known not launched")
			var lease uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT a.job_lock_token FROM probe_start_attempts a JOIN probe_start_preparations p ON p.org_id=a.org_id AND p.attempt_id=a.id WHERE p.org_id=$1 AND p.token=$2`, orgID, token).Scan(&lease), "capture exact current claim for fenced expired-start reconciliation")
			controlID := uuid.New()
			var reconciled int
			require.NoError(t, pool.QueryRow(ctx, sessionLifecycleReconcileExpiredStartProbe, orgID, token, lease, now.Add(10*time.Minute), controlID).Scan(&reconciled), "reconcile must revoke old authority without laundering marker-free uncertainty into no-launch proof")
			require.Equal(t, 1, reconciled, "exact expired current start should be fenced once while preserving acceptance uncertainty")
			var dispatchID uuid.UUID
			var version int64
			var jobStatus, controlStatus, execution string
			var attempts int
			var refunded bool
			var proof *uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT d.id,d.control_version,j.status,j.attempts,j.claim_debit_refunded,control.status,control.no_launch_proof_token,a.execution_state FROM probe_bound_dispatches d JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id JOIN probe_start_controls control ON control.org_id=d.org_id AND control.dispatch_id=d.id JOIN probe_start_attempts a ON a.org_id=d.org_id AND a.id=control.attempt_id WHERE d.org_id=$1`, orgID).Scan(&dispatchID, &version, &jobStatus, &attempts, &refunded, &controlStatus, &proof, &execution), "inspect durable uncertainty after revocation, including claim debit and absence of unsent-control proof")
			require.Equal(t, []string{"awaiting_start_control", "delivery_unknown", "delivery_unknown"}, []string{jobStatus, controlStatus, execution}, "possible provider entry must remain nonrunnable delivery uncertainty, never known unlaunched control")
			require.Equal(t, 1, attempts, "uncertain provider attempts must not receive a no-launch claim refund")
			require.False(t, refunded, "marker-free uncertainty must retain its charged claim debit")
			require.Nil(t, proof, "no positive no-launch proof may be synthesized from FIRST absence")
			before := sessionLifecycleAcceptedIntentProbe(t, ctx, pool, orgID, requestID)
			var retried int
			require.NoError(t, pool.QueryRow(ctx, sessionLifecycleRetryParkedStartProbe, orgID, dispatchID, version).Scan(&retried), "retry must reject uncertain controls even when a replacement allocation has no FIRST marker")
			require.Equal(t, 0, retried, "uncertain bound input must never auto retry as unsent")
			receipt := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO probe_accepted_input_controls SELECT $3,d.org_id,d.request_id,d.id,d.binding_token,control.id,control.control_version,'abandon','authorized' FROM probe_bound_dispatches d JOIN probe_start_controls control ON control.org_id=d.org_id AND control.dispatch_id=d.id WHERE d.org_id=$1 AND d.id=$2`, orgID, dispatchID, receipt)
			require.NoError(t, err, "attempt exact user control without granting no-launch authority")
			var abandoned int
			require.NoError(t, pool.QueryRow(ctx, sessionLifecycleAbandonAcceptedInputProbe, orgID, receipt).Scan(&abandoned), "abandon-as-unsent must inspect all unresolved same-binding provider evidence")
			require.Equal(t, 0, abandoned, "possibly accepted intent cannot be terminalized as known undelivered")
			require.JSONEq(t, string(before), string(sessionLifecycleAcceptedIntentProbe(t, ctx, pool, orgID, requestID)), "failed uncertainty controls preserve committed consent/grant/payload/inbox/job budget exactly")
		})
	}
}

// Proposed sum type only: there are no three optional outcome pointers and no
// exported concrete variants. Real constructors and Match integration remain 0a.
type sessionLifecycleStartResultProbe interface{ sealedStartResultProbe() }
type sessionLifecyclePreparedProbe struct{ token uuid.UUID }
type sessionLifecycleDeferredProbe struct{ reason string }
type sessionLifecycleReadyProbe struct{ permit uuid.UUID }

func (sessionLifecyclePreparedProbe) sealedStartResultProbe() {}
func (sessionLifecycleDeferredProbe) sealedStartResultProbe() {}
func (sessionLifecycleReadyProbe) sealedStartResultProbe()    {}

func sessionLifecycleMatchStartResultProbe(value sessionLifecycleStartResultProbe) (string, []string, error) {
	switch result := value.(type) {
	case sessionLifecyclePreparedProbe:
		if result.token != uuid.Nil {
			return "prepared", []string{}, nil
		}
	case sessionLifecycleDeferredProbe:
		if result.reason != "" {
			return "deferred", []string{}, nil
		}
	case sessionLifecycleReadyProbe:
		if result.permit != uuid.Nil {
			return "ready", []string{"slack_running", "eval_running"}, nil
		}
	}
	return "invalid", nil, errors.New("invalid nil or zero StartResult")
}

func newSessionLifecyclePreparedProbe(token uuid.UUID) (sessionLifecycleStartResultProbe, error) {
	value := sessionLifecyclePreparedProbe{token: token}
	_, _, err := sessionLifecycleMatchStartResultProbe(value)
	if err != nil {
		return nil, err
	}
	return value, nil
}
func newSessionLifecycleDeferredProbe(reason string) (sessionLifecycleStartResultProbe, error) {
	value := sessionLifecycleDeferredProbe{reason: reason}
	_, _, err := sessionLifecycleMatchStartResultProbe(value)
	if err != nil {
		return nil, err
	}
	return value, nil
}
func newSessionLifecycleReadyProbe(permit uuid.UUID) (sessionLifecycleStartResultProbe, error) {
	value := sessionLifecycleReadyProbe{permit: permit}
	_, _, err := sessionLifecycleMatchStartResultProbe(value)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func TestSessionLifecycleSealedStartResultProbe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		make            func() (sessionLifecycleStartResultProbe, error)
		expectedVariant string
		expectedEffects []string
		expectedError   string
	}{
		{name: "valid prepared is setup only", make: func() (sessionLifecycleStartResultProbe, error) { return newSessionLifecyclePreparedProbe(uuid.New()) }, expectedVariant: "prepared", expectedEffects: []string{}},
		{name: "valid deferred has no running effects", make: func() (sessionLifecycleStartResultProbe, error) {
			return newSessionLifecycleDeferredProbe("retryable_io")
		}, expectedVariant: "deferred", expectedEffects: []string{}},
		{name: "only final ready permit selects Slack and eval running effects", make: func() (sessionLifecycleStartResultProbe, error) { return newSessionLifecycleReadyProbe(uuid.New()) }, expectedVariant: "ready", expectedEffects: []string{"slack_running", "eval_running"}},
		{name: "zero preparation constructor rejected", make: func() (sessionLifecycleStartResultProbe, error) { return newSessionLifecyclePreparedProbe(uuid.Nil) }, expectedVariant: "invalid", expectedError: "invalid nil or zero StartResult"},
		{name: "empty deferred constructor rejected", make: func() (sessionLifecycleStartResultProbe, error) { return newSessionLifecycleDeferredProbe("") }, expectedVariant: "invalid", expectedError: "invalid nil or zero StartResult"},
		{name: "zero permit constructor rejected", make: func() (sessionLifecycleStartResultProbe, error) { return newSessionLifecycleReadyProbe(uuid.Nil) }, expectedVariant: "invalid", expectedError: "invalid nil or zero StartResult"},
		{name: "nil sum value rejected", make: func() (sessionLifecycleStartResultProbe, error) { return nil, nil }, expectedVariant: "invalid", expectedError: "invalid nil or zero StartResult"},
		{name: "typed nil variant rejected", make: func() (sessionLifecycleStartResultProbe, error) {
			var value *sessionLifecycleReadyProbe
			return value, nil
		}, expectedVariant: "invalid", expectedError: "invalid nil or zero StartResult"},
		{name: "zero concrete value cannot bypass validating Match", make: func() (sessionLifecycleStartResultProbe, error) { return sessionLifecyclePreparedProbe{}, nil }, expectedVariant: "invalid", expectedError: "invalid nil or zero StartResult"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			value, constructorErr := tt.make()
			variant, effects, err := sessionLifecycleMatchStartResultProbe(value)
			if constructorErr != nil {
				err = constructorErr
			}
			require.Equal(t, tt.expectedVariant, variant, "sealed Match must select one complete validated outcome, never optional pointer combinations")
			require.Equal(t, tt.expectedEffects, effects, "synthetic effect selection allows running Slack/eval effects only after final ready permit, not preparation")
			if tt.expectedError != "" {
				require.EqualError(t, err, tt.expectedError, "nil/zero/typed-nil results must be rejected explicitly")
			} else {
				require.NoError(t, err, "valid sum-type outcome should be exhaustive and usable")
			}
		})
	}
}

// A trusted conformance receipt is a synthetic stand-in for actual pinned-build
// adapter tests of VerifyResumable/ResumeIdentity, not a caller capability bool.
// No adapter test or provider activation is implemented by this policy probe.
const sessionLifecycleActivateResumeCapabilityProbe = `UPDATE probe_resume_capabilities capability
 SET activation_state='active',activation_receipt_id=receipt.id FROM probe_resume_conformance_receipts receipt
 WHERE capability.org_id=$1 AND capability.provider_type=$2 AND capability.provider_build=$3 AND capability.resume_identity_schema=$4
  AND capability.activation_state='deferred' AND receipt.org_id=capability.org_id AND receipt.id=$5
  AND receipt.provider_type=capability.provider_type AND receipt.provider_build=capability.provider_build AND receipt.resume_identity_schema=capability.resume_identity_schema
  AND receipt.adapter_entrypoint='VerifyResumable' AND receipt.suite='resume_identity_and_failure_contract_v1'
  AND receipt.implementation_revision<>'' AND receipt.outcome='passed' RETURNING capability.activation_state`

func TestSessionLifecycleResumeCapabilityGateProbePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, receiptBuild, identity, outcome, entrypoint, initial, expected string
		expectedChanges                                                      int
	}{
		{name: "tested pinned provider build and ResumeIdentity activates", receiptBuild: "provider@1", identity: "native_identity_v1", outcome: "passed", entrypoint: "VerifyResumable", initial: "deferred", expected: "active", expectedChanges: 1},
		{name: "unsupported preactivation remains deferred", receiptBuild: "provider@1", identity: "native_identity_v1", outcome: "unsupported", entrypoint: "VerifyResumable", initial: "deferred", expected: "deferred"},
		{name: "different CLI build is not activation proof", receiptBuild: "provider@2", identity: "native_identity_v1", outcome: "passed", entrypoint: "VerifyResumable", initial: "deferred", expected: "deferred"},
		{name: "different ResumeIdentity variant is not proof", receiptBuild: "provider@1", identity: "foreign_identity_v2", outcome: "passed", entrypoint: "VerifyResumable", initial: "deferred", expected: "deferred"},
		{name: "unrelated provider probe is not VerifyResumable conformance", receiptBuild: "provider@1", identity: "native_identity_v1", outcome: "passed", entrypoint: "Health", initial: "deferred", expected: "deferred"},
		{name: "active build cannot silently downgrade on unexpected unsupported", receiptBuild: "provider@1", identity: "native_identity_v1", outcome: "unsupported", entrypoint: "VerifyResumable", initial: "active", expected: "active"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, _ := newSessionLifecycleReadinessProbe(t, time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := pool.Exec(ctx, `CREATE TABLE probe_resume_capabilities(org_id uuid,provider_type text,provider_build text,resume_identity_schema text,activation_state text,activation_receipt_id uuid);
 CREATE TABLE probe_resume_conformance_receipts(id uuid PRIMARY KEY,org_id uuid,provider_type text,provider_build text,resume_identity_schema text,adapter_entrypoint text,suite text,implementation_revision text,outcome text);`)
			require.NoError(t, err, "create bounded proposed provider/build/identity activation and trusted test-receipt projections only")
			receiptID := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO probe_resume_capabilities VALUES($1,'provider','provider@1','native_identity_v1',$2,NULL)`, orgID, tt.initial)
			require.NoError(t, err, "seed selected provider/build activation state")
			_, err = pool.Exec(ctx, `INSERT INTO probe_resume_conformance_receipts VALUES($1,$2,'provider',$3,$4,$5,'resume_identity_and_failure_contract_v1','synthetic-adapter-revision',$6)`, receiptID, orgID, tt.receiptBuild, tt.identity, tt.entrypoint, tt.outcome)
			require.NoError(t, err, "project exact adapter conformance evidence rather than caller-supplied support boolean")
			rows, err := pool.Query(ctx, sessionLifecycleActivateResumeCapabilityProbe, orgID, "provider", "provider@1", "native_identity_v1", receiptID)
			require.NoError(t, err, "activate only matched passed pinned-build native-resume conformance")
			values, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err, "collect exact bounded activation transition")
			expectedChanges := []string{}
			if tt.expectedChanges == 1 {
				expectedChanges = []string{"active"}
			}
			require.Equal(t, expectedChanges, values, "unsupported or foreign build/identity evidence must not activate or downgrade a v1 provider")
			var actual string
			require.NoError(t, pool.QueryRow(ctx, `SELECT activation_state FROM probe_resume_capabilities WHERE org_id=$1`, orgID).Scan(&actual), "inspect selected provider activation after capability policy probe")
			require.Equal(t, tt.expected, actual, "v0 unsupported is explicitly deferred; active v1 cannot fall back to old unverified behavior")
		})
	}
}

// Candidate image routing is distinct from logical activation: already-active
// sessions must keep the known compatible image until the new exact build tuple
// has passed its trusted conformance suite. No deployment command runs here.
const sessionLifecyclePinnedBuildRolloutProbe = `UPDATE probe_provider_images image SET routable=true
 WHERE image.org_id=$1 AND image.id=$2 AND NOT image.routable
  AND EXISTS(SELECT 1 FROM probe_serving_resume_cohorts cohort WHERE cohort.org_id=image.org_id AND cohort.provider_type=image.provider_type)
  AND NOT EXISTS(SELECT 1 FROM probe_serving_resume_cohorts cohort WHERE cohort.org_id=image.org_id AND cohort.provider_type=image.provider_type
   AND NOT EXISTS(SELECT 1 FROM probe_image_conformance_receipts receipt WHERE receipt.org_id=image.org_id AND receipt.provider_type=image.provider_type
    AND receipt.provider_build=image.provider_build AND receipt.image_digest=image.image_digest AND receipt.resume_identity_schema=cohort.resume_identity_schema
    AND receipt.entrypoint='VerifyResumable' AND receipt.suite='resume_identity_and_failure_contract_v1' AND receipt.implementation_revision<>'' AND receipt.outcome='passed')) RETURNING image.id`

func TestSessionLifecyclePinnedBuildRolloutProbePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, build, image, identity, outcome string
		expected                              bool
	}{
		{name: "exact tested candidate image tuple may become routable", build: "provider@2", image: "synthetic-image-v2", identity: "native_identity_v1", outcome: "passed", expected: true},
		{name: "old tested CLI build cannot authorize new CLI image", build: "provider@1", image: "synthetic-image-v2", identity: "native_identity_v1", outcome: "passed"},
		{name: "different image digest cannot authorize candidate", build: "provider@2", image: "synthetic-image-other", identity: "native_identity_v1", outcome: "passed"},
		{name: "unsupported candidate keeps old compatible image serving", build: "provider@2", image: "synthetic-image-v2", identity: "native_identity_v1", outcome: "unsupported"},
		{name: "wrong ResumeIdentity variant cannot authorize rollout", build: "provider@2", image: "synthetic-image-v2", identity: "another_identity_v2", outcome: "passed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, _ := newSessionLifecycleReadinessProbe(t, time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := pool.Exec(ctx, `CREATE TABLE probe_provider_images(id uuid PRIMARY KEY,org_id uuid,provider_type text,provider_build text,image_digest text,routable boolean);
 CREATE TABLE probe_serving_resume_cohorts(org_id uuid,provider_type text,resume_identity_schema text);
 CREATE TABLE probe_image_conformance_receipts(org_id uuid,provider_type text,provider_build text,image_digest text,resume_identity_schema text,entrypoint text,suite text,implementation_revision text,outcome text)`)
			require.NoError(t, err, "create synthetic routing tuple and trusted build-receipt projections only")
			oldID, candidateID := uuid.New(), uuid.New()
			batch := &pgx.Batch{}
			batch.Queue(`INSERT INTO probe_provider_images VALUES($1,$2,'provider','provider@1','synthetic-image-v1',true),($3,$2,'provider','provider@2','synthetic-image-v2',false)`, oldID, orgID, candidateID)
			batch.Queue(`INSERT INTO probe_serving_resume_cohorts VALUES($1,'provider','native_identity_v1')`, orgID)
			batch.Queue(`INSERT INTO probe_image_conformance_receipts VALUES($1,'provider',$2,$3,$4,'VerifyResumable','resume_identity_and_failure_contract_v1','synthetic-adapter-revision',$5)`, orgID, tt.build, tt.image, tt.identity, tt.outcome)
			require.NoError(t, pool.SendBatch(ctx, batch).Close(), "seed old compatible serving image and candidate evidence without changing any active session to v0")
			rows, err := pool.Query(ctx, sessionLifecyclePinnedBuildRolloutProbe, orgID, candidateID)
			require.NoError(t, err, "routing a new CLI image must first prove each active provider/build/ResumeIdentity cohort at its exact tested image")
			actual, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			require.NoError(t, err, "collect candidate image routing transition")
			expected := []uuid.UUID{}
			if tt.expected {
				expected = []uuid.UUID{candidateID}
			}
			require.Equal(t, expected, actual, "unsupported or foreign tuple evidence must leave the candidate unroutable")
			var imageState json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object('old_routable',(SELECT routable FROM probe_provider_images WHERE org_id=$1 AND id=$2),'candidate_routable',(SELECT routable FROM probe_provider_images WHERE org_id=$1 AND id=$3))`, orgID, oldID, candidateID).Scan(&imageState), "inspect both old and candidate routing state")
			expectedState, err := json.Marshal(map[string]bool{"old_routable": true, "candidate_routable": tt.expected})
			require.NoError(t, err, "construct exact expected serving image state")
			require.JSONEq(t, string(expectedState), string(imageState), "candidate rejection must preserve serving old compatible image rather than downgrade activated semantics")
		})
	}
}

// Fresh attempt-scoped upload receipts and exact-reference publication/retention
// CAS only. No storage upload/delete, real publication fanout or GC runs here.
const sessionLifecyclePublishSnapshotProbe = `UPDATE sessions s SET snapshot_key=upload.snapshot_key,snapshot_availability='available',snapshot_availability_version=s.snapshot_availability_version+1
 ,pending_upload_receipt_id=NULL,pending_snapshot_key=NULL,pending_upload_token=NULL,pending_upload_version=s.pending_upload_version+1
 FROM probe_snapshot_upload_receipts upload JOIN probe_snapshot_objects object ON object.org_id=upload.org_id AND object.snapshot_key=upload.snapshot_key AND object.upload_receipt_id=upload.id
 WHERE s.org_id=$1 AND upload.org_id=s.org_id AND upload.session_id=s.id AND upload.id=$2 AND upload.status='uploaded'
  AND upload.snapshot_key='snapshots/'||upload.attempt_token::text||'/'||upload.id::text
  AND s.pending_upload_receipt_id=upload.id AND s.pending_snapshot_key=upload.snapshot_key
  AND s.pending_upload_token=upload.reservation_token AND s.pending_upload_version=upload.pending_version
  AND s.snapshot_key IS NOT DISTINCT FROM upload.expected_key AND s.snapshot_availability_version=upload.expected_version
  AND s.workspace_generation=upload.workspace_generation AND s.workspace_revision=upload.workspace_revision RETURNING s.snapshot_key`
const sessionLifecycleClearPendingSnapshotProbe = `UPDATE sessions s SET pending_upload_receipt_id=NULL,pending_snapshot_key=NULL,pending_upload_token=NULL,pending_upload_version=s.pending_upload_version+1
 FROM probe_snapshot_upload_receipts upload WHERE s.org_id=$1 AND upload.org_id=s.org_id AND upload.session_id=s.id AND upload.id=$2
  AND s.pending_upload_receipt_id=upload.id AND s.pending_snapshot_key=upload.snapshot_key AND s.pending_upload_token=upload.reservation_token AND s.pending_upload_version=upload.pending_version
 RETURNING s.pending_upload_version`
const sessionLifecycleRetainSnapshotProbe = `UPDATE sessions s SET snapshot_key=CASE WHEN authority.action='clear' THEN NULL ELSE s.snapshot_key END,
 snapshot_availability=CASE WHEN authority.action='clear' THEN 'missing' ELSE 'expired' END,snapshot_availability_version=s.snapshot_availability_version+1
 FROM probe_snapshot_retention_authorities authority WHERE s.org_id=$1 AND authority.org_id=s.org_id AND authority.session_id=s.id AND authority.id=$2
  AND authority.kind='RetentionAuthority' AND authority.status='authorized' AND s.snapshot_key=authority.snapshot_key AND s.snapshot_availability_version=authority.expected_version
  AND (authority.action='expire' OR authority.action='clear' AND s.snapshot_availability='expired') RETURNING s.snapshot_availability`

func newSessionLifecycleSnapshotPublicationProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	receipt, attempt := uuid.New(), uuid.New()
	key := "snapshots/" + attempt.String() + "/" + receipt.String()
	reservation := uuid.New()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "reserve upload and its durable source receipt in one transaction before synthetic Save")
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE sessions SET pending_upload_receipt_id=$2,pending_snapshot_key=$3,pending_upload_token=$4,pending_upload_version=pending_upload_version+1 WHERE org_id=$1`, orgID, receipt, key, reservation)
	require.NoError(t, err, "reserve a fresh current upload receipt/key/token/version before starting the synthetic upload")
	_, err = tx.Exec(ctx, `INSERT INTO probe_snapshot_upload_receipts SELECT $2,s.org_id,s.id,$3,$4,s.snapshot_key,s.snapshot_availability_version,s.workspace_generation,s.workspace_revision,'uploading',s.pending_upload_token,s.pending_upload_version FROM sessions s WHERE s.org_id=$1`, orgID, receipt, attempt, key)
	require.NoError(t, err, "persist exact publication prestate and fresh candidate source before any object can exist")
	require.NoError(t, tx.Commit(ctx), "commit durable upload source receipt before leaving the transaction for hypothetical Save")
	_, err = pool.Exec(ctx, `INSERT INTO probe_snapshot_objects VALUES($1,$2,$3,'synthetic-capture')`, orgID, key, receipt)
	require.NoError(t, err, "store one fresh attempt/candidate-scoped synthetic object without overwriting a prior key")
	_, err = pool.Exec(ctx, `UPDATE probe_snapshot_upload_receipts SET status='uploaded' WHERE org_id=$1 AND id=$2 AND status='uploading'`, orgID, receipt)
	require.NoError(t, err, "record successful synthetic Save only on its pre-existing durable source receipt; actual storage I/O remains unproven")
	return receipt, key
}

// Finite control fields on a pre-Save exact-key cleanup job, not ListObjects,
// a new global object ledger or request pins. Source references below project
// the real current/preparation/parked joins; retention and IO remain 0a work.
const sessionLifecycleTombstoneSnapshotObjectProbe = `
WITH candidate AS MATERIALIZED (
 SELECT cleanup.id FROM probe_snapshot_cleanup_jobs cleanup JOIN sessions s ON s.org_id=cleanup.org_id AND s.id=cleanup.session_id
 WHERE cleanup.org_id=$1 AND cleanup.id=$2 AND cleanup.object_state='live' AND cleanup.delete_state='waiting'
  AND cleanup.writer_state='settled' AND cleanup.upload_deadline<=$3::timestamptz
  AND ((cleanup.superseded_at IS NOT NULL AND cleanup.superseded_at+interval '24 hours'<=$3::timestamptz)
   OR (cleanup.superseded_at IS NULL AND cleanup.upload_deadline+interval '24 hours'<=$3::timestamptz))
  AND s.snapshot_key IS DISTINCT FROM cleanup.snapshot_key AND s.pending_snapshot_key IS DISTINCT FROM cleanup.snapshot_key
  AND NOT EXISTS(SELECT 1 FROM probe_snapshot_source_references ref WHERE ref.org_id=cleanup.org_id AND ref.snapshot_key=cleanup.snapshot_key AND ref.active)
 FOR UPDATE OF s,cleanup
) UPDATE probe_snapshot_cleanup_jobs cleanup SET object_state='tombstoned',delete_state='pending',delete_token=$4,control_version=control_version+1
 FROM candidate c WHERE cleanup.org_id=$1 AND cleanup.id=c.id RETURNING cleanup.snapshot_key`

const sessionLifecycleBindSnapshotSourceProbe = `
WITH candidate AS MATERIALIZED (
 SELECT cleanup.org_id,cleanup.snapshot_key FROM probe_snapshot_cleanup_jobs cleanup JOIN sessions s ON s.org_id=cleanup.org_id AND s.id=cleanup.session_id
 WHERE cleanup.org_id=$1 AND cleanup.id=$2 AND cleanup.object_state='live' FOR UPDATE OF s,cleanup
) INSERT INTO probe_snapshot_source_references SELECT c.org_id,c.snapshot_key,$3,true FROM candidate c RETURNING snapshot_key`

const sessionLifecycleCompleteSnapshotDeleteProbe = `
WITH authority AS MATERIALIZED (
 SELECT cleanup.* FROM probe_snapshot_cleanup_jobs cleanup JOIN sessions s ON s.org_id=cleanup.org_id AND s.id=cleanup.session_id
 WHERE cleanup.org_id=$1 AND cleanup.id=$2 AND cleanup.delete_token=$3 AND cleanup.object_state='tombstoned' AND cleanup.delete_state='pending'
  AND cleanup.writer_state='settled' AND s.snapshot_key IS DISTINCT FROM cleanup.snapshot_key AND s.pending_snapshot_key IS DISTINCT FROM cleanup.snapshot_key
  AND NOT EXISTS(SELECT 1 FROM probe_snapshot_source_references ref WHERE ref.org_id=cleanup.org_id AND ref.snapshot_key=cleanup.snapshot_key AND ref.active)
 FOR UPDATE OF s,cleanup
), removed AS (
 DELETE FROM probe_snapshot_gc_objects object USING authority a WHERE object.org_id=a.org_id AND object.snapshot_key=a.snapshot_key AND object.cleanup_job_id=a.id RETURNING object.snapshot_key
) UPDATE probe_snapshot_cleanup_jobs cleanup SET delete_state='settled' FROM authority a WHERE cleanup.org_id=a.org_id AND cleanup.id=a.id RETURNING cleanup.snapshot_key`

func TestSessionLifecycleSnapshotGarbageCollectionProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, change, action                                  string
		expectedTombstones, expectedDeletes, expectedBindings int
	}{
		{name: "superseded key discoverable from preSave source after bounded grace", change: `UPDATE probe_snapshot_cleanup_jobs SET superseded_at='2026-10-17T11:00:00Z'`, expectedTombstones: 1, expectedDeletes: 1},
		{name: "uninstalled orphan discoverable without object inventory", expectedTombstones: 1, expectedDeletes: 1},
		{name: "superseded grace not yet elapsed defers deletion", change: `UPDATE probe_snapshot_cleanup_jobs SET superseded_at='2026-10-18T11:00:00Z'`},
		{name: "orphan grace uses upload deadline not object age", change: `UPDATE probe_snapshot_cleanup_jobs SET upload_deadline='2026-10-17T13:00:00Z'`},
		{name: "current installed key cannot be deleted by cleanup", change: `UPDATE sessions SET snapshot_key='synthetic-old-object'`},
		{name: "current pending upload key blocks cleanup", change: `UPDATE sessions SET pending_snapshot_key='synthetic-old-object'`},
		{name: "active preparation source reference wins before tombstone", action: "reference_first", expectedBindings: 1},
		{name: "retained parked source reference blocks cleanup", change: `INSERT INTO probe_snapshot_source_references SELECT org_id,snapshot_key,'parked_control',true FROM probe_snapshot_cleanup_jobs`},
		{name: "unknown uploader cannot be declared settled by expired deadline", change: `UPDATE probe_snapshot_cleanup_jobs SET writer_state='unknown'`},
		{name: "late uploader positive settlement enables later cleanup", change: `UPDATE probe_snapshot_cleanup_jobs SET writer_state='unknown'`, action: "settle_late_writer", expectedTombstones: 1, expectedDeletes: 1},
		{name: "tombstone is barrier against later preparation source binding", action: "tombstone_first", expectedTombstones: 1, expectedDeletes: 1},
		{name: "stale deletion token cannot consume current durable handoff", action: "stale_delete", expectedTombstones: 1},
		{name: "crash after tombstone leaves durable replayable exact delete handoff", action: "crash_replay", expectedTombstones: 1, expectedDeletes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, _ := newSessionLifecycleReadinessProbe(t, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := pool.Exec(ctx, `ALTER TABLE sessions ADD COLUMN pending_snapshot_key text;
 CREATE TABLE probe_snapshot_cleanup_jobs(id uuid PRIMARY KEY,org_id uuid,session_id uuid,snapshot_key text,publication_id uuid,upload_token uuid,session_epoch bigint,workspace_generation bigint,upload_deadline timestamptz,superseded_at timestamptz,writer_state text,object_state text,delete_state text,delete_token uuid,control_version bigint);
 CREATE TABLE probe_snapshot_gc_objects(org_id uuid,snapshot_key text,cleanup_job_id uuid,payload text,PRIMARY KEY(org_id,snapshot_key));
 CREATE TABLE probe_snapshot_source_references(org_id uuid,snapshot_key text,kind text,active boolean)`)
			require.NoError(t, err, "project bounded exact-key cleanup-job metadata and source joins without new global inventory or request pin subsystem")
			jobID, deleteToken := uuid.New(), uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO probe_snapshot_cleanup_jobs SELECT $2,s.org_id,s.id,'synthetic-old-object',gen_random_uuid(),gen_random_uuid(),s.admission_epoch,s.workspace_generation,$3,NULL,'uploading','live','waiting',NULL,1 FROM sessions s WHERE s.org_id=$1`, orgID, jobID, now.Add(-25*time.Hour))
			require.NoError(t, err, "durably discover cleanup owner and immutable object key BEFORE hypothetical Save; uninstalled keys must never depend on bucket listing")
			_, err = pool.Exec(ctx, `INSERT INTO probe_snapshot_gc_objects SELECT org_id,snapshot_key,id,'synthetic-bytes' FROM probe_snapshot_cleanup_jobs WHERE org_id=$1 AND id=$2`, orgID, jobID)
			require.NoError(t, err, "stand in for Save only after its exact durable cleanup source exists")
			_, err = pool.Exec(ctx, `INSERT INTO probe_snapshot_gc_objects SELECT org_id,snapshot_key,gen_random_uuid(),'synthetic-successor-bytes' FROM sessions WHERE org_id=$1`, orgID)
			require.NoError(t, err, "preserve a distinct installed successor object whose bytes stale cleanup must never remove")
			_, err = pool.Exec(ctx, `UPDATE probe_snapshot_cleanup_jobs SET writer_state='settled' WHERE org_id=$1 AND id=$2`, orgID, jobID)
			require.NoError(t, err, "stand in for positive uploader completion, never infer settled state from TTL")
			if tt.change != "" {
				_, err = pool.Exec(ctx, tt.change)
				require.NoError(t, err, "represent current reference, bounded grace or unsettled writer evidence")
			}
			var bindings int
			bind := func() {
				rows, err := pool.Query(ctx, sessionLifecycleBindSnapshotSourceProbe, orgID, jobID, "start_preparation")
				require.NoError(t, err, "source-reference creation must share the exact-key tombstone barrier")
				keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
				require.NoError(t, err, "collect source reference CAS outcome")
				bindings += len(keys)
			}
			if tt.action == "reference_first" {
				bind()
			}
			var tombstones []string
			tombstone := func() []string {
				rows, err := pool.Query(ctx, sessionLifecycleTombstoneSnapshotObjectProbe, orgID, jobID, now, deleteToken)
				require.NoError(t, err, "eligible cleanup must atomically install exact-key tombstone and durable delete handoff before any Delete")
				keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
				require.NoError(t, err, "collect exact tombstone and durable deletion-authority outcome")
				return keys
			}
			tombstones = tombstone()
			if tt.action == "settle_late_writer" {
				require.Equal(t, []string{}, tombstones, "expired unknown upload must not produce a deletion authority")
				_, err = pool.Exec(ctx, `UPDATE probe_snapshot_cleanup_jobs SET writer_state='settled' WHERE org_id=$1 AND id=$2`, orgID, jobID)
				require.NoError(t, err, "stand in for independently proven writer stop/completion before allowing deletion")
				tombstones = tombstone()
			}
			if tt.action == "tombstone_first" {
				bind()
			}
			expectedKeys := []string{}
			if tt.expectedTombstones == 1 {
				expectedKeys = []string{"synthetic-old-object"}
			}
			require.Equal(t, expectedKeys, tombstones, "finite age only makes unreferenced positively settled keys eligible; it cannot override a live source or unknown writer")
			require.Equal(t, tt.expectedBindings, bindings, "reference creation and tombstone serialize: first winner blocks the unsafe opposite transition")
			if tt.action == "crash_replay" {
				for range 2 {
					var handoff json.RawMessage
					require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object('key',snapshot_key,'token',delete_token,'state',delete_state,'object_state',object_state) FROM probe_snapshot_cleanup_jobs WHERE org_id=$1 AND id=$2`, orgID, jobID).Scan(&handoff), "a crash/restarted worker must rediscover the same nonterminal exact-key deletion handoff without ListObjects")
					expected, err := json.Marshal(map[string]any{"key": "synthetic-old-object", "token": deleteToken.String(), "state": "pending", "object_state": "tombstoned"})
					require.NoError(t, err, "encode exact persistent delete handoff")
					require.JSONEq(t, string(expected), string(handoff), "recovery reads must neither resurrect availability nor lose pending cleanup authority")
				}
			}
			invocationToken := deleteToken
			if tt.action == "stale_delete" {
				invocationToken = uuid.New()
			}
			rows, err := pool.Query(ctx, sessionLifecycleCompleteSnapshotDeleteProbe, orgID, jobID, invocationToken)
			require.NoError(t, err, "synthetic Delete completion must consume only the durable exact-key tombstone handoff")
			deleted, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err, "collect exact synthetic cleanup completion")
			expectedDeleted := []string{}
			if tt.expectedDeletes == 1 {
				expectedDeleted = []string{"synthetic-old-object"}
			}
			require.Equal(t, expectedDeleted, deleted, "unknown or still referenced objects must never reach physical Delete; actual object-store IO remains unproven")
			var current string
			require.NoError(t, pool.QueryRow(ctx, `SELECT snapshot_key FROM sessions WHERE org_id=$1`, orgID).Scan(&current), "inspect successor installed reference after exact old-object cleanup")
			expectedCurrent := "request-checkpoint"
			if strings.Contains(tt.name, "current installed") {
				expectedCurrent = "synthetic-old-object"
			}
			require.Equal(t, expectedCurrent, current, "cleanup of an old immutable key cannot clear, overwrite or delete the successor reference")
			var remaining json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_object_agg(snapshot_key,payload) FROM probe_snapshot_gc_objects WHERE org_id=$1`, orgID).Scan(&remaining), "compare exact preserved object keys and bytes after candidate cleanup")
			expectedRemaining := map[string]string{"request-checkpoint": "synthetic-successor-bytes", "synthetic-old-object": "synthetic-bytes"}
			if tt.expectedDeletes == 1 {
				delete(expectedRemaining, "synthetic-old-object")
			}
			expectedObjects, err := json.Marshal(expectedRemaining)
			require.NoError(t, err, "construct complete expected surviving key-to-bytes mapping")
			require.JSONEq(t, string(expectedObjects), string(remaining), "only eligible old exact object may be removed after tombstone; successor bytes and durable cleanup source remain")
		})
	}
}

func TestSessionLifecycleSnapshotPublicationCASProbePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, action         string
		expected             []int
		expectedVersion      int64
		expectedAvailability string
	}{
		{name: "each publication uses a fresh attempt scoped key", action: "fresh_two", expected: []int{1, 1}, expectedVersion: 3, expectedAvailability: "available"},
		{name: "late old upload cannot promote over current publication", action: "stale_promotion", expected: []int{1, 0}, expectedVersion: 2, expectedAvailability: "available"},
		{name: "old GC and Clear CAS cannot destroy successor snapshot", action: "stale_retention", expected: []int{1, 0, 0}, expectedVersion: 2, expectedAvailability: "available"},
		{name: "current exact tombstone precedes Clear without implicit physical delete", action: "current_retention", expected: []int{1, 1, 1}, expectedVersion: 4, expectedAvailability: "missing"},
		{name: "old completion cannot publish while newer pending upload owns reservation", action: "pending_successor", expected: []int{0, 1}, expectedVersion: 2, expectedAvailability: "available"},
		{name: "stale pending Clear cannot erase newer pending upload reservation", action: "stale_pending_clear", expected: []int{0, 1}, expectedVersion: 2, expectedAvailability: "available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, _ := newSessionLifecycleReadinessProbe(t, time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := pool.Exec(ctx, `ALTER TABLE sessions ADD COLUMN pending_upload_receipt_id uuid,ADD COLUMN pending_snapshot_key text,ADD COLUMN pending_upload_token uuid,ADD COLUMN pending_upload_version bigint DEFAULT 0;
	 CREATE TABLE probe_snapshot_objects(org_id uuid,snapshot_key text,upload_receipt_id uuid,payload text,PRIMARY KEY(org_id,snapshot_key));
	 CREATE TABLE probe_snapshot_upload_receipts(id uuid PRIMARY KEY,org_id uuid,session_id uuid,attempt_token uuid,snapshot_key text,expected_key text,expected_version bigint,workspace_generation bigint,workspace_revision bigint,status text,reservation_token uuid,pending_version bigint);
 CREATE TABLE probe_snapshot_retention_authorities(id uuid PRIMARY KEY,org_id uuid,session_id uuid,snapshot_key text,expected_version bigint,action text,kind text,status text);`)
			require.NoError(t, err, "create only disjoint publication/reference-CAS and synthetic object receipt projections")
			first, firstKey := newSessionLifecycleSnapshotPublicationProbe(t, ctx, pool, orgID)
			counts := []int{}
			publish := func(id uuid.UUID) {
				rows, err := pool.Query(ctx, sessionLifecyclePublishSnapshotProbe, orgID, id)
				require.NoError(t, err, "publication must compare exact old key/version/generation/revision through fresh candidate receipt")
				values, err := pgx.CollectRows(rows, pgx.RowTo[string])
				require.NoError(t, err, "collect exact candidate publication transition")
				counts = append(counts, len(values))
			}
			if tt.action == "fresh_two" {
				publish(first)
			}
			second, secondKey := newSessionLifecycleSnapshotPublicationProbe(t, ctx, pool, orgID)
			if tt.action == "pending_successor" || tt.action == "stale_pending_clear" {
				_, err := pool.Exec(ctx, `UPDATE probe_snapshot_upload_receipts SET status='uploading' WHERE org_id=$1 AND id=$2`, orgID, second)
				require.NoError(t, err, "represent the newer current reservation whose upload has not completed yet")
				if tt.action == "pending_successor" {
					publish(first)
				} else {
					rows, err := pool.Query(ctx, sessionLifecycleClearPendingSnapshotProbe, orgID, first)
					require.NoError(t, err, "stale upload cleanup must CAS only its exact pending receipt/key/token/version")
					values, err := pgx.CollectRows(rows, pgx.RowTo[int64])
					require.NoError(t, err, "collect stale pending-clear transition")
					counts = append(counts, len(values))
				}
				var currentReceipt uuid.UUID
				var currentKey string
				require.NoError(t, pool.QueryRow(ctx, `SELECT pending_upload_receipt_id,pending_snapshot_key FROM sessions WHERE org_id=$1`, orgID).Scan(&currentReceipt, &currentKey), "inspect still-current newer pending upload after stale promotion/cleanup")
				require.Equal(t, []string{second.String(), secondKey}, []string{currentReceipt.String(), currentKey}, "an older completed upload or stale Clear cannot steal or erase a newer in-flight publication reservation")
				_, err = pool.Exec(ctx, `UPDATE probe_snapshot_upload_receipts SET status='uploaded' WHERE org_id=$1 AND id=$2`, orgID, second)
				require.NoError(t, err, "complete only the newer current synthetic upload")
			}
			publish(second)
			if tt.action == "stale_promotion" {
				publish(first)
			}
			if tt.action == "stale_retention" || tt.action == "current_retention" {
				key, version := "request-checkpoint", int64(1)
				if tt.action == "current_retention" {
					key, version = secondKey, 2
				}
				for _, action := range []string{"expire", "clear"} {
					authority := uuid.New()
					_, err := pool.Exec(ctx, `INSERT INTO probe_snapshot_retention_authorities SELECT $2,s.org_id,s.id,$3,$4,$5,'RetentionAuthority','authorized' FROM sessions s WHERE s.org_id=$1`, orgID, authority, key, version, action)
					require.NoError(t, err, "record exact-reference RetentionAuthority before any hypothetical physical deletion")
					rows, err := pool.Query(ctx, sessionLifecycleRetainSnapshotProbe, orgID, authority)
					require.NoError(t, err, "retention/Clear must not clobber a different or newer current snapshot reference")
					values, err := pgx.CollectRows(rows, pgx.RowTo[string])
					require.NoError(t, err, "collect exact retention reference transition")
					counts = append(counts, len(values))
					if tt.action == "current_retention" {
						version++
					}
				}
			}
			require.Equal(t, tt.expected, counts, "only current exact publication or RetentionAuthority may change the shared snapshot reference")
			var actualKey *string
			var actualVersion int64
			var availability string
			require.NoError(t, pool.QueryRow(ctx, `SELECT snapshot_key,snapshot_availability_version,snapshot_availability FROM sessions WHERE org_id=$1`, orgID).Scan(&actualKey, &actualVersion, &availability), "inspect current snapshot reference after late publication or retention work")
			var expectedKey *string
			if tt.action != "current_retention" {
				expectedKey = &secondKey
			}
			require.Equal(t, expectedKey, actualKey, "late old promotion/GC/Clear must not replace or erase the successor reference")
			require.Equal(t, tt.expectedVersion, actualVersion, "availability/reference CAS must version every committed transition")
			require.Equal(t, tt.expectedAvailability, availability, "tombstoning and Clear record explicit loss before any hypothetical storage removal")
			var keys []string
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY(SELECT snapshot_key FROM probe_snapshot_objects WHERE org_id=$1 ORDER BY snapshot_key)`, orgID).Scan(&keys), "inspect preserved synthetic upload objects without any physical GC/delete claim")
			if firstKey < secondKey {
				require.Equal(t, []string{firstKey, secondKey}, keys, "fresh candidate keys must preserve both attempts without in-place object overwrite")
			} else {
				require.Equal(t, []string{secondKey, firstKey}, keys, "fresh candidate keys must preserve both attempts without in-place object overwrite")
			}
		})
	}
}

func TestSessionLifecyclePreparedStartPolicyProbePostgres(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	type result struct {
		state, reason string
		retry         bool
	}
	type step struct {
		observation, beforeCheck, afterCheck string
		expected                             result
	}
	permit := result{"launch_permitted", "launch_permitted", false}
	unavailable := result{"accepted_undelivered", "INPUT_CONTEXT_UNAVAILABLE", false}
	deferred := result{"start_deferred", "StartDeferred", true}
	recheck := result{"recheck", "recheck", false}
	unknown := result{"delivery_unknown", "delivery_unknown", false}
	tests := []struct {
		name, abandonChange                                  string
		restore, denial, abandon, sibling, physicalBlock     bool
		expectedAbandons, expectedReleases, expectedOrdinary int
		steps                                                []step
	}{
		{name: "verified live target permits only original bound native conversation", steps: []step{{observation: "verified", expected: permit}}},
		{name: "verified exact restored target permits only original bound native conversation", restore: true, steps: []step{{observation: "verified", expected: permit}}},
		{name: "exact committed Cancel denial may resume without creating a grant", denial: true, steps: []step{{observation: "verified", expected: permit}}},
		{name: "generic cancelled request is not a committed bound denial", steps: []step{{observation: "verified", afterCheck: `UPDATE session_human_input_requests SET status='cancelled'`, expected: recheck}}},
		{name: "committed denial unavailable may retry only its same native binding", denial: true, steps: []step{{observation: "unavailable", beforeCheck: `UPDATE probe_native_context_availability SET availability='missing',availability_version=2`, expected: unavailable}, {observation: "verified", beforeCheck: `UPDATE probe_native_context_availability SET availability='available',availability_version=3`, expected: permit}}},
		{name: "answered bound start does not require old pending certificate to advance", steps: []step{{observation: "verified", beforeCheck: `DELETE FROM session_input_workspace_compatibility`, expected: permit}}},
		{name: "missing native context before Execute preserves committed intent", steps: []step{{observation: "unavailable", beforeCheck: `UPDATE probe_native_context_availability SET availability='missing',availability_version=2`, expected: unavailable}}},
		{name: "invalid selected native conversation cannot fall back to history", steps: []step{{observation: "verified", beforeCheck: `UPDATE probe_native_context_availability SET conversation_id='wrong-conversation'`, expected: unavailable}}},
		{name: "unsupported selected adapter is explicit and never generic success", steps: []step{{observation: "unsupported", expected: result{"accepted_undelivered", "VERIFY_RESUMABLE_UNSUPPORTED", false}}}},
		{name: "retryable native I/O defers without attempt consumption", steps: []step{{observation: "retryable_io", expected: deferred}}},
		{name: "stale checked descriptor requires new check not launch", steps: []step{{observation: "verified", afterCheck: `UPDATE sessions SET workspace_revision=workspace_revision+1`, expected: recheck}}},
		{name: "rotated current job lease rejects prior check", steps: []step{{observation: "verified", afterCheck: `UPDATE jobs SET lock_token=gen_random_uuid() WHERE id=(SELECT job_id FROM probe_bound_dispatches)`, expected: recheck}}},
		{name: "expired current job lease rejects prior check", steps: []step{{observation: "verified", afterCheck: `UPDATE jobs SET locked_until='2026-10-18T11:59:59Z' WHERE id=(SELECT job_id FROM probe_bound_dispatches)`, expected: recheck}}},
		{name: "expired preparation cannot authorize provider launch", steps: []step{{observation: "verified", afterCheck: `UPDATE probe_start_preparations SET expires_at='2026-10-18T11:59:59Z'`, expected: recheck}}},
		{name: "expired selected check cannot authorize provider launch", steps: []step{{observation: "verified", afterCheck: `UPDATE probe_resumability_checks SET expires_at='2026-10-18T11:59:59Z'`, expected: recheck}}},
		{name: "different preparation token cannot authorize provider launch", steps: []step{{observation: "verified", afterCheck: `UPDATE probe_resumability_checks SET preparation_token=gen_random_uuid()`, expected: recheck}}},
		{name: "different actual target rejects even exact old native identity", steps: []step{{observation: "verified", afterCheck: `UPDATE probe_start_targets SET target_id=gen_random_uuid()`, expected: recheck}}},
		{name: "new generation cannot rebind an already accepted answer", steps: []step{{observation: "verified", afterCheck: `UPDATE sessions SET workspace_generation=workspace_generation+1`, expected: recheck}}},
		{name: "changed provider request ID cannot retarget accepted answer", steps: []step{{observation: "verified", afterCheck: `UPDATE session_human_input_requests SET provider_request_id='different-provider-request'`, expected: recheck}}},
		{name: "changed answer digest cannot reuse original grant binding", steps: []step{{observation: "verified", afterCheck: `UPDATE session_human_input_requests SET answer_text='different-answer'`, expected: recheck}}},
		{name: "restored source availability version must remain exact", restore: true, steps: []step{{observation: "verified", afterCheck: `UPDATE sessions SET snapshot_availability_version=snapshot_availability_version+1`, expected: recheck}}},
		{name: "possible earlier Execute dominates native unavailable error", steps: []step{{observation: "unavailable", beforeCheck: `UPDATE probe_start_attempts SET execution_state='delivery_unknown'`, expected: unknown}}},
		{name: "provider launch followed by native error remains delivery unknown", steps: []step{{observation: "unavailable", beforeCheck: `UPDATE probe_start_attempts SET execution_state='provider_launched'`, expected: unknown}}},
		{name: "new provider delivery uncertainty fences later positive native check", steps: []step{{observation: "delivery_unknown", expected: unknown}, {observation: "verified", expected: unknown}}},
		{name: "known not launched unavailable may retry only same repaired native binding", steps: []step{{observation: "unavailable", beforeCheck: `UPDATE probe_native_context_availability SET availability='missing',availability_version=2`, expected: unavailable}, {observation: "verified", beforeCheck: `UPDATE probe_native_context_availability SET availability='available',availability_version=3`, expected: permit}}},
		{name: "retryable I/O may retry exact same current binding without new attempt", steps: []step{{observation: "retryable_io", expected: deferred}, {observation: "verified", expected: permit}}},
		{name: "used launch preparation cannot authorize replay", steps: []step{{observation: "verified", expected: permit}, {observation: "verified", expected: recheck}}},
		{name: "answered known undelivered abandon frees exact claim for a distinct ordinary input", abandon: true, expectedAbandons: 1, expectedReleases: 1, expectedOrdinary: 1, steps: []step{{observation: "unavailable", beforeCheck: `UPDATE probe_native_context_availability SET availability='missing',availability_version=2`, expected: unavailable}}},
		{name: "committed denial abandon preserves cancellation and frees only its dedicated claim", denial: true, abandon: true, expectedAbandons: 1, expectedReleases: 1, expectedOrdinary: 1, steps: []step{{observation: "unavailable", expected: unavailable}}},
		{name: "possible delivered intent cannot be abandoned as known undelivered", abandon: true, expectedAbandons: 0, steps: []step{{observation: "unavailable", beforeCheck: `UPDATE probe_start_attempts SET execution_state='delivery_unknown'`, expected: unknown}}},
		{name: "successor session rejects stale abandon authority", abandon: true, abandonChange: `UPDATE sessions SET admission_epoch=admission_epoch+1`, steps: []step{{observation: "unavailable", expected: unavailable}}},
		{name: "new generation rejects stale abandon authority", abandon: true, abandonChange: `UPDATE sessions SET workspace_generation=workspace_generation+1`, steps: []step{{observation: "unavailable", expected: unavailable}}},
		{name: "changed control version rejects stale preparation abandon", abandon: true, abandonChange: `UPDATE probe_bound_dispatches SET control_version=control_version+1`, steps: []step{{observation: "unavailable", expected: unavailable}}},
		{name: "abandon releases only A and preserves running sibling capacity", abandon: true, sibling: true, expectedAbandons: 1, expectedReleases: 1, steps: []step{{observation: "unavailable", expected: unavailable}}},
		{name: "abandon preserves unsettled physical preparation blocker", abandon: true, physicalBlock: true, expectedAbandons: 1, steps: []step{{observation: "unavailable", expected: unavailable}}},
		{name: "abandon never clears a foreign current admission claim", abandon: true, expectedAbandons: 1, abandonChange: `UPDATE probe_admission_claims SET binding_token=gen_random_uuid()`, steps: []step{{observation: "unavailable", expected: unavailable}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, requestID := newSessionLifecycleReadinessProbe(t, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			token, expectedContext := prepareSessionLifecycleStartProbe(t, ctx, pool, orgID, requestID, now, tt.restore, tt.denial)
			if tt.sibling {
				_, err := pool.Exec(ctx, `INSERT INTO probe_admission_claims SELECT t.org_id,t.session_id,t.id,gen_random_uuid(),gen_random_uuid(),s.admission_epoch,t.admission_epoch,s.workspace_generation,'running','settled'
				 FROM session_threads t JOIN sessions s ON s.org_id=t.org_id AND s.id=t.session_id WHERE t.id<>(SELECT thread_id FROM probe_bound_dispatches);
				 UPDATE probe_admission_capacity SET running_slots=running_slots+1;
				 UPDATE session_threads SET execution_status='running'`)
				require.NoError(t, err, "represent independent unrelated sibling work that abandonment must not clear or rewrite into the dedicated answer/denial job")
			}
			if tt.physicalBlock {
				_, err := pool.Exec(ctx, `UPDATE probe_admission_claims SET physical_state='cleanup_pending'`)
				require.NoError(t, err, "represent unsettled physical preparation that may outlive intent abandonment and still block fresh admission")
			}
			previousState := ""
			for index, item := range tt.steps {
				if index > 0 && (previousState == "accepted_undelivered" || previousState == "start_deferred") {
					token, expectedContext = restartSessionLifecycleBoundStartProbe(t, ctx, pool, orgID, requestID, now, expectedContext)
				}
				if item.beforeCheck != "" {
					_, err := pool.Exec(ctx, item.beforeCheck)
					require.NoError(t, err, "represent the selected native/attempt observation before the outside-transaction check")
				}
				// No Begin spans this stand-in for external VerifyResumable I/O.
				require.Equal(t, int32(0), pool.Stat().AcquiredConns(), "selected native checking must not retain a database transaction or its locks across external I/O")
				checkID := uuid.New()
				_, err := pool.Exec(ctx, `DELETE FROM probe_resumability_checks WHERE org_id=$1 AND preparation_token=$2`, orgID, token)
				require.NoError(t, err, "replace one bounded current check projection rather than accumulate an attempt/check ledger")
				_, err = pool.Exec(ctx, `INSERT INTO probe_resumability_checks SELECT $3,p.org_id,p.token,p.attempt_id,p.job_lock_token,p.provider_type,p.provider_build,p.conversation_id,p.workdir_scope,
				 p.target_id,p.source_kind,p.source_ref,p.source_availability_version,p.workspace_generation,p.workspace_state,n.availability_version,$4,$5,'observed'
				 FROM probe_start_preparations p JOIN probe_bound_dispatches d ON d.org_id=p.org_id AND d.id=p.dispatch_id
				 LEFT JOIN probe_native_context_availability n ON n.org_id=d.org_id AND n.request_id=d.request_id WHERE p.org_id=$1 AND p.token=$2`, orgID, token, checkID, item.observation, now.Add(time.Minute))
				require.NoError(t, err, "record synthetic selected-provider result bound to exact prepared target/native identity, not manufacture actual adapter availability proof")
				if item.afterCheck != "" {
					_, err := pool.Exec(ctx, item.afterCheck)
					require.NoError(t, err, "represent authority or target state changing after external checking but before final authorization")
				}
				before := sessionLifecycleAcceptedIntentProbe(t, ctx, pool, orgID, requestID)
				var actual result
				var contextJSON json.RawMessage
				controlID := uuid.New()
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleAuthorizePreparedStartProbe, orgID, token, checkID, now, controlID).Scan(&actual.state, &actual.reason, &actual.retry, &contextJSON), "final authorization must consume only current exact prepared check, preserving consent on every deferred/unavailable/uncertain path")
				require.Equal(t, item.expected, actual, "native unavailability before Execute, retryable I/O, stale checks and possible prior delivery require distinct non-rebinding outcomes")
				if actual.state == "launch_permitted" {
					require.JSONEq(t, string(expectedContext), string(contextJSON), "the only launch permit must name the original bound request/provider/native conversation/build/workdir/generation/lease and checked target")
				} else {
					require.Equal(t, json.RawMessage(nil), contextJSON, "failed or uncertain verification must emit no provider launch context and no fresh/history fallback")
				}
				expectedIntent := expectedSessionLifecycleStartOutcomeProbe(t, before, actual.state, controlID)
				require.JSONEq(t, string(expectedIntent), string(sessionLifecycleAcceptedIntentProbe(t, ctx, pool, orgID, requestID)), "prelaunch outcomes may park/refund/revoke only owned execution metadata; never unanswer, revoke/regrant, rebind payload or acknowledge/deliver inbox intent")
				previousState = actual.state
			}
			if tt.abandon {
				if tt.abandonChange != "" {
					_, err := pool.Exec(ctx, tt.abandonChange)
					require.NoError(t, err, "represent a successor, stale control version or foreign claim before exact accepted-intent control authorization")
				}
				before := sessionLifecycleAcceptedIntentProbe(t, ctx, pool, orgID, requestID)
				admissionBefore := sessionLifecycleAdmissionStateProbe(t, ctx, pool, orgID)
				receiptID := uuid.New()
				_, err := pool.Exec(ctx, `INSERT INTO probe_accepted_input_controls SELECT $3,d.org_id,d.request_id,d.id,d.binding_token,control.id,d.control_version,'abandon','authorized'
				 FROM probe_bound_dispatches d JOIN probe_start_controls control ON control.org_id=d.org_id AND control.dispatch_id=d.id WHERE d.org_id=$1 AND d.request_id=$2`, orgID, requestID, receiptID)
				require.NoError(t, err, "record explicit answered-request abandon authority for only the original exact accepted binding")
				var abandoned int
				tx, err := pool.Begin(ctx)
				require.NoError(t, err, "begin one outer transaction for accepted-intent control and conditional exact-owned admission release")
				defer func() { _ = tx.Rollback(ctx) }()
				require.NoError(t, tx.QueryRow(ctx, sessionLifecycleAbandonAcceptedInputProbe, orgID, receiptID).Scan(&abandoned), "parked answered/denial control must remain reachable after lease/preparation expiry without reanswering or pending-only recovery eligibility")
				var released int
				require.NoError(t, tx.QueryRow(ctx, sessionLifecycleReleaseAbandonedClaimProbe, orgID, receiptID).Scan(&released), "AdmissionAuthority must release only this dedicated known-unlaunched claim and preserve physical/foreign/sibling blockers")
				require.NoError(t, tx.Commit(ctx), "commit intent termination and eligible claim/capacity/status projections together")
				require.Equal(t, tt.expectedAbandons, abandoned, "only positively known undelivered intent may be terminalized as abandoned; delivery uncertainty must remain fenced")
				require.Equal(t, tt.expectedReleases, released, "abandonment must not leak its own settled admission claim or clear foreign/sibling/unsettled physical claims")
				var expectedAdmission map[string]any
				require.NoError(t, json.Unmarshal(admissionBefore, &expectedAdmission), "decode complete admission claims/status/capacity before exact-owned abandonment")
				var boundBefore map[string]any
				require.NoError(t, json.Unmarshal(before, &boundBefore), "decode the original dedicated binding for exact claim/status ownership checks")
				if released == 1 {
					bound := boundBefore["dispatch"].(map[string]any)
					for _, value := range expectedAdmission["claims"].([]any) {
						claim := value.(map[string]any)
						if claim["dispatch_id"] == bound["id"] {
							claim["state"] = "released"
						}
					}
					capacity := expectedAdmission["capacity"].(map[string]any)
					capacity["running_slots"] = capacity["running_slots"].(float64) - 1
					for _, value := range expectedAdmission["threads"].([]any) {
						thread := value.(map[string]any)
						if thread["id"] == bound["thread_id"] && thread["execution_status"] == "running" {
							thread["execution_status"] = "idle"
						}
					}
					parent := expectedAdmission["session"].(map[string]any)
					if capacity["running_slots"] == float64(0) && parent["execution_status"] == "running" {
						parent["execution_status"] = "idle"
					}
				}
				expectedAdmissionJSON, err := json.Marshal(expectedAdmission)
				require.NoError(t, err, "encode only the exact owned claim release and nonfailed sibling-aware status/counter projection")
				require.JSONEq(t, string(expectedAdmissionJSON), string(sessionLifecycleAdmissionStateProbe(t, ctx, pool, orgID)), "abandonment must preserve every foreign/sibling/physical claim and current epoch/generation field while releasing only owned eligible capacity")
				var expected map[string]any
				require.NoError(t, json.Unmarshal(before, &expected), "decode the complete accepted-intent snapshot for exact terminal-metadata comparison")
				if abandoned == 1 {
					var revokedLease uuid.UUID
					require.NoError(t, pool.QueryRow(ctx, `SELECT revoked_job_lock_token FROM probe_start_controls WHERE org_id=$1 AND dispatch_id=(SELECT id FROM probe_bound_dispatches WHERE org_id=$1 AND request_id=$2)`, orgID, requestID).Scan(&revokedLease), "capture exact previously revoked lease carried by the durable parked control")
					expected["dispatch"].(map[string]any)["control_state"] = "abandoned"
					expected["dispatch"].(map[string]any)["control_version"] = expected["dispatch"].(map[string]any)["control_version"].(float64) + 1
					expected["inbox"].(map[string]any)["delivery_state"] = "abandoned"
					for _, value := range expected["jobs"].([]any) {
						job := value.(map[string]any)
						if job["id"] == expected["dispatch"].(map[string]any)["job_id"] {
							job["abandoned_lease_token"], job["lock_token"], job["locked_until"] = revokedLease.String(), nil, nil
							job["status"], job["cancellation_kind"], job["cancelled_control_receipt_id"] = "cancelled", "user_abandoned_unlaunched", receiptID.String()
						}
					}
				}
				expectedJSON, err := json.Marshal(expected)
				require.NoError(t, err, "encode expected abandonment changing only undelivered dispatch/inbox terminal metadata")
				require.JSONEq(t, string(expectedJSON), string(sessionLifecycleAcceptedIntentProbe(t, ctx, pool, orgID, requestID)), "explicit abandon may close only dedicated intent and its exact known-unlaunched job lease; preserve request/grant/payload/attempts/other jobs without ack or provider failure")
				var ordinary int
				require.NoError(t, pool.QueryRow(ctx, sessionLifecycleOrdinaryAdmissionAfterAbandonProbe, orgID, uuid.New(), uuid.New()).Scan(&ordinary), "a distinct ordinary input may make progress only after exact-owned logical/physical capacity really becomes free")
				require.Equal(t, tt.expectedOrdinary, ordinary, "settled abandoned work must not leave a phantom running slot; real sibling/foreign/physical/successor blockers must remain respected")
			}
		})
	}
}

func sessionLifecycleAcceptedIntentProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, requestID uuid.UUID) json.RawMessage {
	t.Helper()
	var value json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object('request',to_jsonb(q),
	 'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM session_capability_grants g WHERE g.org_id=q.org_id AND g.request_id=q.id),
	 'dispatch',(SELECT to_jsonb(d) FROM probe_bound_dispatches d WHERE d.org_id=q.org_id AND d.request_id=q.id),
	 'inbox',(SELECT to_jsonb(i) FROM probe_pending_input_inbox i WHERE i.org_id=q.org_id AND i.request_id=q.id),
	 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE j.org_id=q.org_id))
	 FROM session_human_input_requests q WHERE q.org_id=$1 AND q.id=$2`, orgID, requestID).Scan(&value), "capture full committed answer/grant/binding/inbox/jobs without treating readiness as a future launch promise")
	return value
}

func expectedSessionLifecycleStartOutcomeProbe(t *testing.T, before json.RawMessage, state string, controlID uuid.UUID) json.RawMessage {
	t.Helper()
	var expected map[string]any
	require.NoError(t, json.Unmarshal(before, &expected), "decode exact committed intent and execution metadata before start outcome")
	if state == "accepted_undelivered" || state == "start_deferred" {
		dispatch := expected["dispatch"].(map[string]any)
		if state == "accepted_undelivered" {
			dispatch["control_state"] = "awaiting_control"
			dispatch["control_version"] = dispatch["control_version"].(float64) + 1
		}
		for _, value := range expected["jobs"].([]any) {
			job := value.(map[string]any)
			if job["id"] == dispatch["job_id"] {
				job["lock_token"], job["locked_until"] = nil, nil
				if job["claim_debit_refunded"] == false {
					job["attempts"] = job["attempts"].(float64) - 1
				}
				job["claim_debit_refunded"] = true
				job["status"] = "pending"
				if state == "accepted_undelivered" {
					job["status"], job["parked_control_id"] = "awaiting_start_control", controlID.String()
				}
			}
		}
	}
	value, err := json.Marshal(expected)
	require.NoError(t, err, "encode only allowed durable parking/defer execution metadata changes")
	return value
}

func restartSessionLifecycleBoundStartProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, requestID uuid.UUID, now time.Time, previousContext json.RawMessage) (uuid.UUID, json.RawMessage) {
	t.Helper()
	token, lease := uuid.New(), uuid.New()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin explicit same-binding retry then fresh claim/preparation without provider launch")
	defer func() { _ = tx.Rollback(ctx) }()
	var dispatchID uuid.UUID
	var jobStatus string
	var version int64
	require.NoError(t, tx.QueryRow(ctx, `SELECT d.id,j.status,d.control_version FROM probe_bound_dispatches d JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id WHERE d.org_id=$1 AND d.request_id=$2`, orgID, requestID).Scan(&dispatchID, &jobStatus, &version), "resolve original durable binding and current parked version, never a new conversation/job")
	if jobStatus == "awaiting_start_control" {
		var retried int
		require.NoError(t, tx.QueryRow(ctx, sessionLifecycleRetryParkedStartProbe, orgID, dispatchID, version).Scan(&retried), "explicit retry must consume exact durable parked authority without an unexpired execution lease/preparation")
		require.Equal(t, 1, retried, "only this same current binding/native context/generation may requeue after positive known-not-launched parking")
	}
	var claimed uuid.UUID
	require.NoError(t, tx.QueryRow(ctx, `UPDATE jobs j SET status='running',lock_token=$3,locked_until=$4,attempts=j.attempts+1,claim_debit_refunded=false
	 FROM probe_bound_dispatches d JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id
	 JOIN session_threads t ON t.org_id=d.org_id AND t.id=d.thread_id AND t.session_id=d.session_id
	 JOIN probe_start_attempts a ON a.org_id=d.org_id AND a.dispatch_id=d.id
	 JOIN probe_admission_claims claim ON claim.org_id=d.org_id AND claim.dispatch_id=d.id AND claim.binding_token=d.binding_token
	 WHERE j.org_id=$1 AND d.id=$2 AND j.id=d.job_id AND j.org_id=d.org_id AND j.status='pending' AND j.lock_token IS NULL AND j.claim_debit_refunded
	  AND d.control_state='active' AND s.admission_epoch=d.session_epoch AND t.admission_epoch=d.thread_epoch AND s.workspace_generation=d.workspace_generation
	  AND claim.physical_state='settled' AND a.first_provider_launch_marker IS NULL AND a.execution_state='known_not_launched'
	  AND NOT EXISTS(SELECT 1 FROM probe_start_attempts prior WHERE prior.org_id=d.org_id AND prior.dispatch_id=d.id AND `+sessionLifecyclePossibleAcceptanceProbe+`) RETURNING j.id`, orgID, dispatchID, lease, now.Add(time.Minute)).Scan(&claimed), "a fresh claim must honor same-binding current epochs/generation and all-allocation possible-acceptance fences without a net retry debit")
	_, err = tx.Exec(ctx, `UPDATE probe_start_attempts a SET job_lock_token=$3 FROM probe_bound_dispatches d WHERE a.org_id=$1 AND a.dispatch_id=d.id AND d.org_id=a.org_id AND d.id=$2`, orgID, dispatchID, lease)
	require.NoError(t, err, "retain allocation first-marker history while refreshing only the exact current claim lease")
	_, err = tx.Exec(ctx, `UPDATE probe_start_preparations p SET token=$3,job_lock_token=$4,control_version=d.control_version,status='preparing',expires_at=$5,
	 workspace_state=`+sessionLifecycleWorkspaceStateProbe+`,target_id=target.target_id,source_kind=target.source_kind,source_ref=target.source_ref,source_availability_version=target.source_availability_version
	 FROM probe_bound_dispatches d JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id JOIN probe_start_targets target ON target.org_id=d.org_id AND target.session_id=d.session_id
	 WHERE p.org_id=$1 AND p.dispatch_id=$2 AND d.org_id=p.org_id AND d.id=p.dispatch_id AND p.job_id=$6`, orgID, dispatchID, token, lease, now.Add(30*time.Second), claimed)
	require.NoError(t, err, "create fresh bounded execution preparation for the same immutable binding before selected external verification")
	require.NoError(t, tx.Commit(ctx), "commit explicit parked control consumption and fresh exact same-binding claim/preparation")
	var expected map[string]any
	require.NoError(t, json.Unmarshal(previousContext, &expected), "decode original expected native context; retry may refresh only current lease metadata")
	expected["job_lock_token"] = lease.String()
	value, err := json.Marshal(expected)
	require.NoError(t, err, "encode same original input/native binding with fresh current job lease")
	return token, value
}

func sessionLifecycleAdmissionStateProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID) json.RawMessage {
	t.Helper()
	var value json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object('session',to_jsonb(s),
	 'threads',(SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM session_threads t WHERE t.org_id=s.org_id AND t.session_id=s.id),
	 'capacity',(SELECT to_jsonb(capacity) FROM probe_admission_capacity capacity WHERE capacity.org_id=s.org_id AND capacity.session_id=s.id),
	 'claims',(SELECT jsonb_agg(to_jsonb(claim) ORDER BY claim.dispatch_id) FROM probe_admission_claims claim WHERE claim.org_id=s.org_id AND claim.session_id=s.id)) FROM sessions s WHERE s.org_id=$1`, orgID).Scan(&value), "capture exact current logical/physical admission ownership and sibling-aware projections")
	return value
}

func prepareSessionLifecycleStartProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, requestID uuid.UUID, now time.Time, restore, denial bool) (uuid.UUID, json.RawMessage) {
	t.Helper()
	_, err := pool.Exec(ctx, `ALTER TABLE session_capability_grants ADD COLUMN id uuid;
	 ALTER TABLE jobs ADD COLUMN attempts int DEFAULT 1,ADD COLUMN locked_until timestamptz,
	  ADD COLUMN cancellation_kind text,ADD COLUMN cancelled_control_receipt_id uuid,ADD COLUMN abandoned_lease_token uuid,
	  ADD COLUMN claim_debit_refunded boolean DEFAULT false,ADD COLUMN parked_control_id uuid,ADD COLUMN payload jsonb DEFAULT '{}'::jsonb;
	 ALTER TABLE session_human_input_requests ADD COLUMN answer_payload jsonb;
	 ALTER TABLE sessions ADD COLUMN execution_status text DEFAULT 'idle';
	 ALTER TABLE session_threads ADD COLUMN execution_status text DEFAULT 'idle';
	 CREATE TABLE probe_bound_dispatches(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid,thread_id uuid,request_id uuid,grant_id uuid,inbox_id uuid,job_id uuid,binding_token uuid,
	  session_epoch bigint,thread_epoch bigint,workspace_generation bigint,provider_type text,provider_build text,conversation_id text,workdir_scope text,control_state text,provider_request_id text,answer_digest text,input_kind text,denial_receipt_id uuid,control_version bigint,anchor_digest text);
	 CREATE TABLE probe_pending_input_inbox(id uuid PRIMARY KEY,org_id uuid NOT NULL,request_id uuid,dispatch_id uuid,delivery_state text);
	 CREATE TABLE probe_start_attempts(id uuid PRIMARY KEY,org_id uuid NOT NULL,dispatch_id uuid,job_id uuid,job_lock_token uuid,execution_state text,
	  first_provider_launch_marker uuid,launch_ordinal bigint DEFAULT 0,invocation_nonce uuid,invocation_purpose text);
	 CREATE TABLE probe_start_targets(org_id uuid NOT NULL,session_id uuid PRIMARY KEY,target_id uuid,source_kind text,source_ref text,source_availability_version bigint,availability text);
	 CREATE TABLE probe_start_preparations(token uuid PRIMARY KEY,org_id uuid NOT NULL,dispatch_id uuid,binding_token uuid,job_id uuid,job_lock_token uuid,attempt_id uuid,
	  session_epoch bigint,thread_epoch bigint,workspace_generation bigint,provider_type text,provider_build text,conversation_id text,workdir_scope text,
	  target_id uuid,source_kind text,source_ref text,source_availability_version bigint,workspace_state jsonb,expires_at timestamptz,status text,control_version bigint,
	  invocation_authority_token uuid,invocation_purpose text,UNIQUE(org_id,job_id));
	 CREATE TABLE probe_resumability_checks(id uuid PRIMARY KEY,org_id uuid NOT NULL,preparation_token uuid,attempt_id uuid,job_lock_token uuid,
	  provider_type text,provider_build text,conversation_id text,workdir_scope text,target_id uuid,source_kind text,source_ref text,source_availability_version bigint,
	  workspace_generation bigint,workspace_state jsonb,native_availability_version bigint,disposition text,expires_at timestamptz,status text,UNIQUE(org_id,preparation_token));
	 CREATE TABLE probe_start_controls(id uuid PRIMARY KEY,org_id uuid NOT NULL,dispatch_id uuid,binding_token uuid,control_version bigint,job_id uuid,
	  revoked_preparation_token uuid,attempt_id uuid,revoked_job_lock_token uuid,session_epoch bigint,thread_epoch bigint,workspace_generation bigint,
	  provider_type text,provider_build text,conversation_id text,workdir_scope text,no_launch_proof_token uuid,reason text,status text,job_payload_digest text,UNIQUE(org_id,dispatch_id));
	 CREATE TABLE probe_accepted_input_controls(id uuid PRIMARY KEY,org_id uuid NOT NULL,request_id uuid,dispatch_id uuid,binding_token uuid,parked_control_id uuid,control_version bigint,action text,status text);
	 CREATE TABLE probe_committed_denial_receipts(id uuid PRIMARY KEY,org_id uuid NOT NULL,request_id uuid,provider_request_id text,answer_digest text,payload jsonb);
	 CREATE TABLE probe_admission_claims(org_id uuid NOT NULL,session_id uuid,thread_id uuid,dispatch_id uuid PRIMARY KEY,binding_token uuid,session_epoch bigint,thread_epoch bigint,workspace_generation bigint,state text,physical_state text);
	 CREATE TABLE probe_admission_capacity(org_id uuid NOT NULL,session_id uuid PRIMARY KEY,running_slots int);`)
	require.NoError(t, err, "create minimal proposed start preparation/check projections only, without production dispatch/schema/runtime implementation")
	token, dispatchID, grantID, inboxID, jobID, bindingToken, attemptID, leaseToken, targetID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sourceKind := "live"
	if restore {
		sourceKind = "restore"
	}
	inputKind, answerText := "answer", "accepted answer"
	var boundGrant any = grantID
	var denialReceipt any
	if denial {
		inputKind, answerText = "denial", "Cancelled human input request."
		boundGrant, denialReceipt = nil, uuid.New()
	}
	batch := &pgx.Batch{}
	if denial {
		batch.Queue(`UPDATE session_human_input_requests SET status='cancelled',answer_text=$3,answer_payload='{"decision":"deny","cancelled":true}'::jsonb WHERE org_id=$1 AND id=$2`, orgID, requestID, answerText)
		batch.Queue(`INSERT INTO probe_committed_denial_receipts SELECT $3,q.org_id,q.id,q.provider_request_id,encode(sha256(convert_to(q.answer_text,'UTF8')),'hex'),q.answer_payload FROM session_human_input_requests q WHERE q.org_id=$1 AND q.id=$2`, orgID, requestID, denialReceipt)
	} else {
		batch.Queue(`UPDATE session_human_input_requests SET status='answered',answer_text=$3 WHERE org_id=$1 AND id=$2`, orgID, requestID, answerText)
		batch.Queue(`INSERT INTO session_capability_grants(org_id,request_id,id) VALUES($1,$2,$3)`, orgID, requestID, grantID)
	}
	batch.Queue(`INSERT INTO jobs(id,org_id,session_id,lock_token,status,attempts,locked_until,payload) SELECT $3,q.org_id,q.session_id,$4,'running',1,$5,jsonb_build_object('human_input_request_id',q.id,'provider_request_id',q.provider_request_id,'binding_token',$6::uuid,'input_kind',$7::text)
	 FROM session_human_input_requests q WHERE q.org_id=$1 AND q.id=$2`, orgID, requestID, jobID, leaseToken, now.Add(time.Minute), bindingToken, inputKind)
	batch.Queue(`INSERT INTO probe_bound_dispatches SELECT $3,q.org_id,q.session_id,q.thread_id,q.id,$4,$5,$6,$7,q.admission_session_epoch,q.admission_thread_epoch,s.workspace_generation,q.provider_type,q.provider_build,q.conversation_id,q.workdir_scope,'active',q.provider_request_id,encode(sha256(convert_to(q.answer_text,'UTF8')),'hex'),$8,$9,1,q.pause_anchor_digest
	 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id WHERE q.org_id=$1 AND q.id=$2`, orgID, requestID, dispatchID, boundGrant, inboxID, jobID, bindingToken, inputKind, denialReceipt)
	batch.Queue(`INSERT INTO probe_pending_input_inbox VALUES($1,$2,$3,$4,'pending')`, inboxID, orgID, requestID, dispatchID)
	batch.Queue(`INSERT INTO probe_start_attempts(id,org_id,dispatch_id,job_id,job_lock_token,execution_state) VALUES($1,$2,$3,$4,$5,'known_not_launched')`, attemptID, orgID, dispatchID, jobID, leaseToken)
	batch.Queue(`INSERT INTO probe_start_targets SELECT q.org_id,q.session_id,$3::uuid,$4::text,CASE WHEN $4::text='restore' THEN s.snapshot_key ELSE 'live-target:'||($3::uuid)::text END,s.snapshot_availability_version,'available'
	 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id WHERE q.org_id=$1 AND q.id=$2`, orgID, requestID, targetID, sourceKind)
	batch.Queue(`INSERT INTO probe_start_preparations SELECT $3,d.org_id,d.id,d.binding_token,d.job_id,$4,$5,d.session_epoch,d.thread_epoch,d.workspace_generation,d.provider_type,d.provider_build,d.conversation_id,d.workdir_scope,
	 target.target_id,target.source_kind,target.source_ref,target.source_availability_version,`+sessionLifecycleWorkspaceStateProbe+`,$6,'preparing',d.control_version
	 FROM probe_bound_dispatches d JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id JOIN probe_start_targets target ON target.org_id=d.org_id AND target.session_id=d.session_id WHERE d.org_id=$1 AND d.id=$2`, orgID, dispatchID, token, leaseToken, attemptID, now.Add(30*time.Second))
	batch.Queue(`INSERT INTO probe_admission_claims SELECT d.org_id,d.session_id,d.thread_id,d.id,d.binding_token,d.session_epoch,d.thread_epoch,d.workspace_generation,'running','settled' FROM probe_bound_dispatches d WHERE d.org_id=$1 AND d.id=$2`, orgID, dispatchID)
	batch.Queue(`INSERT INTO probe_admission_capacity SELECT org_id,id,1 FROM sessions WHERE org_id=$1`, orgID)
	batch.Queue(`UPDATE sessions SET execution_status='running' WHERE org_id=$1`, orgID)
	batch.Queue(`UPDATE session_threads SET execution_status='running' WHERE org_id=$1 AND id=(SELECT thread_id FROM probe_bound_dispatches WHERE org_id=$1 AND id=$2)`, orgID, dispatchID)
	require.NoError(t, pool.SendBatch(ctx, batch).Close(), "commit the original answer/grant/binding/inbox and a preparation reservation before any selected external native-context check")
	answerDigest := sha256.Sum256([]byte(answerText))
	expected, err := json.Marshal(map[string]any{
		"dispatch_id": dispatchID, "binding_token": bindingToken, "request_id": requestID, "provider_request_id": "provider-input",
		"provider_type": "synthetic-provider", "provider_build": "synthetic-provider@1", "conversation_id": "conversation-A", "workdir_scope": "trusted-workdir-A",
		"workspace_generation": 11, "target_id": targetID, "attempt_id": attemptID, "job_lock_token": leaseToken,
		"grant_id": boundGrant, "answer_digest": hex.EncodeToString(answerDigest[:]), "input_kind": inputKind,
	})
	require.NoError(t, err, "construct exact independently expected original bound native launch identity")
	return token, expected
}

func newSessionLifecycleReadinessProbe(t *testing.T, now time.Time) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL for M0 proposed readiness query probes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err, "connect to the explicitly configured disposable PostgreSQL database")
	schema := "lifecycle_readiness_probe_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	require.NoError(t, err, "create a separately owned schema for each parallel readiness fixture")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		require.NoError(t, err, "remove only this probe's owned schema and synthetic rows")
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "parse the isolated readiness pool configuration")
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "open the isolated readiness pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
	 CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid NOT NULL,admission_epoch bigint NOT NULL,snapshot_key text,workspace_generation bigint,workspace_revision bigint,checkpoint_runtime_id uuid,checkpoint_attempt_token uuid,checkpoint_phase_id uuid,checkpoint_workspace_revision bigint,checkpoint_published_at timestamptz);
	 CREATE TABLE session_threads(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,admission_epoch bigint NOT NULL);
	 CREATE TABLE thread_runtimes(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,thread_id uuid NOT NULL,session_epoch bigint,thread_epoch bigint,lease_token uuid,status text,drain_receipt_id uuid,writer_version int,lease_expires_at timestamptz,can_publish_checkpoint boolean);
	 CREATE TABLE session_input_dispatches(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,thread_id uuid NOT NULL,session_epoch bigint,thread_epoch bigint,runtime_id uuid,job_id uuid,job_lock_token uuid,runtime_lease_token uuid);
	 CREATE TABLE session_activity_phases(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,thread_id uuid NOT NULL,runtime_id uuid,status text,kind text,turn_number int);
	 CREATE TABLE session_human_input_requests(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,thread_id uuid NOT NULL,provider_request_id text,request_kind text,turn_number int,status text,answer_text text,admission_session_epoch bigint,admission_thread_epoch bigint,producer_runtime_id uuid,producer_job_id uuid,producer_attempt_token uuid,activity_phase_id uuid,readiness_state text,readiness_deadline_at timestamptz,readiness_proof jsonb,proof_policy_version int,enforcement_version int,legacy_request boolean NOT NULL);
	 CREATE TABLE jobs(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,lock_token uuid,status text);
	 CREATE TABLE session_capability_grants(org_id uuid NOT NULL,request_id uuid NOT NULL);
	 ALTER TABLE sessions ADD COLUMN lifecycle_activated_at timestamptz,ADD COLUMN workspace_effect_receipt_id uuid,
	  ADD COLUMN snapshot_availability_version bigint,
	  ADD COLUMN snapshot_availability text,ADD COLUMN physical_workspace_availability text;
	 ALTER TABLE session_human_input_requests ADD COLUMN request_created_at timestamptz,
	  ADD COLUMN pause_snapshot_key text,ADD COLUMN pause_workspace_generation bigint,ADD COLUMN pause_workspace_revision bigint,
	  ADD COLUMN pause_snapshot_captured_revision bigint,ADD COLUMN pause_checkpoint_published_at timestamptz,
	  ADD COLUMN pause_finalized_revision bigint,ADD COLUMN pause_finalized_at timestamptz,ADD COLUMN pause_proof_sealed_at timestamptz,
	  ADD COLUMN pause_anchor_digest text,ADD COLUMN context_scope_digest text,ADD COLUMN pause_seal_receipt_id uuid,
	  ADD COLUMN provider_type text,ADD COLUMN provider_build text,ADD COLUMN conversation_id text,ADD COLUMN workdir_scope text,ADD COLUMN readiness_published_at timestamptz;
	 CREATE TABLE session_input_workspace_compatibility(org_id uuid NOT NULL,request_id uuid PRIMARY KEY,anchor_digest text,context_scope_digest text,state text,
	  workspace_generation bigint,workspace_revision bigint,snapshot_key text,workspace_state jsonb,last_authority_receipt_id uuid,last_authority_kind text);
	 CREATE TABLE probe_workspace_effect_sources(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,request_id uuid NOT NULL,
	  kind text,actor_thread_id uuid,actor_thread_epoch bigint,actor_runtime_id uuid,context_scope_digest text,workspace_generation bigint);
	 CREATE TABLE probe_workspace_effect_receipts(id uuid PRIMARY KEY,source_id uuid NOT NULL,org_id uuid NOT NULL,session_id uuid NOT NULL,request_id uuid NOT NULL,
	  anchor_digest text,from_revision bigint,to_revision bigint,from_snapshot_key text,to_snapshot_key text,from_state jsonb,to_state jsonb,status text);
	 CREATE TABLE probe_workspace_operation_receipts(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,kind text,
	  actor_thread_id uuid,actor_thread_epoch bigint,from_state jsonb,to_state jsonb,status text);
	 CREATE TABLE probe_request_context_effects(operation_id uuid,org_id uuid NOT NULL,request_id uuid NOT NULL,anchor_digest text,context_scope_digest text,
	  effect_kind text,PRIMARY KEY(operation_id,request_id));
	 CREATE TABLE probe_native_context_availability(org_id uuid NOT NULL,request_id uuid PRIMARY KEY,session_id uuid,thread_id uuid,
	  provider_type text,provider_build text,conversation_id text,workdir_scope text,workspace_generation bigint,
	  availability text,availability_version bigint,source_receipt_id uuid);
	 CREATE TABLE probe_input_context_revalidations(id uuid PRIMARY KEY,org_id uuid NOT NULL,request_id uuid NOT NULL,anchor_digest text,
	  provider_type text,provider_build text,conversation_id text,workdir_scope text,availability_version bigint,workspace_state jsonb,expires_at timestamptz,status text);
	 CREATE TABLE probe_context_expiry_receipts(id uuid PRIMARY KEY,org_id uuid NOT NULL,request_id uuid NOT NULL,
	  provider_type text,provider_build text,conversation_id text,workdir_scope text,workspace_generation bigint,availability_version bigint,availability text,authority_kind text,status text);
	`)
	require.NoError(t, err, "create only the proposed tuple fields needed by this policy probe, without pretending to apply production migrations")
	orgID, sessionID, threadID, requestID, runtimeID, jobID, attemptToken, leaseToken, phaseID, drainID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sealID, sealSourceID := uuid.New(), uuid.New()
	contextScopeDigest := "trusted-session-scope:" + uuid.NewString()
	proof, err := json.Marshal(map[string]any{
		"proof_policy_version": 1, "request_id": requestID, "provider_request_id": "provider-input", "request_kind": "free_text",
		"org_id": orgID, "session_id": sessionID, "thread_id": threadID, "session_epoch": 3, "thread_epoch": 7, "turn_number": 9,
		"producer_job_id": jobID, "producer_attempt_token": attemptToken, "producer_runtime_id": runtimeID, "runtime_lease_token": leaseToken,
		"phase_id": phaseID, "snapshot_key": "request-checkpoint", "checkpoint_runtime_id": runtimeID, "checkpoint_attempt_token": attemptToken,
		"workspace_generation": 11, "workspace_revision": 13, "checkpoint_workspace_revision": 13, "producer_pause_finalized_revision": 12,
		"snapshot_captured_revision": 11, "context_scope_digest": contextScopeDigest, "producer_drained": true, "drain_receipt_id": drainID,
		"provider_type": "synthetic-provider", "provider_build": "synthetic-provider@1", "conversation_id": "conversation-A", "workdir_scope": "trusted-workdir-A",
	})
	require.NoError(t, err, "serialize an independently constructed exact synthetic readiness proof")
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO sessions(id,org_id,admission_epoch,snapshot_key,workspace_generation,workspace_revision,checkpoint_runtime_id,checkpoint_attempt_token,checkpoint_phase_id,checkpoint_workspace_revision,checkpoint_published_at)
	 VALUES($1,$2,3,'request-checkpoint',11,13,$3,$4,$5,13,$6)`, sessionID, orgID, runtimeID, attemptToken, phaseID, now.Add(-20*time.Second))
	batch.Queue(`INSERT INTO session_threads VALUES($1,$2,$3,7)`, threadID, orgID, sessionID)
	batch.Queue(`INSERT INTO session_threads VALUES($1,$2,$3,4)`, uuid.New(), orgID, sessionID)
	batch.Queue(`INSERT INTO thread_runtimes VALUES($1,$2,$3,$4,3,7,$5,'closed',$6,1,$7,true)`, runtimeID, orgID, sessionID, threadID, leaseToken, drainID, now.Add(10*time.Minute))
	batch.Queue(`INSERT INTO session_input_dispatches VALUES($1,$2,$3,$4,3,7,$5,$6,$7,$8)`, uuid.New(), orgID, sessionID, threadID, runtimeID, jobID, attemptToken, leaseToken)
	batch.Queue(`INSERT INTO session_activity_phases VALUES($1,$2,$3,$4,$5,'completed','human_input',9)`, phaseID, orgID, sessionID, threadID, runtimeID)
	batch.Queue(`INSERT INTO session_human_input_requests VALUES($1,$2,$3,$4,'provider-input','free_text',9,'pending',NULL,3,7,$5,$6,$7,$8,'unclassified',NULL,$9,1,1,true)`, requestID, orgID, sessionID, threadID, runtimeID, jobID, attemptToken, phaseID, proof)
	batch.Queue(`INSERT INTO jobs VALUES($1,$2,$3,$4,'running')`, jobID, orgID, sessionID, attemptToken)
	batch.Queue(`UPDATE sessions SET lifecycle_activated_at=$1,workspace_effect_receipt_id=$2,checkpoint_published_at=$3,
	 snapshot_availability_version=1,snapshot_availability='available',physical_workspace_availability='available'`, now.Add(-45*time.Second), sealID, now.Add(-40*time.Second))
	batch.Queue(`UPDATE session_human_input_requests SET request_created_at=$1,pause_snapshot_key='request-checkpoint',pause_workspace_generation=11,pause_workspace_revision=13,
	 pause_snapshot_captured_revision=11,pause_checkpoint_published_at=$2,pause_finalized_revision=12,pause_finalized_at=$3,pause_proof_sealed_at=$4,
	 pause_anchor_digest=encode(sha256(convert_to(readiness_proof::text,'UTF8')),'hex'),context_scope_digest=$5,pause_seal_receipt_id=$6,
	 provider_type='synthetic-provider',provider_build='synthetic-provider@1',conversation_id='conversation-A',workdir_scope='trusted-workdir-A',readiness_published_at=$4`, now.Add(-time.Minute), now.Add(-40*time.Second), now.Add(-30*time.Second), now.Add(-20*time.Second), contextScopeDigest, sealID)
	batch.Queue(`INSERT INTO probe_native_context_availability SELECT org_id,id,session_id,thread_id,provider_type,provider_build,conversation_id,workdir_scope,pause_workspace_generation,'available',1,$1 FROM session_human_input_requests`, uuid.New())
	batch.Queue(`INSERT INTO probe_workspace_effect_sources SELECT $1,org_id,session_id,id,'pause_metadata_seal',thread_id,admission_thread_epoch,producer_runtime_id,context_scope_digest,pause_workspace_generation FROM session_human_input_requests`, sealSourceID)
	batch.Queue(`INSERT INTO probe_workspace_effect_receipts SELECT $1,$2,q.org_id,q.session_id,q.id,q.pause_anchor_digest,11,13,q.pause_snapshot_key,q.pause_snapshot_key,'{}'::jsonb,`+sessionLifecycleWorkspaceStateProbe+`,'applied'
	 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id`, sealID, sealSourceID)
	batch.Queue(`INSERT INTO probe_workspace_operation_receipts SELECT $1,s.org_id,s.id,'pause_metadata_seal',$2,7,'{}'::jsonb,`+sessionLifecycleWorkspaceStateProbe+`,'applied' FROM sessions s`, sealID, threadID)
	batch.Queue(`INSERT INTO session_input_workspace_compatibility SELECT q.org_id,q.id,q.pause_anchor_digest,q.context_scope_digest,'certified',s.workspace_generation,s.workspace_revision,s.snapshot_key,` + sessionLifecycleWorkspaceStateProbe + `,q.pause_seal_receipt_id,'pause_metadata_seal'
	 FROM session_human_input_requests q JOIN sessions s ON s.org_id=q.org_id AND s.id=q.session_id`)
	require.NoError(t, pool.SendBatch(ctx, batch).Close(), "seed one exact producer, immutable dispatch, phase, checkpoint and pending request")
	return pool, orgID, requestID
}
