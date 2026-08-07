package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type ActivityPhaseStatus string

const (
	ActivityPhaseStatusRunning     ActivityPhaseStatus = "running"
	ActivityPhaseStatusCompleted   ActivityPhaseStatus = "completed"
	ActivityPhaseStatusFailed      ActivityPhaseStatus = "failed"
	ActivityPhaseStatusCancelled   ActivityPhaseStatus = "cancelled"
	ActivityPhaseStatusInterrupted ActivityPhaseStatus = "interrupted"
)

func (s ActivityPhaseStatus) Validate() error {
	switch s {
	case ActivityPhaseStatusRunning, ActivityPhaseStatusCompleted, ActivityPhaseStatusFailed,
		ActivityPhaseStatusCancelled, ActivityPhaseStatusInterrupted:
		return nil
	default:
		return fmt.Errorf("invalid ActivityPhaseStatus: %q", s)
	}
}

type ActivityPhaseBoundaryReason string

const (
	ActivityPhaseBoundaryFinalResponse     ActivityPhaseBoundaryReason = "final_response"
	ActivityPhaseBoundaryHumanInput        ActivityPhaseBoundaryReason = "human_input"
	ActivityPhaseBoundaryApproval          ActivityPhaseBoundaryReason = "approval"
	ActivityPhaseBoundaryPlanApproval      ActivityPhaseBoundaryReason = "plan_approval"
	ActivityPhaseBoundarySteered           ActivityPhaseBoundaryReason = "steered"
	ActivityPhaseBoundaryMaintenance       ActivityPhaseBoundaryReason = "maintenance"
	ActivityPhaseBoundaryRuntimeLost       ActivityPhaseBoundaryReason = "runtime_lost"
	ActivityPhaseBoundaryCapacitySuspended ActivityPhaseBoundaryReason = "capacity_suspended"
	ActivityPhaseBoundaryInterrupted       ActivityPhaseBoundaryReason = "interrupted"
	ActivityPhaseBoundaryStopped           ActivityPhaseBoundaryReason = "stopped"
	ActivityPhaseBoundaryCancelled         ActivityPhaseBoundaryReason = "cancelled"
	ActivityPhaseBoundaryError             ActivityPhaseBoundaryReason = "error"
)

func (r ActivityPhaseBoundaryReason) Validate() error {
	switch r {
	case ActivityPhaseBoundaryFinalResponse, ActivityPhaseBoundaryHumanInput,
		ActivityPhaseBoundaryApproval, ActivityPhaseBoundaryPlanApproval,
		ActivityPhaseBoundarySteered, ActivityPhaseBoundaryMaintenance,
		ActivityPhaseBoundaryRuntimeLost, ActivityPhaseBoundaryCapacitySuspended,
		ActivityPhaseBoundaryInterrupted, ActivityPhaseBoundaryStopped,
		ActivityPhaseBoundaryCancelled, ActivityPhaseBoundaryError:
		return nil
	default:
		return fmt.Errorf("invalid ActivityPhaseBoundaryReason: %q", r)
	}
}

func (r *ActivityPhaseBoundaryReason) ScanText(value pgtype.Text) error {
	if !value.Valid {
		*r = ""
		return nil
	}
	*r = ActivityPhaseBoundaryReason(value.String)
	return nil
}

type ActivityPhaseTriggerKind string

const (
	ActivityPhaseTriggerInitial    ActivityPhaseTriggerKind = "initial"
	ActivityPhaseTriggerInboxBatch ActivityPhaseTriggerKind = "inbox_batch"
	ActivityPhaseTriggerRecovery   ActivityPhaseTriggerKind = "recovery"
)

func (k ActivityPhaseTriggerKind) Validate() error {
	switch k {
	case ActivityPhaseTriggerInitial, ActivityPhaseTriggerInboxBatch, ActivityPhaseTriggerRecovery:
		return nil
	default:
		return fmt.Errorf("invalid ActivityPhaseTriggerKind: %q", k)
	}
}

type InboxDeliveryBatchStatus string

const (
	InboxDeliveryBatchAcknowledged InboxDeliveryBatchStatus = "acknowledged"
	InboxDeliveryBatchStarted      InboxDeliveryBatchStatus = "started"
	InboxDeliveryBatchAbandoned    InboxDeliveryBatchStatus = "abandoned"
)

func (s InboxDeliveryBatchStatus) Validate() error {
	switch s {
	case InboxDeliveryBatchAcknowledged, InboxDeliveryBatchStarted, InboxDeliveryBatchAbandoned:
		return nil
	default:
		return fmt.Errorf("invalid InboxDeliveryBatchStatus: %q", s)
	}
}

type ActivityPhaseTrigger struct {
	Kind    ActivityPhaseTriggerKind
	BatchID *uuid.UUID
}

// ActivityPhaseWriteGuard proves that a transcript write still belongs to the
// runtime lease that opened its phase. It is write-only process metadata: the
// lease token must never be serialized into transcript or API responses.
type ActivityPhaseWriteGuard struct {
	RuntimeID  uuid.UUID `json:"-"`
	LeaseToken uuid.UUID `json:"-"`
}

type SessionActivityPhase struct {
	ID                   uuid.UUID                   `db:"id" json:"id"`
	OrgID                uuid.UUID                   `db:"org_id" json:"org_id"`
	SessionID            uuid.UUID                   `db:"session_id" json:"session_id"`
	ThreadID             uuid.UUID                   `db:"thread_id" json:"thread_id"`
	TurnNumber           int                         `db:"turn_number" json:"turn_number"`
	PhaseNumber          int                         `db:"phase_number" json:"phase_number"`
	Status               ActivityPhaseStatus         `db:"status" json:"status"`
	BoundaryReason       ActivityPhaseBoundaryReason `db:"boundary_reason" json:"boundary_reason,omitempty"`
	StartedAt            time.Time                   `db:"started_at" json:"started_at"`
	CompletedAt          *time.Time                  `db:"completed_at" json:"completed_at,omitempty"`
	RuntimeID            *uuid.UUID                  `db:"runtime_id" json:"runtime_id,omitempty"`
	TriggerKind          ActivityPhaseTriggerKind    `db:"trigger_kind" json:"trigger_kind"`
	TriggerBatchID       *uuid.UUID                  `db:"trigger_batch_id" json:"trigger_batch_id,omitempty"`
	TriggerSequenceStart *int64                      `db:"trigger_sequence_start" json:"trigger_sequence_start,omitempty"`
	TriggerSequenceEnd   *int64                      `db:"trigger_sequence_end" json:"trigger_sequence_end,omitempty"`
	CreatedAt            time.Time                   `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time                   `db:"updated_at" json:"updated_at"`
}

type ThreadInboxDeliveryBatch struct {
	ID             uuid.UUID                `db:"id" json:"id"`
	OrgID          uuid.UUID                `db:"org_id" json:"org_id"`
	SessionID      uuid.UUID                `db:"session_id" json:"session_id"`
	ThreadID       uuid.UUID                `db:"thread_id" json:"thread_id"`
	RuntimeID      uuid.UUID                `db:"runtime_id" json:"runtime_id"`
	SequenceStart  int64                    `db:"sequence_start" json:"sequence_start"`
	SequenceEnd    int64                    `db:"sequence_end" json:"sequence_end"`
	Status         InboxDeliveryBatchStatus `db:"status" json:"status"`
	AcknowledgedAt time.Time                `db:"acknowledged_at" json:"acknowledged_at"`
	StartedAt      *time.Time               `db:"started_at" json:"started_at,omitempty"`
	AbandonedAt    *time.Time               `db:"abandoned_at" json:"abandoned_at,omitempty"`
	CreatedAt      time.Time                `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time                `db:"updated_at" json:"updated_at"`
}
