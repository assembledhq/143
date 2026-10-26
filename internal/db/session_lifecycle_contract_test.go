package db

import (
	"crypto/sha256"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAdmissionFenceContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		change   func(*uuid.UUID, **uuid.UUID, *int64, **int64)
		invalid  bool
		threaded bool
	}{
		{name: "explicit lane with known zero epochs", threaded: true},
		{name: "session-wide has no inferred primary lane", change: func(_ *uuid.UUID, lane **uuid.UUID, _ *int64, epoch **int64) { *lane, *epoch = nil, nil }},
		{name: "zero parent rejected", invalid: true, change: func(id *uuid.UUID, _ **uuid.UUID, _ *int64, _ **int64) { *id = uuid.Nil }},
		{name: "negative parent epoch rejected", invalid: true, change: func(_ *uuid.UUID, _ **uuid.UUID, epoch *int64, _ **int64) { *epoch = -1 }},
		{name: "unknown lane epoch not inferred", invalid: true, change: func(_ *uuid.UUID, _ **uuid.UUID, _ *int64, epoch **int64) { *epoch = nil }},
		{name: "lane epoch cannot select a primary lane", invalid: true, change: func(_ *uuid.UUID, lane **uuid.UUID, _ *int64, _ **int64) { *lane = nil }},
		{name: "zero explicit lane rejected", invalid: true, change: func(_ *uuid.UUID, lane **uuid.UUID, _ *int64, _ **int64) { **lane = uuid.Nil }},
		{name: "negative lane epoch rejected", invalid: true, change: func(_ *uuid.UUID, _ **uuid.UUID, _ *int64, epoch **int64) { **epoch = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parent, lane, parentEpoch, laneEpoch := uuid.New(), uuid.New(), int64(0), int64(0)
			lanePointer, epochPointer := &lane, &laneEpoch
			if tt.change != nil {
				tt.change(&parent, &lanePointer, &parentEpoch, &epochPointer)
			}
			value, err := NewAdmissionFence(parent, lanePointer, parentEpoch, epochPointer)
			if tt.invalid {
				require.Error(t, err, "malformed or unknown scope must not become a usable fence")
				require.Equal(t, AdmissionFence{}, value, "failed scope construction returns no partial identity")
				return
			}
			require.NoError(t, err, "complete explicit scope should have a valid contract shape")
			require.NoError(t, value.Validate(), "constructed scope remains shape-valid")
			require.Equal(t, parent, value.SessionID(), "the explicit parent is retained without discovery")
			require.Equal(t, parentEpoch, value.SessionEpoch(), "known zero and absent epochs remain distinct")
			actualLane, threaded := value.ThreadID()
			actualEpoch, hasEpoch := value.ThreadEpoch()
			require.Equal(t, tt.threaded, threaded, "lane presence follows the explicit command")
			require.Equal(t, tt.threaded, hasEpoch, "lane ID and epoch presence stay paired")
			if threaded {
				require.Equal(t, lane, actualLane, "no primary or sibling lane can replace the explicit lane")
				require.Equal(t, laneEpoch, actualEpoch, "the exact lane epoch is retained")
				lane, laneEpoch = uuid.New(), 99
				copiedLane, _ := value.ThreadID()
				copiedEpoch, _ := value.ThreadEpoch()
				require.Equal(t, actualLane, copiedLane, "caller-owned pointer mutation cannot change an existing fence")
				require.Equal(t, actualEpoch, copiedEpoch, "caller-owned epoch mutation cannot change an existing fence")
			} else {
				require.Equal(t, uuid.Nil, actualLane, "session-wide execution never gains a lane ID")
				require.Equal(t, int64(0), actualEpoch, "absent lane epoch has no inferred value")
			}
		})
	}
}

func TestLifecycleDigestAndIngressContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                               string
		size                               int
		zero, blankKind, blankKey, invalid bool
	}{
		{name: "exact digest and identity", size: 32},
		{name: "short digest rejected", size: 31, invalid: true},
		{name: "long digest rejected", size: 33, invalid: true},
		{name: "zero digest rejected", size: 32, zero: true, invalid: true},
		{name: "blank ingress kind rejected", size: 32, blankKind: true, invalid: true},
		{name: "blank ingress key rejected", size: 32, blankKey: true, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bytes := make([]byte, tt.size)
			if !tt.zero {
				bytes[0] = 7
			}
			digest, err := NewLifecycleDigest(bytes)
			if tt.size != 32 || tt.zero {
				require.Error(t, err, "digest length and zero value cannot create an immutable payload identity")
				require.Equal(t, LifecycleDigest{}, digest, "invalid digest returns no partial value")
				return
			}
			require.NoError(t, err, "an exact nonzero SHA-256 shape is accepted")
			bytes[0] = 8
			require.Equal(t, byte(7), digest[0], "digest construction must not retain caller-owned byte storage")
			kind, key := "client_message", "synthetic-command"
			if tt.blankKind {
				kind = "  "
			}
			if tt.blankKey {
				key = "\t"
			}
			value, err := NewLifecycleIngress(kind, key, digest)
			if tt.invalid {
				require.Error(t, err, "an absent ingress identity is not replaced by content equality")
				require.Equal(t, LifecycleIngress{}, value, "an invalid identity returns no partial receipt")
				return
			}
			require.NoError(t, err, "an explicit ingress key and digest should be retained independently")
			require.Equal(t, kind, value.Kind(), "the exact ingress namespace is immutable")
			require.Equal(t, key, value.Key(), "the exact caller command identity is immutable")
			require.Equal(t, digest, value.Digest(), "the digest detects conflicting reuse but does not replace the key")
		})
	}
}

func TestLifecycleJobBindingContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                         string
		kind                                                         models.LifecycleDispatchKind
		count                                                        int
		request, zeroRequest, zeroJob, zeroInput, duplicate, invalid bool
	}{
		{name: "ordinary exact input", kind: models.LifecycleDispatchMessage, count: 1},
		{name: "new ordinary job may bind a selected batch", kind: models.LifecycleDispatchMessage, count: 2},
		{name: "answer binds one request intent", kind: models.LifecycleDispatchAnswer, count: 1, request: true},
		{name: "denial binds one request intent", kind: models.LifecycleDispatchDenial, count: 1, request: true},
		{name: "initial exact control", kind: models.LifecycleDispatchInitial, count: 1},
		{name: "retry exact control", kind: models.LifecycleDispatchRetry, count: 1},
		{name: "recovery exact control", kind: models.LifecycleDispatchRecovery, count: 1},
		{name: "answer cannot absorb ordinary input", kind: models.LifecycleDispatchAnswer, count: 2, request: true, invalid: true},
		{name: "denial cannot omit request", kind: models.LifecycleDispatchDenial, count: 1, invalid: true},
		{name: "ordinary input cannot borrow request authority", kind: models.LifecycleDispatchMessage, count: 1, request: true, invalid: true},
		{name: "zero request not normalized to NULL", kind: models.LifecycleDispatchAnswer, count: 1, request: true, zeroRequest: true, invalid: true},
		{name: "no inferred consumer job", kind: models.LifecycleDispatchMessage, count: 1, zeroJob: true, invalid: true},
		{name: "empty input set rejected", kind: models.LifecycleDispatchMessage, invalid: true},
		{name: "zero input rejected", kind: models.LifecycleDispatchMessage, count: 1, zeroInput: true, invalid: true},
		{name: "duplicate input rejected rather than silently normalized", kind: models.LifecycleDispatchMessage, count: 2, duplicate: true, invalid: true},
		{name: "unknown dispatch kind rejected", kind: "future", count: 1, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fence := newLifecycleContractFence(t)
			jobID := uuid.New()
			if tt.zeroJob {
				jobID = uuid.Nil
			}
			ids := make([]uuid.UUID, tt.count)
			for index := range ids {
				ids[index] = uuid.New()
			}
			if tt.zeroInput {
				ids[0] = uuid.Nil
			}
			if tt.duplicate {
				ids[1] = ids[0]
			}
			var requestID *uuid.UUID
			if tt.request {
				id := uuid.New()
				if tt.zeroRequest {
					id = uuid.Nil
				}
				requestID = &id
			}
			value, err := NewLifecycleJobBinding(fence, jobID, ids, tt.kind, requestID)
			if tt.invalid {
				require.Error(t, err, "malformed input binding must not manufacture durable consumer evidence")
				require.Equal(t, LifecycleJobBinding{}, value, "failed binding construction returns no partial consumer")
				return
			}
			require.NoError(t, err, "an exact typed binding shape should be valid before its mandatory DB checks")
			expected := append([]uuid.UUID(nil), ids...)
			ids[0] = uuid.New()
			got := value.DispatchIDs()
			require.Equal(t, expected, got, "a caller cannot append or replace the immutable job input set")
			got[0] = uuid.New()
			require.Equal(t, expected, value.DispatchIDs(), "a returned input slice cannot mutate the binding")
			require.Equal(t, fence, value.Fence(), "a binding preserves the exact session and lane epochs")
			require.Equal(t, jobID, value.JobID(), "a dedupe family never replaces the exact job identity")
			require.Equal(t, tt.kind, value.Kind(), "input kind remains part of the dedicated binding")
			gotRequest, present := value.RequestID()
			require.Equal(t, tt.request, present, "request intent is explicit rather than inferred from an inbox")
			if requestID != nil {
				require.Equal(t, *requestID, gotRequest, "request binding names exactly one request")
			}
		})
	}
}

func TestLifecycleAttemptAndAdmissionAuthorityContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                                                    string
		zeroOrg, zeroJobToken, zeroRuntime, zeroRuntimeToken, zeroDigest, negativeTurn, invalid bool
	}{
		{name: "exact immutable allocation with known zero turn"},
		{name: "missing tenant", zeroOrg: true, invalid: true},
		{name: "missing job attempt", zeroJobToken: true, invalid: true},
		{name: "missing runtime", zeroRuntime: true, invalid: true},
		{name: "missing runtime token", zeroRuntimeToken: true, invalid: true},
		{name: "missing allocation digest", zeroDigest: true, invalid: true},
		{name: "unknown turn", negativeTurn: true, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fence := newLifecycleContractFence(t)
			binding, err := NewLifecycleJobBinding(fence, uuid.New(), []uuid.UUID{uuid.New()}, models.LifecycleDispatchMessage, nil)
			require.NoError(t, err, "construct an exact ordinary input binding for allocation evidence")
			orgID, jobToken, runtimeID, runtimeToken := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			digest := LifecycleDigest(sha256.Sum256([]byte("synthetic immutable allocation")))
			turn := 0
			if tt.zeroOrg {
				orgID = uuid.Nil
			}
			if tt.zeroJobToken {
				jobToken = uuid.Nil
			}
			if tt.zeroRuntime {
				runtimeID = uuid.Nil
			}
			if tt.zeroRuntimeToken {
				runtimeToken = uuid.Nil
			}
			if tt.zeroDigest {
				digest = LifecycleDigest{}
			}
			if tt.negativeTurn {
				turn = -1
			}
			attempt, err := NewAttemptFence(binding, jobToken, runtimeID, runtimeToken, digest, turn)
			if tt.invalid && !tt.zeroOrg {
				require.Error(t, err, "partial allocation must not become current or historical attempt authority")
				require.Equal(t, AttemptFence{}, attempt, "invalid allocation returns no partial attempt")
				return
			}
			require.NoError(t, err, "all immutable allocation components are required before authority shape checks")
			current, currentErr := NewRuntimeAuthority(orgID, attempt)
			historical, historicalErr := NewHistoricalAttemptAuthority(orgID, attempt)
			if tt.zeroOrg {
				require.Error(t, currentErr, "current execution cannot omit tenant scope")
				require.Nil(t, current, "invalid current authority must not expose a typed usable value")
				require.Error(t, historicalErr, "historical settlement cannot omit tenant scope")
				require.Nil(t, historical, "invalid historical authority must not expose a typed usable value")
				return
			}
			require.NoError(t, currentErr, "current execution input retains allocation shape but still needs fresh DB authorization")
			require.NoError(t, historicalErr, "historical settlement input retains allocation shape without adopting a current lease")
			_, currentAsHistorical := current.(HistoricalAttemptAuthority)
			_, historicalAsCurrent := historical.(RuntimeAuthority)
			require.False(t, currentAsHistorical, "current execution authority cannot satisfy a historical-settlement parameter by accident")
			require.False(t, historicalAsCurrent, "old allocation authority must not satisfy a current-execution parameter")
			require.Equal(t, binding, attempt.Binding(), "allocation preserves the exact job input binding")
			require.Equal(t, jobToken, attempt.JobToken(), "immutable job attempt token cannot be replaced by a current claim")
			require.Equal(t, runtimeID, attempt.RuntimeID(), "runtime identity belongs to the allocation")
			require.Equal(t, runtimeToken, attempt.RuntimeToken(), "historical runtime token survives mutable lease replacement")
			require.Equal(t, digest, attempt.AllocationDigest(), "allocation digest remains exact")
			require.Equal(t, turn, attempt.ExpectedTurn(), "known turn zero remains distinct from unknown")
		})
	}
}

func newLifecycleContractFence(t *testing.T) AdmissionFence {
	t.Helper()
	lane, epoch := uuid.New(), int64(3)
	fence, err := NewAdmissionFence(uuid.New(), &lane, 7, &epoch)
	require.NoError(t, err, "construct a fresh explicit synthetic parent and lane fence")
	return fence
}

func TestLifecycleConsumerBindingContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                      string
		kind                                                      models.LifecycleConsumerKind
		job, runtime, allocation, wrongJob, wrongRuntime, invalid bool
	}{
		{name: "waiting input has no consumer", kind: models.LifecycleConsumerNone},
		{name: "new exact job", kind: models.LifecycleConsumerJob, job: true},
		{name: "job and allocated runtime remain one consumer", kind: models.LifecycleConsumerJob, job: true, runtime: true, allocation: true},
		{name: "runtime retains originating job", kind: models.LifecycleConsumerRuntime, job: true, runtime: true, allocation: true},
		{name: "runtime allocation proves originating job without second pointer", kind: models.LifecycleConsumerRuntime, runtime: true, allocation: true},
		{name: "none cannot retain job", kind: models.LifecycleConsumerNone, job: true, invalid: true},
		{name: "job cannot be inferred", kind: models.LifecycleConsumerJob, invalid: true},
		{name: "runtime cannot be inferred", kind: models.LifecycleConsumerRuntime, job: true, invalid: true},
		{name: "runtime requires allocation", kind: models.LifecycleConsumerRuntime, runtime: true, invalid: true},
		{name: "job cannot borrow unrelated runtime", kind: models.LifecycleConsumerJob, job: true, runtime: true, allocation: true, wrongRuntime: true, invalid: true},
		{name: "runtime cannot borrow unrelated job", kind: models.LifecycleConsumerRuntime, job: true, runtime: true, allocation: true, wrongJob: true, invalid: true},
		{name: "unknown kind rejected", kind: "future", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fence := newLifecycleContractFence(t)
			jobID, runtimeID := uuid.New(), uuid.New()
			binding, err := NewLifecycleJobBinding(fence, jobID, []uuid.UUID{uuid.New()}, models.LifecycleDispatchMessage, nil)
			require.NoError(t, err, "create a synthetic immutable job binding for logical consumer comparison")
			attempt, err := NewAttemptFence(binding, uuid.New(), runtimeID, uuid.New(), LifecycleDigest(sha256.Sum256([]byte("synthetic allocation"))), 1)
			require.NoError(t, err, "create an exact runtime allocation rather than equating two pointer columns")
			var job, runtime *uuid.UUID
			var allocation *AttemptFence
			if tt.job {
				job = &jobID
			}
			if tt.runtime {
				runtime = &runtimeID
			}
			if tt.allocation {
				allocation = &attempt
			}
			if tt.wrongJob {
				id := uuid.New()
				job = &id
			}
			if tt.wrongRuntime {
				id := uuid.New()
				runtime = &id
			}
			value, err := NewLifecycleConsumerBinding(tt.kind, job, runtime, allocation)
			if tt.invalid {
				require.Error(t, err, "an inconsistent consumer cannot become binding evidence")
				require.Equal(t, LifecycleConsumerBinding{}, value, "invalid consumer shape returns no partial identity")
				return
			}
			require.NoError(t, err, "an exact single logical consumer shape is valid before durable arbitration")
			require.Equal(t, tt.kind, value.Kind(), "consumer kind is retained without runtime/job double-counting")
			actualJob, hasJob := value.JobID()
			actualRuntime, hasRuntime := value.RuntimeID()
			require.Equal(t, tt.job, hasJob, "originating job pointer remains explicitly optional for runtime evidence")
			require.Equal(t, tt.runtime, hasRuntime, "runtime identity cannot appear before an exact allocation")
			if tt.job {
				require.Equal(t, jobID, actualJob, "the job identity must match the immutable allocation")
			}
			if tt.runtime {
				require.Equal(t, runtimeID, actualRuntime, "the runtime identity must match the immutable allocation")
			}
			if allocation != nil {
				allocation.runtimeID = uuid.New()
				allocation.binding.dispatchIDs[0] = uuid.New()
				require.NoError(t, value.Validate(), "caller mutation of an allocation argument cannot corrupt retained consumer identity")
				require.Equal(t, binding.DispatchIDs(), value.allocation.Binding().DispatchIDs(), "consumer retains its own immutable input set")
			}
		})
	}
}

func TestAdmissionAuthorityShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		missing string
		invalid bool
	}{
		{name: "exact pre-allocation claim receipt"},
		{name: "missing tenant", missing: "org", invalid: true},
		{name: "missing scope", missing: "fence", invalid: true},
		{name: "missing dispatch", missing: "dispatch", invalid: true},
		{name: "missing claim token", missing: "token", invalid: true},
		{name: "unknown turn", missing: "turn", invalid: true},
		{name: "missing committed ingress", missing: "ingress", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, dispatch, token := uuid.New(), uuid.New(), uuid.New()
			fence, turn := newLifecycleContractFence(t), 1
			ingress, err := NewLifecycleIngress("client_message", uuid.NewString(), LifecycleDigest(sha256.Sum256([]byte("synthetic message"))))
			require.NoError(t, err, "create the exact committed-command identity for compensation shape")
			switch tt.missing {
			case "org":
				org = uuid.Nil
			case "fence":
				fence = AdmissionFence{}
			case "dispatch":
				dispatch = uuid.Nil
			case "token":
				token = uuid.Nil
			case "turn":
				turn = -1
			case "ingress":
				ingress = LifecycleIngress{}
			}
			value, err := NewAdmissionAuthority(org, fence, dispatch, token, turn, ingress)
			if tt.invalid {
				require.Error(t, err, "partial compensation identity cannot release a logical claim")
				require.Equal(t, AdmissionAuthority{}, value, "invalid admission receipt returns no partial authority input")
				return
			}
			require.NoError(t, err, "a complete compensation shape still requires future locked durable validation")
			require.Equal(t, AdmissionAuthority{orgID: org, dispatchID: dispatch, claimToken: token, fence: fence, ingress: ingress, turn: turn}, value, "all committed ingress scope and claim fields are retained exactly")
		})
	}
}

func TestProjectionDispositionContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   ProjectionDisposition
		invalid bool
	}{
		{name: "applied", value: ProjectionApplied},
		{name: "already applied", value: ProjectionAlreadyApplied},
		{name: "own receipt settled shared state stale", value: ProjectionStaleSharedState},
		{name: "zero", invalid: true},
		{name: "not a ready permit", value: "ready", invalid: true},
		{name: "no stale admission success", value: "stale_admission_success", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.invalid {
				require.Error(t, tt.value.Validate(), "zero or admission-like projection dispositions cannot imply success")
			} else {
				require.NoError(t, tt.value.Validate(), "the named projection disposition belongs only to own-receipt settlement")
			}
		})
	}
}
