package db

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type lifecycleStartControlFixture struct {
	spec     lifecycleStartControlSpec
	snapshot lifecycleStartControlSnapshot
	dispatch lifecycleStartControlDispatchRow
	job      models.Job
}

func newLifecycleStartControlFixture(t *testing.T, kind models.LifecycleDispatchKind, allocated, continuation, restore bool) lifecycleStartControlFixture {
	t.Helper()
	f := newWorkspaceContractFixture(t)
	binding, err := NewLifecycleJobBinding(f.scope.OrgID, f.scope.Fence, uuid.New(), []uuid.UUID{uuid.New()}, kind, &f.scope.RequestID)
	require.NoError(t, err, "construct the fixed dedicated request binding")
	payload, err := encodeLifecycleJobBindingPayload(json.RawMessage(`{"provider_request_id":"synthetic-request","large_identity":900719925474099312345678901234567890,"nested":{"counter":1.00}}`), binding)
	require.NoError(t, err, "retain exact large numeric domain identity in the immutable job")
	jobDigest, err := lifecycleStartControlPayloadDigest(payload)
	require.NoError(t, err, "hash exact supplied payload text without floating-point conversion")
	identity := f.identity
	if continuation {
		identity, err = NewWorkspaceContinuationResumeIdentity(f.scope, "synthetic-request", uuid.New(), *f.descriptor.Spec().Publication, "synthetic-answer-v1")
		require.NoError(t, err, "construct the supported bound workspace continuation identity")
	}
	targetSpec := f.target.Spec()
	if restore {
		targetSpec.Kind, targetSpec.Source = ResumabilityRestoreTarget, &f.descriptor
	}
	target, err := NewResumabilityTarget(targetSpec)
	require.NoError(t, err, "retain the exact selected live or prepared-restore target")
	permit, refund := uuid.New(), uuid.New()
	due := f.now.Add(-time.Hour)
	spec := lifecycleStartControlSpec{ReceiptID: uuid.New(), Version: 3, State: lifecycleStartControlAwaiting,
		Binding: binding, JobPayloadDigest: jobDigest, InputDigest: f.digest, ClaimToken: uuid.New(), ClaimTurn: 2,
		Identity: identity, Target: target, Outcome: ResumabilityUnavailable,
		Revocation: lifecycleStartControlRevocation{PreparationID: targetSpec.PreparationID, PreparationToken: targetSpec.PreparationToken,
			JobClaimToken: uuid.New(), PermitReceiptID: &permit}, Evidence: lifecycleStartControlEvidence{Kind: lifecycleStartControlNoLaunch, ReceiptID: uuid.New()},
		NextAttemptAt: &due, RefundReceiptID: &refund}
	if allocated {
		allocation, err := NewAttemptFence(binding, spec.Revocation.JobClaimToken, uuid.New(), uuid.New(), f.digest, 2)
		require.NoError(t, err, "retain an exact revoked allocation reference without granting a current lease")
		spec.Revocation.Allocations = []AttemptFence{allocation}
	}
	snapshot, err := newLifecycleStartControlSnapshot(spec)
	require.NoError(t, err, "construct the finite historical control snapshot")
	spec, err = snapshot.Spec()
	require.NoError(t, err, "obtain a defensive copy of the validated retained snapshot")
	raw, err := encodeLifecycleStartControlSnapshot(snapshot)
	require.NoError(t, err, "encode every retained identity using the supported wire shape")
	threadID, threaded := f.scope.Fence.ThreadID()
	epoch, _ := f.scope.Fence.ThreadEpoch()
	require.True(t, threaded, "the dedicated bound request must name an explicit lane")
	sessionEpoch := f.scope.Fence.SessionEpoch()
	dispatch := lifecycleStartControlDispatchRow{ID: binding.dispatchIDs[0], OrgID: binding.orgID, SessionID: binding.fence.sessionID,
		ThreadID: &threadID, RequestID: &f.scope.RequestID, JobID: &binding.jobID, SessionEpoch: &sessionEpoch, ThreadEpoch: &epoch,
		Kind: kind, AdmissionState: "bound", ConsumerKind: models.LifecycleConsumerJob,
		AdmissionClaimToken: &spec.ClaimToken, AdmissionClaimTurn: &spec.ClaimTurn, PayloadDigest: fmtLifecycleStartControlDigest(spec.InputDigest),
		ControlReceiptID: &spec.ReceiptID, ControlVersion: &spec.Version, ControlState: &spec.State,
		ControlSnapshot: raw, ControlNextAttemptAt: spec.NextAttemptAt, ControlRefundReceiptID: spec.RefundReceiptID}
	if allocated {
		a := spec.Revocation.Allocations[0]
		dispatch.ConsumerKind, dispatch.RuntimeID, dispatch.JobLockToken, dispatch.RuntimeLeaseToken = models.LifecycleConsumerRuntime, &a.runtimeID, &a.jobToken, &a.runtimeToken
	}
	return lifecycleStartControlFixture{spec: spec, snapshot: snapshot, dispatch: dispatch,
		job: models.Job{ID: binding.jobID, OrgID: binding.orgID, Payload: payload, Status: models.JobStatusAwaitingStartControl}}
}

func TestLifecycleStartControlDuePrecision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		offset int
	}{
		{name: "UTC nanoseconds"},
		{name: "offset nanoseconds", offset: 90 * 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, false, false, false)
			due := time.Date(2030, time.January, 1, 0, 0, 0, 123456789, time.FixedZone("synthetic", tt.offset))
			original := due
			fixture.spec.NextAttemptAt = &due
			snapshot, err := newLifecycleStartControlSnapshot(fixture.spec)
			require.NoError(t, err, "constructor must capture mirrored Go due time at PostgreSQL precision")
			spec, err := snapshot.Spec()
			require.NoError(t, err, "read back the canonical retained due time")
			want := due.UTC().Truncate(time.Microsecond)
			require.Equal(t, &want, spec.NextAttemptAt, "mirrored scalar and JSON must share exact UTC microseconds")
			require.Equal(t, original, due, "canonicalizing retained due time must not mutate caller storage")
			raw, err := encodeLifecycleStartControlSnapshot(snapshot)
			require.NoError(t, err, "canonical due time must encode without losing stored identity")
			decoded, err := decodeLifecycleStartControlSnapshot(raw)
			require.NoError(t, err, "canonical microsecond encoding must decode exactly")
			got, err := decoded.Spec()
			require.NoError(t, err, "decoded retained control must remain shape-valid")
			require.Equal(t, &want, got.NextAttemptAt, "round trip must preserve the scalar's exact microsecond value")
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &fields), "alter only the stored due-time encoding in the negative case")
			fields["next_attempt_at"], err = json.Marshal(original)
			require.NoError(t, err, "construct a noncanonical stored JSON due time")
			raw, err = json.Marshal(fields)
			require.NoError(t, err, "retain every other original snapshot field")
			invalid, err := decodeLifecycleStartControlSnapshot(raw)
			require.Error(t, err, "stored nanosecond JSON cannot silently become a different mirrored scalar identity")
			require.Equal(t, lifecycleStartControlSnapshot{}, invalid, "invalid precision must expose no partial retained control")
		})
	}
}

func TestLifecycleStartControlSnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                             string
		kind                             models.LifecycleDispatchKind
		allocated, continuation, restore bool
	}{
		{name: "answer native live before allocation", kind: models.LifecycleDispatchAnswer},
		{name: "denial native live with revoked allocation", kind: models.LifecycleDispatchDenial, allocated: true},
		{name: "answer native restored target", kind: models.LifecycleDispatchAnswer, restore: true},
		{name: "answer workspace live continuation", kind: models.LifecycleDispatchAnswer, continuation: true},
		{name: "denial restored workspace continuation", kind: models.LifecycleDispatchDenial, allocated: true, continuation: true, restore: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newLifecycleStartControlFixture(t, tt.kind, tt.allocated, tt.continuation, tt.restore)
			actual, err := decodeLifecycleStartControlRows(f.dispatch, f.job)
			require.NoError(t, err, "historical controls must match without a current clock, live lease or retained preparation row")
			actualSpec, err := actual.Spec()
			require.NoError(t, err, "decoded control should expose a defensive shape-valid copy")
			require.Equal(t, f.spec, actualSpec, "round trip must retain every binding, target, native identity, revoked reference and control scalar")
			require.NoError(t, matchLifecycleStartControlRows(f.snapshot, f.dispatch, f.job), "the supplied typed value must equal the actual retained row and immutable job")
			encoded, err := encodeLifecycleStartControlSnapshot(actual)
			require.NoError(t, err, "a decoded control should re-encode without losing private sealed values")
			require.Equal(t, f.dispatch.ControlSnapshot, encoded, "supported wire values must encode deterministically")
		})
	}
}

func TestLifecycleStartControlFiniteHistoricalStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		state     lifecycleStartControlState
		job       models.JobStatus
		admission string
	}{
		{"awaiting control", lifecycleStartControlAwaiting, models.JobStatusAwaitingStartControl, "bound"},
		{"retry queued", lifecycleStartControlRetryQueued, models.JobStatusPending, "bound"},
		{"abandoned", lifecycleStartControlAbandoned, models.JobStatusCancelled, "terminal"},
		{"superseded", lifecycleStartControlSuperseded, models.JobStatusCancelled, "historical"},
		{"superseded delivery uncertain", lifecycleStartControlDeliveryUncertain, models.JobStatusFailed, "historical"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, true, false, false)
			f.spec.State = tt.state
			if tt.state != lifecycleStartControlAwaiting {
				f.spec.NextAttemptAt = nil
			}
			if tt.state == lifecycleStartControlDeliveryUncertain {
				f.spec.Evidence.Kind, f.spec.RefundReceiptID = lifecycleStartControlUncertain, nil
				f.spec.Outcome = ResumabilityVerified
			}
			snapshot, err := newLifecycleStartControlSnapshot(f.spec)
			require.NoError(t, err, "each supported historical disposition must remain shape-representable without authorizing a transition")
			f.dispatch.ControlState, f.dispatch.ControlNextAttemptAt, f.dispatch.ControlRefundReceiptID = &tt.state, f.spec.NextAttemptAt, f.spec.RefundReceiptID
			f.dispatch.ControlSnapshot, err = encodeLifecycleStartControlSnapshot(snapshot)
			require.NoError(t, err, "encode the exact finite historical control state")
			f.job.Status, f.dispatch.AdmissionState = tt.job, tt.admission
			require.NoError(t, matchLifecycleStartControlRows(snapshot, f.dispatch, f.job), "historical disposition matching must not invent current actor, epoch, launch or provider authority")
		})
	}
}

func TestLifecycleStartControlRejectsInvalidSpecs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*lifecycleStartControlSpec)
	}{
		{"zero control receipt", func(s *lifecycleStartControlSpec) { s.ReceiptID = uuid.Nil }},
		{"zero control version", func(s *lifecycleStartControlSpec) { s.Version = 0 }},
		{"unsupported state", func(s *lifecycleStartControlSpec) { s.State = "active" }},
		{"zero binding", func(s *lifecycleStartControlSpec) { s.Binding = LifecycleJobBinding{} }},
		{"non-dedicated message binding", func(s *lifecycleStartControlSpec) {
			s.Binding.kind, s.Binding.requestID = models.LifecycleDispatchMessage, uuid.Nil
		}},
		{"zero job digest", func(s *lifecycleStartControlSpec) { s.JobPayloadDigest = LifecycleDigest{} }},
		{"zero input digest", func(s *lifecycleStartControlSpec) { s.InputDigest = LifecycleDigest{} }},
		{"zero claim", func(s *lifecycleStartControlSpec) { s.ClaimToken = uuid.Nil }},
		{"negative claim turn", func(s *lifecycleStartControlSpec) { s.ClaimTurn = -1 }},
		{"claim turn narrowing", func(s *lifecycleStartControlSpec) { s.ClaimTurn = math.MaxInt32 + 1 }},
		{"nil sealed identity", func(s *lifecycleStartControlSpec) { s.Identity = nil }},
		{"zero target", func(s *lifecycleStartControlSpec) { s.Target = ResumabilityTarget{} }},
		{"initial context is not bound resume", func(s *lifecycleStartControlSpec) {
			value := s.Identity.(resumeIdentity)
			value.kind, value.details.Scope.RequestID, value.details.NativeConversationID = ResumeInitialTurn, uuid.Nil, ""
			s.Identity = value
		}},
		{"foreign native tenant", func(s *lifecycleStartControlSpec) {
			value := s.Identity.(resumeIdentity)
			value.details.Scope.OrgID = uuid.New()
			s.Identity = value
		}},
		{"foreign native lane", func(s *lifecycleStartControlSpec) {
			value := s.Identity.(resumeIdentity)
			value.details.Scope.Fence.threadID = uuid.New()
			s.Identity = value
		}},
		{"foreign native generation", func(s *lifecycleStartControlSpec) {
			value := s.Identity.(resumeIdentity)
			value.details.Scope.Generation++
			s.Identity = value
		}},
		{"foreign native provider", func(s *lifecycleStartControlSpec) {
			value := s.Identity.(resumeIdentity)
			value.details.Scope.Provider = models.AgentTypeClaudeCode
			s.Identity = value
		}},
		{"foreign native build", func(s *lifecycleStartControlSpec) {
			value := s.Identity.(resumeIdentity)
			value.details.Scope.PinnedBuild = "synthetic-other-build"
			s.Identity = value
		}},
		{"foreign preparation", func(s *lifecycleStartControlSpec) { s.Revocation.PreparationID = uuid.New() }},
		{"foreign preparation token", func(s *lifecycleStartControlSpec) { s.Revocation.PreparationToken = uuid.New() }},
		{"zero revoked claim", func(s *lifecycleStartControlSpec) { s.Revocation.JobClaimToken = uuid.Nil }},
		{"zero optional revoked permit", func(s *lifecycleStartControlSpec) { s.Revocation.PermitReceiptID = new(uuid.UUID) }},
		{"zero evidence reference", func(s *lifecycleStartControlSpec) { s.Evidence.ReceiptID = uuid.Nil }},
		{"unknown evidence variant", func(s *lifecycleStartControlSpec) { s.Evidence.Kind = "verified" }},
		{"uncertainty is not no-launch", func(s *lifecycleStartControlSpec) { s.Evidence.Kind = lifecycleStartControlUncertain }},
		{"uncertain disposition cannot retain no-launch", func(s *lifecycleStartControlSpec) {
			s.State = lifecycleStartControlDeliveryUncertain
			s.RefundReceiptID = nil
		}},
		{"uncertain claim cannot refund", func(s *lifecycleStartControlSpec) {
			s.State = lifecycleStartControlDeliveryUncertain
			s.Evidence.Kind = lifecycleStartControlUncertain
		}},
		{"park requires due discovery", func(s *lifecycleStartControlSpec) { s.NextAttemptAt = nil }},
		{"zero optional due", func(s *lifecycleStartControlSpec) { s.NextAttemptAt = new(time.Time) }},
		{"zero optional refund", func(s *lifecycleStartControlSpec) { s.RefundReceiptID = new(uuid.UUID) }},
		{"unknown outcome", func(s *lifecycleStartControlSpec) { s.Outcome = "missing" }},
		{"verified is not a prelaunch failure park", func(s *lifecycleStartControlSpec) { s.Outcome = ResumabilityVerified }},
		{"foreign revoked allocation binding", func(s *lifecycleStartControlSpec) { s.Revocation.Allocations[0].binding.jobID = uuid.New() }},
		{"duplicate revoked allocation", func(s *lifecycleStartControlSpec) {
			s.Revocation.Allocations = append(s.Revocation.Allocations, s.Revocation.Allocations[0])
		}},
		{"zero revoked runtime token", func(s *lifecycleStartControlSpec) { s.Revocation.Allocations[0].runtimeToken = uuid.Nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, true, false, false)
			tt.mutate(&f.spec)
			actual, err := newLifecycleStartControlSnapshot(f.spec)
			require.Error(t, err, "invalid snapshot shape must not become retained control evidence")
			require.Equal(t, lifecycleStartControlSnapshot{}, actual, "constructor rejection must expose no partial control")
			encoded, err := encodeLifecycleStartControlSnapshot(lifecycleStartControlSnapshot{spec: f.spec})
			require.Error(t, err, "encoder must independently reject invalid package-local values")
			require.Nil(t, encoded, "invalid encoding must expose no partially serialized control")
		})
	}
}

func TestLifecycleStartControlOptionalAndIntegerShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*lifecycleStartControlSpec)
	}{
		{"retryable historical outcome", func(s *lifecycleStartControlSpec) { s.Outcome = ResumabilityRetryable }},
		{"unsupported historical outcome", func(s *lifecycleStartControlSpec) { s.Outcome = ResumabilityUnsupported }},
		{"no issued permit or refund reference", func(s *lifecycleStartControlSpec) { s.Revocation.PermitReceiptID, s.RefundReceiptID = nil, nil }},
		{"zero claim turn", func(s *lifecycleStartControlSpec) { s.ClaimTurn = 0 }},
		{"zero recorded epochs", func(s *lifecycleStartControlSpec) {
			s.Binding.fence.sessionEpoch, s.Binding.fence.threadEpoch = 0, 0
			identity := s.Identity.(resumeIdentity)
			identity.details.Scope.Fence = s.Binding.fence
			s.Identity = identity
		}},
		{"maximum recorded epochs", func(s *lifecycleStartControlSpec) {
			s.Binding.fence.sessionEpoch, s.Binding.fence.threadEpoch = math.MaxInt64, math.MaxInt64
			identity := s.Identity.(resumeIdentity)
			identity.details.Scope.Fence = s.Binding.fence
			s.Identity = identity
		}},
		{"live target without snapshot publication", func(s *lifecycleStartControlSpec) {
			s.Target.spec.Descriptor.spec.Publication = nil
			s.Target.spec.Descriptor.spec.SnapshotAvailability.State = WorkspaceUnavailable
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, false, false, false)
			tt.mutate(&f.spec)
			snapshot, err := newLifecycleStartControlSnapshot(f.spec)
			require.NoError(t, err, "supported optional and exact integer boundary shapes must remain representable")
			raw, err := encodeLifecycleStartControlSnapshot(snapshot)
			require.NoError(t, err, "encode every supported optional and integer boundary value")
			decoded, err := decodeLifecycleStartControlSnapshot(raw)
			require.NoError(t, err, "decode must preserve supported optional fields and large recorded epochs")
			want, err := snapshot.Spec()
			require.NoError(t, err, "copy expected complete retained snapshot")
			got, err := decoded.Spec()
			require.NoError(t, err, "copy actual complete retained snapshot")
			require.Equal(t, want, got, "wire conversion must retain every field without defaulting missing scope or narrowing epochs")
		})
	}
}

func setLifecycleStartControlJSON(t *testing.T, raw json.RawMessage, path string, value json.RawMessage) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "decode only the selected synthetic object without numeric conversion")
	key, rest, nested := strings.Cut(path, ".")
	if nested {
		fields[key] = setLifecycleStartControlJSON(t, fields[key], rest, value)
	} else if value == nil {
		delete(fields, key)
	} else {
		fields[key] = value
	}
	result, err := json.Marshal(fields)
	require.NoError(t, err, "retain exact raw nested values while modifying a synthetic boundary")
	return result
}

func TestLifecycleStartControlRejectsAmbiguousEncoding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path, value string
		transform         func(json.RawMessage) json.RawMessage
	}{
		{name: "empty", transform: func(json.RawMessage) json.RawMessage { return nil }},
		{name: "null", transform: func(json.RawMessage) json.RawMessage { return json.RawMessage(`null`) }},
		{name: "array", transform: func(json.RawMessage) json.RawMessage { return json.RawMessage(`[]`) }},
		{name: "trailing value", transform: func(raw json.RawMessage) json.RawMessage { return append(raw, []byte(` {}`)...) }},
		{name: "duplicate root", transform: func(raw json.RawMessage) json.RawMessage {
			return json.RawMessage(strings.Replace(string(raw), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1))
		}},
		{name: "duplicate nested", transform: func(raw json.RawMessage) json.RawMessage {
			return json.RawMessage(strings.Replace(string(raw), `"state":"available"`, `"state":"available","state":"available"`, 1))
		}},
		{name: "case alias", path: "Schema_Version", value: `1`},
		{name: "unknown root", path: "future", value: `true`},
		{name: "unknown nested", path: "target.descriptor.future", value: `true`},
		{name: "aliased nested", path: "target.descriptor.Generation", value: `4`},
		{name: "missing required version", path: "schema_version"},
		{name: "null version", path: "schema_version", value: `null`},
		{name: "future version", path: "schema_version", value: `2`},
		{name: "fractional version encoding", path: "schema_version", value: `1.0`},
		{name: "zero receipt", path: "receipt_id", value: `"00000000-0000-0000-0000-000000000000"`},
		{name: "upper digest", path: "job_payload_digest", value: `"ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"`},
		{name: "short digest", path: "input_digest", value: `"abc"`},
		{name: "zero digest", path: "input_digest", value: `"0000000000000000000000000000000000000000000000000000000000000000"`},
		{name: "missing zero-capable epoch", path: "binding.session_epoch"},
		{name: "negative epoch", path: "binding.session_epoch", value: `-1`},
		{name: "mismatched nested fence", path: "identity.fence.session_epoch", value: `99`},
		{name: "narrowing claim turn", path: "claim_turn", value: `2147483648`},
		{name: "exponent claim turn", path: "claim_turn", value: `2e0`},
		{name: "negative claim turn", path: "claim_turn", value: `-1`},
		{name: "missing claim turn", path: "claim_turn"},
		{name: "null optional due", path: "next_attempt_at", value: `null`},
		{name: "null optional refund", path: "refund_receipt_id", value: `null`},
		{name: "null optional source", path: "target.source", value: `null`},
		{name: "null allocations", path: "revocation.allocations", value: `null`},
		{name: "unknown outcome", path: "outcome", value: `"provider_missing"`},
		{name: "unknown evidence", path: "evidence.kind", value: `"assumed_no_launch"`},
		{name: "unknown resume variant", path: "identity.kind", value: `"reconstructed_history"`},
		{name: "empty irrelevant optional identity", path: "identity.provider_request_id", value: `""`},
		{name: "zero irrelevant optional phase", path: "identity.producer_phase_id", value: `"00000000-0000-0000-0000-000000000000"`},
		{name: "unknown target variant", path: "target.kind", value: `"fallback"`},
		{name: "restore without source", path: "target.kind", value: `"prepared_restore"`},
		{name: "malformed nested publication", path: "target.descriptor.publication.receipt_id", value: `"00000000-0000-0000-0000-000000000000"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, false, false, false)
			raw := f.dispatch.ControlSnapshot
			if tt.transform != nil {
				raw = tt.transform(raw)
			} else {
				var value json.RawMessage
				if tt.value != "" {
					value = json.RawMessage(tt.value)
				}
				raw = setLifecycleStartControlJSON(t, raw, tt.path, value)
			}
			actual, err := decodeLifecycleStartControlSnapshot(raw)
			require.Error(t, err, "unsupported or ambiguous JSON must not acquire retained control meaning")
			require.Equal(t, lifecycleStartControlSnapshot{}, actual, "decode rejection must discard every partially parsed identity")
			require.NotContains(t, err.Error(), "synthetic-native-id", "control decoding errors must not expose native identity contents")
		})
	}
}

func TestLifecycleStartControlRejectsActualRowDrift(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*lifecycleStartControlFixture)
	}{
		{"foreign dispatch tenant", func(f *lifecycleStartControlFixture) { f.dispatch.OrgID = uuid.New() }},
		{"foreign dispatch", func(f *lifecycleStartControlFixture) { f.dispatch.ID = uuid.New() }},
		{"foreign session", func(f *lifecycleStartControlFixture) { f.dispatch.SessionID = uuid.New() }},
		{"foreign lane", func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.ThreadID = &value }},
		{"missing lane", func(f *lifecycleStartControlFixture) { f.dispatch.ThreadID = nil }},
		{"missing parent epoch", func(f *lifecycleStartControlFixture) { f.dispatch.SessionEpoch = nil }},
		{"different parent epoch", func(f *lifecycleStartControlFixture) { value := int64(99); f.dispatch.SessionEpoch = &value }},
		{"missing lane epoch", func(f *lifecycleStartControlFixture) { f.dispatch.ThreadEpoch = nil }},
		{"foreign request", func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.RequestID = &value }},
		{"different request intent kind", func(f *lifecycleStartControlFixture) { f.dispatch.Kind = models.LifecycleDispatchDenial }},
		{"missing consumer job", func(f *lifecycleStartControlFixture) { f.dispatch.JobID = nil }},
		{"foreign job", func(f *lifecycleStartControlFixture) { f.job.ID = uuid.New() }},
		{"foreign job tenant", func(f *lifecycleStartControlFixture) { f.job.OrgID = uuid.New() }},
		{"missing claim token", func(f *lifecycleStartControlFixture) { f.dispatch.AdmissionClaimToken = nil }},
		{"different claim token", func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.AdmissionClaimToken = &value }},
		{"different claim turn", func(f *lifecycleStartControlFixture) { value := 99; f.dispatch.AdmissionClaimTurn = &value }},
		{"different input digest", func(f *lifecycleStartControlFixture) { f.dispatch.PayloadDigest = strings.Repeat("a", 64) }},
		{"different control receipt", func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.ControlReceiptID = &value }},
		{"different control version", func(f *lifecycleStartControlFixture) { value := int64(99); f.dispatch.ControlVersion = &value }},
		{"different control state", func(f *lifecycleStartControlFixture) {
			value := lifecycleStartControlRetryQueued
			f.dispatch.ControlState = &value
		}},
		{"missing snapshot", func(f *lifecycleStartControlFixture) { f.dispatch.ControlSnapshot = nil }},
		{"different next attempt", func(f *lifecycleStartControlFixture) {
			value := f.spec.NextAttemptAt.Add(time.Second)
			f.dispatch.ControlNextAttemptAt = &value
		}},
		{"missing refund receipt", func(f *lifecycleStartControlFixture) { f.dispatch.ControlRefundReceiptID = nil }},
		{"different refund receipt", func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.ControlRefundReceiptID = &value }},
		{"waiting instead of bound", func(f *lifecycleStartControlFixture) { f.dispatch.AdmissionState = "waiting" }},
		{"no consumer", func(f *lifecycleStartControlFixture) { f.dispatch.ConsumerKind = models.LifecycleConsumerNone }},
		{"runtime pointer without allocation", func(f *lifecycleStartControlFixture) {
			value := uuid.New()
			f.dispatch.ConsumerKind, f.dispatch.RuntimeID = models.LifecycleConsumerRuntime, &value
		}},
		{"job consumer cannot carry execution token", func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.JobLockToken = &value }},
		{"job consumer cannot carry runtime token", func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.RuntimeLeaseToken = &value }},
		{"not parked job", func(f *lifecycleStartControlFixture) { f.job.Status = models.JobStatusPending }},
		{"unknown job status", func(f *lifecycleStartControlFixture) { f.job.Status = "unknown" }},
		{"retained node owner", func(f *lifecycleStartControlFixture) { value := "synthetic-node"; f.job.LockedByNodeID = &value }},
		{"retained run owner", func(f *lifecycleStartControlFixture) { value := "synthetic-run-owner"; f.job.RunOwnerID = &value }},
		{"retained claim token", func(f *lifecycleStartControlFixture) { value := uuid.New(); f.job.LockToken = &value }},
		{"retained claim timestamp", func(f *lifecycleStartControlFixture) { value := time.Now(); f.job.LockedAt = &value }},
		{"retained lease deadline", func(f *lifecycleStartControlFixture) {
			value := time.Now().Add(-time.Hour)
			f.job.LeaseExpiresAt = &value
		}},
		{"retained completion timestamp", func(f *lifecycleStartControlFixture) { value := time.Now().Add(-time.Hour); f.job.CompletedAt = &value }},
		{"full job domain payload drift", func(f *lifecycleStartControlFixture) {
			f.job.Payload = json.RawMessage(strings.Replace(string(f.job.Payload), "900719925474099312345678901234567890", "900719925474099312345678901234567891", 1))
		}},
		{"same numeric value different encoding", func(f *lifecycleStartControlFixture) {
			f.job.Payload = json.RawMessage(strings.Replace(string(f.job.Payload), `"counter":1.00`, `"counter":1.0`, 1))
		}},
		{"different full job dispatch set", func(f *lifecycleStartControlFixture) {
			f.job.Payload = json.RawMessage(strings.Replace(string(f.job.Payload), f.spec.Binding.dispatchIDs[0].String(), uuid.NewString(), 1))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, false, false, false)
			tt.mutate(&f)
			actual, err := decodeLifecycleStartControlRows(f.dispatch, f.job)
			require.Error(t, err, "actual row drift must not be silently adopted by a valid snapshot")
			require.Equal(t, lifecycleStartControlSnapshot{}, actual, "row mismatch must return no partially matched snapshot")
			require.Error(t, matchLifecycleStartControlRows(f.snapshot, f.dispatch, f.job), "standalone matching must also validate every actual row association")
		})
	}
}

func TestLifecycleStartControlRetainsExactPayloadDigest(t *testing.T) {
	t.Parallel()
	f := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, false, false, false)
	want := LifecycleDigest(sha256.Sum256(f.job.Payload))
	require.Equal(t, want, f.spec.JobPayloadDigest, "digest must include every exact numeric domain identity and persisted byte")
	actual, err := decodeLifecycleJobBindingPayload(f.job.OrgID, f.job.ID, f.job.Payload)
	require.NoError(t, err, "shape decoder must still recover the immutable dedicated job binding")
	require.True(t, sameLifecycleJobBinding(actual, f.spec.Binding), "domain payload remains separate from exact input-set identity")
	spec := f.spec
	spec.ClaimTurn = math.MaxInt32
	snapshot, err := newLifecycleStartControlSnapshot(spec)
	require.NoError(t, err, "the maximum PostgreSQL integer claim turn should remain representable without narrowing")
	raw, err := encodeLifecycleStartControlSnapshot(snapshot)
	require.NoError(t, err, "encode the exact integer boundary")
	decoded, err := decodeLifecycleStartControlSnapshot(raw)
	require.NoError(t, err, "decode must preserve the maximum claim turn exactly")
	decodedSpec, err := decoded.Spec()
	require.NoError(t, err, "copy decoded boundary state")
	require.Equal(t, spec.ClaimTurn, decodedSpec.ClaimTurn, "claim turn must not wrap during the wire conversion")
	zero, err := (lifecycleStartControlSnapshot{}).Spec()
	require.Error(t, err, "zero control must not expose default evidence")
	require.Equal(t, lifecycleStartControlSpec{}, zero, "zero control copy must remain completely empty")
}

func TestLifecycleStartControlCopiesEveryRetainedReference(t *testing.T) {
	t.Parallel()
	f := newLifecycleStartControlFixture(t, models.LifecycleDispatchDenial, true, true, true)
	snapshot, err := newLifecycleStartControlSnapshot(f.spec)
	require.NoError(t, err, "retain independent copies of all nested values")
	before, err := encodeLifecycleStartControlSnapshot(snapshot)
	require.NoError(t, err, "capture the exact retained value before caller mutation")
	f.spec.Binding.dispatchIDs[0] = uuid.New()
	f.spec.Revocation.Allocations[0].binding.dispatchIDs[0] = uuid.New()
	*f.spec.Revocation.PermitReceiptID = uuid.New()
	*f.spec.RefundReceiptID = uuid.New()
	*f.spec.NextAttemptAt = f.spec.NextAttemptAt.Add(time.Hour)
	f.spec.Target.spec.Source.spec.Publication.Key = "synthetic-mutated-source"
	identity := f.spec.Identity.(resumeIdentity)
	identity.details.Checkpoint.Key = "synthetic-mutated-checkpoint"
	after, err := encodeLifecycleStartControlSnapshot(snapshot)
	require.NoError(t, err, "caller changes must not invalidate previously retained evidence")
	require.Equal(t, before, after, "constructor must deep-copy input sets, pointers, source descriptors and continuation checkpoints")
	copy, err := snapshot.Spec()
	require.NoError(t, err, "obtain a separately owned copy")
	copy.Binding.dispatchIDs[0] = uuid.New()
	copy.Revocation.Allocations[0].binding.dispatchIDs[0] = uuid.New()
	copy.Target.spec.Descriptor.spec.Publication.Key = "synthetic-mutated-descriptor"
	*copy.RefundReceiptID = uuid.New()
	after, err = encodeLifecycleStartControlSnapshot(snapshot)
	require.NoError(t, err, "mutating an accessor result must not invalidate retained control")
	require.Equal(t, before, after, "accessor must not expose internal slice or publication ownership")
	decoded, err := decodeLifecycleStartControlSnapshot(before)
	require.NoError(t, err, "decode should own all retained native identity bytes")
	before[0] = '['
	retained, err := encodeLifecycleStartControlSnapshot(decoded)
	require.NoError(t, err, "mutating original JSON bytes must not affect a decoded value")
	require.Equal(t, after, retained, "decoded snapshot must retain independent data rather than the caller's raw JSON buffer")
}

func TestLifecycleStartControlMatchesStoredValueAndRuntimeTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*lifecycleStartControlFixture)
		fail   bool
	}{
		{name: "exact runtime allocation"},
		{name: "equivalent due timestamp zone", mutate: func(f *lifecycleStartControlFixture) {
			value := f.spec.NextAttemptAt.In(time.FixedZone("synthetic", 3600))
			f.dispatch.ControlNextAttemptAt = &value
		}},
		{name: "changed runtime job token", mutate: func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.JobLockToken = &value }, fail: true},
		{name: "changed runtime lease token", mutate: func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.RuntimeLeaseToken = &value }, fail: true},
		{name: "changed runtime identity", mutate: func(f *lifecycleStartControlFixture) { value := uuid.New(); f.dispatch.RuntimeID = &value }, fail: true},
		{name: "missing runtime token", mutate: func(f *lifecycleStartControlFixture) { f.dispatch.RuntimeLeaseToken = nil }, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, true, false, false)
			if tt.mutate != nil {
				tt.mutate(&f)
			}
			err := matchLifecycleStartControlRows(f.snapshot, f.dispatch, f.job)
			if tt.fail {
				require.Error(t, err, "runtime pointer drift must not match retained immutable allocation")
			} else {
				require.NoError(t, err, "exact historical allocation tokens should match without a current lease")
			}
		})
	}
	f := newLifecycleStartControlFixture(t, models.LifecycleDispatchAnswer, false, false, false)
	changed := f.spec
	changed.Target.spec.TargetID = "synthetic-different-target"
	other, err := newLifecycleStartControlSnapshot(changed)
	require.NoError(t, err, "a different exact target can be shape-valid but is not the stored control")
	require.Error(t, matchLifecycleStartControlRows(other, f.dispatch, f.job), "matching must compare the supplied value with the actual stored snapshot, not only scalar control columns")
	require.Error(t, matchLifecycleStartControlRows(lifecycleStartControlSnapshot{}, f.dispatch, f.job), "zero supplied snapshot must not match a valid stored row")
}
