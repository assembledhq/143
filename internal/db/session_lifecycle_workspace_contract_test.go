package db

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type workspaceContractFixture struct {
	now          time.Time
	scope        ResumeIdentityScope
	sessionFence AdmissionFence
	descriptor   WorkspaceDescriptor
	identity     ResumeIdentity
	anchor       InputWorkspaceAnchor
	target       ResumabilityTarget
	digest       LifecycleDigest
}

func newWorkspaceContractFixture(t *testing.T) workspaceContractFixture {
	t.Helper()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	laneID, laneEpoch := uuid.New(), int64(2)
	fence, err := NewAdmissionFence(uuid.New(), &laneID, 3, &laneEpoch)
	require.NoError(t, err, "fixture should have a complete explicit lane fence")
	sessionFence, err := NewAdmissionFence(fence.SessionID(), nil, fence.SessionEpoch(), nil)
	require.NoError(t, err, "recovery fixture must reserve the whole session workspace")
	scope := ResumeIdentityScope{OrgID: uuid.New(), RequestID: uuid.New(), Fence: fence,
		Provider: models.AgentTypeCodex, PinnedBuild: "synthetic-build", Workdir: "synthetic-workdir-scope", Generation: 4}
	operation := WorkspaceOperationReceipt{ID: uuid.New(), Kind: WorkspaceSiblingCheckpoint}
	descriptor, err := NewWorkspaceDescriptor(WorkspaceDescriptorSpec{OrgID: scope.OrgID, SessionID: fence.SessionID(),
		Generation: scope.Generation, Revision: 7, LastOperation: operation,
		SnapshotAvailability: WorkspaceAvailability{State: WorkspaceAvailable, Version: 8},
		PhysicalAvailability: WorkspaceAvailability{State: WorkspaceAvailable, Version: 9},
		Publication: &WorkspacePublication{ID: uuid.New(), ReceiptID: uuid.New(), Key: "synthetic-publication-key", Version: 5,
			Producer: operation, Checkpoint: &WorkspaceCheckpointProvenance{RuntimeID: uuid.New(), AttemptToken: uuid.New(),
				PhaseID: uuid.New(), Revision: 6, PublishedAt: now.Add(-time.Minute)}}})
	require.NoError(t, err, "fixture should retain complete publication and availability identity")
	identity, err := NewNativeConversationResumeIdentity(scope, "synthetic-native-id")
	require.NoError(t, err, "fixture native identity should bind exact request and pinned build")
	digest := LifecycleDigest(sha256.Sum256([]byte("synthetic immutable identity")))
	request, err := NewProviderRequestIdentity(scope.RequestID, models.HumanInputRequestKindToolApproval, "synthetic-native-request")
	require.NoError(t, err, "fixture question identity must remain separate from its native conversation")
	anchor, err := NewInputWorkspaceAnchor(InputWorkspaceAnchorSpec{Request: request, Identity: identity, PausedDescriptor: descriptor, Turn: 2,
		ProducerJobID: uuid.New(), ProducerRuntimeID: uuid.New(), ProducerAttemptToken: uuid.New(), PhaseID: uuid.New(),
		ProducerRuntimeLeaseToken: uuid.New(), FinalizationReceiptID: uuid.New(), FinalizedAt: now, DrainReceiptID: uuid.New(), AnchorDigest: digest, ContextScopeDigest: digest})
	require.NoError(t, err, "fixture request anchor should retain exact finalization and drain receipts")
	target, err := NewResumabilityTarget(ResumabilityTargetSpec{Kind: ResumabilityLiveTarget, TargetID: "synthetic-target",
		Provider: scope.Provider, PinnedBuild: scope.PinnedBuild, ImageBuild: "synthetic-image-build", VerificationProtocol: "synthetic-protocol-v1",
		PreparationID: uuid.New(), PreparationToken: uuid.New(), Descriptor: descriptor,
		TargetAvailability: WorkspaceAvailability{State: WorkspaceAvailable, Version: 3}})
	require.NoError(t, err, "fixture target should bind a complete preparation and actual target")
	return workspaceContractFixture{now: now, scope: scope, sessionFence: sessionFence, descriptor: descriptor, identity: identity, anchor: anchor, target: target, digest: digest}
}

func TestWorkspaceContractEnums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value interface{ Validate() error }
	}{
		{"available", WorkspaceAvailable}, {"unavailable", WorkspaceUnavailable},
		{"checkpoint", WorkspaceSiblingCheckpoint}, {"diff", WorkspaceSiblingDiff},
		{"metadata", WorkspaceMetadataRevision}, {"seal", WorkspacePauseMetadataSeal},
		{"publication", WorkspacePublicationChange}, {"preview", WorkspacePreviewChange},
		{"split", WorkspaceAcceptSplit}, {"retarget", WorkspaceScopeRetarget},
		{"restore", WorkspaceRestore}, {"context replacement", WorkspaceContextReplace},
		{"generation", WorkspaceGenerationChange}, {"snapshot expiry", WorkspaceSnapshotExpiry},
		{"physical loss", WorkspacePhysicalLoss}, {"native", ResumeNativeConversation},
		{"continuation", ResumeWorkspaceContinuation}, {"initial", ResumeInitialTurn},
		{"checkpoint recovery", WorkspaceRecoveryCheckpoint}, {"start over", WorkspaceRecoveryStartOver},
		{"age", WorkspaceRetentionAge}, {"archive", WorkspaceRetentionArchive}, {"merged PR", WorkspaceRetentionMergedPR},
		{"settled", SnapshotWriterSettled}, {"fenced and drained", SnapshotWriterFencedDrained},
		{"live", ResumabilityLiveTarget}, {"prepared restore", ResumabilityRestoreTarget},
		{"verified", ResumabilityVerified}, {"retryable", ResumabilityRetryable},
		{"unsupported", ResumabilityUnsupported}, {"target unavailable", ResumabilityUnavailable},
		{"provider request", RequestIdentityProvider}, {"capability request", RequestIdentityCapability},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, tt.value.Validate(), "named contract enum should validate without granting authority")
		})
	}
	invalid := []struct {
		name  string
		value interface{ Validate() error }
	}{
		{"availability", WorkspaceAvailabilityState("")}, {"operation", WorkspaceOperationKind("compatible")},
		{"identity", ResumeIdentityKind("fresh_fallback")}, {"recovery", WorkspaceRecoveryMode("lane_reset")},
		{"retention", WorkspaceRetentionPolicy("provider_lease")}, {"writer", SnapshotWriterSettlementKind("expired")},
		{"target", ResumabilityTargetKind("")}, {"disposition", ResumabilityDisposition("")},
		{"request identity", RequestIdentityKind("")},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, tt.value.Validate(), "unknown or zero enum must never imply usable evidence")
		})
	}
}

func TestWorkspaceDescriptorRejectsMalformedShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*WorkspaceDescriptorSpec)
	}{
		{"zero org", func(s *WorkspaceDescriptorSpec) { s.OrgID = uuid.Nil }},
		{"zero session", func(s *WorkspaceDescriptorSpec) { s.SessionID = uuid.Nil }},
		{"negative generation", func(s *WorkspaceDescriptorSpec) { s.Generation = -1 }},
		{"negative revision", func(s *WorkspaceDescriptorSpec) { s.Revision = -1 }},
		{"unknown snapshot availability", func(s *WorkspaceDescriptorSpec) { s.SnapshotAvailability.State = "unknown" }},
		{"unversioned physical availability", func(s *WorkspaceDescriptorSpec) { s.PhysicalAvailability.Version = 0 }},
		{"unreceipted descriptor", func(s *WorkspaceDescriptorSpec) { s.LastOperation.ID = uuid.Nil }},
		{"snapshot without publication", func(s *WorkspaceDescriptorSpec) { s.Publication = nil }},
		{"publication without key", func(s *WorkspaceDescriptorSpec) { s.Publication.Key = " " }},
		{"publication without receipt", func(s *WorkspaceDescriptorSpec) { s.Publication.ReceiptID = uuid.Nil }},
		{"unversioned publication", func(s *WorkspaceDescriptorSpec) { s.Publication.Version = 0 }},
		{"unreceipted producer", func(s *WorkspaceDescriptorSpec) { s.Publication.Producer.ID = uuid.Nil }},
		{"incomplete checkpoint", func(s *WorkspaceDescriptorSpec) { s.Publication.Checkpoint.AttemptToken = uuid.Nil }},
		{"untimed checkpoint", func(s *WorkspaceDescriptorSpec) { s.Publication.Checkpoint.PublishedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newWorkspaceContractFixture(t)
			spec := fixture.descriptor.Spec()
			tt.mutate(&spec)
			actual, err := NewWorkspaceDescriptor(spec)
			require.Error(t, err, "malformed descriptor must fail shape validation")
			require.Equal(t, WorkspaceDescriptor{}, actual, "invalid descriptor must not expose partial evidence")
		})
	}
}

func TestWorkspaceDescriptorUsesExactIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*WorkspaceDescriptorSpec)
		equal  bool
	}{
		{"copied descriptor", func(*WorkspaceDescriptorSpec) {}, true},
		{"key only", func(s *WorkspaceDescriptorSpec) { s.Publication.Key = "synthetic-successor-key" }, false},
		{"publication only", func(s *WorkspaceDescriptorSpec) { s.Publication.ID = uuid.New() }, false},
		{"publication version", func(s *WorkspaceDescriptorSpec) { s.Publication.Version++ }, false},
		{"receipt only", func(s *WorkspaceDescriptorSpec) { s.Publication.ReceiptID = uuid.New() }, false},
		{"producer only", func(s *WorkspaceDescriptorSpec) { s.Publication.Producer.ID = uuid.New() }, false},
		{"checkpoint phase", func(s *WorkspaceDescriptorSpec) { s.Publication.Checkpoint.PhaseID = uuid.New() }, false},
		{"checkpoint time", func(s *WorkspaceDescriptorSpec) {
			s.Publication.Checkpoint.PublishedAt = s.Publication.Checkpoint.PublishedAt.Add(time.Second)
		}, false},
		{"snapshot availability only", func(s *WorkspaceDescriptorSpec) { s.SnapshotAvailability.Version++ }, false},
		{"physical availability only", func(s *WorkspaceDescriptorSpec) { s.PhysicalAvailability.Version++ }, false},
		{"snapshot tombstone", func(s *WorkspaceDescriptorSpec) { s.SnapshotAvailability.State = WorkspaceUnavailable }, false},
		{"higher revision", func(s *WorkspaceDescriptorSpec) { s.Revision++ }, false},
		{"higher generation", func(s *WorkspaceDescriptorSpec) { s.Generation++ }, false},
		{"different operation", func(s *WorkspaceDescriptorSpec) { s.LastOperation.ID = uuid.New() }, false},
		{"different org", func(s *WorkspaceDescriptorSpec) { s.OrgID = uuid.New() }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newWorkspaceContractFixture(t)
			spec := fixture.descriptor.Spec()
			tt.mutate(&spec)
			actual, err := NewWorkspaceDescriptor(spec)
			require.NoError(t, err, "identity changes may form valid descriptors without incrementing revision")
			require.Equal(t, tt.equal, fixture.descriptor.Equal(actual), "descriptor equality must compare every exact identity and availability field")
		})
	}
}

func TestWorkspaceDescriptorAllowsUsableLiveTargetWithoutSnapshot(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceContractFixture(t)
	spec := fixture.descriptor.Spec()
	spec.Publication = nil
	spec.SnapshotAvailability.State = WorkspaceUnavailable
	actual, err := NewWorkspaceDescriptor(spec)
	require.NoError(t, err, "explicitly available physical workspace must not require an installed snapshot")
	require.Equal(t, spec, actual.Spec(), "absent snapshot provenance must remain absent instead of being invented")
	require.False(t, (WorkspaceDescriptor{}).Equal(WorkspaceDescriptor{}), "zero descriptor must never compare as usable evidence")
}

func TestWorkspaceReceiptConstructorsRejectIncompleteInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func(workspaceContractFixture) (any, error)
		zero  any
	}{
		{"upload lacks cleanup registration", func(f workspaceContractFixture) (any, error) {
			s := validPendingUploadSpec(f)
			s.CleanupJobID = uuid.Nil
			return NewPendingUploadReceipt(s)
		}, PendingUploadReceipt{}},
		{"upload cannot reuse installed key", func(f workspaceContractFixture) (any, error) {
			s := validPendingUploadSpec(f)
			s.Key = f.descriptor.Spec().Publication.Key
			return NewPendingUploadReceipt(s)
		}, PendingUploadReceipt{}},
		{"upload cannot reuse installed publication", func(f workspaceContractFixture) (any, error) {
			s := validPendingUploadSpec(f)
			s.PublicationID = f.descriptor.Spec().Publication.ID
			return NewPendingUploadReceipt(s)
		}, PendingUploadReceipt{}},
		{"upload cannot change tenant", func(f workspaceContractFixture) (any, error) {
			s := validPendingUploadSpec(f)
			s.OrgID = uuid.New()
			return NewPendingUploadReceipt(s)
		}, PendingUploadReceipt{}},
		{"upload cannot change generation", func(f workspaceContractFixture) (any, error) {
			s := validPendingUploadSpec(f)
			s.Generation++
			return NewPendingUploadReceipt(s)
		}, PendingUploadReceipt{}},
		{"anchor lacks drain", func(f workspaceContractFixture) (any, error) {
			s := f.anchor.Spec()
			s.DrainReceiptID = uuid.Nil
			return NewInputWorkspaceAnchor(s)
		}, InputWorkspaceAnchor{}},
		{"anchor lacks context scope", func(f workspaceContractFixture) (any, error) {
			s := f.anchor.Spec()
			s.ContextScopeDigest = LifecycleDigest{}
			return NewInputWorkspaceAnchor(s)
		}, InputWorkspaceAnchor{}},
		{"recovery lacks acknowledgement", func(f workspaceContractFixture) (any, error) {
			s := validRecoveryReservationSpec(f)
			s.AcknowledgementDigest = LifecycleDigest{}
			return NewWorkspaceRecoveryReservation(s)
		}, WorkspaceRecoveryReservation{}},
		{"recovery cannot reserve only one lane", func(f workspaceContractFixture) (any, error) {
			s := validRecoveryReservationSpec(f)
			s.Fence = f.scope.Fence
			return NewWorkspaceRecoveryReservation(s)
		}, WorkspaceRecoveryReservation{}},
		{"checkpoint recovery lacks source", func(f workspaceContractFixture) (any, error) {
			s := validRecoveryReservationSpec(f)
			s.Mode = WorkspaceRecoveryCheckpoint
			return NewWorkspaceRecoveryReservation(s)
		}, WorkspaceRecoveryReservation{}},
		{"start over cannot implicitly restore", func(f workspaceContractFixture) (any, error) {
			s := validRecoveryReservationSpec(f)
			s.Checkpoint = f.descriptor.Spec().Publication
			return NewWorkspaceRecoveryReservation(s)
		}, WorkspaceRecoveryReservation{}},
		{"recovery cannot change session", func(f workspaceContractFixture) (any, error) {
			s := validRecoveryReservationSpec(f)
			var err error
			s.Fence, err = NewAdmissionFence(uuid.New(), nil, 0, nil)
			if err != nil {
				return nil, err
			}
			return NewWorkspaceRecoveryReservation(s)
		}, WorkspaceRecoveryReservation{}},
		{"retention lacks policy receipt", func(f workspaceContractFixture) (any, error) {
			return NewRetentionAuthority(RetentionAuthoritySpec{Descriptor: f.descriptor, Policy: WorkspaceRetentionAge, PolicyVersion: 1})
		}, RetentionAuthority{}},
		{"cleanup has invalid expected tombstone", func(f workspaceContractFixture) (any, error) {
			s := validRegisteredCleanupSpec(f)
			s.ExpectedTombstoneVersion = -1
			return NewRegisteredSnapshotCleanupAuthority(s)
		}, RegisteredSnapshotCleanupAuthority{}},
		{"cleanup cannot infer drain from expiry", func(f workspaceContractFixture) (any, error) {
			s := validRegisteredCleanupSpec(f)
			s.WriterSettlement.Kind = "expired"
			return NewRegisteredSnapshotCleanupAuthority(s)
		}, RegisteredSnapshotCleanupAuthority{}},
		{"restore target lacks source", func(f workspaceContractFixture) (any, error) {
			s := f.target.Spec()
			s.Kind = ResumabilityRestoreTarget
			return NewResumabilityTarget(s)
		}, ResumabilityTarget{}},
		{"live target cannot hide restore", func(f workspaceContractFixture) (any, error) {
			s := f.target.Spec()
			s.Source = &f.descriptor
			return NewResumabilityTarget(s)
		}, ResumabilityTarget{}},
		{"target lacks image build", func(f workspaceContractFixture) (any, error) {
			s := f.target.Spec()
			s.ImageBuild = ""
			return NewResumabilityTarget(s)
		}, ResumabilityTarget{}},
		{"target lacks verification protocol", func(f workspaceContractFixture) (any, error) {
			s := f.target.Spec()
			s.VerificationProtocol = ""
			return NewResumabilityTarget(s)
		}, ResumabilityTarget{}},
		{"observation differs from actual target provider", func(f workspaceContractFixture) (any, error) {
			o := validResumabilityObservation(f)
			o.Target.spec.Provider = models.AgentTypeClaudeCode
			return NewResumabilityReceipt(o)
		}, ResumabilityReceipt{}},
		{"observation differs from actual target build", func(f workspaceContractFixture) (any, error) {
			o := validResumabilityObservation(f)
			o.Target.spec.PinnedBuild = "synthetic-other-build"
			return NewResumabilityReceipt(o)
		}, ResumabilityReceipt{}},
		{"observation differs from target protocol", func(f workspaceContractFixture) (any, error) {
			o := validResumabilityObservation(f)
			o.ProtocolVersion = "synthetic-other-protocol"
			return NewResumabilityReceipt(o)
		}, ResumabilityReceipt{}},
		{"verified target unavailable", func(f workspaceContractFixture) (any, error) {
			o := validResumabilityObservation(f)
			o.Target.spec.TargetAvailability.State = WorkspaceUnavailable
			return NewResumabilityReceipt(o)
		}, ResumabilityReceipt{}},
		{"verified live physical workspace unavailable", func(f workspaceContractFixture) (any, error) {
			o := validResumabilityObservation(f)
			o.Target.spec.Descriptor.spec.PhysicalAvailability.State = WorkspaceUnavailable
			return NewResumabilityReceipt(o)
		}, ResumabilityReceipt{}},
		{"observation expiry must follow observation", func(f workspaceContractFixture) (any, error) {
			o := validResumabilityObservation(f)
			o.ExpiresAt = o.ObservedAt
			return NewResumabilityReceipt(o)
		}, ResumabilityReceipt{}},
		{"observation cannot change tenant", func(f workspaceContractFixture) (any, error) {
			o := validResumabilityObservation(f)
			s := f.scope
			s.OrgID = uuid.New()
			identity, err := NewNativeConversationResumeIdentity(s, "synthetic-native-id")
			if err != nil {
				return nil, err
			}
			o.Identity = identity
			return NewResumabilityReceipt(o)
		}, ResumabilityReceipt{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := tt.build(newWorkspaceContractFixture(t))
			require.Error(t, err, "incomplete or cross-scope record must be rejected by its constructor")
			require.Equal(t, tt.zero, actual, "constructor failure must return no partial receipt or authority shape")
		})
	}
}

func validPendingUploadSpec(f workspaceContractFixture) PendingUploadReceiptSpec {
	return PendingUploadReceiptSpec{OrgID: f.scope.OrgID, Fence: f.scope.Fence, PublicationID: uuid.New(), UploadID: uuid.New(),
		Token: uuid.New(), CleanupJobID: uuid.New(), Key: "synthetic-never-installed-key", Generation: f.scope.Generation,
		Version: 1, ExpectedDescriptor: f.descriptor, Deadline: f.now.Add(time.Minute)}
}

func validRecoveryReservationSpec(f workspaceContractFixture) WorkspaceRecoveryReservationSpec {
	return WorkspaceRecoveryReservationSpec{OrgID: f.scope.OrgID, Fence: f.sessionFence, RecoveryID: uuid.New(), Token: uuid.New(),
		PhysicalOperationID: uuid.New(), Version: 1, Descriptor: f.descriptor, Mode: WorkspaceRecoveryStartOver, AcknowledgementDigest: f.digest}
}

func validRegisteredCleanupSpec(f workspaceContractFixture) RegisteredSnapshotCleanupAuthoritySpec {
	return RegisteredSnapshotCleanupAuthoritySpec{OrgID: f.scope.OrgID, SessionID: f.scope.Fence.SessionID(),
		CleanupJobID: uuid.New(), JobLeaseToken: uuid.New(), PublicationID: uuid.New(), UploadID: uuid.New(), Token: uuid.New(),
		Key: "synthetic-orphan-key", Generation: f.scope.Generation, ControlVersion: 2, ExpectedTombstoneVersion: 1,
		WriterSettlement: SnapshotWriterSettlementReceipt{Kind: SnapshotWriterFencedDrained, WriterID: uuid.New(), OperationID: uuid.New(), ReceiptID: uuid.New()}}
}

func validResumabilityObservation(f workspaceContractFixture) ResumabilityObservation {
	return ResumabilityObservation{ID: uuid.New(), Identity: f.identity, Target: f.target,
		Disposition: ResumabilityVerified, ProtocolVersion: "synthetic-protocol-v1", ObservedAt: f.now, ExpiresAt: f.now.Add(time.Minute)}
}

func TestWorkspaceResumeIdentityVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    ResumeIdentityKind
		mutate  func(*ResumeIdentityDetails)
		wantErr bool
	}{
		{"native exact identity", ResumeNativeConversation, func(*ResumeIdentityDetails) {}, false},
		{"native lacks ID", ResumeNativeConversation, func(d *ResumeIdentityDetails) { d.NativeConversationID = "" }, true},
		{"native mixes request protocol", ResumeNativeConversation, func(d *ResumeIdentityDetails) { d.ProviderRequestID = "invented-request" }, true},
		{"native lacks pinned build", ResumeNativeConversation, func(d *ResumeIdentityDetails) { d.Scope.PinnedBuild = "" }, true},
		{"native lacks workdir", ResumeNativeConversation, func(d *ResumeIdentityDetails) { d.Scope.Workdir = "" }, true},
		{"workspace exact protocol", ResumeWorkspaceContinuation, func(*ResumeIdentityDetails) {}, false},
		{"workspace lacks tested protocol identity", ResumeWorkspaceContinuation, func(d *ResumeIdentityDetails) { d.AnswerProtocolVersion = "" }, true},
		{"workspace lacks request", ResumeWorkspaceContinuation, func(d *ResumeIdentityDetails) { d.Scope.RequestID = uuid.Nil }, true},
		{"workspace lacks checkpoint", ResumeWorkspaceContinuation, func(d *ResumeIdentityDetails) { d.Checkpoint = nil }, true},
		{"workspace lacks phase", ResumeWorkspaceContinuation, func(d *ResumeIdentityDetails) { d.ProducerPhaseID = uuid.Nil }, true},
		{"initial exact identity", ResumeInitialTurn, func(*ResumeIdentityDetails) {}, false},
		{"initial binds approval", ResumeInitialTurn, func(d *ResumeIdentityDetails) { d.Scope.RequestID = uuid.New() }, true},
		{"initial impersonates conversation", ResumeInitialTurn, func(d *ResumeIdentityDetails) { d.NativeConversationID = "old-native" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWorkspaceContractFixture(t)
			details := ResumeIdentityDetails{Scope: f.scope}
			switch tt.kind {
			case ResumeNativeConversation:
				details.NativeConversationID = "synthetic-native-id"
			case ResumeWorkspaceContinuation:
				details.ProviderRequestID, details.ProducerPhaseID = "synthetic-request", uuid.New()
				details.Checkpoint, details.AnswerProtocolVersion = f.descriptor.Spec().Publication, "synthetic-answer-v1"
			case ResumeInitialTurn:
				details.Scope.RequestID = uuid.Nil
			}
			tt.mutate(&details)
			identity, err := newResumeIdentity(tt.kind, details)
			if tt.wantErr {
				require.Error(t, err, "incomplete or mixed resume variants must fail before observation")
				require.Nil(t, identity, "invalid union payload must return no identity")
				return
			}
			require.NoError(t, err, "a complete explicit resume variant should validate its shape")
			require.Equal(t, details, identity.Details(), "resume variant must preserve the complete exact input identity")
			require.Equal(t, tt.kind, identity.Kind(), "sealed identity must expose exactly its selected variant")
		})
	}
}

func TestWorkspaceBoundIdentityRejectsInitialAndUnscopedValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		make func(*testing.T, workspaceContractFixture) ResumeIdentity
	}{
		{"nil", func(*testing.T, workspaceContractFixture) ResumeIdentity { return nil }},
		{"typed nil", func(*testing.T, workspaceContractFixture) ResumeIdentity { var i *resumeIdentity; return i }},
		{"zero variant", func(*testing.T, workspaceContractFixture) ResumeIdentity { return resumeIdentity{} }},
		{"session only", func(t *testing.T, f workspaceContractFixture) ResumeIdentity {
			fence, err := NewAdmissionFence(f.scope.Fence.SessionID(), nil, f.scope.Fence.SessionEpoch(), nil)
			require.NoError(t, err, "session-only identity is representable for ordinary unbound work")
			f.scope.Fence = fence
			identity, err := NewNativeConversationResumeIdentity(f.scope, "synthetic-native-id")
			require.NoError(t, err, "native shape alone must not claim bound-answer eligibility")
			return identity
		}},
		{"initial turn", func(t *testing.T, f workspaceContractFixture) ResumeIdentity {
			f.scope.RequestID = uuid.Nil
			identity, err := NewInitialTurnResumeIdentity(f.scope)
			require.NoError(t, err, "fresh initial turn should have its own explicit variant")
			return identity
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWorkspaceContractFixture(t)
			require.Error(t, ValidateBoundResumeIdentity(tt.make(t, f), f.scope.RequestID), "answer and denial must reject missing lane or fresh-context identity")
		})
	}
}

func TestWorkspaceRequestIdentityRemainsSeparateFromResumeIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    RequestIdentityKind
		mutate  func(*RequestIdentityDetails)
		wantErr bool
	}{
		{"provider exact request", RequestIdentityProvider, func(*RequestIdentityDetails) {}, false},
		{"provider missing native request", RequestIdentityProvider, func(d *RequestIdentityDetails) { d.ProviderRequestID = "" }, true},
		{"provider mixes capability", RequestIdentityProvider, func(d *RequestIdentityDetails) { d.CapabilityItemID = "item" }, true},
		{"capability exact item", RequestIdentityCapability, func(*RequestIdentityDetails) {}, false},
		{"capability invents provider request", RequestIdentityCapability, func(d *RequestIdentityDetails) { d.ProviderRequestID = "invented" }, true},
		{"capability missing phase", RequestIdentityCapability, func(d *RequestIdentityDetails) { d.ProducerPhaseID = uuid.Nil }, true},
		{"capability missing runtime", RequestIdentityCapability, func(d *RequestIdentityDetails) { d.ProducerRuntimeID = uuid.Nil }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWorkspaceContractFixture(t)
			a := f.anchor.Spec()
			details := RequestIdentityDetails{RequestID: f.scope.RequestID, RequestKind: models.HumanInputRequestKindToolApproval}
			if tt.kind == RequestIdentityProvider {
				details.ProviderRequestID = "synthetic-native-question"
			} else {
				details.CapabilityItemID, details.ProducerPhaseID, details.ProducerRuntimeID = "synthetic-capability-item", a.PhaseID, a.ProducerRuntimeID
			}
			tt.mutate(&details)
			identity, err := newRequestIdentity(tt.kind, details)
			if tt.wantErr {
				require.Error(t, err, "request variant must reject fabricated or missing native/capability fields")
				require.Nil(t, identity, "invalid request identity must not escape as a variant")
				return
			}
			require.NoError(t, err, "exact provider or capability request should validate independently of resume mode")
			a.Request = identity
			anchor, err := NewInputWorkspaceAnchor(a)
			require.NoError(t, err, "matching request and producer identities should form an immutable anchor")
			require.Equal(t, a, anchor.Spec(), "request identity must remain independent of its native conversation")
		})
	}
}

func TestWorkspaceCertificateTransitionRetainsImmutableAnchor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*WorkspaceCertificateTransition)
		wantErr bool
	}{
		{"key only transition", func(*WorkspaceCertificateTransition) {}, false},
		{"wrong operation receipt", func(c *WorkspaceCertificateTransition) { c.Operation.ID = uuid.New() }, true},
		{"zero classification version", func(c *WorkspaceCertificateTransition) { c.ClassificationVersion = 0 }, true},
		{"wrong previous tenant", func(c *WorkspaceCertificateTransition) { c.Previous.spec.OrgID = uuid.New() }, true},
		{"wrong latest session", func(c *WorkspaceCertificateTransition) { c.Latest.spec.SessionID = uuid.New() }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWorkspaceContractFixture(t)
			latestSpec := f.descriptor.Spec()
			latestSpec.Publication.Key, latestSpec.LastOperation.ID = "synthetic-sibling-key", uuid.New()
			latest, err := NewWorkspaceDescriptor(latestSpec)
			require.NoError(t, err, "sibling key-only change should remain a valid exact descriptor")
			transition := WorkspaceCertificateTransition{Anchor: f.anchor, ClassificationVersion: 1,
				Previous: f.descriptor, Latest: latest, Operation: latestSpec.LastOperation}
			tt.mutate(&transition)
			if tt.wantErr {
				require.Error(t, transition.Validate(), "transition must bind exact scoped descriptors and operation receipt")
				return
			}
			require.NoError(t, transition.Validate(), "certificate shape must allow authorized key-only transitions without a revision increment")
			require.Equal(t, f.anchor.Spec(), transition.Anchor.Spec(), "B publication must never replace A immutable pause/provider/producer identity")
			require.Equal(t, f.descriptor.Spec().Revision, latest.Spec().Revision, "key-only transition should not invent a numeric revision rule")
		})
	}
}

func TestWorkspaceResumabilityReceiptBindsExactIdentityTargetAndExpiry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*ResumeIdentityScope, *ResumeIdentityDetails, *ResumabilityTargetSpec, *time.Time)
		match  bool
	}{
		{"exact observation", func(*ResumeIdentityScope, *ResumeIdentityDetails, *ResumabilityTargetSpec, *time.Time) {}, true},
		{"provider build", func(s *ResumeIdentityScope, _ *ResumeIdentityDetails, _ *ResumabilityTargetSpec, _ *time.Time) {
			s.PinnedBuild = "synthetic-other-build"
		}, false},
		{"provider", func(s *ResumeIdentityScope, _ *ResumeIdentityDetails, _ *ResumabilityTargetSpec, _ *time.Time) {
			s.Provider = models.AgentTypeClaudeCode
		}, false},
		{"request", func(s *ResumeIdentityScope, _ *ResumeIdentityDetails, _ *ResumabilityTargetSpec, _ *time.Time) {
			s.RequestID = uuid.New()
		}, false},
		{"workdir", func(s *ResumeIdentityScope, _ *ResumeIdentityDetails, _ *ResumabilityTargetSpec, _ *time.Time) {
			s.Workdir = "synthetic-other-scope"
		}, false},
		{"native ID", func(_ *ResumeIdentityScope, d *ResumeIdentityDetails, _ *ResumabilityTargetSpec, _ *time.Time) {
			d.NativeConversationID = "synthetic-other-native"
		}, false},
		{"actual target", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, s *ResumabilityTargetSpec, _ *time.Time) {
			s.TargetID = "synthetic-other-target"
		}, false},
		{"target provider", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, s *ResumabilityTargetSpec, _ *time.Time) {
			s.Provider = models.AgentTypeClaudeCode
		}, false},
		{"target build", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, s *ResumabilityTargetSpec, _ *time.Time) {
			s.PinnedBuild = "synthetic-other-build"
		}, false},
		{"target image", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, s *ResumabilityTargetSpec, _ *time.Time) {
			s.ImageBuild = "synthetic-other-image"
		}, false},
		{"target protocol", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, s *ResumabilityTargetSpec, _ *time.Time) {
			s.VerificationProtocol = "synthetic-other-protocol"
		}, false},
		{"preparation", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, s *ResumabilityTargetSpec, _ *time.Time) {
			s.PreparationID = uuid.New()
		}, false},
		{"preparation token", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, s *ResumabilityTargetSpec, _ *time.Time) {
			s.PreparationToken = uuid.New()
		}, false},
		{"target availability version", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, s *ResumabilityTargetSpec, _ *time.Time) {
			s.TargetAvailability.Version++
		}, false},
		{"current descriptor", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, s *ResumabilityTargetSpec, _ *time.Time) {
			s.Descriptor.spec.Revision++
		}, false},
		{"exact expiry", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, _ *ResumabilityTargetSpec, at *time.Time) {
			*at = at.Add(time.Minute)
		}, false},
		{"before observation", func(_ *ResumeIdentityScope, _ *ResumeIdentityDetails, _ *ResumabilityTargetSpec, at *time.Time) {
			*at = at.Add(-time.Second)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWorkspaceContractFixture(t)
			receipt, err := NewResumabilityReceipt(validResumabilityObservation(f))
			require.NoError(t, err, "complete observation should form only a receipt shape, not a start permit")
			details, targetSpec, at := f.identity.Details(), f.target.Spec(), f.now
			scope := details.Scope
			tt.mutate(&scope, &details, &targetSpec, &at)
			details.Scope = scope
			identity, err := newResumeIdentity(ResumeNativeConversation, details)
			require.NoError(t, err, "different exact identity remains a structurally valid comparison target")
			target, err := NewResumabilityTarget(targetSpec)
			require.NoError(t, err, "different exact target remains a structurally valid comparison target")
			require.Equal(t, tt.match, receipt.MatchesExact(identity, target, at), "observation must not transfer across target, preparation, build, native context or expiry")
		})
	}
}

func TestWorkspaceContractsDefensivelyCopyMutableInputs(t *testing.T) {
	t.Parallel()
	f := newWorkspaceContractFixture(t)
	original := f.descriptor.Spec()
	spec := f.descriptor.Spec()
	descriptor, err := NewWorkspaceDescriptor(spec)
	require.NoError(t, err, "descriptor should copy nested publication provenance at construction")
	spec.Publication.Key, spec.Publication.Checkpoint.PhaseID = "mutated-caller-key", uuid.New()
	exposed := descriptor.Spec()
	exposed.Publication.Checkpoint.RuntimeID = uuid.New()
	require.Equal(t, original, descriptor.Spec(), "neither constructor inputs nor accessor copies may mutate retained descriptor identity")

	checkpoint := f.descriptor.Spec().Publication
	identity, err := NewWorkspaceContinuationResumeIdentity(f.scope, "synthetic-provider-request", uuid.New(), *checkpoint, "answer-v1")
	require.NoError(t, err, "workspace continuation should capture the exact checkpoint by value")
	expectedIdentity := identity.Details()
	checkpoint.Checkpoint.PhaseID = uuid.New()
	identity.Details().Checkpoint.Key = "mutated-accessor-key"
	require.Equal(t, expectedIdentity, identity.Details(), "sealed continuation must copy nested checkpoint input and output")

	reservationSpec := validRecoveryReservationSpec(f)
	reservationSpec.Mode, reservationSpec.Checkpoint = WorkspaceRecoveryCheckpoint, f.descriptor.Spec().Publication
	reservation, err := NewWorkspaceRecoveryReservation(reservationSpec)
	require.NoError(t, err, "checkpoint reservation must retain immutable consent/source identity")
	expectedReservation := reservation.Spec()
	reservationSpec.Checkpoint.Key = "mutated-recovery-source"
	reservation.Spec().Checkpoint.Checkpoint.Revision++
	require.Equal(t, expectedReservation, reservation.Spec(), "recovery source must not drift through mutable caller or accessor pointers")

	source := f.descriptor
	targetSpec := f.target.Spec()
	targetSpec.Kind, targetSpec.Source = ResumabilityRestoreTarget, &source
	target, err := NewResumabilityTarget(targetSpec)
	require.NoError(t, err, "prepared restore should capture its exact source descriptor")
	expectedTarget := target.Spec()
	source = WorkspaceDescriptor{}
	*target.Spec().Source = WorkspaceDescriptor{}
	require.Equal(t, expectedTarget, target.Spec(), "target source pointer must not remain aliased to caller or accessor memory")
}

func TestWorkspaceAuthorityRecordsRemainDistinctAndPassive(t *testing.T) {
	t.Parallel()
	f := newWorkspaceContractFixture(t)
	uploadSpec := validPendingUploadSpec(f)
	upload, err := NewPendingUploadReceipt(uploadSpec)
	require.NoError(t, err, "upload registration should carry exact expected descriptor and pre-Save cleanup responsibility")
	require.Equal(t, uploadSpec, upload.Spec(), "pending upload must preserve every exact registration field")
	retentionSpec := RetentionAuthoritySpec{Descriptor: f.descriptor, Policy: WorkspaceRetentionAge, PolicyVersion: 1, PolicyReceiptID: uuid.New()}
	retention, err := NewRetentionAuthority(retentionSpec)
	require.NoError(t, err, "current-reference retention record should validate its explicit policy shape")
	require.Equal(t, retentionSpec, retention.Spec(), "retention record must preserve current descriptor and policy identity")
	cleanupSpec := validRegisteredCleanupSpec(f)
	cleanupSpec.ExpectedTombstoneVersion = 0
	cleanup, err := NewRegisteredSnapshotCleanupAuthority(cleanupSpec)
	require.NoError(t, err, "the first tombstone must be representable before an installed tombstone exists")
	require.Equal(t, cleanupSpec, cleanup.Spec(), "registered cleanup must retain the exact no-tombstone comparison target")
	require.NotEqual(t, any(retention), any(cleanup), "resource-job cleanup and current-reference retention must remain distinct concrete inputs")
	reservationSpec := validRecoveryReservationSpec(f)
	reservation, err := NewWorkspaceRecoveryReservation(reservationSpec)
	require.NoError(t, err, "recovery reservation should retain exclusion without an expiry-based release")
	require.Equal(t, reservationSpec, reservation.Spec(), "reservation must retain recovery token and exact consent descriptor until a later durable transition")
}

func TestWorkspaceZeroRecordsAreInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value interface{ Validate() error }
	}{
		{"descriptor", WorkspaceDescriptor{}}, {"availability", WorkspaceAvailability{}},
		{"publication", WorkspacePublication{}}, {"operation", WorkspaceOperationReceipt{}},
		{"upload", PendingUploadReceipt{}}, {"anchor", InputWorkspaceAnchor{}},
		{"certificate transition", WorkspaceCertificateTransition{}}, {"recovery reservation", WorkspaceRecoveryReservation{}},
		{"retention", RetentionAuthority{}}, {"registered cleanup", RegisteredSnapshotCleanupAuthority{}},
		{"writer settlement", SnapshotWriterSettlementReceipt{}}, {"target", ResumabilityTarget{}},
		{"resumability receipt", ResumabilityReceipt{}}, {"resume variant", resumeIdentity{}},
		{"request variant", requestIdentity{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, tt.value.Validate(), "zero contract record must never stand in for valid evidence or permission")
		})
	}
}

func TestWorkspaceContinuationAnchorRejectsDifferentQuestionOrPhase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*InputWorkspaceAnchorSpec, *ResumeIdentityDetails)
		wantErr bool
	}{
		{"exact question and phase", func(*InputWorkspaceAnchorSpec, *ResumeIdentityDetails) {}, false},
		{"different question", func(_ *InputWorkspaceAnchorSpec, d *ResumeIdentityDetails) {
			d.ProviderRequestID = "synthetic-other-question"
		}, true},
		{"different phase", func(_ *InputWorkspaceAnchorSpec, d *ResumeIdentityDetails) { d.ProducerPhaseID = uuid.New() }, true},
		{"different checkpoint publication", func(_ *InputWorkspaceAnchorSpec, d *ResumeIdentityDetails) { d.Checkpoint.ID = uuid.New() }, true},
		{"different checkpoint key", func(_ *InputWorkspaceAnchorSpec, d *ResumeIdentityDetails) {
			d.Checkpoint.Key = "synthetic-other-checkpoint"
		}, true},
		{"different checkpoint receipt", func(_ *InputWorkspaceAnchorSpec, d *ResumeIdentityDetails) { d.Checkpoint.ReceiptID = uuid.New() }, true},
		{"missing paused checkpoint", func(s *InputWorkspaceAnchorSpec, _ *ResumeIdentityDetails) {
			s.PausedDescriptor.spec.Publication = nil
			s.PausedDescriptor.spec.SnapshotAvailability.State = WorkspaceUnavailable
		}, true},
		{"different generation", func(s *InputWorkspaceAnchorSpec, _ *ResumeIdentityDetails) { s.PausedDescriptor.spec.Generation++ }, true},
		{"zero request kind", func(s *InputWorkspaceAnchorSpec, _ *ResumeIdentityDetails) {
			r := s.Request.(requestIdentity)
			r.details.RequestKind = ""
			s.Request = r
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWorkspaceContractFixture(t)
			spec := f.anchor.Spec()
			details := ResumeIdentityDetails{Scope: f.scope, ProviderRequestID: spec.Request.Details().ProviderRequestID,
				ProducerPhaseID: spec.PhaseID, Checkpoint: f.descriptor.Spec().Publication, AnswerProtocolVersion: "answer-v1"}
			tt.mutate(&spec, &details)
			identity, err := newResumeIdentity(ResumeWorkspaceContinuation, details)
			require.NoError(t, err, "comparison identity can be well formed while belonging to another request or phase")
			spec.Identity = identity
			anchor, err := NewInputWorkspaceAnchor(spec)
			if tt.wantErr {
				require.Error(t, err, "anchor must reject contradictory request, phase, generation or kind identity")
				require.Equal(t, InputWorkspaceAnchor{}, anchor, "contradictory immutable evidence must not form an anchor")
				return
			}
			require.NoError(t, err, "exact continuation question and producer phase should compose with its immutable anchor")
			require.Equal(t, spec, anchor.Spec(), "anchor must retain every exact provider question and continuation field")
		})
	}
}

func TestWorkspaceRestoreReceiptBindsExactSourceAvailability(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*WorkspaceDescriptorSpec)
	}{
		{"source key", func(s *WorkspaceDescriptorSpec) { s.Publication.Key = "synthetic-other-source" }},
		{"source availability version", func(s *WorkspaceDescriptorSpec) { s.SnapshotAvailability.Version++ }},
		{"source publication receipt", func(s *WorkspaceDescriptorSpec) { s.Publication.ReceiptID = uuid.New() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWorkspaceContractFixture(t)
			source := f.descriptor
			targetSpec := f.target.Spec()
			targetSpec.Kind, targetSpec.Source = ResumabilityRestoreTarget, &source
			target, err := NewResumabilityTarget(targetSpec)
			require.NoError(t, err, "prepared restore must retain the full exact available source descriptor")
			observation := validResumabilityObservation(f)
			observation.Target = target
			receipt, err := NewResumabilityReceipt(observation)
			require.NoError(t, err, "selected source is part of the exact observation receipt")
			require.True(t, receipt.MatchesExact(f.identity, target, f.now), "unchanged source and target should match the observation interval")
			changedSourceSpec := source.Spec()
			tt.mutate(&changedSourceSpec)
			changedSource, err := NewWorkspaceDescriptor(changedSourceSpec)
			require.NoError(t, err, "source drift may be structurally valid while invalidating an older observation")
			targetSpec.Source = &changedSource
			changedTarget, err := NewResumabilityTarget(targetSpec)
			require.NoError(t, err, "comparison target must preserve an explicit selected restore source")
			require.False(t, receipt.MatchesExact(f.identity, changedTarget, f.now), "source key/availability/receipt drift must invalidate an exact-target observation")
		})
	}
}

func TestWorkspaceCapabilityRequestRejectsWorkspaceContinuation(t *testing.T) {
	t.Parallel()
	f := newWorkspaceContractFixture(t)
	spec := f.anchor.Spec()
	request, err := NewCapabilityRequestIdentity(f.scope.RequestID, models.HumanInputRequestKindToolApproval,
		"synthetic-capability-item", spec.PhaseID, spec.ProducerRuntimeID)
	require.NoError(t, err, "capability request must retain its real item/phase/runtime without a provider request ID")
	identity, err := NewWorkspaceContinuationResumeIdentity(f.scope, "fabricated-provider-request", spec.PhaseID,
		*f.descriptor.Spec().Publication, "synthetic-answer-v1")
	require.NoError(t, err, "a continuation variant can have complete shape while being incompatible with capability request identity")
	spec.Request, spec.Identity = request, identity
	anchor, err := NewInputWorkspaceAnchor(spec)
	require.ErrorContains(t, err, "capability request", "capability request cannot borrow a freeform provider request ID for workspace continuation")
	require.Equal(t, InputWorkspaceAnchor{}, anchor, "incompatible request/resume variants must return no immutable anchor")
}

func TestWorkspaceContinuationRestoreReceiptRequiresSelectedCheckpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*ResumabilityObservation)
		wantErr bool
	}{
		{"exact usable source", func(*ResumabilityObservation) {}, false},
		{"different source publication", func(o *ResumabilityObservation) { o.Target.spec.Source.spec.Publication.ID = uuid.New() }, true},
		{"different source key", func(o *ResumabilityObservation) {
			o.Target.spec.Source.spec.Publication.Key = "synthetic-other-checkpoint"
		}, true},
		{"different source receipt", func(o *ResumabilityObservation) { o.Target.spec.Source.spec.Publication.ReceiptID = uuid.New() }, true},
		{"verified source unavailable", func(o *ResumabilityObservation) {
			o.Target.spec.Source.spec.SnapshotAvailability.State = WorkspaceUnavailable
		}, true},
		{"unavailable source observation", func(o *ResumabilityObservation) {
			o.Target.spec.Source.spec.SnapshotAvailability.State = WorkspaceUnavailable
			o.Disposition = ResumabilityUnavailable
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWorkspaceContractFixture(t)
			identity, err := NewWorkspaceContinuationResumeIdentity(f.scope, "synthetic-native-request", f.anchor.Spec().PhaseID,
				*f.descriptor.Spec().Publication, "synthetic-answer-v1")
			require.NoError(t, err, "continuation fixture must bind its exact checkpoint publication")
			targetSpec := f.target.Spec()
			source := f.descriptor
			targetSpec.Kind, targetSpec.Source = ResumabilityRestoreTarget, &source
			target, err := NewResumabilityTarget(targetSpec)
			require.NoError(t, err, "restore target fixture must retain an explicit selected checkpoint source")
			observation := validResumabilityObservation(f)
			observation.Identity, observation.Target = identity, target
			tt.mutate(&observation)
			receipt, err := NewResumabilityReceipt(observation)
			if tt.wantErr {
				require.Error(t, err, "continuation receipt must reject contradictory checkpoint identity or verified unavailable source")
				require.Equal(t, ResumabilityReceipt{}, receipt, "contradictory source evidence must not form a receipt")
				return
			}
			require.NoError(t, err, "receipt shape should retain an exact source with its explicit observation outcome")
			require.Equal(t, observation, receipt.Observation(), "shape validation must preserve the complete source identity and outcome")
		})
	}
}
