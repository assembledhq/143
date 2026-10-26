package db

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type lifecycleConsumerTestResult struct {
	blocked bool
	cause   error
}

func lifecycleConsumerResult(blocked bool, err error) lifecycleConsumerTestResult {
	if errors.Is(err, ErrLifecycleConsumerReconciliation) {
		return lifecycleConsumerTestResult{blocked, ErrLifecycleConsumerReconciliation}
	}
	if errors.Is(err, ErrLifecycleConsumerDiscoveryIncomplete) {
		return lifecycleConsumerTestResult{blocked, ErrLifecycleConsumerDiscoveryIncomplete}
	}
	return lifecycleConsumerTestResult{blocked, err}
}

func lifecycleConsumerTestPtr[T any](v T) *T { return &v }

func TestLifecycleConsumerEvidenceScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                               string
		threaded, whole, threadless, contradictory, missingParent, sibling bool
		expected                                                           lifecycleConsumerTestResult
	}{
		{name: "same lane", threaded: true, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "compatible sibling", threaded: true, sibling: true},
		{name: "session-wide request overlaps sibling", sibling: true, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "threadless consumer overlaps any lane", threaded: true, threadless: true, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "whole-workspace physical operation overlaps sibling", threaded: true, sibling: true, whole: true, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "contradictory session and actual lane parent", threaded: true, contradictory: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "missing lane parent is not an unrelated sibling", threaded: true, missingParent: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, session, lane := legacyJobRouteTestIDs()
			fence := AdmissionFence{sessionID: session, threadID: lane, threaded: tt.threaded}
			if !tt.threaded {
				fence.threadID = uuid.Nil
			}
			v := lifecycleConsumerEvidence{source: lifecycleConsumerRuntime, id: uuid.New(), sessionID: session, threadID: &lane, threadSessionID: &session, wholeSession: tt.whole}
			if tt.sibling {
				v.threadID = lifecycleConsumerTestPtr(uuid.New())
			}
			if tt.threadless {
				v.threadID = nil
				v.threadSessionID = nil
			}
			if tt.contradictory {
				v.sessionID = uuid.New()
			}
			if tt.missingParent {
				v.threadSessionID = nil
			}
			blocked, err := lifecycleConsumerEvidenceOverlaps(v, fence)
			require.Equal(t, tt.expected, lifecycleConsumerResult(blocked, err), "scope overlap must retain contradictory anchors while permitting a proven compatible sibling")
		})
	}
}

func TestLifecycleConsumerEvidenceClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                      string
		source                                    lifecycleConsumerSource
		state, control                            string
		missingJob, unknownOwner, physical, owned bool
		expected                                  lifecycleConsumerTestResult
	}{
		{name: "bound job", source: lifecycleConsumerDispatch, state: "bound", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "parked control never expires here", source: lifecycleConsumerDispatch, state: "bound", control: "awaiting_control", owned: true, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "uncertain superseded delivery", source: lifecycleConsumerDispatch, state: "historical", control: "superseded_delivery_uncertain", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "dangling reverse reference", source: lifecycleConsumerDispatch, state: "bound", missingJob: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "exact own dispatch", source: lifecycleConsumerDispatch, state: "bound", owned: true},
		{name: "live runtime", source: lifecycleConsumerRuntime, state: "live", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "expired lease is not examined as drain", source: lifecycleConsumerRuntime, state: "draining", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "terminal runtime needs normalization", source: lifecycleConsumerRuntime, state: "closed", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "lost runtime cannot be own-excluded", source: lifecycleConsumerRuntime, state: "lost", owned: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "exact own runtime", source: lifecycleConsumerRuntime, state: "live", owned: true},
		{name: "live executor", source: lifecycleConsumerExecutor, state: "running", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "own job does not exclude another executor", source: lifecycleConsumerExecutor, state: "starting", owned: true, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "completed executor needs physical proof", source: lifecycleConsumerExecutor, state: "completed", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "unknown inbox survives own exception", source: lifecycleConsumerInbox, state: "unknown_delivery", owned: true, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "acked inbox lacks positive application", source: lifecycleConsumerInbox, state: "acked", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "previously delivered pending inbox remains uncertain", source: lifecycleConsumerInbox, state: "pending", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "previously delivered dead letter remains uncertain", source: lifecycleConsumerInbox, state: "dead_letter", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "open own preparation is not automatically excluded", source: lifecycleConsumerPreparation, state: "authorized", owned: true, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "closed physical operation remains unresolved", source: lifecycleConsumerPreparation, state: "closed", physical: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "runtime holder resolves actual own allocation", source: lifecycleConsumerHolder, state: "live", owned: true},
		{name: "holder without actual owner", source: lifecycleConsumerHolder, state: "active", unknownOwner: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "active domain association", source: lifecycleConsumerAutomation, state: "running", expected: lifecycleConsumerTestResult{blocked: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, session, lane := legacyJobRouteTestIDs()
			job, runtime, dispatch := uuid.New(), uuid.New(), uuid.New()
			binding, err := NewLifecycleJobBinding(org, AdmissionFence{sessionID: session, threadID: lane, threaded: true}, job, []uuid.UUID{dispatch}, models.LifecycleDispatchMessage, nil)
			require.NoError(t, err, "construct immutable comparison input without treating it as durable ownership")
			v := lifecycleConsumerEvidence{source: tt.source, id: dispatch, sessionID: session, threadID: &lane, threadSessionID: &session, jobID: &job, foundJobID: &job, runtimeID: &runtime, jobStatus: "pending", state: tt.state, controlState: tt.control, physical: tt.physical, ownerMatches: !tt.unknownOwner}
			if tt.missingJob {
				v.foundJobID = nil
			}
			own := lifecycleOwnConsumerProof{}
			if tt.owned {
				own = lifecycleOwnConsumerProof{binding: &binding, runtimeID: runtime}
			}
			blocked, err := classifyLifecycleConsumerEvidence(v, own)
			require.Equal(t, tt.expected, lifecycleConsumerResult(blocked, err), "logical ownership and unresolved physical evidence must remain independent of lease time, terminal labels and same-job hints")
		})
	}
}

func TestLifecycleConsumerLegacyScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, payload             string
		reverse, parentOther, own bool
		expected                  lifecycleConsumerTestResult
	}{
		{name: "threadless session spans lane", payload: `{"session_id":"22222222-2222-4222-8222-222222222222"}`, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "invalid thread cannot narrow session", payload: `{"session_id":"22222222-2222-4222-8222-222222222222","thread_id":"bad"}`, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "unrelated malformed required route", payload: `{"session_id":"bad"}`},
		{name: "malformed payload with scoped reverse ref", payload: `42`, reverse: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "folded session alias is scoped", payload: `{"ſeſſion_id":"22222222-2222-4222-8222-222222222222"}`, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "valid anchor survives later malformed duplicate", payload: `{"session_id":"22222222-2222-4222-8222-222222222222","SESSION_ID":false}`, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "thread A anchors session B payload", payload: `{"session_id":"55555555-5555-4555-8555-555555555555","thread_id":"33333333-3333-4333-8333-333333333333"}`, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "session A has thread from another session", payload: `{"session_id":"22222222-2222-4222-8222-222222222222","thread_id":"33333333-3333-4333-8333-333333333333"}`, parentOther: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "foreign payload org cannot authorize tenant read", payload: `{"org_id":"99999999-9999-4999-8999-999999999999","session_id":"22222222-2222-4222-8222-222222222222"}`, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "exact already-proved own job", payload: `{"session_id":"22222222-2222-4222-8222-222222222222","thread_id":"33333333-3333-4333-8333-333333333333"}`, own: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, session, lane := legacyJobRouteTestIDs()
			jobID := uuid.New()
			parents := map[uuid.UUID]uuid.UUID{lane: session}
			if tt.parentOther {
				parents[lane] = uuid.New()
			}
			proof := lifecycleOwnConsumerProof{}
			if tt.own {
				proof.binding = &LifecycleJobBinding{jobID: jobID}
			}
			job := lifecycleLegacyConsumerJob{jobID, decodeLegacyJobRoute(org, []byte(tt.payload))}
			blocked, err := classifyLegacyLifecycleConsumer(job, AdmissionFence{sessionID: session, threadID: lane, threaded: true}, parents, tt.reverse, proof)
			require.Equal(t, tt.expected, lifecycleConsumerResult(blocked, err), "partial legacy anchors must be reconciled within their real scoped parent rather than erased or broadened tenant-wide")
		})
	}
}

func TestLifecycleConsumerSuppliedTransactionBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		recovery  bool
		policy    string
		expected  lifecycleConsumerTestResult
		errorText string
	}{
		{name: "bounded supplied read", policy: "read committed"},
		{name: "recovery spans all lanes", policy: "read committed", recovery: true, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "repeatable read rejected", policy: "repeatable read", errorText: "configured READ COMMITTED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated supplied-transaction expectations")
			defer mock.Close()
			org, session, lane := legacyJobRouteTestIDs()
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleTransactionPolicyQuery)).WillReturnRows(pgxmock.NewRows([]string{"isolation", "marker", "lock", "statement"}).AddRow(tt.policy, "1", "250ms", "1500ms"))
			if tt.errorText == "" {
				mock.ExpectQuery("SELECT workspace_recovery_id IS NOT NULL").WithArgs(org, session, true, lane).WillReturnRows(pgxmock.NewRows([]string{"recovery", "member"}).AddRow(tt.recovery, true))
				mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerLaneKeysQuery)).WithArgs(pgx.QueryExecModeExec, org, session, 4097).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(lane))
				mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerGenerationKeysQuery)).WithArgs(pgx.QueryExecModeExec, org, session, 33).WillReturnRows(pgxmock.NewRows([]string{"target_id", "generation"}))
				mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerRuntimeKeysQuery)).WithArgs(pgx.QueryExecModeExec, org, session, "{"+lane.String()+"}", 513).WillReturnRows(pgxmock.NewRows([]string{"id"}))
				mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerEvidenceQuery)).WithArgs(pgx.QueryExecModeExec, org, session, 513, "{"+lane.String()+"}", "{}").WillReturnRows(pgxmock.NewRows([]string{"source", "id", "session", "thread", "parent", "job", "runtime", "state", "control", "whole", "physical", "owner", "found_job", "job_status"}))
				mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerLegacyJobsQuery)).WithArgs(org, 513, legacyJobRouteMaxBytes).WillReturnRows(pgxmock.NewRows([]string{"id", "size", "payload"}))
			}
			blocked, err := HasCompetingInputConsumerInTx(ctx, mock, org, AdmissionFence{sessionID: session, threadID: lane, threaded: true}, nil)
			if tt.errorText != "" {
				require.ErrorContains(t, err, tt.errorText, "the reader must require existing transaction policy without installing or replacing it")
			} else {
				require.Equal(t, tt.expected, lifecycleConsumerResult(blocked, err), "the reader must compose read-only probes without beginning, locking discovered jobs or settling the owner transaction")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "only the supplied transaction's bounded reads may execute")
		})
	}
}

func TestLifecycleConsumerUUIDArray(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		ids      []uuid.UUID
		expected string
	}{
		{name: "nil is empty", expected: "{}"},
		{name: "empty is empty", ids: []uuid.UUID{}, expected: "{}"},
		{name: "canonical zero", ids: []uuid.UUID{uuid.Nil}, expected: "{00000000-0000-0000-0000-000000000000}"},
		{name: "preserve exact ordered values", ids: []uuid.UUID{{15: 2}, {15: 1}}, expected: "{00000000-0000-0000-0000-000000000002,00000000-0000-0000-0000-000000000001}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, lifecycleConsumerUUIDArray(tt.ids), "custom-plan UUID-array parameters must be exact canonical bound text without connection-wide codec changes")
		})
	}
}

func TestLifecycleConsumerScopeKeyBudgets(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, failure string }{
		{name: "complete exact keys"},
		{name: "lane overflow", failure: "lanes"},
		{name: "33 historical generations exceed scalar probe budget", failure: "generations"},
		{name: "runtime overflow", failure: "runtimes"},
		{name: "lane read error", failure: "lane error"},
		{name: "generation read error", failure: "generation error"},
		{name: "runtime read error", failure: "runtime error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the isolated supplied scope-observation transaction")
			t.Cleanup(mock.Close)
			org, session, lane := legacyJobRouteTestIDs()
			runtime, target := uuid.New(), uuid.New()
			limits := defaultLifecycleConsumerLimits()
			if tt.failure == "lanes" {
				limits.laneAnchors = 1
			}
			if tt.failure == "runtimes" {
				limits.structuredRows = 1
			}
			failure := errors.New("synthetic scope-key read failure")
			// Independent SQL expectations pin unfiltered actual lane/history
			// membership, not archived/hidden/active-only approximations.
			laneRead := mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM session_threads WHERE org_id=$1 AND session_id=$2 ORDER BY id LIMIT $3")).WithArgs(pgx.QueryExecModeExec, org, session, limits.laneAnchors+1)
			if tt.failure == "lane error" {
				laneRead.WillReturnError(failure)
			} else {
				rows := pgxmock.NewRows([]string{"id"}).AddRow(lane)
				if tt.failure == "lanes" {
					rows.AddRow(uuid.New())
				}
				laneRead.WillReturnRows(rows)
			}
			if tt.failure != "lane error" && tt.failure != "lanes" {
				generationRead := mock.ExpectQuery(regexp.QuoteMeta("SELECT target_id,generation FROM automation_target_sessions WHERE org_id=$1 AND session_id=$2 ORDER BY target_id,generation LIMIT $3")).WithArgs(pgx.QueryExecModeExec, org, session, min(limits.laneAnchors, 32)+1)
				if tt.failure == "generation error" {
					generationRead.WillReturnError(failure)
				} else {
					rows := pgxmock.NewRows([]string{"target_id", "generation"})
					count := 1
					if tt.failure == "generations" {
						count = 33
					}
					for generation := 1; generation <= count; generation++ {
						rows.AddRow(target, generation)
					}
					generationRead.WillReturnRows(rows)
				}
				if tt.failure != "generation error" && tt.failure != "generations" {
					runtimeRead := mock.ExpectQuery(regexp.QuoteMeta(`( SELECT id FROM thread_runtimes WHERE org_id=$1 AND session_id=$2 ORDER BY id LIMIT $4)
					 UNION ALL ( SELECT id FROM thread_runtimes WHERE org_id=$1 AND thread_id=ANY($3::uuid[]) AND session_id IS DISTINCT FROM $2 ORDER BY id LIMIT $4) LIMIT $4`)).WithArgs(pgx.QueryExecModeExec, org, session, "{"+lane.String()+"}", limits.structuredRows+1)
					if tt.failure == "runtime error" {
						runtimeRead.WillReturnError(failure)
					} else {
						rows := pgxmock.NewRows([]string{"id"}).AddRow(runtime)
						if tt.failure == "runtimes" {
							rows.AddRow(uuid.New())
						}
						runtimeRead.WillReturnRows(rows)
					}
				}
			}
			keys, err := readLifecycleConsumerScopeKeysInTx(context.Background(), mock, org, AdmissionFence{sessionID: session}, limits)
			if tt.failure == "" {
				require.NoError(t, err, "complete target scope observation must preserve all exact actual keys")
				require.Equal(t, lifecycleConsumerScopeKeys{lanes: []uuid.UUID{lane}, runtimes: []uuid.UUID{runtime}, generations: []lifecycleConsumerGenerationKey{{targetID: target, generation: 1}}}, keys, "scope keys must retain exact generation pairs and actual UUID membership without manufacturing authority")
			} else {
				require.Equal(t, lifecycleConsumerScopeKeys{}, keys, "overflow or SQL failure must return no partial key set that a caller could mistake for a complete negative")
				if strings.Contains(tt.failure, "error") {
					require.ErrorIs(t, err, failure, "the original scope observation failure must remain available to the owner")
				} else {
					require.ErrorIs(t, err, ErrLifecycleConsumerDiscoveryIncomplete, "a bounded array or generation-query budget overflow must never become clear or partial success")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet(), "overflow must stop before later probes and every skew-sensitive read must request custom planning rather than a cached generic plan")
		})
	}
}

func TestLifecycleConsumerDispatchSet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		left, right []uuid.UUID
		expected    bool
	}{
		{name: "same set reordered", left: []uuid.UUID{{15: 1}, {15: 2}}, right: []uuid.UUID{{15: 2}, {15: 1}}, expected: true},
		{name: "extra input", left: []uuid.UUID{{15: 1}}, right: []uuid.UUID{{15: 1}, {15: 2}}},
		{name: "duplicate input", left: []uuid.UUID{{15: 1}, {15: 1}}, right: []uuid.UUID{{15: 1}, {15: 1}}},
		{name: "zero input", left: []uuid.UUID{{}}, right: []uuid.UUID{{}}},
		{name: "empty input set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, sameLifecycleDispatchSet(tt.left, tt.right), "allocation identity must compare a complete nonzero unique input set")
		})
	}
}

func TestLifecycleConsumerOwnProofDatabaseIdentity(t *testing.T) {
	t.Parallel()
	// Keep these query expectations independent of production SQL: changing a
	// fresh database clock or the scoped, NULL-safe request join must fail here.
	jobQuery := `SELECT CASE WHEN octet_length(payload::text)<=$3 THEN payload ELSE NULL END,
	 status,queue,job_type,lock_token,COALESCE(lease_expires_at>statement_timestamp(),false),octet_length(payload::text) FROM jobs WHERE org_id=$1 AND id=$2`
	dispatchQuery := `SELECT d.id,d.session_id,d.thread_id,d.session_epoch,d.thread_epoch,d.kind,d.request_id,
	 d.admission_state,d.consumer_kind,d.job_id,d.runtime_id,d.job_lock_token,d.runtime_lease_token,
	 d.request_id IS NULL OR (r.id IS NOT NULL AND r.thread_id IS NOT DISTINCT FROM d.thread_id)
	 FROM session_input_dispatches d
	 LEFT JOIN session_human_input_requests r ON r.org_id=d.org_id AND r.session_id=d.session_id AND r.id=d.request_id
	 WHERE d.org_id=$1 AND (d.id=ANY($2::uuid[]) OR d.job_id=$3) ORDER BY d.id LIMIT $4`
	runtimeQuery := `SELECT COALESCE(job_id,'00000000-0000-0000-0000-000000000000'),COALESCE(job_attempt_token,'00000000-0000-0000-0000-000000000000'),lease_token,session_id,thread_id,
	 COALESCE(admission_session_epoch,-1),COALESCE(admission_thread_epoch,-1),COALESCE(allocation_digest,''),COALESCE(input_dispatch_ids,'{}'::uuid[]),COALESCE(allocation_expected_turn,-1),status,
	 COALESCE(lease_expires_at>statement_timestamp(),false),drain_proof IS NOT NULL OR first_provider_launch_marker IS NOT NULL OR lease_loss_receipt IS NOT NULL
	 OR aborted_allocation_receipt IS NOT NULL OR provider_terminal_result IS NOT NULL
	 OR provider_terminal_result_digest IS NOT NULL OR provider_terminal_settled_at IS NOT NULL
	 FROM thread_runtimes WHERE org_id=$1 AND id=$2`
	tests := []struct {
		name                                                       string
		kind                                                       models.LifecycleDispatchKind
		runtime, sessionWide, jobLive, runtimeLive, requestMatches bool
		wantReconciliation                                         bool
	}{
		{name: "pending message needs no live lease", kind: models.LifecycleDispatchMessage, requestMatches: true},
		{name: "exact allocation accepts both database live booleans", kind: models.LifecycleDispatchMessage, runtime: true, jobLive: true, runtimeLive: true, requestMatches: true},
		{name: "database false job lease rejects before dispatch read", kind: models.LifecycleDispatchMessage, runtime: true, runtimeLive: true, requestMatches: true, wantReconciliation: true},
		{name: "database false runtime lease rejects exact allocation", kind: models.LifecycleDispatchMessage, runtime: true, jobLive: true, requestMatches: true, wantReconciliation: true},
		{name: "pending answer matches actual request", kind: models.LifecycleDispatchAnswer, requestMatches: true},
		{name: "running denial matches actual request", kind: models.LifecycleDispatchDenial, runtime: true, jobLive: true, runtimeLive: true, requestMatches: true},
		{name: "pending answer request mismatch returns zero proof", kind: models.LifecycleDispatchAnswer, wantReconciliation: true},
		{name: "running denial request mismatch returns zero proof", kind: models.LifecycleDispatchDenial, runtime: true, jobLive: true, runtimeLive: true, wantReconciliation: true},
		{name: "session-wide answer matches null request lane", kind: models.LifecycleDispatchAnswer, sessionWide: true, requestMatches: true},
		{name: "session-wide denial rejects non-null request lane", kind: models.LifecycleDispatchDenial, sessionWide: true, wantReconciliation: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated exact own-proof query expectations")
			defer mock.Close()
			org, session, lane := legacyJobRouteTestIDs()
			fence := AdmissionFence{sessionID: session, threadID: lane, threaded: true}
			var thread *uuid.UUID
			var threadEpoch *int64
			if tt.sessionWide {
				fence.threadID, fence.threaded = uuid.Nil, false
			} else {
				thread, threadEpoch = &lane, lifecycleConsumerTestPtr(int64(0))
			}
			var request *uuid.UUID
			if tt.kind == models.LifecycleDispatchAnswer || tt.kind == models.LifecycleDispatchDenial {
				request = lifecycleConsumerTestPtr(uuid.New())
			}
			binding, err := NewLifecycleJobBinding(org, fence, uuid.New(), []uuid.UUID{uuid.New()}, tt.kind, request)
			require.NoError(t, err, "construct one exact immutable own-consumer input")
			payload, err := encodeLifecycleJobBindingPayload(nil, binding)
			require.NoError(t, err, "encode the actual job's immutable input binding")
			own := &lifecycleOwnConsumer{binding: binding}
			var token, runtimeID, runtimeToken *uuid.UUID
			status, consumer := "pending", "job"
			if tt.runtime {
				digest, err := ParseLifecycleDigest(strings.Repeat("a", 64))
				require.NoError(t, err, "construct exact allocation digest without lease-clock authority")
				attempt, err := NewAttemptFence(binding, uuid.New(), uuid.New(), uuid.New(), digest, 7)
				require.NoError(t, err, "construct the exact canonical runtime identity")
				own.attempt = &attempt
				token, runtimeID, runtimeToken = &attempt.jobToken, &attempt.runtimeID, &attempt.runtimeToken
				status, consumer = "running", "runtime"
			}
			mock.ExpectQuery(regexp.QuoteMeta(jobQuery)).WithArgs(org, binding.jobID, legacyJobRouteMaxBytes).
				WillReturnRows(pgxmock.NewRows([]string{"payload", "status", "queue", "job_type", "token", "lease_current", "size"}).AddRow(payload, status, "agent", "continue_session", token, tt.jobLive, len(payload)))
			if !tt.runtime || tt.jobLive {
				mock.ExpectQuery(regexp.QuoteMeta(dispatchQuery)).WithArgs(org, binding.dispatchIDs, binding.jobID, 513).
					WillReturnRows(pgxmock.NewRows([]string{"id", "session", "thread", "session_epoch", "thread_epoch", "kind", "request", "state", "consumer", "job", "runtime", "job_token", "runtime_token", "request_matches"}).
						AddRow(binding.dispatchIDs[0], session, thread, lifecycleConsumerTestPtr(int64(0)), threadEpoch, tt.kind, request, "bound", consumer, &binding.jobID, runtimeID, token, runtimeToken, tt.requestMatches))
				if tt.requestMatches {
					associations := pgxmock.NewRows([]string{"source", "id", "session", "thread", "runtime", "consistent"})
					if tt.runtime {
						associations.AddRow(lifecycleConsumerRuntime, *runtimeID, &session, thread, runtimeID, true)
					}
					mock.ExpectQuery("SELECT source,id,session_id,thread_id,runtime_id,consistent FROM").WithArgs(org, binding.jobID, 513).WillReturnRows(associations)
					if tt.runtime {
						mock.ExpectQuery(regexp.QuoteMeta(runtimeQuery)).WithArgs(org, *runtimeID).
							WillReturnRows(pgxmock.NewRows([]string{"job", "job_token", "runtime_token", "session", "thread", "session_epoch", "thread_epoch", "digest", "inputs", "turn", "status", "lease_current", "unresolved"}).
								AddRow(binding.jobID, *token, *runtimeToken, session, lane, int64(0), int64(0), strings.Repeat("a", 64), binding.dispatchIDs, 7, "live", tt.runtimeLive, false))
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			actual, err := proveOwnLifecycleConsumerInTx(ctx, mock, org, fence, own, 512)
			expected := lifecycleOwnConsumerProof{}
			if tt.wantReconciliation {
				require.ErrorIs(t, err, ErrLifecycleConsumerReconciliation, "non-live lease or contradictory request identity must require scoped reconciliation")
			} else {
				require.NoError(t, err, "database-confirmed lease and request identity must permit only exact logical own proof")
				expected.binding = &binding
				if tt.runtime {
					expected.runtimeID = *runtimeID
				}
			}
			require.Equal(t, expected, actual, "failed own-proof reads must return zero and successful reads must return only the complete exact identity")
			require.NoError(t, mock.ExpectationsWereMet(), "lease authority must use fresh database statement time and request membership must use actual tenant/session/null-safe-lane rows")
		})
	}
}

func TestLifecycleConsumerRejectsMissingProtocolInputs(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, change, want string }{
		{name: "nil context", change: "context", want: "context and supplied transaction"},
		{name: "nil transaction", change: "transaction", want: "context and supplied transaction"},
		{name: "zero organization", change: "org", want: "nonzero organization"},
		{name: "missing exact session", change: "fence", want: "exact session"},
		{name: "no whole-unit deadline", change: "deadline", want: "existing deadline"},
		{name: "nonpositive discovery budget", change: "budget", want: "budgets must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated dependency boundary mock")
			defer mock.Close()
			org, session, lane := legacyJobRouteTestIDs()
			fence := AdmissionFence{sessionID: session, threadID: lane, threaded: true}
			limits := defaultLifecycleConsumerLimits()
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			if tt.change == "context" {
				ctx = nil
			}
			if tt.change == "org" {
				org = uuid.Nil
			}
			if tt.change == "fence" {
				fence = AdmissionFence{}
			}
			if tt.change == "deadline" {
				ctx = context.Background()
			}
			if tt.change == "budget" {
				limits.legacyJobs = 0
			}
			var blocked bool
			if tt.change == "transaction" {
				blocked, err = hasCompetingInputConsumerInTx(ctx, nil, org, fence, nil, limits)
			} else {
				blocked, err = hasCompetingInputConsumerInTx(ctx, mock, org, fence, nil, limits)
			}
			require.False(t, blocked, "invalid caller protocol must never establish a positive or clear admission result")
			require.ErrorContains(t, err, tt.want, "the consumer helper must reject absent prerequisites before querying or configuring anything")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid protocol inputs must cause no transaction side effects or reads")
		})
	}
}

func TestLifecycleConsumerSourceValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		source   lifecycleConsumerSource
		expected bool
	}{
		{lifecycleConsumerDispatch, true}, {lifecycleConsumerRuntime, true}, {lifecycleConsumerExecutor, true}, {lifecycleConsumerInbox, true}, {lifecycleConsumerPreparation, true}, {lifecycleConsumerRecheck, true}, {lifecycleConsumerAutomation, true}, {lifecycleConsumerHolder, true}, {lifecycleConsumerLegacyJob, true}, {"invented", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.source), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.source.validate() == nil, "consumer source outcomes must remain a finite typed set")
		})
	}
}

func TestLifecycleStartControlReaderPrerequisites(t *testing.T) {
	t.Parallel()
	databaseErr := errors.New("synthetic retained row read failed")
	tests := []struct {
		name, change, text string
		cause              error
	}{
		{name: "nil context", change: "context", text: "context and supplied transaction"},
		{name: "nil transaction", change: "transaction", text: "context and supplied transaction"},
		{name: "missing whole-unit deadline", change: "deadline", text: "existing deadline"},
		{name: "nonpositive bounded read", change: "budget", text: "budgets must be positive"},
		{name: "non-dedicated binding", change: "binding", cause: ErrLifecycleConsumerReconciliation},
		{name: "canceled caller", change: "canceled", cause: context.Canceled},
		{name: "policy SQL failure preserved", change: "policy", cause: databaseErr},
		{name: "actual row SQL failure preserved", change: "sql", cause: databaseErr},
		{name: "missing actual row reconciles", change: "missing", cause: ErrLifecycleConsumerReconciliation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated retained-control transaction expectations")
			defer mock.Close()
			f := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, false, false, false)
			binding := f.spec.Binding
			limits := defaultLifecycleStartControlReadLimits()
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			switch tt.change {
			case "context":
				ctx = nil
			case "deadline":
				ctx = context.Background()
			case "budget":
				limits.snapshotBytes = 0
			case "binding":
				binding.kind = models.LifecycleDispatchMessage
				binding.requestID = uuid.Nil
			case "canceled":
				cancel()
			case "policy":
				mock.ExpectQuery(regexp.QuoteMeta(lifecycleTransactionPolicyQuery)).WillReturnError(databaseErr)
			case "sql", "missing":
				mock.ExpectQuery(regexp.QuoteMeta(lifecycleTransactionPolicyQuery)).WillReturnRows(pgxmock.NewRows([]string{"isolation", "marker", "lock", "statement"}).AddRow("read committed", "1", "250ms", "1500ms"))
				query := mock.ExpectQuery(regexp.QuoteMeta(lifecycleStartControlReadQuery)).WithArgs(binding.orgID, binding.dispatchIDs[0], limits.snapshotBytes, limits.payloadBytes, lifecycleStartControlMetadataBytes)
				if tt.change == "missing" {
					query.WillReturnError(pgx.ErrNoRows)
				} else {
					query.WillReturnError(databaseErr)
				}
			}
			var actual lifecycleStartControlSnapshot
			if tt.change == "transaction" {
				actual, err = readLifecycleStartControlWithLimitsInTx(ctx, nil, binding.orgID, binding, limits)
			} else {
				actual, err = readLifecycleStartControlWithLimitsInTx(ctx, mock, binding.orgID, binding, limits)
			}
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "retained-control lookup must preserve SQL/context causes and use typed reconciliation for absent evidence")
			} else {
				require.ErrorContains(t, err, tt.text, "retained-control reads must require existing caller protocol rather than create it")
			}
			require.Equal(t, lifecycleStartControlSnapshot{}, actual, "invalid prerequisites or failed reads must expose no partial snapshot")
			require.NoError(t, mock.ExpectationsWereMet(), "the reader may only inspect supplied transaction policy and exact bounded rows")
		})
	}
}
