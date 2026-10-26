package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type lifecycleStartControlPostgresFixture struct {
	consumer lifecyclePredicateFixture
	expected lifecycleStartControlSpec
	snapshot lifecycleStartControlSnapshot
}

func TestLifecycleStartControlReaderPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                       string
		kind                       models.LifecycleDispatchKind
		preparation, nanos, offset bool
	}{
		{name: "answer without preparation", kind: models.LifecycleDispatchAnswer},
		{name: "denial without preparation", kind: models.LifecycleDispatchDenial},
		{name: "answer survives expired closed preparation", kind: models.LifecycleDispatchAnswer, preparation: true},
		{name: "denial survives expired closed preparation", kind: models.LifecycleDispatchDenial, preparation: true},
		{name: "nanosecond due persists at PostgreSQL precision", kind: models.LifecycleDispatchAnswer, nanos: true},
		{name: "offset nanosecond due persists at PostgreSQL precision", kind: models.LifecycleDispatchDenial, nanos: true, offset: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedStartControlReaderFixture(t, pool, tt.kind, tt.nanos, tt.offset, false)
			if tt.preparation {
				seedStartControlReaderPreparation(t, pool, f)
			}
			before := startControlReaderGraph(t, pool, f)
			actual, err := runStartControlReader(t, pool, f.consumer.org, f.consumer.own.binding, defaultLifecycleStartControlReadLimits())
			require.NoError(t, err, "an exact retained control must survive old due/preparation timing without deriving current launch authority")
			spec, err := actual.Spec()
			require.NoError(t, err, "the returned snapshot must remain a complete defensive finite value")
			require.Equal(t, f.expected, spec, "readback must preserve the complete retained control from actual immutable PostgreSQL payload text")
			require.Equal(t, before, startControlReaderGraph(t, pool, f), "reading retained control must not mutate the dispatch, job, request or optional preparation")
			proof, proofErr := runStartControlOwnProof(t, pool, f)
			require.NoError(t, proofErr, "coherent job-only parked identity must support exact logical replay without live lease validation")
			require.Equal(t, lifecycleOwnConsumerProof{binding: &f.consumer.own.binding}, proof, "the parked proof must contain only the exact immutable logical job, never a runtime or execution authority")
			require.Equal(t, lifecycleConsumerTestResult{blocked: true}, runLifecycleConsumerPredicate(t, pool, f.consumer, defaultLifecycleConsumerLimits()), "awaiting control must remain competing occupancy even when the exact parked logical own proof is valid")
			require.Equal(t, before, startControlReaderGraph(t, pool, f), "logical proof and competing-consumer reads must preserve all retained evidence unchanged")
		})
	}
}

func TestLifecycleStartControlReaderRejectsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, change string
		incomplete   bool
	}{
		{name: "malformed finite snapshot", change: "snapshot"},
		{name: "unsupported snapshot version", change: "schema"},
		{name: "receipt differs from snapshot", change: "receipt"},
		{name: "control version differs from snapshot", change: "version"},
		{name: "claim token differs from snapshot", change: "claim"},
		{name: "claim turn differs from snapshot", change: "turn"},
		{name: "request differs from snapshot", change: "request"},
		{name: "actual request lane contradicts binding", change: "request_lane"},
		{name: "actual job numeric payload drift", change: "payload"},
		{name: "control metadata is absent", change: "missing_control"},
		{name: "dispatch is absent", change: "missing_dispatch"},
		{name: "foreign tenant lookup", change: "foreign"},
		{name: "expected binding has wrong request", change: "expected_request"},
		{name: "controller job route cannot qualify", change: "controller"},
		{name: "different queue cannot qualify", change: "queue"},
		{name: "snapshot bytes bounded before decoding", change: "snapshot_budget", incomplete: true},
		{name: "payload bytes bounded before decoding", change: "payload_budget", incomplete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedStartControlReaderFixture(t, pool, models.LifecycleDispatchAnswer, false, false, false)
			binding := f.consumer.own.binding
			org := f.consumer.org
			dispatch := binding.dispatchIDs[0]
			limits := defaultLifecycleStartControlReadLimits()
			switch tt.change {
			case "snapshot":
				predicateExec(t, pool, `UPDATE session_input_dispatches SET control_snapshot='{}' WHERE org_id=$1 AND id=$2`, org, dispatch)
			case "schema":
				predicateExec(t, pool, `UPDATE session_input_dispatches SET control_snapshot=jsonb_set(control_snapshot,'{schema_version}','2') WHERE org_id=$1 AND id=$2`, org, dispatch)
			case "receipt":
				predicateExec(t, pool, `UPDATE session_input_dispatches SET control_receipt_id=gen_random_uuid() WHERE org_id=$1 AND id=$2`, org, dispatch)
			case "version":
				predicateExec(t, pool, `UPDATE session_input_dispatches SET control_version=control_version+1 WHERE org_id=$1 AND id=$2`, org, dispatch)
			case "claim":
				predicateExec(t, pool, `UPDATE session_input_dispatches SET admission_claim_token=gen_random_uuid() WHERE org_id=$1 AND id=$2`, org, dispatch)
			case "turn":
				predicateExec(t, pool, `UPDATE session_input_dispatches SET admission_claim_turn=admission_claim_turn+1 WHERE org_id=$1 AND id=$2`, org, dispatch)
			case "request":
				request := seedStartControlReaderRequest(t, pool, f.consumer)
				predicateExec(t, pool, `UPDATE session_input_dispatches SET request_id=$3 WHERE org_id=$1 AND id=$2`, org, dispatch, request)
			case "request_lane":
				predicateExec(t, pool, `UPDATE session_human_input_requests SET thread_id=$3 WHERE org_id=$1 AND id=$2`, org, binding.requestID, f.consumer.sibling)
			case "payload":
				predicateExec(t, pool, `UPDATE jobs SET payload=jsonb_set(payload,'{nested,counter}','1.0') WHERE org_id=$1 AND id=$2`, org, binding.jobID)
			case "missing_control":
				predicateExec(t, pool, `UPDATE session_input_dispatches SET control_receipt_id=NULL,control_version=NULL,control_state=NULL,control_snapshot=NULL,control_next_attempt_at=NULL,control_refund_receipt_id=NULL WHERE org_id=$1 AND id=$2`, org, dispatch)
			case "missing_dispatch":
				binding.dispatchIDs = []uuid.UUID{uuid.New()}
			case "foreign":
				org = uuid.New()
				binding.orgID = org
			case "expected_request":
				binding.requestID = uuid.New()
			case "controller":
				predicateExec(t, pool, `UPDATE jobs SET job_type='run_code_review_recheck' WHERE org_id=$1 AND id=$2`, org, binding.jobID)
			case "queue":
				predicateExec(t, pool, `UPDATE jobs SET queue='maintenance' WHERE org_id=$1 AND id=$2`, org, binding.jobID)
			case "snapshot_budget":
				limits.snapshotBytes = 8
			case "payload_budget":
				limits.payloadBytes = 8
			}
			before := startControlReaderGraph(t, pool, f)
			actual, err := runStartControlReader(t, pool, org, binding, limits)
			expectedCause := ErrLifecycleConsumerReconciliation
			if tt.incomplete {
				expectedCause = ErrLifecycleConsumerDiscoveryIncomplete
			}
			require.ErrorIs(t, err, expectedCause, "missing, drifting or bounded-out retained control must never become exact logical replay proof")
			require.Equal(t, lifecycleStartControlSnapshot{}, actual, "a rejected read must expose no partial retained snapshot")
			require.Equal(t, before, startControlReaderGraph(t, pool, f), "failed lookup or validation must preserve every retained row unchanged")
		})
	}
}

func TestLifecycleStartControlParkedRuntimeProofPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		attempt bool
	}{
		{name: "runtime-bound parked input without current attempt"},
		{name: "runtime-bound parked input with historical attempt", attempt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedStartControlReaderFixture(t, pool, models.LifecycleDispatchDenial, false, false, true)
			actual, err := runStartControlReader(t, pool, f.consumer.org, f.consumer.own.binding, defaultLifecycleStartControlReadLimits())
			require.NoError(t, err, "retained allocation references may be read structurally without becoming trusted historical or physical proof")
			spec, err := actual.Spec()
			require.NoError(t, err, "copy all structural revoked-allocation references")
			require.Equal(t, f.expected, spec, "reading a runtime-bound parked snapshot must preserve every historical reference")
			if tt.attempt {
				a := f.expected.Revocation.Allocations[0]
				f.consumer.own.attempt = &a
			}
			before := startControlReaderGraph(t, pool, f)
			proof, err := runStartControlOwnProof(t, pool, f)
			require.ErrorIs(t, err, ErrLifecycleConsumerReconciliation, "parked runtime evidence cannot use current live-lease validation or structural revocation references as B historical proof")
			require.Equal(t, lifecycleOwnConsumerProof{}, proof, "a parked runtime-bound rejection must expose no own allocation exception")
			require.Equal(t, before, startControlReaderGraph(t, pool, f), "rejecting historical allocation authority must not rewrite retained evidence")
		})
	}
}

func TestLifecycleStartControlReaderImmediateGuardsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, assignment, state string }{
		{name: "parked job cannot retain lock token", assignment: "lock_token=gen_random_uuid()", state: "23514"},
		{name: "parked job cannot retain owner", assignment: "run_owner_id='synthetic-owner'", state: "23514"},
		{name: "parked job cannot retain lease", assignment: "lease_expires_at=now()", state: "23514"},
		{name: "parked job cannot retain completion", assignment: "completed_at=now()", state: "23514"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedStartControlReaderFixture(t, pool, models.LifecycleDispatchAnswer, false, false, false)
			before := startControlReaderGraph(t, pool, f)
			_, err := pool.Exec(context.Background(), "UPDATE jobs SET "+tt.assignment+" WHERE org_id=$1 AND id=$2", f.consumer.org, f.consumer.own.binding.jobID)
			requireSessionLifecycleSQLState(t, err, tt.state, "the full schema must immediately reject parked lease/owner residue without partial writes")
			require.Equal(t, before, startControlReaderGraph(t, pool, f), "the rejected parked-state write must preserve the complete original control graph")
		})
	}
}

func TestLifecycleStartControlReaderImmediateReferencesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, change string }{
		{name: "foreign job reference", change: "job"},
		{name: "foreign request reference", change: "request"},
		{name: "foreign lane reference", change: "lane"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedStartControlReaderFixture(t, pool, models.LifecycleDispatchDenial, false, false, false)
			other := seedStartControlReaderFixture(t, pool, models.LifecycleDispatchDenial, false, false, false)
			before := startControlReaderGraph(t, pool, f)
			column, value := "job_id", other.consumer.own.binding.jobID
			if tt.change == "request" {
				column, value = "request_id", other.consumer.own.binding.requestID
			}
			if tt.change == "lane" {
				column, value = "thread_id", other.consumer.lane
			}
			_, err := pool.Exec(context.Background(), "UPDATE session_input_dispatches SET "+column+"=$3 WHERE org_id=$1 AND id=$2", f.consumer.org, f.consumer.own.binding.dispatchIDs[0], value)
			requireSessionLifecycleSQLState(t, err, "23503", "actual full-schema controls must retain immediate tenant/session/lane parent references")
			require.Equal(t, before, startControlReaderGraph(t, pool, f), "a rejected cross-scope reference must leave every retained control row unchanged")
		})
	}
}

func seedStartControlReaderFixture(t *testing.T, pool *pgxpool.Pool, kind models.LifecycleDispatchKind, nanos, offset, allocated bool) lifecycleStartControlPostgresFixture {
	t.Helper()
	consumer := newLifecyclePredicateFixture(t, pool)
	base := newLifecycleStartControlFixture(t, kind, false, false, false)
	request := seedStartControlReaderRequest(t, pool, consumer)
	binding, err := NewLifecycleJobBinding(consumer.org, consumer.fence, uuid.New(), []uuid.UUID{uuid.New()}, kind, &request)
	require.NoError(t, err, "bind a dedicated answer or denial to real tenant/session/lane/request identities")
	payload, err := encodeLifecycleJobBindingPayload(json.RawMessage(`{"provider_request_id":"synthetic-request","large_identity":900719925474099312345678901234567890,"nested":{"counter":1.00}}`), binding)
	require.NoError(t, err, "encode the immutable job while preserving synthetic numeric domain identities")
	predicateExec(t, pool, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status) VALUES($1,$2,'agent','continue_session',$3,'awaiting_start_control')`, binding.jobID, consumer.org, payload)
	var payloadText string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT payload::text FROM jobs WHERE org_id=$1 AND id=$2`, consumer.org, binding.jobID).Scan(&payloadText), "capture the actual PostgreSQL immutable payload representation for its digest")
	jobDigest, err := lifecycleStartControlPayloadDigest(json.RawMessage(payloadText))
	require.NoError(t, err, "hash actual database text rather than a re-marshalled map")
	spec := base.spec
	spec.Binding = binding
	spec.JobPayloadDigest = jobDigest
	scope := spec.Identity.Details().Scope
	scope.OrgID, scope.RequestID, scope.Fence = consumer.org, request, consumer.fence
	spec.Identity, err = NewNativeConversationResumeIdentity(scope, spec.Identity.Details().NativeConversationID)
	require.NoError(t, err, "reconstruct the sealed native identity against the actual request scope")
	target := spec.Target.Spec()
	descriptor := target.Descriptor.Spec()
	descriptor.OrgID, descriptor.SessionID = consumer.org, consumer.session
	target.Descriptor, err = NewWorkspaceDescriptor(descriptor)
	require.NoError(t, err, "reconstruct an exact scoped descriptor without interpreting retained availability as live proof")
	spec.Target, err = NewResumabilityTarget(target)
	require.NoError(t, err, "reconstruct the sealed target using real scope and retained preparation references")
	if nanos {
		zone := time.UTC
		if offset {
			zone = time.FixedZone("synthetic-offset", -5*60*60)
		}
		due := time.Date(2026, time.January, 2, 3, 4, 5, 123456789, zone)
		spec.NextAttemptAt = &due
	}
	var runtimeID, jobToken, runtimeToken *uuid.UUID
	consumerKind := models.LifecycleConsumerJob
	if allocated {
		r := createSeedRuntime(t, pool, consumer.org, consumer.session, consumer.lane)
		seedPredicateRuntimeAllocation(t, pool, consumer.org, r.ID, binding, spec.Revocation.JobClaimToken, r.LeaseToken, spec.ClaimTurn)
		// Match the actual persisted allocation digest without treating its
		// structural reference as current lease or historical drain authority.
		digest, err := ParseLifecycleDigest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		require.NoError(t, err, "read the synthetic allocation digest used by the full-schema fixture")
		a, err := NewAttemptFence(binding, spec.Revocation.JobClaimToken, r.ID, r.LeaseToken, digest, spec.ClaimTurn)
		require.NoError(t, err, "retain the exact immutable historical allocation reference")
		spec.Revocation.Allocations = []AttemptFence{a}
		runtimeID, jobToken, runtimeToken = &r.ID, &a.jobToken, &r.LeaseToken
		consumerKind = models.LifecycleConsumerRuntime
	}
	snapshot, err := newLifecycleStartControlSnapshot(spec)
	require.NoError(t, err, "construct the complete retained snapshot through its finite sealed DTO boundary")
	spec, err = snapshot.Spec()
	require.NoError(t, err, "retain the canonical defensive snapshot values used for database persistence")
	raw, err := encodeLifecycleStartControlSnapshot(snapshot)
	require.NoError(t, err, "persist only the supported finite retained snapshot wire representation")
	inputDigest, err := spec.InputDigest.Encode()
	require.NoError(t, err, "encode the exact input identity retained by the control")
	predicateExec(t, pool, `INSERT INTO session_input_dispatches(id,org_id,session_id,thread_id,session_epoch,thread_epoch,request_id,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,
	 admission_state,consumer_kind,job_id,runtime_id,job_lock_token,runtime_lease_token,admission_claim_token,admission_claim_turn,
	 control_receipt_id,control_version,control_state,control_snapshot,control_next_attempt_at,control_refund_receipt_id,deadline_at)
	 VALUES($1,$2,$3,$4,0,0,$5,$6,'synthetic_retained_control',$1::uuid::text,$7,'{}',1,'bound',$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,now()-interval '1 day')`,
		binding.dispatchIDs[0], consumer.org, consumer.session, consumer.lane, request, kind, inputDigest, consumerKind, binding.jobID, runtimeID, jobToken, runtimeToken, spec.ClaimToken, spec.ClaimTurn,
		spec.ReceiptID, spec.Version, spec.State, raw, spec.NextAttemptAt, spec.RefundReceiptID)
	consumer.own = &lifecycleOwnConsumer{binding: binding}
	return lifecycleStartControlPostgresFixture{consumer: consumer, expected: spec, snapshot: snapshot}
}

func seedStartControlReaderRequest(t *testing.T, pool *pgxpool.Pool, f lifecyclePredicateFixture) uuid.UUID {
	t.Helper()
	id := uuid.New()
	predicateExec(t, pool, `INSERT INTO session_human_input_requests(id,org_id,session_id,thread_id,turn_number,agent_type,provider_request_id,request_kind,status,title,body,admission_session_epoch,admission_thread_epoch)
	 VALUES($1,$2,$3,$4,2,$5,$1::uuid::text,'tool_approval','answered','Synthetic retained request','Synthetic request body',0,0)`, id, f.org, f.session, f.lane, models.AgentTypeCodex)
	return id
}

func seedStartControlReaderPreparation(t *testing.T, pool *pgxpool.Pool, f lifecycleStartControlPostgresFixture) {
	t.Helper()
	s := f.expected
	b := s.Binding
	predicateExec(t, pool, `INSERT INTO session_runtime_start_preparations(id,org_id,session_id,thread_id,job_id,input_dispatch_ids,dispatch_kind,request_id,session_epoch,thread_epoch,version,token,job_attempt_token,state,deadline_at,next_attempt_at,revoked_at,target,resume_identity)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,0,0,1,$9,$10,'closed',now()-interval '2 days',now()-interval '1 day',now()-interval '1 day','{}','{}')`, s.Revocation.PreparationID, b.orgID, b.fence.sessionID, b.fence.threadID, b.jobID, b.dispatchIDs, b.kind, b.requestID, s.Revocation.PreparationToken, s.Revocation.JobClaimToken)
}

func runStartControlReader(t *testing.T, pool *pgxpool.Pool, org uuid.UUID, binding LifecycleJobBinding, limits lifecycleStartControlReadLimits) (lifecycleStartControlSnapshot, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "caller owns the bounded retained-control transaction")
	defer func() { _ = tx.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "caller configures retained-control reads before acquiring its known locks")
	// Job identity is known before scope acquisition; no discovered locks are
	// introduced by the reader. A foreign lookup locks no absent parent.
	var jobID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE`, org, binding.jobID).Scan(&jobID)
	if err == nil {
		var lanes []uuid.UUID
		if binding.fence.threaded {
			lanes = []uuid.UUID{binding.fence.threadID}
		}
		_, err = lockLifecycleScopeInTx(ctx, tx, org, lifecycleScope{sessionID: binding.fence.sessionID, threadIDs: lanes})
		require.NoError(t, err, "caller locks the exact parent and explicit lane after its known higher-rank job")
	} else {
		require.ErrorIs(t, err, pgx.ErrNoRows, "only a deliberately absent tenant-scoped job may avoid scope acquisition in a negative fixture")
	}
	actual, readErr := readLifecycleStartControlWithLimitsInTx(ctx, tx, org, binding, limits)
	var open int
	require.NoError(t, tx.QueryRow(ctx, "SELECT 1").Scan(&open), "retained-control reads and typed rejection leave the caller transaction open")
	require.Equal(t, 1, open, "the reader cannot settle or replace its caller transaction")
	require.NoError(t, tx.Rollback(ctx), "the caller settles its retained-control read transaction")
	return actual, readErr
}

func runStartControlOwnProof(t *testing.T, pool *pgxpool.Pool, f lifecycleStartControlPostgresFixture) (lifecycleOwnConsumerProof, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "caller owns the parked exact-replay proof transaction")
	defer func() { _ = tx.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "caller configures the parked replay proof transaction")
	var job uuid.UUID
	require.NoError(t, tx.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE`, f.consumer.org, f.consumer.own.binding.jobID).Scan(&job), "caller acquires its known exact higher-rank job before the parent scope")
	_, err = lockLifecycleScopeInTx(ctx, tx, f.consumer.org, lifecycleScope{sessionID: f.consumer.session, threadIDs: []uuid.UUID{f.consumer.lane}})
	require.NoError(t, err, "caller acquires the exact parent and lane for parked logical proof")
	proof, proofErr := proveOwnLifecycleConsumerInTx(ctx, tx, f.consumer.org, f.consumer.fence, f.consumer.own, 512)
	require.NoError(t, tx.Rollback(ctx), "only the caller may settle parked logical proof")
	return proof, proofErr
}

func startControlReaderGraph(t *testing.T, pool *pgxpool.Pool, f lifecycleStartControlPostgresFixture) json.RawMessage {
	t.Helper()
	b := f.consumer.own.binding
	var graph json.RawMessage
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
	 'dispatch',(SELECT to_jsonb(d) FROM session_input_dispatches d WHERE d.org_id=$1 AND d.id=$2),
	 'job',(SELECT to_jsonb(j) FROM jobs j WHERE j.org_id=$1 AND j.id=$3),
	 'request',(SELECT to_jsonb(r) FROM session_human_input_requests r WHERE r.org_id=$1 AND r.id=$4),
	 'preparations',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM session_runtime_start_preparations p WHERE p.org_id=$1 AND p.job_id=$3))`, b.orgID, b.dispatchIDs[0], b.jobID, b.requestID).Scan(&graph), "capture complete synthetic retained rows to prove reads perform no mutation")
	return graph
}
