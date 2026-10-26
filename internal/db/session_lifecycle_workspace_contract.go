package db

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// These records freeze C.0's inputs, not executable policy. Construction and
// validation establish shape only: no record proves durable ownership, physical
// drain, provider conformance or permission to mutate, restore, delete or start.
// Future canonical seams must re-read exact durable evidence in their own tx.

type WorkspaceAvailabilityState string

const (
	WorkspaceAvailable   WorkspaceAvailabilityState = "available"
	WorkspaceUnavailable WorkspaceAvailabilityState = "unavailable"
)

func (s WorkspaceAvailabilityState) Validate() error {
	if s != WorkspaceAvailable && s != WorkspaceUnavailable {
		return fmt.Errorf("invalid workspace availability: %q", s)
	}
	return nil
}

type WorkspaceAvailability struct {
	State   WorkspaceAvailabilityState
	Version int64
}

func (a WorkspaceAvailability) Validate() error {
	if a.Version <= 0 {
		return errors.New("workspace availability requires a positive version")
	}
	return a.State.Validate()
}

type WorkspaceOperationKind string

const (
	WorkspaceSiblingCheckpoint WorkspaceOperationKind = "sibling_checkpoint"
	WorkspaceSiblingDiff       WorkspaceOperationKind = "sibling_diff"
	WorkspaceMetadataRevision  WorkspaceOperationKind = "metadata_revision"
	WorkspacePauseMetadataSeal WorkspaceOperationKind = "pause_metadata_seal"
	WorkspacePublicationChange WorkspaceOperationKind = "publication_promotion"
	WorkspacePreviewChange     WorkspaceOperationKind = "preview_effect"
	WorkspaceAcceptSplit       WorkspaceOperationKind = "accept_split"
	WorkspaceScopeRetarget     WorkspaceOperationKind = "scope_retarget"
	WorkspaceRestore           WorkspaceOperationKind = "restore_replacement"
	WorkspaceContextReplace    WorkspaceOperationKind = "context_replacement"
	WorkspaceGenerationChange  WorkspaceOperationKind = "generation_change"
	WorkspaceSnapshotExpiry    WorkspaceOperationKind = "shared_snapshot_expiry"
	WorkspacePhysicalLoss      WorkspaceOperationKind = "workspace_expiry"
)

func (k WorkspaceOperationKind) Validate() error {
	switch k {
	case WorkspaceSiblingCheckpoint, WorkspaceSiblingDiff, WorkspaceMetadataRevision,
		WorkspacePauseMetadataSeal, WorkspacePublicationChange, WorkspacePreviewChange,
		WorkspaceAcceptSplit, WorkspaceScopeRetarget, WorkspaceRestore,
		WorkspaceContextReplace, WorkspaceGenerationChange, WorkspaceSnapshotExpiry,
		WorkspacePhysicalLoss:
		return nil
	default:
		return fmt.Errorf("invalid workspace operation kind: %q", k)
	}
}

type WorkspaceOperationReceipt struct {
	ID   uuid.UUID
	Kind WorkspaceOperationKind
}

func (r WorkspaceOperationReceipt) Validate() error {
	if r.ID == uuid.Nil {
		return errors.New("workspace operation requires an exact receipt")
	}
	return r.Kind.Validate()
}

// WorkspaceCheckpointProvenance is optional for publications by specialist
// owners, which must not fabricate runtime provenance. When present it is exact.
type WorkspaceCheckpointProvenance struct {
	RuntimeID, AttemptToken, PhaseID uuid.UUID
	Revision                         int64
	PublishedAt                      time.Time
}

type WorkspacePublication struct {
	ID, ReceiptID uuid.UUID
	Key           string
	Version       int64
	Producer      WorkspaceOperationReceipt
	Checkpoint    *WorkspaceCheckpointProvenance
}

func (p WorkspacePublication) Validate() error {
	if p.ID == uuid.Nil || p.ReceiptID == uuid.Nil || strings.TrimSpace(p.Key) == "" || p.Version <= 0 {
		return errors.New("workspace publication requires exact IDs, key and positive version")
	}
	if err := p.Producer.Validate(); err != nil {
		return err
	}
	if c := p.Checkpoint; c != nil && (c.RuntimeID == uuid.Nil || c.AttemptToken == uuid.Nil ||
		c.PhaseID == uuid.Nil || c.Revision < 0 || c.PublishedAt.IsZero()) {
		return errors.New("workspace checkpoint provenance is incomplete")
	}
	return nil
}

func cloneWorkspacePublication(p *WorkspacePublication) *WorkspacePublication {
	if p == nil {
		return nil
	}
	copy := *p
	if p.Checkpoint != nil {
		checkpoint := *p.Checkpoint
		copy.Checkpoint = &checkpoint
	}
	return &copy
}

type WorkspaceDescriptorSpec struct {
	OrgID, SessionID                           uuid.UUID
	Generation, Revision                       int64
	Publication                                *WorkspacePublication
	SnapshotAvailability, PhysicalAvailability WorkspaceAvailability
	LastOperation                              WorkspaceOperationReceipt
}

// WorkspaceDescriptor is a full exact CAS value. A newer revision alone is not
// equivalent; key-only, producer-only and availability-only changes also differ.
type WorkspaceDescriptor struct{ spec WorkspaceDescriptorSpec }

func NewWorkspaceDescriptor(spec WorkspaceDescriptorSpec) (WorkspaceDescriptor, error) {
	spec.Publication = cloneWorkspacePublication(spec.Publication)
	d := WorkspaceDescriptor{spec: spec}
	if err := d.Validate(); err != nil {
		return WorkspaceDescriptor{}, err
	}
	return d, nil
}

func (d WorkspaceDescriptor) Spec() WorkspaceDescriptorSpec {
	spec := d.spec
	spec.Publication = cloneWorkspacePublication(spec.Publication)
	return spec
}

func (d WorkspaceDescriptor) Validate() error {
	s := d.spec
	if s.OrgID == uuid.Nil || s.SessionID == uuid.Nil || s.Generation < 0 || s.Revision < 0 {
		return errors.New("workspace descriptor requires exact scope and nonnegative generation/revision")
	}
	if err := s.SnapshotAvailability.Validate(); err != nil {
		return err
	}
	if err := s.PhysicalAvailability.Validate(); err != nil {
		return err
	}
	if err := s.LastOperation.Validate(); err != nil {
		return err
	}
	if s.Publication == nil {
		if s.SnapshotAvailability.State == WorkspaceAvailable {
			return errors.New("available snapshot requires an exact publication")
		}
		return nil
	}
	return s.Publication.Validate()
}

func (d WorkspaceDescriptor) Equal(other WorkspaceDescriptor) bool {
	if d.Validate() != nil || other.Validate() != nil {
		return false
	}
	a, b := d.spec, other.spec
	if a.OrgID != b.OrgID || a.SessionID != b.SessionID || a.Generation != b.Generation ||
		a.Revision != b.Revision || a.SnapshotAvailability != b.SnapshotAvailability ||
		a.PhysicalAvailability != b.PhysicalAvailability || a.LastOperation != b.LastOperation {
		return false
	}
	return equalWorkspacePublication(a.Publication, b.Publication)
}

func equalWorkspacePublication(a, b *WorkspacePublication) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.ID != b.ID || a.ReceiptID != b.ReceiptID || a.Key != b.Key || a.Version != b.Version || a.Producer != b.Producer {
		return false
	}
	if a.Checkpoint == nil || b.Checkpoint == nil {
		return a.Checkpoint == nil && b.Checkpoint == nil
	}
	ac, bc := *a.Checkpoint, *b.Checkpoint
	return ac.RuntimeID == bc.RuntimeID && ac.AttemptToken == bc.AttemptToken && ac.PhaseID == bc.PhaseID &&
		ac.Revision == bc.Revision && ac.PublishedAt.Equal(bc.PublishedAt)
}

type PendingUploadReceiptSpec struct {
	OrgID                                        uuid.UUID
	Fence                                        AdmissionFence
	PublicationID, UploadID, Token, CleanupJobID uuid.UUID
	Key                                          string
	Generation, Version                          int64
	ExpectedDescriptor                           WorkspaceDescriptor
	Deadline                                     time.Time
}

type PendingUploadReceipt struct{ spec PendingUploadReceiptSpec }

func NewPendingUploadReceipt(spec PendingUploadReceiptSpec) (PendingUploadReceipt, error) {
	r := PendingUploadReceipt{spec: spec}
	if err := r.Validate(); err != nil {
		return PendingUploadReceipt{}, err
	}
	return r, nil
}

func (r PendingUploadReceipt) Spec() PendingUploadReceiptSpec { return r.spec }

func (r PendingUploadReceipt) Validate() error {
	s := r.spec
	if err := validateWorkspaceFence(s.OrgID, s.Fence, s.ExpectedDescriptor); err != nil {
		return err
	}
	if s.PublicationID == uuid.Nil || s.UploadID == uuid.Nil || s.Token == uuid.Nil || s.CleanupJobID == uuid.Nil ||
		strings.TrimSpace(s.Key) == "" || s.Version <= 0 || s.Deadline.IsZero() || s.Generation != s.ExpectedDescriptor.spec.Generation {
		return errors.New("pending upload requires exact registration, cleanup job, generation, version and deadline")
	}
	if p := s.ExpectedDescriptor.spec.Publication; p != nil && (p.Key == s.Key || p.ID == s.PublicationID) {
		return errors.New("pending upload must use a new publication and never-installed key")
	}
	return nil
}

func validateWorkspaceFence(orgID uuid.UUID, fence AdmissionFence, descriptor WorkspaceDescriptor) error {
	if err := fence.Validate(); err != nil {
		return err
	}
	if err := descriptor.Validate(); err != nil {
		return err
	}
	if orgID == uuid.Nil || descriptor.spec.OrgID != orgID || descriptor.spec.SessionID != fence.SessionID() {
		return errors.New("workspace receipt scope differs from its exact descriptor")
	}
	return nil
}

type ResumeIdentityKind string

const (
	ResumeNativeConversation    ResumeIdentityKind = "native_conversation"
	ResumeWorkspaceContinuation ResumeIdentityKind = "workspace_continuation"
	ResumeInitialTurn           ResumeIdentityKind = "initial_turn"
)

func (k ResumeIdentityKind) Validate() error {
	switch k {
	case ResumeNativeConversation, ResumeWorkspaceContinuation, ResumeInitialTurn:
		return nil
	default:
		return fmt.Errorf("invalid resume identity kind: %q", k)
	}
}

type ResumeIdentityScope struct {
	OrgID, RequestID     uuid.UUID
	Fence                AdmissionFence
	Provider             models.AgentType
	PinnedBuild, Workdir string
	Generation           int64
}

type ResumeIdentityDetails struct {
	Scope                                   ResumeIdentityScope
	NativeConversationID, ProviderRequestID string
	ProducerPhaseID                         uuid.UUID
	Checkpoint                              *WorkspacePublication
	AnswerProtocolVersion                   string
}

// ResumeIdentity is a sealed union, not proof that an adapter supports a mode.
type ResumeIdentity interface {
	resumeIdentity()
	Kind() ResumeIdentityKind
	Details() ResumeIdentityDetails
	Validate() error
}

type resumeIdentity struct {
	kind    ResumeIdentityKind
	details ResumeIdentityDetails
}

func (resumeIdentity) resumeIdentity()            {}
func (i resumeIdentity) Kind() ResumeIdentityKind { return i.kind }
func (i resumeIdentity) Details() ResumeIdentityDetails {
	d := i.details
	d.Checkpoint = cloneWorkspacePublication(d.Checkpoint)
	return d
}

func NewNativeConversationResumeIdentity(scope ResumeIdentityScope, nativeID string) (ResumeIdentity, error) {
	return newResumeIdentity(ResumeNativeConversation, ResumeIdentityDetails{Scope: scope, NativeConversationID: nativeID})
}

func NewWorkspaceContinuationResumeIdentity(scope ResumeIdentityScope, providerRequestID string, phaseID uuid.UUID, checkpoint WorkspacePublication, protocolVersion string) (ResumeIdentity, error) {
	return newResumeIdentity(ResumeWorkspaceContinuation, ResumeIdentityDetails{Scope: scope,
		ProviderRequestID: providerRequestID, ProducerPhaseID: phaseID, Checkpoint: &checkpoint, AnswerProtocolVersion: protocolVersion})
}

func NewInitialTurnResumeIdentity(scope ResumeIdentityScope) (ResumeIdentity, error) {
	return newResumeIdentity(ResumeInitialTurn, ResumeIdentityDetails{Scope: scope})
}

func newResumeIdentity(kind ResumeIdentityKind, details ResumeIdentityDetails) (ResumeIdentity, error) {
	details.Checkpoint = cloneWorkspacePublication(details.Checkpoint)
	i := resumeIdentity{kind: kind, details: details}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return i, nil
}

func (i resumeIdentity) Validate() error {
	d, s := i.details, i.details.Scope
	if err := s.Fence.Validate(); err != nil {
		return err
	}
	if err := s.Provider.Validate(); err != nil {
		return err
	}
	if s.OrgID == uuid.Nil || strings.TrimSpace(s.PinnedBuild) == "" || strings.TrimSpace(s.Workdir) == "" || s.Generation < 0 {
		return errors.New("resume identity requires exact scope, pinned build, workdir and generation")
	}
	switch i.kind {
	case ResumeNativeConversation:
		if strings.TrimSpace(d.NativeConversationID) == "" || d.ProviderRequestID != "" || d.ProducerPhaseID != uuid.Nil || d.Checkpoint != nil || d.AnswerProtocolVersion != "" {
			return errors.New("native resume requires only its exact native conversation ID")
		}
	case ResumeWorkspaceContinuation:
		if s.RequestID == uuid.Nil || strings.TrimSpace(d.ProviderRequestID) == "" || d.ProducerPhaseID == uuid.Nil ||
			d.Checkpoint == nil || strings.TrimSpace(d.AnswerProtocolVersion) == "" || d.NativeConversationID != "" {
			return errors.New("workspace continuation requires exact request, phase, checkpoint and answer protocol")
		}
		return d.Checkpoint.Validate()
	case ResumeInitialTurn:
		if s.RequestID != uuid.Nil || d.NativeConversationID != "" || d.ProviderRequestID != "" || d.ProducerPhaseID != uuid.Nil || d.Checkpoint != nil || d.AnswerProtocolVersion != "" {
			return errors.New("initial turn cannot identify a bound request or continuation")
		}
	default:
		return i.kind.Validate()
	}
	return nil
}

// ValidateBoundResumeIdentity rejects initial/fresh context for an answer or
// denial; it validates binding shape, not request eligibility or adapter support.
func ValidateBoundResumeIdentity(identity ResumeIdentity, requestID uuid.UUID) error {
	if err := validateResumeIdentity(identity); err != nil {
		return err
	}
	if requestID == uuid.Nil || identity.Kind() == ResumeInitialTurn || identity.Details().Scope.RequestID != requestID {
		return errors.New("bound input requires its exact non-initial resume identity")
	}
	if _, threaded := identity.Details().Scope.Fence.ThreadID(); !threaded {
		return errors.New("bound input requires its exact normalized lane and lane epoch")
	}
	return nil
}

func validateResumeIdentity(identity ResumeIdentity) error {
	// Only the value variant is admitted; nil, typed-nil and pointer variants fail.
	i, ok := identity.(resumeIdentity)
	if !ok {
		return errors.New("invalid sealed resume identity")
	}
	return i.Validate()
}

type RequestIdentityKind string

const (
	RequestIdentityProvider   RequestIdentityKind = "provider_request"
	RequestIdentityCapability RequestIdentityKind = "capability_request"
)

func (k RequestIdentityKind) Validate() error {
	if k != RequestIdentityProvider && k != RequestIdentityCapability {
		return fmt.Errorf("invalid request identity kind: %q", k)
	}
	return nil
}

type RequestIdentityDetails struct {
	RequestID, ProducerPhaseID, ProducerRuntimeID uuid.UUID
	ProviderRequestID, CapabilityItemID           string
	RequestKind                                   models.HumanInputRequestKind
}

// RequestIdentity binds the question/approval itself. ResumeIdentity separately
// identifies the conversation or supported workspace-answer protocol.
type RequestIdentity interface {
	requestIdentity()
	Kind() RequestIdentityKind
	Details() RequestIdentityDetails
	Validate() error
}

type requestIdentity struct {
	kind    RequestIdentityKind
	details RequestIdentityDetails
}

func (requestIdentity) requestIdentity()                  {}
func (i requestIdentity) Kind() RequestIdentityKind       { return i.kind }
func (i requestIdentity) Details() RequestIdentityDetails { return i.details }

func NewProviderRequestIdentity(requestID uuid.UUID, kind models.HumanInputRequestKind, providerRequestID string) (RequestIdentity, error) {
	return newRequestIdentity(RequestIdentityProvider, RequestIdentityDetails{RequestID: requestID, RequestKind: kind, ProviderRequestID: providerRequestID})
}

func NewCapabilityRequestIdentity(requestID uuid.UUID, kind models.HumanInputRequestKind, capabilityItemID string, phaseID, runtimeID uuid.UUID) (RequestIdentity, error) {
	return newRequestIdentity(RequestIdentityCapability, RequestIdentityDetails{RequestID: requestID, RequestKind: kind,
		CapabilityItemID: capabilityItemID, ProducerPhaseID: phaseID, ProducerRuntimeID: runtimeID})
}

func newRequestIdentity(kind RequestIdentityKind, details RequestIdentityDetails) (RequestIdentity, error) {
	i := requestIdentity{kind: kind, details: details}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return i, nil
}

func (i requestIdentity) Validate() error {
	d := i.details
	if err := d.RequestKind.Validate(); err != nil {
		return err
	}
	if d.RequestID == uuid.Nil {
		return errors.New("request identity requires an exact internal request ID")
	}
	switch i.kind {
	case RequestIdentityProvider:
		if strings.TrimSpace(d.ProviderRequestID) == "" || d.CapabilityItemID != "" || d.ProducerPhaseID != uuid.Nil || d.ProducerRuntimeID != uuid.Nil {
			return errors.New("provider request requires only its exact native request ID")
		}
	case RequestIdentityCapability:
		if strings.TrimSpace(d.CapabilityItemID) == "" || d.ProducerPhaseID == uuid.Nil || d.ProducerRuntimeID == uuid.Nil || d.ProviderRequestID != "" {
			return errors.New("capability request requires its item and exact producing phase/runtime without a fake native ID")
		}
	default:
		return i.kind.Validate()
	}
	return nil
}

type InputWorkspaceAnchorSpec struct {
	Request                                                         RequestIdentity
	Identity                                                        ResumeIdentity
	PausedDescriptor                                                WorkspaceDescriptor
	Turn                                                            int32
	ProducerJobID, ProducerRuntimeID, ProducerAttemptToken, PhaseID uuid.UUID
	ProducerRuntimeLeaseToken                                       uuid.UUID
	FinalizationReceiptID, DrainReceiptID                           uuid.UUID
	FinalizedAt                                                     time.Time
	AnchorDigest, ContextScopeDigest                                LifecycleDigest
}

// InputWorkspaceAnchor is immutable A identity; current sibling publication
// identity belongs only to the separate certificate descriptors.
type InputWorkspaceAnchor struct{ spec InputWorkspaceAnchorSpec }

func NewInputWorkspaceAnchor(spec InputWorkspaceAnchorSpec) (InputWorkspaceAnchor, error) {
	a := InputWorkspaceAnchor{spec: spec}
	if err := a.Validate(); err != nil {
		return InputWorkspaceAnchor{}, err
	}
	return a, nil
}

func (a InputWorkspaceAnchor) Spec() InputWorkspaceAnchorSpec { return a.spec }

func (a InputWorkspaceAnchor) Validate() error {
	s := a.spec
	request, ok := s.Request.(requestIdentity)
	if !ok {
		return errors.New("invalid sealed request identity")
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if err := validateResumeIdentity(s.Identity); err != nil {
		return err
	}
	if err := ValidateBoundResumeIdentity(s.Identity, s.Identity.Details().Scope.RequestID); err != nil {
		return err
	}
	scope := s.Identity.Details().Scope
	if err := validateWorkspaceFence(scope.OrgID, scope.Fence, s.PausedDescriptor); err != nil {
		return err
	}
	if scope.Generation != s.PausedDescriptor.spec.Generation {
		return errors.New("request pause descriptor differs from its bound generation")
	}
	if request.details.RequestID != s.Identity.Details().Scope.RequestID ||
		(request.kind == RequestIdentityCapability && (request.details.ProducerPhaseID != s.PhaseID || request.details.ProducerRuntimeID != s.ProducerRuntimeID)) {
		return errors.New("request identity differs from its immutable resume/producer anchor")
	}
	resume := s.Identity.Details()
	if s.Identity.Kind() == ResumeWorkspaceContinuation && (resume.ProducerPhaseID != s.PhaseID ||
		(request.kind == RequestIdentityProvider && resume.ProviderRequestID != request.details.ProviderRequestID)) {
		return errors.New("workspace continuation differs from the exact request or producing phase")
	}
	if s.Identity.Kind() == ResumeWorkspaceContinuation && (s.PausedDescriptor.spec.Publication == nil ||
		!equalWorkspacePublication(resume.Checkpoint, s.PausedDescriptor.spec.Publication)) {
		return errors.New("workspace continuation differs from the exact paused checkpoint publication")
	}
	if s.Turn < 0 || s.ProducerJobID == uuid.Nil || s.ProducerRuntimeID == uuid.Nil || s.ProducerAttemptToken == uuid.Nil ||
		s.ProducerRuntimeLeaseToken == uuid.Nil || s.PhaseID == uuid.Nil || s.FinalizationReceiptID == uuid.Nil || s.DrainReceiptID == uuid.Nil || s.FinalizedAt.IsZero() {
		return errors.New("request anchor requires exact producer, finalization and drain receipts")
	}
	if err := s.AnchorDigest.Validate(); err != nil {
		return err
	}
	return s.ContextScopeDigest.Validate()
}

type WorkspaceCertificateTransition struct {
	Anchor                InputWorkspaceAnchor
	ClassificationVersion int64
	Previous, Latest      WorkspaceDescriptor
	Operation             WorkspaceOperationReceipt
}

func (c WorkspaceCertificateTransition) Validate() error {
	if err := c.Anchor.Validate(); err != nil {
		return err
	}
	if c.ClassificationVersion <= 0 {
		return errors.New("certificate transition requires an exact classification version")
	}
	s := c.Anchor.spec.Identity.Details().Scope
	if err := validateWorkspaceFence(s.OrgID, s.Fence, c.Previous); err != nil {
		return err
	}
	if err := validateWorkspaceFence(s.OrgID, s.Fence, c.Latest); err != nil {
		return err
	}
	if err := c.Operation.Validate(); err != nil {
		return err
	}
	if c.Latest.spec.LastOperation != c.Operation {
		return errors.New("certificate transition must retain the exact latest operation receipt")
	}
	return nil
}

type WorkspaceRecoveryMode string

const (
	WorkspaceRecoveryCheckpoint WorkspaceRecoveryMode = "checkpoint_reask"
	WorkspaceRecoveryStartOver  WorkspaceRecoveryMode = "start_over"
)

func (m WorkspaceRecoveryMode) Validate() error {
	if m != WorkspaceRecoveryCheckpoint && m != WorkspaceRecoveryStartOver {
		return fmt.Errorf("invalid workspace recovery mode: %q", m)
	}
	return nil
}

type WorkspaceRecoveryReservationSpec struct {
	OrgID                                  uuid.UUID
	Fence                                  AdmissionFence
	RecoveryID, Token, PhysicalOperationID uuid.UUID
	Version                                int64
	Descriptor                             WorkspaceDescriptor
	Mode                                   WorkspaceRecoveryMode
	Checkpoint                             *WorkspacePublication
	AcknowledgementDigest                  LifecycleDigest
}

// WorkspaceRecoveryReservation always excludes the whole session workspace.
// No lease deadline is an implicit release or positive physical-drain receipt.
// Its digest binds the separately validated whole-session acknowledged set.
type WorkspaceRecoveryReservation struct {
	spec WorkspaceRecoveryReservationSpec
}

func NewWorkspaceRecoveryReservation(spec WorkspaceRecoveryReservationSpec) (WorkspaceRecoveryReservation, error) {
	spec.Checkpoint = cloneWorkspacePublication(spec.Checkpoint)
	r := WorkspaceRecoveryReservation{spec: spec}
	if err := r.Validate(); err != nil {
		return WorkspaceRecoveryReservation{}, err
	}
	return r, nil
}

func (r WorkspaceRecoveryReservation) Spec() WorkspaceRecoveryReservationSpec {
	s := r.spec
	s.Checkpoint = cloneWorkspacePublication(s.Checkpoint)
	return s
}

func (r WorkspaceRecoveryReservation) Validate() error {
	s := r.spec
	if err := validateWorkspaceFence(s.OrgID, s.Fence, s.Descriptor); err != nil {
		return err
	}
	if _, threaded := s.Fence.ThreadID(); threaded {
		return errors.New("workspace recovery reservation requires whole-session scope, not an initiating-lane fence")
	}
	if s.RecoveryID == uuid.Nil || s.Token == uuid.Nil || s.PhysicalOperationID == uuid.Nil || s.Version <= 0 {
		return errors.New("recovery reservation requires exact recovery, token, operation and version")
	}
	if err := s.AcknowledgementDigest.Validate(); err != nil {
		return err
	}
	if err := s.Mode.Validate(); err != nil {
		return err
	}
	if s.Mode == WorkspaceRecoveryCheckpoint {
		if s.Checkpoint == nil {
			return errors.New("checkpoint recovery requires an exact source publication")
		}
		return s.Checkpoint.Validate()
	}
	if s.Checkpoint != nil {
		return errors.New("start-over reservation cannot silently retain a restore source")
	}
	return nil
}

type WorkspaceRetentionPolicy string

const (
	WorkspaceRetentionAge      WorkspaceRetentionPolicy = "snapshot_age"
	WorkspaceRetentionArchive  WorkspaceRetentionPolicy = "archive"
	WorkspaceRetentionMergedPR WorkspaceRetentionPolicy = "merged_pr"
)

func (p WorkspaceRetentionPolicy) Validate() error {
	switch p {
	case WorkspaceRetentionAge, WorkspaceRetentionArchive, WorkspaceRetentionMergedPR:
		return nil
	default:
		return fmt.Errorf("invalid workspace retention policy: %q", p)
	}
}

type RetentionAuthoritySpec struct {
	Descriptor      WorkspaceDescriptor
	Policy          WorkspaceRetentionPolicy
	PolicyVersion   int64
	PolicyReceiptID uuid.UUID
}

// RetentionAuthority describes current-reference policy evidence. It cannot be
// substituted for an orphan cleanup job and never grants reset/end authority.
type RetentionAuthority struct{ spec RetentionAuthoritySpec }

func NewRetentionAuthority(spec RetentionAuthoritySpec) (RetentionAuthority, error) {
	a := RetentionAuthority{spec: spec}
	if err := a.Validate(); err != nil {
		return RetentionAuthority{}, err
	}
	return a, nil
}

func (a RetentionAuthority) Spec() RetentionAuthoritySpec { return a.spec }

func (a RetentionAuthority) Validate() error {
	s := a.spec
	if err := s.Descriptor.Validate(); err != nil {
		return err
	}
	if s.Descriptor.spec.Publication == nil || s.PolicyVersion <= 0 || s.PolicyReceiptID == uuid.Nil {
		return errors.New("retention evidence requires exact current publication, policy version and receipt")
	}
	return s.Policy.Validate()
}

type SnapshotWriterSettlementKind string

const (
	SnapshotWriterSettled       SnapshotWriterSettlementKind = "settled"
	SnapshotWriterFencedDrained SnapshotWriterSettlementKind = "fenced_drained"
)

func (k SnapshotWriterSettlementKind) Validate() error {
	if k != SnapshotWriterSettled && k != SnapshotWriterFencedDrained {
		return fmt.Errorf("invalid snapshot writer settlement: %q", k)
	}
	return nil
}

type SnapshotWriterSettlementReceipt struct {
	Kind                             SnapshotWriterSettlementKind
	WriterID, OperationID, ReceiptID uuid.UUID
}

func (r SnapshotWriterSettlementReceipt) Validate() error {
	if r.WriterID == uuid.Nil || r.OperationID == uuid.Nil || r.ReceiptID == uuid.Nil {
		return errors.New("snapshot settlement requires exact writer, operation and receipt")
	}
	return r.Kind.Validate()
}

type RegisteredSnapshotCleanupAuthoritySpec struct {
	OrgID, SessionID, CleanupJobID, JobLeaseToken, PublicationID, UploadID, Token uuid.UUID
	Key                                                                           string
	Generation, ControlVersion                                                    int64
	// ExpectedTombstoneVersion is zero only for an exact object with no tombstone
	// yet; the future delete/settlement seam requires a positive current version.
	ExpectedTombstoneVersion int64
	WriterSettlement         SnapshotWriterSettlementReceipt
}

// RegisteredSnapshotCleanupAuthority describes only one registered superseded
// or orphan resource job. Current-reference expiry uses RetentionAuthority.
type RegisteredSnapshotCleanupAuthority struct {
	spec RegisteredSnapshotCleanupAuthoritySpec
}

func NewRegisteredSnapshotCleanupAuthority(spec RegisteredSnapshotCleanupAuthoritySpec) (RegisteredSnapshotCleanupAuthority, error) {
	a := RegisteredSnapshotCleanupAuthority{spec: spec}
	if err := a.Validate(); err != nil {
		return RegisteredSnapshotCleanupAuthority{}, err
	}
	return a, nil
}

func (a RegisteredSnapshotCleanupAuthority) Spec() RegisteredSnapshotCleanupAuthoritySpec {
	return a.spec
}

func (a RegisteredSnapshotCleanupAuthority) Validate() error {
	s := a.spec
	if s.OrgID == uuid.Nil || s.SessionID == uuid.Nil || s.CleanupJobID == uuid.Nil || s.JobLeaseToken == uuid.Nil ||
		s.PublicationID == uuid.Nil || s.UploadID == uuid.Nil || s.Token == uuid.Nil || strings.TrimSpace(s.Key) == "" ||
		s.Generation < 0 || s.ControlVersion <= 0 || s.ExpectedTombstoneVersion < 0 {
		return errors.New("registered cleanup requires exact object, lease, control and expected tombstone evidence")
	}
	return s.WriterSettlement.Validate()
}

type ResumabilityTargetKind string

const (
	ResumabilityLiveTarget    ResumabilityTargetKind = "live"
	ResumabilityRestoreTarget ResumabilityTargetKind = "prepared_restore"
)

func (k ResumabilityTargetKind) Validate() error {
	if k != ResumabilityLiveTarget && k != ResumabilityRestoreTarget {
		return fmt.Errorf("invalid resumability target kind: %q", k)
	}
	return nil
}

type ResumabilityTargetSpec struct {
	Kind                            ResumabilityTargetKind
	TargetID                        string
	Provider                        models.AgentType
	PinnedBuild, ImageBuild         string
	VerificationProtocol            string
	PreparationID, PreparationToken uuid.UUID
	Descriptor                      WorkspaceDescriptor
	TargetAvailability              WorkspaceAvailability
	Source                          *WorkspaceDescriptor
}

type ResumabilityTarget struct{ spec ResumabilityTargetSpec }

func NewResumabilityTarget(spec ResumabilityTargetSpec) (ResumabilityTarget, error) {
	if spec.Source != nil {
		source := *spec.Source
		spec.Source = &source
	}
	t := ResumabilityTarget{spec: spec}
	if err := t.Validate(); err != nil {
		return ResumabilityTarget{}, err
	}
	return t, nil
}

func (t ResumabilityTarget) Spec() ResumabilityTargetSpec {
	s := t.spec
	if s.Source != nil {
		source := *s.Source
		s.Source = &source
	}
	return s
}

func (t ResumabilityTarget) Validate() error {
	s := t.spec
	if err := s.Kind.Validate(); err != nil {
		return err
	}
	if err := s.Provider.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(s.TargetID) == "" || strings.TrimSpace(s.PinnedBuild) == "" || strings.TrimSpace(s.ImageBuild) == "" ||
		strings.TrimSpace(s.VerificationProtocol) == "" || s.PreparationID == uuid.Nil || s.PreparationToken == uuid.Nil {
		return errors.New("resumability target requires exact target, provider/build/image/protocol and preparation identity")
	}
	if err := s.Descriptor.Validate(); err != nil {
		return err
	}
	if err := s.TargetAvailability.Validate(); err != nil {
		return err
	}
	if s.Kind == ResumabilityRestoreTarget && s.Source == nil {
		return errors.New("prepared restore requires an exact source descriptor")
	}
	if s.Kind == ResumabilityLiveTarget && s.Source != nil {
		return errors.New("live target cannot implicitly select a restore source")
	}
	if s.Source != nil {
		if err := s.Source.Validate(); err != nil {
			return err
		}
		if s.Source.spec.OrgID != s.Descriptor.spec.OrgID || s.Source.spec.SessionID != s.Descriptor.spec.SessionID ||
			s.Source.spec.Generation != s.Descriptor.spec.Generation || s.Source.spec.Publication == nil {
			return errors.New("restore source must identify this session generation and publication")
		}
	}
	return nil
}

func (t ResumabilityTarget) Equal(other ResumabilityTarget) bool {
	if t.Validate() != nil || other.Validate() != nil {
		return false
	}
	a, b := t.spec, other.spec
	if a.Kind != b.Kind || a.TargetID != b.TargetID || a.PreparationID != b.PreparationID ||
		a.PreparationToken != b.PreparationToken || a.Provider != b.Provider || a.PinnedBuild != b.PinnedBuild ||
		a.ImageBuild != b.ImageBuild || a.VerificationProtocol != b.VerificationProtocol ||
		a.TargetAvailability != b.TargetAvailability || !a.Descriptor.Equal(b.Descriptor) {
		return false
	}
	if a.Source == nil || b.Source == nil {
		return a.Source == nil && b.Source == nil
	}
	return a.Source.Equal(*b.Source)
}

type ResumabilityDisposition string

const (
	ResumabilityVerified    ResumabilityDisposition = "verified"
	ResumabilityRetryable   ResumabilityDisposition = "retryable"
	ResumabilityUnavailable ResumabilityDisposition = "unavailable"
	ResumabilityUnsupported ResumabilityDisposition = "unsupported"
)

func (d ResumabilityDisposition) Validate() error {
	switch d {
	case ResumabilityVerified, ResumabilityRetryable, ResumabilityUnavailable, ResumabilityUnsupported:
		return nil
	default:
		return fmt.Errorf("invalid resumability disposition: %q", d)
	}
}

type ResumabilityObservation struct {
	ID                    uuid.UUID
	Identity              ResumeIdentity
	Target                ResumabilityTarget
	Disposition           ResumabilityDisposition
	ProtocolVersion       string
	ObservedAt, ExpiresAt time.Time
}

// ResumabilityReceipt is a sealed observation record, never a StartPermit.
// Its constructor does not perform VerifyResumable or certify provider support.
type ResumabilityReceipt struct{ observation ResumabilityObservation }

func NewResumabilityReceipt(observation ResumabilityObservation) (ResumabilityReceipt, error) {
	r := ResumabilityReceipt{observation: observation}
	if err := r.Validate(); err != nil {
		return ResumabilityReceipt{}, err
	}
	return r, nil
}

func (r ResumabilityReceipt) Observation() ResumabilityObservation { return r.observation }

func (r ResumabilityReceipt) Validate() error {
	o := r.observation
	if err := validateResumeIdentity(o.Identity); err != nil {
		return err
	}
	if err := o.Target.Validate(); err != nil {
		return err
	}
	if err := o.Disposition.Validate(); err != nil {
		return err
	}
	s, d := o.Identity.Details().Scope, o.Target.spec.Descriptor.spec
	if s.OrgID != d.OrgID || s.Fence.SessionID() != d.SessionID || s.Generation != d.Generation {
		return errors.New("resumability observation identity differs from target scope/generation")
	}
	if s.Provider != o.Target.spec.Provider || s.PinnedBuild != o.Target.spec.PinnedBuild || o.ProtocolVersion != o.Target.spec.VerificationProtocol {
		return errors.New("resumability observation differs from actual target provider/build/protocol")
	}
	if o.ID == uuid.Nil || strings.TrimSpace(o.ProtocolVersion) == "" ||
		o.ObservedAt.IsZero() || !o.ExpiresAt.After(o.ObservedAt) {
		return errors.New("resumability observation requires exact receipt, protocol and bounded observation interval")
	}
	if o.Disposition == ResumabilityVerified && (o.Target.spec.TargetAvailability.State != WorkspaceAvailable ||
		(o.Target.spec.Source != nil && o.Target.spec.Source.spec.SnapshotAvailability.State != WorkspaceAvailable)) {
		return errors.New("verified observation requires explicit usable target and selected restore source")
	}
	return nil
}

// MatchesExact compares observations only; even verified equality is not final
// start authorization. Expiry and every source/target identity are exact.
func (r ResumabilityReceipt) MatchesExact(identity ResumeIdentity, target ResumabilityTarget, at time.Time) bool {
	if r.Validate() != nil || validateResumeIdentity(identity) != nil || at.IsZero() ||
		at.Before(r.observation.ObservedAt) || !at.Before(r.observation.ExpiresAt) || !r.observation.Target.Equal(target) {
		return false
	}
	a, b := r.observation.Identity, identity
	ad, bd := a.Details(), b.Details()
	as, bs := ad.Scope, bd.Scope
	return a.Kind() == b.Kind() && as.OrgID == bs.OrgID && as.RequestID == bs.RequestID &&
		equalWorkspaceResumeFence(as.Fence, bs.Fence) && as.Provider == bs.Provider && as.PinnedBuild == bs.PinnedBuild &&
		as.Workdir == bs.Workdir && as.Generation == bs.Generation && ad.NativeConversationID == bd.NativeConversationID &&
		ad.ProviderRequestID == bd.ProviderRequestID && ad.ProducerPhaseID == bd.ProducerPhaseID &&
		ad.AnswerProtocolVersion == bd.AnswerProtocolVersion && equalWorkspacePublication(ad.Checkpoint, bd.Checkpoint)
}

func equalWorkspaceResumeFence(a, b AdmissionFence) bool {
	at, ahas := a.ThreadID()
	bt, bhas := b.ThreadID()
	ae, aepoch := a.ThreadEpoch()
	be, bepoch := b.ThreadEpoch()
	return a.SessionID() == b.SessionID() && a.SessionEpoch() == b.SessionEpoch() &&
		at == bt && ahas == bhas && ae == be && aepoch == bepoch
}
