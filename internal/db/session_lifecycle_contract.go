package db

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// LifecycleDigest is an exact SHA-256 identity, not a dedupe key or permission.
type LifecycleDigest [32]byte

func NewLifecycleDigest(value []byte) (LifecycleDigest, error) {
	if len(value) != 32 {
		return LifecycleDigest{}, errors.New("lifecycle digest must contain exactly 32 bytes")
	}
	var digest LifecycleDigest
	copy(digest[:], value)
	if err := digest.Validate(); err != nil {
		return LifecycleDigest{}, err
	}
	return digest, nil
}

func (d LifecycleDigest) Validate() error {
	if d == (LifecycleDigest{}) {
		return errors.New("lifecycle digest must not be zero")
	}
	return nil
}

// Encode returns the one persisted representation: 64 lowercase hexadecimal
// characters. Zero is not a valid persisted digest.
func (d LifecycleDigest) Encode() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	return hex.EncodeToString(d[:]), nil
}

func ParseLifecycleDigest(value string) (LifecycleDigest, error) {
	if len(value) != 64 || strings.ToLower(value) != value {
		return LifecycleDigest{}, errors.New("lifecycle digest requires 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return LifecycleDigest{}, errors.New("invalid lifecycle digest hexadecimal encoding")
	}
	return NewLifecycleDigest(decoded)
}

// AdmissionFence carries explicit expected scope, never the scope discovered
// from a current/primary lane. Shape validation is not durable authorization.
// Private value fields prevent a caller from changing a retained fence through
// a pointer after a command or receipt has been constructed.
type AdmissionFence struct {
	sessionID    uuid.UUID
	threadID     uuid.UUID
	sessionEpoch int64
	threadEpoch  int64
	threaded     bool
}

func NewAdmissionFence(sessionID uuid.UUID, threadID *uuid.UUID, sessionEpoch int64, threadEpoch *int64) (AdmissionFence, error) {
	fence := AdmissionFence{sessionID: sessionID, sessionEpoch: sessionEpoch}
	if (threadID == nil) != (threadEpoch == nil) {
		return AdmissionFence{}, errors.New("lifecycle lane and lane epoch must be supplied together")
	}
	if threadID != nil {
		fence.threadID, fence.threadEpoch, fence.threaded = *threadID, *threadEpoch, true
	}
	if err := fence.Validate(); err != nil {
		return AdmissionFence{}, err
	}
	return fence, nil
}

func (f AdmissionFence) Validate() error {
	if f.sessionID == uuid.Nil || f.sessionEpoch < 0 {
		return errors.New("lifecycle fence requires an exact session and nonnegative epoch")
	}
	if f.threaded {
		if f.threadID == uuid.Nil || f.threadEpoch < 0 {
			return errors.New("lifecycle fence requires an exact lane and nonnegative lane epoch")
		}
	} else if f.threadID != uuid.Nil || f.threadEpoch != 0 {
		return errors.New("session-wide lifecycle fence cannot contain lane metadata")
	}
	return nil
}

func (f AdmissionFence) SessionID() uuid.UUID        { return f.sessionID }
func (f AdmissionFence) SessionEpoch() int64         { return f.sessionEpoch }
func (f AdmissionFence) ThreadID() (uuid.UUID, bool) { return f.threadID, f.threaded }
func (f AdmissionFence) ThreadEpoch() (int64, bool)  { return f.threadEpoch, f.threaded }

// LifecycleIngress identifies the exact command. A digest detects conflicting
// reuse of this identity; content equality alone never creates a replay key.
type LifecycleIngress struct {
	kind, key string
	digest    LifecycleDigest
}

func NewLifecycleIngress(kind, key string, digest LifecycleDigest) (LifecycleIngress, error) {
	value := LifecycleIngress{kind: kind, key: key, digest: digest}
	if err := value.Validate(); err != nil {
		return LifecycleIngress{}, err
	}
	return value, nil
}

func (v LifecycleIngress) Validate() error {
	if strings.TrimSpace(v.kind) == "" || strings.TrimSpace(v.key) == "" {
		return errors.New("lifecycle ingress requires a nonempty kind and exact key")
	}
	return v.digest.Validate()
}

func (v LifecycleIngress) Kind() string            { return v.kind }
func (v LifecycleIngress) Key() string             { return v.key }
func (v LifecycleIngress) Digest() LifecycleDigest { return v.digest }

// LifecycleJobBinding is the immutable input set of one job. Constructing one
// does not bind durable inputs: the mutation must validate every scoped row and
// prove it has no other consumer under the canonical locks. A dedupe collision
// (including Enqueue's nil-ID result) cannot supply this evidence.
type LifecycleJobBinding struct {
	orgID       uuid.UUID
	fence       AdmissionFence
	jobID       uuid.UUID
	dispatchIDs []uuid.UUID
	kind        models.LifecycleDispatchKind
	requestID   uuid.UUID
}

func NewLifecycleJobBinding(orgID uuid.UUID, fence AdmissionFence, jobID uuid.UUID, dispatchIDs []uuid.UUID, kind models.LifecycleDispatchKind, requestID *uuid.UUID) (LifecycleJobBinding, error) {
	v := LifecycleJobBinding{orgID: orgID, fence: fence, jobID: jobID, dispatchIDs: slices.Clone(dispatchIDs), kind: kind}
	if requestID != nil {
		if *requestID == uuid.Nil {
			return LifecycleJobBinding{}, errors.New("lifecycle binding cannot infer a NULL request from a zero ID")
		}
		v.requestID = *requestID
	}
	if err := v.Validate(); err != nil {
		return LifecycleJobBinding{}, err
	}
	return v, nil
}

func (v LifecycleJobBinding) Validate() error {
	if err := v.fence.Validate(); err != nil {
		return err
	}
	if err := v.kind.Validate(); err != nil {
		return err
	}
	if v.orgID == uuid.Nil || v.jobID == uuid.Nil || len(v.dispatchIDs) == 0 {
		return errors.New("lifecycle binding requires an exact tenant/job and nonempty input set")
	}
	seen := make(map[uuid.UUID]struct{}, len(v.dispatchIDs))
	for _, id := range v.dispatchIDs {
		if id == uuid.Nil {
			return errors.New("lifecycle binding requires nonzero exact dispatch IDs")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("lifecycle binding cannot contain a dispatch twice")
		}
		seen[id] = struct{}{}
	}
	requestIntent := v.kind == models.LifecycleDispatchAnswer || v.kind == models.LifecycleDispatchDenial
	if requestIntent != (v.requestID != uuid.Nil) {
		return errors.New("only an answer or denial binding requires its exact request")
	}
	if v.kind != models.LifecycleDispatchMessage && len(v.dispatchIDs) != 1 {
		return errors.New("only ordinary messages may share a newly allocated binding")
	}
	return nil
}

func (v LifecycleJobBinding) Fence() AdmissionFence              { return v.fence }
func (v LifecycleJobBinding) OrgID() uuid.UUID                   { return v.orgID }
func (v LifecycleJobBinding) JobID() uuid.UUID                   { return v.jobID }
func (v LifecycleJobBinding) DispatchIDs() []uuid.UUID           { return slices.Clone(v.dispatchIDs) }
func (v LifecycleJobBinding) Kind() models.LifecycleDispatchKind { return v.kind }
func (v LifecycleJobBinding) RequestID() (uuid.UUID, bool) {
	return v.requestID, v.requestID != uuid.Nil
}

// AttemptFence retains immutable allocation evidence, independently of a mutable
// current lease. Historical settlement and current execution use distinct sealed
// authority variants below; neither can substitute for the other's durable checks.
type AttemptFence struct {
	binding                           LifecycleJobBinding
	jobToken, runtimeID, runtimeToken uuid.UUID
	allocationDigest                  LifecycleDigest
	expectedTurn                      int
}

func NewAttemptFence(binding LifecycleJobBinding, jobToken, runtimeID, runtimeToken uuid.UUID, allocationDigest LifecycleDigest, expectedTurn int) (AttemptFence, error) {
	v := AttemptFence{binding: binding, jobToken: jobToken, runtimeID: runtimeID, runtimeToken: runtimeToken, allocationDigest: allocationDigest, expectedTurn: expectedTurn}
	if err := v.Validate(); err != nil {
		return AttemptFence{}, err
	}
	// Retain no caller-owned slice, even if the input came from a future package
	// helper rather than the validating binding constructor.
	v.binding.dispatchIDs = slices.Clone(binding.dispatchIDs)
	return v, nil
}

func (v AttemptFence) Validate() error {
	if err := v.binding.Validate(); err != nil {
		return err
	}
	if v.jobToken == uuid.Nil || v.runtimeID == uuid.Nil || v.runtimeToken == uuid.Nil || v.expectedTurn < 0 || v.expectedTurn > math.MaxInt32 {
		return errors.New("attempt fence requires exact job/runtime allocation tokens and a PostgreSQL integer turn")
	}
	return v.allocationDigest.Validate()
}

func (v AttemptFence) Binding() LifecycleJobBinding {
	result := v.binding
	result.dispatchIDs = slices.Clone(result.dispatchIDs)
	return result
}
func (v AttemptFence) JobToken() uuid.UUID               { return v.jobToken }
func (v AttemptFence) RuntimeID() uuid.UUID              { return v.runtimeID }
func (v AttemptFence) RuntimeToken() uuid.UUID           { return v.runtimeToken }
func (v AttemptFence) AllocationDigest() LifecycleDigest { return v.allocationDigest }
func (v AttemptFence) ExpectedTurn() int                 { return v.expectedTurn }

// LifecycleConsumerBinding describes one logical consumer. A runtime may be
// added to its originating job only by changing the canonical encoding from
// job-only to runtime-with-job-and-allocation. Both pointers name ONE consumer.
// This contract deliberately has no replace/rebind method. Authorized later
// reclaim must retain prior allocation and possible-acceptance evidence.
type LifecycleConsumerBinding struct {
	orgID, dispatchID uuid.UUID
	kind              models.LifecycleConsumerKind
	jobID             uuid.UUID
	runtimeID         uuid.UUID
	allocation        *AttemptFence
}

func NewLifecycleConsumerBinding(orgID, dispatchID uuid.UUID, kind models.LifecycleConsumerKind, jobID, runtimeID *uuid.UUID, allocation *AttemptFence) (LifecycleConsumerBinding, error) {
	v := LifecycleConsumerBinding{orgID: orgID, dispatchID: dispatchID, kind: kind}
	if jobID != nil {
		if *jobID == uuid.Nil {
			return LifecycleConsumerBinding{}, errors.New("consumer job ID must be explicit and nonzero")
		}
		v.jobID = *jobID
	}
	if runtimeID != nil {
		if *runtimeID == uuid.Nil {
			return LifecycleConsumerBinding{}, errors.New("consumer runtime ID must be explicit and nonzero")
		}
		v.runtimeID = *runtimeID
	}
	if allocation != nil {
		copied := *allocation
		copied.binding = allocation.Binding()
		v.allocation = &copied
	}
	if err := v.Validate(); err != nil {
		return LifecycleConsumerBinding{}, err
	}
	return v, nil
}

func (v LifecycleConsumerBinding) Validate() error {
	if v.orgID == uuid.Nil || v.dispatchID == uuid.Nil {
		return errors.New("consumer binding requires an exact tenant and dispatch")
	}
	if err := v.kind.Validate(); err != nil {
		return err
	}
	switch v.kind {
	case models.LifecycleConsumerNone:
		if v.jobID != uuid.Nil || v.runtimeID != uuid.Nil || v.allocation != nil {
			return errors.New("unbound input cannot contain consumer or allocation identities")
		}
	case models.LifecycleConsumerJob:
		if v.jobID == uuid.Nil || v.runtimeID != uuid.Nil || v.allocation != nil {
			return errors.New("job consumer requires only its exact job before allocation")
		}
	case models.LifecycleConsumerRuntime:
		if v.runtimeID == uuid.Nil || v.jobID == uuid.Nil || v.allocation == nil {
			return errors.New("runtime consumer requires its exact job runtime and allocation")
		}
	}
	if v.runtimeID != uuid.Nil && v.allocation == nil {
		return errors.New("runtime consumer requires immutable allocation evidence")
	}
	if v.allocation != nil {
		if err := v.allocation.Validate(); err != nil {
			return err
		}
		_, threaded := v.allocation.binding.fence.ThreadID()
		if !threaded || v.orgID != v.allocation.binding.orgID || !slices.Contains(v.allocation.binding.dispatchIDs, v.dispatchID) {
			return errors.New("runtime allocation must contain this exact tenant/lane input")
		}
		if v.runtimeID != v.allocation.runtimeID || v.jobID != v.allocation.binding.jobID {
			return errors.New("consumer pointers must name the same immutable job/runtime allocation")
		}
	}
	return nil
}

func (v LifecycleConsumerBinding) Kind() models.LifecycleConsumerKind { return v.kind }
func (v LifecycleConsumerBinding) OrgID() uuid.UUID                   { return v.orgID }
func (v LifecycleConsumerBinding) DispatchID() uuid.UUID              { return v.dispatchID }
func (v LifecycleConsumerBinding) JobID() (uuid.UUID, bool)           { return v.jobID, v.jobID != uuid.Nil }
func (v LifecycleConsumerBinding) RuntimeID() (uuid.UUID, bool) {
	return v.runtimeID, v.runtimeID != uuid.Nil
}

// RuntimeAuthority and HistoricalAttemptAuthority are intentionally incompatible
// inputs. Constructors check shape only. Every consuming mutation must re-read
// allocation provenance; current execution additionally needs live lease, owner,
// epochs and claim checks. Neither value is a transferable permission object.
type RuntimeAuthority interface {
	lifecycleRuntimeAuthority()
}

type HistoricalAttemptAuthority interface {
	lifecycleHistoricalAttemptAuthority()
}

type runtimeAuthority struct {
	orgID   uuid.UUID
	attempt AttemptFence
}
type historicalAttemptAuthority struct {
	orgID   uuid.UUID
	attempt AttemptFence
}

func (runtimeAuthority) lifecycleRuntimeAuthority()                     {}
func (historicalAttemptAuthority) lifecycleHistoricalAttemptAuthority() {}

func NewRuntimeAuthority(orgID uuid.UUID, attempt AttemptFence) (RuntimeAuthority, error) {
	if err := validateLifecycleAttemptAuthority(orgID, attempt); err != nil {
		return nil, err
	}
	return runtimeAuthority{orgID: orgID, attempt: attempt}, nil
}

func NewHistoricalAttemptAuthority(orgID uuid.UUID, attempt AttemptFence) (HistoricalAttemptAuthority, error) {
	if err := validateLifecycleAttemptAuthority(orgID, attempt); err != nil {
		return nil, err
	}
	return historicalAttemptAuthority{orgID: orgID, attempt: attempt}, nil
}

func validateLifecycleAttemptAuthority(orgID uuid.UUID, attempt AttemptFence) error {
	if orgID == uuid.Nil || orgID != attempt.binding.orgID {
		return errors.New("attempt authority requires its allocation's exact organization")
	}
	return attempt.Validate()
}

// AdmissionAuthority names the exact committed claim receipt for pre-allocation
// compensation. The session/lane claim pair and job outcome must still be checked
// durably. It cannot settle provider output or release a successor's claim.
type AdmissionAuthority struct {
	orgID, dispatchID, claimToken uuid.UUID
	fence                         AdmissionFence
	ingress                       LifecycleIngress
	turn                          int
}

func NewAdmissionAuthority(orgID uuid.UUID, fence AdmissionFence, dispatchID, claimToken uuid.UUID, turn int, ingress LifecycleIngress) (AdmissionAuthority, error) {
	v := AdmissionAuthority{orgID: orgID, fence: fence, dispatchID: dispatchID, claimToken: claimToken, turn: turn, ingress: ingress}
	if err := v.Validate(); err != nil {
		return AdmissionAuthority{}, err
	}
	return v, nil
}

func (v AdmissionAuthority) Validate() error {
	if v.orgID == uuid.Nil || v.dispatchID == uuid.Nil || v.claimToken == uuid.Nil || v.turn < 0 || v.turn > math.MaxInt32 {
		return errors.New("admission authority requires exact tenant dispatch claim token and PostgreSQL integer turn")
	}
	if err := v.fence.Validate(); err != nil {
		return err
	}
	return v.ingress.Validate()
}

// ProjectionDisposition is used only after successful own-receipt settlement.
// Stale shared state never means successful admission/start/input consumption.
type ProjectionDisposition string

const (
	ProjectionApplied          ProjectionDisposition = "applied"
	ProjectionAlreadyApplied   ProjectionDisposition = "already_applied"
	ProjectionStaleSharedState ProjectionDisposition = "stale_shared_state"
)

func (v ProjectionDisposition) Validate() error {
	switch v {
	case ProjectionApplied, ProjectionAlreadyApplied, ProjectionStaleSharedState:
		return nil
	default:
		return fmt.Errorf("invalid ProjectionDisposition: %q", v)
	}
}
