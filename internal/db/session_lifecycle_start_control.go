package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type lifecycleStartControlState string

const (
	lifecycleStartControlAwaiting          lifecycleStartControlState = "awaiting_control"
	lifecycleStartControlRetryQueued       lifecycleStartControlState = "retry_queued"
	lifecycleStartControlAbandoned         lifecycleStartControlState = "abandoned"
	lifecycleStartControlSuperseded        lifecycleStartControlState = "superseded"
	lifecycleStartControlDeliveryUncertain lifecycleStartControlState = "superseded_delivery_uncertain"
)

func (s lifecycleStartControlState) validate() error {
	switch s {
	case lifecycleStartControlAwaiting, lifecycleStartControlRetryQueued, lifecycleStartControlAbandoned,
		lifecycleStartControlSuperseded, lifecycleStartControlDeliveryUncertain:
		return nil
	default:
		return errors.New("unsupported retained start-control state")
	}
}

type lifecycleStartControlEvidenceKind string

const (
	lifecycleStartControlNoLaunch  lifecycleStartControlEvidenceKind = "no_launch"
	lifecycleStartControlUncertain lifecycleStartControlEvidenceKind = "delivery_uncertain"
)

// These are retained references, not proofs. In particular, a nonzero receipt
// cannot establish the ALL-allocation no-entry/no-acceptance predicate, physical
// drain, provider support or permission to launch. B/E must verify the actual
// immutable evidence and current authority under their canonical locks.
type lifecycleStartControlEvidence struct {
	Kind      lifecycleStartControlEvidenceKind
	ReceiptID uuid.UUID
}

type lifecycleStartControlRevocation struct {
	PreparationID, PreparationToken, JobClaimToken uuid.UUID
	PermitReceiptID                                *uuid.UUID
	Allocations                                    []AttemptFence
}

type lifecycleStartControlSpec struct {
	ReceiptID                     uuid.UUID
	Version                       int64
	State                         lifecycleStartControlState
	Binding                       LifecycleJobBinding
	JobPayloadDigest, InputDigest LifecycleDigest
	ClaimToken                    uuid.UUID
	ClaimTurn                     int
	Identity                      ResumeIdentity
	Target                        ResumabilityTarget
	Outcome                       ResumabilityDisposition
	Revocation                    lifecycleStartControlRevocation
	Evidence                      lifecycleStartControlEvidence
	NextAttemptAt                 *time.Time
	// A reference alone is not the shared claim-keyed debit-refund CAS.
	// Deferral and parking must compose that one guard at their B/E writer seam.
	RefundReceiptID *uuid.UUID
}

// The finite snapshot is owned by the retained dispatch, not the execution
// preparation. It deliberately has no current-time/lease/actor eligibility
// method. Preparation deletion and observation expiry cannot erase this record.
type lifecycleStartControlSnapshot struct{ spec lifecycleStartControlSpec }

func newLifecycleStartControlSnapshot(spec lifecycleStartControlSpec) (lifecycleStartControlSnapshot, error) {
	if err := validateLifecycleStartControlSpec(spec); err != nil {
		return lifecycleStartControlSnapshot{}, err
	}
	// Reconstruct sealed values through their constructors, retaining no caller
	// slices, pointers, descriptor publications or continuation checkpoints.
	copied, err := lifecycleStartControlToWire(spec).spec()
	if err != nil {
		return lifecycleStartControlSnapshot{}, err
	}
	return lifecycleStartControlSnapshot{spec: copied}, nil
}

func (s lifecycleStartControlSnapshot) Spec() (lifecycleStartControlSpec, error) {
	if err := validateLifecycleStartControlSpec(s.spec); err != nil {
		return lifecycleStartControlSpec{}, err
	}
	return lifecycleStartControlToWire(s.spec).spec()
}

func validateLifecycleStartControlSpec(s lifecycleStartControlSpec) error {
	bad := func() error { return errors.New("invalid retained start-control snapshot") }
	if s.ReceiptID == uuid.Nil || s.Version <= 0 || s.State.validate() != nil || s.Binding.Validate() != nil ||
		s.JobPayloadDigest.Validate() != nil || s.InputDigest.Validate() != nil || s.ClaimToken == uuid.Nil ||
		s.ClaimTurn < 0 || s.ClaimTurn > math.MaxInt32 || s.Outcome.Validate() != nil {
		return bad()
	}
	if s.Binding.kind != models.LifecycleDispatchAnswer && s.Binding.kind != models.LifecycleDispatchDenial {
		return bad()
	}
	if ValidateBoundResumeIdentity(s.Identity, s.Binding.requestID) != nil || s.Target.Validate() != nil {
		return bad()
	}
	i, target := s.Identity.Details().Scope, s.Target.Spec()
	descriptor := target.Descriptor.Spec()
	if i.OrgID != s.Binding.orgID || i.Fence != s.Binding.fence || i.Generation != descriptor.Generation ||
		descriptor.OrgID != s.Binding.orgID || descriptor.SessionID != s.Binding.fence.sessionID ||
		i.Provider != target.Provider || i.PinnedBuild != target.PinnedBuild ||
		s.Revocation.PreparationID != target.PreparationID || s.Revocation.PreparationToken != target.PreparationToken ||
		s.Revocation.JobClaimToken == uuid.Nil || s.Evidence.ReceiptID == uuid.Nil {
		return bad()
	}
	if s.Identity.Kind() == ResumeWorkspaceContinuation && target.Source != nil &&
		!equalWorkspacePublication(s.Identity.Details().Checkpoint, target.Source.Spec().Publication) {
		return bad()
	}
	if (s.NextAttemptAt != nil && s.NextAttemptAt.IsZero()) ||
		(s.RefundReceiptID != nil && *s.RefundReceiptID == uuid.Nil) ||
		(s.Revocation.PermitReceiptID != nil && *s.Revocation.PermitReceiptID == uuid.Nil) {
		return bad()
	}
	if s.State == lifecycleStartControlDeliveryUncertain {
		if s.Evidence.Kind != lifecycleStartControlUncertain || s.RefundReceiptID != nil {
			return bad()
		}
	} else if s.Evidence.Kind != lifecycleStartControlNoLaunch {
		return bad()
	}
	if s.State == lifecycleStartControlAwaiting && (s.NextAttemptAt == nil || s.Outcome == ResumabilityVerified) {
		return bad()
	}
	seen := make(map[uuid.UUID]bool, len(s.Revocation.Allocations))
	for _, allocation := range s.Revocation.Allocations {
		if allocation.Validate() != nil || !sameLifecycleJobBinding(allocation.binding, s.Binding) || seen[allocation.runtimeID] {
			return bad()
		}
		seen[allocation.runtimeID] = true
	}
	return nil
}

func encodeLifecycleStartControlSnapshot(snapshot lifecycleStartControlSnapshot) (json.RawMessage, error) {
	if err := validateLifecycleStartControlSpec(snapshot.spec); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(lifecycleStartControlToWire(snapshot.spec))
	if err != nil {
		return nil, errors.New("encode retained start-control snapshot")
	}
	return encoded, nil
}

func decodeLifecycleStartControlSnapshot(raw json.RawMessage) (lifecycleStartControlSnapshot, error) {
	var wire lifecycleStartControlWire
	if err := decodeLifecycleStartControlJSON(raw, &wire); err != nil || wire.SchemaVersion != 1 ||
		(wire.NextAttemptAt != nil && !wire.NextAttemptAt.Equal(wire.NextAttemptAt.Truncate(time.Microsecond))) {
		return lifecycleStartControlSnapshot{}, errors.New("invalid retained start-control encoding")
	}
	spec, err := wire.spec()
	if err != nil {
		return lifecycleStartControlSnapshot{}, err
	}
	return newLifecycleStartControlSnapshot(spec)
}

// lifecycleStartControlPayloadDigest hashes the actual immutable PostgreSQL
// payload::text bytes. Callers must read that representation, not re-marshal a
// decoded map (which can lose large numeric identities or numeric encodings).
func lifecycleStartControlPayloadDigest(payloadText json.RawMessage) (LifecycleDigest, error) {
	if _, err := lifecycleJobPayloadObject(payloadText); err != nil {
		return LifecycleDigest{}, errors.New("invalid start-control job payload")
	}
	digest := sha256.Sum256(payloadText)
	return NewLifecycleDigest(digest[:])
}

// These facts must come from the actual tenant-scoped dispatch row. The matcher
// performs no database access; supplying facts is not a replacement for reading
// and locking durable rows at the later writer seam.
type lifecycleStartControlDispatchRow struct {
	ID, OrgID, SessionID                                 uuid.UUID
	ThreadID, RequestID, JobID, RuntimeID                *uuid.UUID
	SessionEpoch, ThreadEpoch                            *int64
	Kind                                                 models.LifecycleDispatchKind
	AdmissionState                                       string
	ConsumerKind                                         models.LifecycleConsumerKind
	JobLockToken, RuntimeLeaseToken, AdmissionClaimToken *uuid.UUID
	AdmissionClaimTurn                                   *int
	PayloadDigest                                        string
	ControlReceiptID                                     *uuid.UUID
	ControlVersion                                       *int64
	ControlState                                         *lifecycleStartControlState
	ControlSnapshot                                      json.RawMessage
	ControlNextAttemptAt                                 *time.Time
	ControlRefundReceiptID                               *uuid.UUID
}

func decodeLifecycleStartControlRows(dispatch lifecycleStartControlDispatchRow, job models.Job) (lifecycleStartControlSnapshot, error) {
	snapshot, err := decodeLifecycleStartControlSnapshot(dispatch.ControlSnapshot)
	if err != nil {
		return lifecycleStartControlSnapshot{}, err
	}
	if err := matchLifecycleStartControlFacts(snapshot, dispatch, job); err != nil {
		return lifecycleStartControlSnapshot{}, err
	}
	return snapshot, nil
}

// matchLifecycleStartControlRows compares the supplied value with both the
// actual stored snapshot and every retained row association. It is strict shape
// and identity matching, not StartControlAuthority or current start eligibility.
func matchLifecycleStartControlRows(snapshot lifecycleStartControlSnapshot, dispatch lifecycleStartControlDispatchRow, job models.Job) error {
	actual, err := decodeLifecycleStartControlRows(dispatch, job)
	if err != nil {
		return err
	}
	want, err := encodeLifecycleStartControlSnapshot(snapshot)
	if err != nil {
		return err
	}
	got, err := encodeLifecycleStartControlSnapshot(actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return errors.New("retained start-control snapshot differs from stored row")
	}
	return nil
}

func matchLifecycleStartControlFacts(snapshot lifecycleStartControlSnapshot, d lifecycleStartControlDispatchRow, job models.Job) error {
	s := snapshot.spec
	bad := func() error { return errors.New("retained start-control row association mismatch") }
	if d.OrgID != s.Binding.orgID || d.ID != s.Binding.dispatchIDs[0] || d.SessionID != s.Binding.fence.sessionID ||
		(d.ThreadID != nil) != s.Binding.fence.threaded || (d.ThreadID != nil && *d.ThreadID != s.Binding.fence.threadID) ||
		d.SessionEpoch == nil || *d.SessionEpoch != s.Binding.fence.sessionEpoch ||
		(d.ThreadEpoch != nil) != s.Binding.fence.threaded || (d.ThreadEpoch != nil && *d.ThreadEpoch != s.Binding.fence.threadEpoch) ||
		d.RequestID == nil || *d.RequestID != s.Binding.requestID || d.Kind != s.Binding.kind ||
		d.JobID == nil || *d.JobID != s.Binding.jobID || job.ID != s.Binding.jobID || job.OrgID != s.Binding.orgID ||
		d.AdmissionClaimToken == nil || *d.AdmissionClaimToken != s.ClaimToken ||
		d.AdmissionClaimTurn == nil || *d.AdmissionClaimTurn != s.ClaimTurn ||
		d.ControlReceiptID == nil || *d.ControlReceiptID != s.ReceiptID || d.ControlVersion == nil || *d.ControlVersion != s.Version ||
		d.ControlState == nil || *d.ControlState != s.State || !equalLifecycleStartControlTime(d.ControlNextAttemptAt, s.NextAttemptAt) ||
		!equalLifecycleStartControlUUID(d.ControlRefundReceiptID, s.RefundReceiptID) || job.Status.Validate() != nil ||
		!slices.Contains([]string{"bound", "terminal", "historical"}, d.AdmissionState) {
		return bad()
	}
	inputDigest, err := ParseLifecycleDigest(d.PayloadDigest)
	if err != nil || inputDigest != s.InputDigest {
		return bad()
	}
	actual, err := decodeLifecycleJobBindingPayload(job.OrgID, job.ID, job.Payload)
	if err != nil || !sameLifecycleJobBinding(actual, s.Binding) {
		return bad()
	}
	digest, err := lifecycleStartControlPayloadDigest(job.Payload)
	if err != nil || digest != s.JobPayloadDigest {
		return bad()
	}
	var allocation *AttemptFence
	if d.ConsumerKind == models.LifecycleConsumerRuntime && d.RuntimeID != nil {
		for _, item := range s.Revocation.Allocations {
			if item.runtimeID == *d.RuntimeID {
				copy := item
				allocation = &copy
			}
		}
		if allocation == nil || d.JobLockToken == nil || *d.JobLockToken != allocation.jobToken ||
			d.RuntimeLeaseToken == nil || *d.RuntimeLeaseToken != allocation.runtimeToken {
			return bad()
		}
	} else if d.ConsumerKind != models.LifecycleConsumerJob || d.JobLockToken != nil || d.RuntimeLeaseToken != nil {
		return bad()
	}
	if _, err := NewLifecycleConsumerBinding(d.OrgID, d.ID, d.ConsumerKind, d.JobID, d.RuntimeID, allocation); err != nil {
		return bad()
	}
	if s.State == lifecycleStartControlAwaiting && (d.AdmissionState != "bound" || job.Status != models.JobStatusAwaitingStartControl ||
		job.LockedByNodeID != nil || job.RunOwnerID != nil || job.LockToken != nil || job.LockedAt != nil ||
		job.LeaseExpiresAt != nil || job.CompletedAt != nil) {
		return bad()
	}
	return nil
}

func equalLifecycleStartControlUUID(a, b *uuid.UUID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func equalLifecycleStartControlTime(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}

// Version one uses explicit DTOs. Private sealed values themselves intentionally
// have no JSON fields. Optional values are absent, never JSON null.
type lifecycleStartControlWire struct {
	SchemaVersion    int                                 `json:"schema_version"`
	ReceiptID        uuid.UUID                           `json:"receipt_id"`
	Version          int64                               `json:"version"`
	State            lifecycleStartControlState          `json:"state"`
	Binding          lifecycleStartControlBindingWire    `json:"binding"`
	JobPayloadDigest string                              `json:"job_payload_digest"`
	InputDigest      string                              `json:"input_digest"`
	ClaimToken       uuid.UUID                           `json:"claim_token"`
	ClaimTurn        int                                 `json:"claim_turn"`
	Identity         lifecycleStartControlIdentityWire   `json:"identity"`
	Target           lifecycleStartControlTargetWire     `json:"target"`
	Outcome          ResumabilityDisposition             `json:"outcome"`
	Revocation       lifecycleStartControlRevocationWire `json:"revocation"`
	Evidence         lifecycleStartControlEvidenceWire   `json:"evidence"`
	NextAttemptAt    *time.Time                          `json:"next_attempt_at,omitempty"`
	RefundReceiptID  *uuid.UUID                          `json:"refund_receipt_id,omitempty"`
}

type lifecycleStartControlBindingWire struct {
	OrgID        uuid.UUID                    `json:"org_id"`
	SessionID    uuid.UUID                    `json:"session_id"`
	ThreadID     *uuid.UUID                   `json:"thread_id,omitempty"`
	SessionEpoch int64                        `json:"session_epoch"`
	ThreadEpoch  *int64                       `json:"thread_epoch,omitempty"`
	JobID        uuid.UUID                    `json:"job_id"`
	DispatchIDs  []uuid.UUID                  `json:"dispatch_ids"`
	Kind         models.LifecycleDispatchKind `json:"kind"`
	RequestID    *uuid.UUID                   `json:"request_id,omitempty"`
}

type lifecycleStartControlFenceWire struct {
	SessionID    uuid.UUID  `json:"session_id"`
	ThreadID     *uuid.UUID `json:"thread_id,omitempty"`
	SessionEpoch int64      `json:"session_epoch"`
	ThreadEpoch  *int64     `json:"thread_epoch,omitempty"`
}

type lifecycleStartControlAvailabilityWire struct {
	State   WorkspaceAvailabilityState `json:"state"`
	Version int64                      `json:"version"`
}

type lifecycleStartControlOperationWire struct {
	ID   uuid.UUID              `json:"id"`
	Kind WorkspaceOperationKind `json:"kind"`
}

type lifecycleStartControlCheckpointWire struct {
	RuntimeID    uuid.UUID `json:"runtime_id"`
	AttemptToken uuid.UUID `json:"attempt_token"`
	PhaseID      uuid.UUID `json:"phase_id"`
	Revision     int64     `json:"revision"`
	PublishedAt  time.Time `json:"published_at"`
}

type lifecycleStartControlPublicationWire struct {
	ID         uuid.UUID                            `json:"id"`
	ReceiptID  uuid.UUID                            `json:"receipt_id"`
	Key        string                               `json:"key"`
	Version    int64                                `json:"version"`
	Producer   lifecycleStartControlOperationWire   `json:"producer"`
	Checkpoint *lifecycleStartControlCheckpointWire `json:"checkpoint,omitempty"`
}

type lifecycleStartControlDescriptorWire struct {
	OrgID                uuid.UUID                             `json:"org_id"`
	SessionID            uuid.UUID                             `json:"session_id"`
	Generation           int64                                 `json:"generation"`
	Revision             int64                                 `json:"revision"`
	Publication          *lifecycleStartControlPublicationWire `json:"publication,omitempty"`
	SnapshotAvailability lifecycleStartControlAvailabilityWire `json:"snapshot_availability"`
	PhysicalAvailability lifecycleStartControlAvailabilityWire `json:"physical_availability"`
	LastOperation        lifecycleStartControlOperationWire    `json:"last_operation"`
}

type lifecycleStartControlIdentityWire struct {
	Kind                  ResumeIdentityKind                    `json:"kind"`
	OrgID                 uuid.UUID                             `json:"org_id"`
	RequestID             uuid.UUID                             `json:"request_id"`
	Fence                 lifecycleStartControlFenceWire        `json:"fence"`
	Provider              models.AgentType                      `json:"provider"`
	PinnedBuild           string                                `json:"pinned_build"`
	Workdir               string                                `json:"workdir"`
	Generation            int64                                 `json:"generation"`
	NativeConversationID  string                                `json:"native_conversation_id,omitempty"`
	ProviderRequestID     string                                `json:"provider_request_id,omitempty"`
	ProducerPhaseID       *uuid.UUID                            `json:"producer_phase_id,omitempty"`
	Checkpoint            *lifecycleStartControlPublicationWire `json:"checkpoint,omitempty"`
	AnswerProtocolVersion string                                `json:"answer_protocol_version,omitempty"`
}

type lifecycleStartControlTargetWire struct {
	Kind                 ResumabilityTargetKind                `json:"kind"`
	TargetID             string                                `json:"target_id"`
	Provider             models.AgentType                      `json:"provider"`
	PinnedBuild          string                                `json:"pinned_build"`
	ImageBuild           string                                `json:"image_build"`
	VerificationProtocol string                                `json:"verification_protocol"`
	PreparationID        uuid.UUID                             `json:"preparation_id"`
	PreparationToken     uuid.UUID                             `json:"preparation_token"`
	Descriptor           lifecycleStartControlDescriptorWire   `json:"descriptor"`
	TargetAvailability   lifecycleStartControlAvailabilityWire `json:"target_availability"`
	Source               *lifecycleStartControlDescriptorWire  `json:"source,omitempty"`
}

type lifecycleStartControlAllocationWire struct {
	Binding          lifecycleStartControlBindingWire `json:"binding"`
	JobToken         uuid.UUID                        `json:"job_token"`
	RuntimeID        uuid.UUID                        `json:"runtime_id"`
	RuntimeToken     uuid.UUID                        `json:"runtime_token"`
	AllocationDigest string                           `json:"allocation_digest"`
	ExpectedTurn     int                              `json:"expected_turn"`
}

type lifecycleStartControlRevocationWire struct {
	PreparationID    uuid.UUID                             `json:"preparation_id"`
	PreparationToken uuid.UUID                             `json:"preparation_token"`
	JobClaimToken    uuid.UUID                             `json:"job_claim_token"`
	PermitReceiptID  *uuid.UUID                            `json:"permit_receipt_id,omitempty"`
	Allocations      []lifecycleStartControlAllocationWire `json:"allocations"`
}

type lifecycleStartControlEvidenceWire struct {
	Kind      lifecycleStartControlEvidenceKind `json:"kind"`
	ReceiptID uuid.UUID                         `json:"receipt_id"`
}

func lifecycleStartControlBindingToWire(b LifecycleJobBinding) lifecycleStartControlBindingWire {
	w := lifecycleStartControlBindingWire{OrgID: b.orgID, SessionID: b.fence.sessionID, SessionEpoch: b.fence.sessionEpoch,
		JobID: b.jobID, DispatchIDs: b.DispatchIDs(), Kind: b.kind}
	if b.fence.threaded {
		thread, epoch := b.fence.threadID, b.fence.threadEpoch
		w.ThreadID, w.ThreadEpoch = &thread, &epoch
	}
	if b.requestID != uuid.Nil {
		request := b.requestID
		w.RequestID = &request
	}
	return w
}

func (w lifecycleStartControlBindingWire) binding() (LifecycleJobBinding, error) {
	fence, err := NewAdmissionFence(w.SessionID, w.ThreadID, w.SessionEpoch, w.ThreadEpoch)
	if err != nil {
		return LifecycleJobBinding{}, err
	}
	return NewLifecycleJobBinding(w.OrgID, fence, w.JobID, w.DispatchIDs, w.Kind, w.RequestID)
}

func lifecycleStartControlFenceToWire(f AdmissionFence) lifecycleStartControlFenceWire {
	w := lifecycleStartControlFenceWire{SessionID: f.sessionID, SessionEpoch: f.sessionEpoch}
	if f.threaded {
		thread, epoch := f.threadID, f.threadEpoch
		w.ThreadID, w.ThreadEpoch = &thread, &epoch
	}
	return w
}

func lifecycleStartControlPublicationToWire(p *WorkspacePublication) *lifecycleStartControlPublicationWire {
	if p == nil {
		return nil
	}
	w := &lifecycleStartControlPublicationWire{ID: p.ID, ReceiptID: p.ReceiptID, Key: p.Key, Version: p.Version,
		Producer: lifecycleStartControlOperationWire{p.Producer.ID, p.Producer.Kind}}
	if c := p.Checkpoint; c != nil {
		w.Checkpoint = &lifecycleStartControlCheckpointWire{c.RuntimeID, c.AttemptToken, c.PhaseID, c.Revision, c.PublishedAt.UTC()}
	}
	return w
}

func (w *lifecycleStartControlPublicationWire) publication() *WorkspacePublication {
	if w == nil {
		return nil
	}
	p := &WorkspacePublication{ID: w.ID, ReceiptID: w.ReceiptID, Key: w.Key, Version: w.Version,
		Producer: WorkspaceOperationReceipt{ID: w.Producer.ID, Kind: w.Producer.Kind}}
	if c := w.Checkpoint; c != nil {
		p.Checkpoint = &WorkspaceCheckpointProvenance{RuntimeID: c.RuntimeID, AttemptToken: c.AttemptToken, PhaseID: c.PhaseID,
			Revision: c.Revision, PublishedAt: c.PublishedAt.UTC()}
	}
	return p
}

func lifecycleStartControlDescriptorToWire(d WorkspaceDescriptor) lifecycleStartControlDescriptorWire {
	s := d.Spec()
	return lifecycleStartControlDescriptorWire{OrgID: s.OrgID, SessionID: s.SessionID, Generation: s.Generation, Revision: s.Revision,
		Publication:          lifecycleStartControlPublicationToWire(s.Publication),
		SnapshotAvailability: lifecycleStartControlAvailabilityWire{s.SnapshotAvailability.State, s.SnapshotAvailability.Version},
		PhysicalAvailability: lifecycleStartControlAvailabilityWire{s.PhysicalAvailability.State, s.PhysicalAvailability.Version},
		LastOperation:        lifecycleStartControlOperationWire{s.LastOperation.ID, s.LastOperation.Kind}}
}

func (w lifecycleStartControlDescriptorWire) descriptor() (WorkspaceDescriptor, error) {
	return NewWorkspaceDescriptor(WorkspaceDescriptorSpec{OrgID: w.OrgID, SessionID: w.SessionID, Generation: w.Generation, Revision: w.Revision,
		Publication: w.Publication.publication(), SnapshotAvailability: WorkspaceAvailability{State: w.SnapshotAvailability.State, Version: w.SnapshotAvailability.Version},
		PhysicalAvailability: WorkspaceAvailability{State: w.PhysicalAvailability.State, Version: w.PhysicalAvailability.Version},
		LastOperation:        WorkspaceOperationReceipt{ID: w.LastOperation.ID, Kind: w.LastOperation.Kind}})
}

func lifecycleStartControlToWire(s lifecycleStartControlSpec) lifecycleStartControlWire {
	d, target := s.Identity.Details(), s.Target.Spec()
	identity := lifecycleStartControlIdentityWire{Kind: s.Identity.Kind(), OrgID: d.Scope.OrgID, RequestID: d.Scope.RequestID,
		Fence: lifecycleStartControlFenceToWire(d.Scope.Fence), Provider: d.Scope.Provider, PinnedBuild: d.Scope.PinnedBuild, Workdir: d.Scope.Workdir,
		Generation: d.Scope.Generation, NativeConversationID: d.NativeConversationID, ProviderRequestID: d.ProviderRequestID,
		Checkpoint: lifecycleStartControlPublicationToWire(d.Checkpoint), AnswerProtocolVersion: d.AnswerProtocolVersion}
	if d.ProducerPhaseID != uuid.Nil {
		phase := d.ProducerPhaseID
		identity.ProducerPhaseID = &phase
	}
	targetWire := lifecycleStartControlTargetWire{Kind: target.Kind, TargetID: target.TargetID, Provider: target.Provider,
		PinnedBuild: target.PinnedBuild, ImageBuild: target.ImageBuild, VerificationProtocol: target.VerificationProtocol,
		PreparationID: target.PreparationID, PreparationToken: target.PreparationToken, Descriptor: lifecycleStartControlDescriptorToWire(target.Descriptor),
		TargetAvailability: lifecycleStartControlAvailabilityWire{target.TargetAvailability.State, target.TargetAvailability.Version}}
	if target.Source != nil {
		source := lifecycleStartControlDescriptorToWire(*target.Source)
		targetWire.Source = &source
	}
	allocations := make([]lifecycleStartControlAllocationWire, 0, len(s.Revocation.Allocations))
	for _, a := range s.Revocation.Allocations {
		allocations = append(allocations, lifecycleStartControlAllocationWire{Binding: lifecycleStartControlBindingToWire(a.binding),
			JobToken: a.jobToken, RuntimeID: a.runtimeID, RuntimeToken: a.runtimeToken, AllocationDigest: fmtLifecycleStartControlDigest(a.allocationDigest), ExpectedTurn: a.expectedTurn})
	}
	slices.SortFunc(allocations, func(a, b lifecycleStartControlAllocationWire) int {
		return bytes.Compare(a.RuntimeID[:], b.RuntimeID[:])
	})
	return lifecycleStartControlWire{SchemaVersion: 1, ReceiptID: s.ReceiptID, Version: s.Version, State: s.State,
		Binding: lifecycleStartControlBindingToWire(s.Binding), JobPayloadDigest: fmtLifecycleStartControlDigest(s.JobPayloadDigest),
		InputDigest: fmtLifecycleStartControlDigest(s.InputDigest), ClaimToken: s.ClaimToken, ClaimTurn: s.ClaimTurn, Identity: identity,
		Target: targetWire, Outcome: s.Outcome, Revocation: lifecycleStartControlRevocationWire{s.Revocation.PreparationID, s.Revocation.PreparationToken,
			s.Revocation.JobClaimToken, cloneLifecycleStartControlUUID(s.Revocation.PermitReceiptID), allocations},
		Evidence:      lifecycleStartControlEvidenceWire{s.Evidence.Kind, s.Evidence.ReceiptID},
		NextAttemptAt: cloneLifecycleStartControlTime(s.NextAttemptAt), RefundReceiptID: cloneLifecycleStartControlUUID(s.RefundReceiptID)}
}

func fmtLifecycleStartControlDigest(d LifecycleDigest) string {
	// Only validated values reach the DTO encoder. Hex formatting cannot fail.
	return hex.EncodeToString(d[:])
}

func cloneLifecycleStartControlUUID(p *uuid.UUID) *uuid.UUID {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}

func cloneLifecycleStartControlTime(p *time.Time) *time.Time {
	if p == nil {
		return nil
	}
	// This value is mirrored in a timestamptz column. Match pgx's encoding
	// precision before capturing both representations; historical checkpoint
	// timestamps are separate and are deliberately not normalized here.
	value := p.UTC().Truncate(time.Microsecond)
	return &value
}

func (w lifecycleStartControlWire) spec() (lifecycleStartControlSpec, error) {
	bad := func() (lifecycleStartControlSpec, error) {
		return lifecycleStartControlSpec{}, errors.New("invalid retained start-control fields")
	}
	if w.SchemaVersion != 1 {
		return bad()
	}
	binding, err := w.Binding.binding()
	if err != nil {
		return bad()
	}
	f := w.Identity.Fence
	identityFence, err := NewAdmissionFence(f.SessionID, f.ThreadID, f.SessionEpoch, f.ThreadEpoch)
	if err != nil || identityFence != binding.fence {
		return bad()
	}
	scope := ResumeIdentityScope{OrgID: w.Identity.OrgID, RequestID: w.Identity.RequestID, Fence: identityFence,
		Provider: w.Identity.Provider, PinnedBuild: w.Identity.PinnedBuild, Workdir: w.Identity.Workdir, Generation: w.Identity.Generation}
	var phase uuid.UUID
	if w.Identity.ProducerPhaseID != nil {
		phase = *w.Identity.ProducerPhaseID
		if phase == uuid.Nil {
			return bad()
		}
	}
	identity, err := newResumeIdentity(w.Identity.Kind, ResumeIdentityDetails{Scope: scope, NativeConversationID: w.Identity.NativeConversationID,
		ProviderRequestID: w.Identity.ProviderRequestID, ProducerPhaseID: phase, Checkpoint: w.Identity.Checkpoint.publication(), AnswerProtocolVersion: w.Identity.AnswerProtocolVersion})
	if err != nil {
		return bad()
	}
	descriptor, err := w.Target.Descriptor.descriptor()
	if err != nil {
		return bad()
	}
	targetSpec := ResumabilityTargetSpec{Kind: w.Target.Kind, TargetID: w.Target.TargetID, Provider: w.Target.Provider,
		PinnedBuild: w.Target.PinnedBuild, ImageBuild: w.Target.ImageBuild, VerificationProtocol: w.Target.VerificationProtocol,
		PreparationID: w.Target.PreparationID, PreparationToken: w.Target.PreparationToken, Descriptor: descriptor,
		TargetAvailability: WorkspaceAvailability{State: w.Target.TargetAvailability.State, Version: w.Target.TargetAvailability.Version}}
	if w.Target.Source != nil {
		source, sourceErr := w.Target.Source.descriptor()
		if sourceErr != nil {
			return bad()
		}
		targetSpec.Source = &source
	}
	target, err := NewResumabilityTarget(targetSpec)
	if err != nil {
		return bad()
	}
	jobDigest, err := ParseLifecycleDigest(w.JobPayloadDigest)
	if err != nil {
		return bad()
	}
	inputDigest, err := ParseLifecycleDigest(w.InputDigest)
	if err != nil {
		return bad()
	}
	allocations := make([]AttemptFence, 0, len(w.Revocation.Allocations))
	for _, a := range w.Revocation.Allocations {
		binding, bindingErr := a.Binding.binding()
		if bindingErr != nil {
			return bad()
		}
		digest, digestErr := ParseLifecycleDigest(a.AllocationDigest)
		if digestErr != nil {
			return bad()
		}
		allocation, allocationErr := NewAttemptFence(binding, a.JobToken, a.RuntimeID, a.RuntimeToken, digest, a.ExpectedTurn)
		if allocationErr != nil {
			return bad()
		}
		allocations = append(allocations, allocation)
	}
	spec := lifecycleStartControlSpec{ReceiptID: w.ReceiptID, Version: w.Version, State: w.State, Binding: binding,
		JobPayloadDigest: jobDigest, InputDigest: inputDigest, ClaimToken: w.ClaimToken, ClaimTurn: w.ClaimTurn,
		Identity: identity, Target: target, Outcome: w.Outcome,
		Revocation: lifecycleStartControlRevocation{w.Revocation.PreparationID, w.Revocation.PreparationToken, w.Revocation.JobClaimToken,
			cloneLifecycleStartControlUUID(w.Revocation.PermitReceiptID), allocations},
		Evidence: lifecycleStartControlEvidence{w.Evidence.Kind, w.Evidence.ReceiptID}, NextAttemptAt: cloneLifecycleStartControlTime(w.NextAttemptAt),
		RefundReceiptID: cloneLifecycleStartControlUUID(w.RefundReceiptID)}
	if validateLifecycleStartControlSpec(spec) != nil {
		return bad()
	}
	return spec, nil
}

func decodeLifecycleStartControlJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateLifecycleJobJSONKeys(decoder); err != nil {
		return errors.New("ambiguous retained start-control JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("retained start control requires one JSON value")
	}
	if err := validateLifecycleStartControlJSONShape(raw, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return errors.New("invalid retained start-control JSON values")
	}
	return nil
}

// All supported DTO objects have an exact case-sensitive field set. Required
// fields must be present (including numeric zero); optional fields must be
// absent rather than null. Validation also traverses every nested object/array.
func validateLifecycleStartControlJSONShape(raw json.RawMessage, kind reflect.Type) error {
	bad := func() error { return errors.New("unsupported retained start-control JSON shape") }
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return bad()
	}
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if kind == reflect.TypeFor[uuid.UUID]() || kind == reflect.TypeFor[time.Time]() {
		return nil
	}
	switch kind.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return bad()
		}
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			value, present := fields[name]
			if !present {
				if options != "omitempty" {
					return bad()
				}
				continue
			}
			if options == "omitempty" && field.Type.Kind() == reflect.String && bytes.Equal(bytes.TrimSpace(value), []byte(`""`)) {
				return bad()
			}
			if err := validateLifecycleStartControlJSONShape(value, field.Type); err != nil {
				return err
			}
			delete(fields, name)
		}
		if len(fields) != 0 {
			return bad()
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return bad()
		}
		for _, value := range values {
			if err := validateLifecycleStartControlJSONShape(value, kind.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
