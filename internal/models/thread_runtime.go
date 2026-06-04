package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ThreadInboxEntryType string

const (
	ThreadInboxEntryTypeUserMessage      ThreadInboxEntryType = "user_message"
	ThreadInboxEntryTypeHumanInputAnswer ThreadInboxEntryType = "human_input_answer"
	ThreadInboxEntryTypeControl          ThreadInboxEntryType = "control"
)

func (t ThreadInboxEntryType) Validate() error {
	switch t {
	case ThreadInboxEntryTypeUserMessage,
		ThreadInboxEntryTypeHumanInputAnswer,
		ThreadInboxEntryTypeControl:
		return nil
	default:
		return fmt.Errorf("invalid ThreadInboxEntryType: %q", t)
	}
}

type ThreadInboxDeliveryState string

const (
	ThreadInboxDeliveryStatePending         ThreadInboxDeliveryState = "pending"
	ThreadInboxDeliveryStateDelivering      ThreadInboxDeliveryState = "delivering"
	ThreadInboxDeliveryStateDelivered       ThreadInboxDeliveryState = "delivered"
	ThreadInboxDeliveryStateUnknownDelivery ThreadInboxDeliveryState = "unknown_delivery"
	ThreadInboxDeliveryStateAcked           ThreadInboxDeliveryState = "acked"
	ThreadInboxDeliveryStateDeadLetter      ThreadInboxDeliveryState = "dead_letter"
)

func (s ThreadInboxDeliveryState) Validate() error {
	switch s {
	case ThreadInboxDeliveryStatePending,
		ThreadInboxDeliveryStateDelivering,
		ThreadInboxDeliveryStateDelivered,
		ThreadInboxDeliveryStateUnknownDelivery,
		ThreadInboxDeliveryStateAcked,
		ThreadInboxDeliveryStateDeadLetter:
		return nil
	default:
		return fmt.Errorf("invalid ThreadInboxDeliveryState: %q", s)
	}
}

type ThreadInboxSummaryState string

const (
	ThreadInboxSummaryStateIdle            ThreadInboxSummaryState = "idle"
	ThreadInboxSummaryStatePending         ThreadInboxSummaryState = "pending"
	ThreadInboxSummaryStateDelivering      ThreadInboxSummaryState = "delivering"
	ThreadInboxSummaryStateDelivered       ThreadInboxSummaryState = "delivered"
	ThreadInboxSummaryStateUnknownDelivery ThreadInboxSummaryState = "unknown_delivery"
	ThreadInboxSummaryStateAcked           ThreadInboxSummaryState = "acked"
	ThreadInboxSummaryStateDeadLetter      ThreadInboxSummaryState = "dead_letter"
)

func (s ThreadInboxSummaryState) Validate() error {
	switch s {
	case ThreadInboxSummaryStateIdle,
		ThreadInboxSummaryStatePending,
		ThreadInboxSummaryStateDelivering,
		ThreadInboxSummaryStateDelivered,
		ThreadInboxSummaryStateUnknownDelivery,
		ThreadInboxSummaryStateAcked,
		ThreadInboxSummaryStateDeadLetter:
		return nil
	default:
		return fmt.Errorf("invalid ThreadInboxSummaryState: %q", s)
	}
}

type ThreadInboxDeliverySummary struct {
	ThreadID             uuid.UUID               `json:"thread_id"`
	State                ThreadInboxSummaryState `json:"state"`
	PendingCount         int                     `json:"pending_count"`
	DeliveringCount      int                     `json:"delivering_count"`
	DeliveredCount       int                     `json:"delivered_count"`
	UnknownDeliveryCount int                     `json:"unknown_delivery_count"`
	AckedCount           int                     `json:"acked_count"`
	DeadLetterCount      int                     `json:"dead_letter_count"`
	LastSequenceNo       int64                   `json:"last_sequence_no"`
	LastAcceptedAt       *time.Time              `json:"last_accepted_at,omitempty"`
	LastDeliveredAt      *time.Time              `json:"last_delivered_at,omitempty"`
	LastAckedAt          *time.Time              `json:"last_acked_at,omitempty"`
	LastError            *string                 `json:"last_error,omitempty"`
}

type SendThreadMessageResponse struct {
	Message       SessionMessage           `json:"message"`
	InboxEntry    *ThreadInboxEntry        `json:"inbox_entry,omitempty"`
	ThreadStatus  ThreadStatus             `json:"thread_status"`
	DeliveryState ThreadInboxDeliveryState `json:"delivery_state"`
}

func (s *ThreadInboxDeliverySummary) Normalize() {
	if s == nil {
		return
	}
	switch {
	case s.DeadLetterCount > 0:
		s.State = ThreadInboxSummaryStateDeadLetter
	case s.UnknownDeliveryCount > 0:
		s.State = ThreadInboxSummaryStateUnknownDelivery
	case s.PendingCount > 0:
		s.State = ThreadInboxSummaryStatePending
	case s.DeliveringCount > 0:
		s.State = ThreadInboxSummaryStateDelivering
	case s.DeliveredCount > 0:
		s.State = ThreadInboxSummaryStateDelivered
	case s.AckedCount > 0:
		s.State = ThreadInboxSummaryStateAcked
	default:
		s.State = ThreadInboxSummaryStateIdle
	}
}

type ThreadRuntimeStatus string

const (
	ThreadRuntimeStatusStarting ThreadRuntimeStatus = "starting"
	ThreadRuntimeStatusLive     ThreadRuntimeStatus = "live"
	ThreadRuntimeStatusPaused   ThreadRuntimeStatus = "paused"
	ThreadRuntimeStatusDraining ThreadRuntimeStatus = "draining"
	ThreadRuntimeStatusLost     ThreadRuntimeStatus = "lost"
	ThreadRuntimeStatusClosed   ThreadRuntimeStatus = "closed"
	ThreadRuntimeStatusFailed   ThreadRuntimeStatus = "failed"
)

func (s ThreadRuntimeStatus) Validate() error {
	switch s {
	case ThreadRuntimeStatusStarting,
		ThreadRuntimeStatusLive,
		ThreadRuntimeStatusPaused,
		ThreadRuntimeStatusDraining,
		ThreadRuntimeStatusLost,
		ThreadRuntimeStatusClosed,
		ThreadRuntimeStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid ThreadRuntimeStatus: %q", s)
	}
}

type SessionSandboxHolderKind string

const (
	SessionSandboxHolderKindThreadRuntime SessionSandboxHolderKind = "thread_runtime"
	SessionSandboxHolderKindPreview       SessionSandboxHolderKind = "preview"
	SessionSandboxHolderKindSnapshot      SessionSandboxHolderKind = "snapshot"
	SessionSandboxHolderKindOperator      SessionSandboxHolderKind = "operator"
)

func (k SessionSandboxHolderKind) Validate() error {
	switch k {
	case SessionSandboxHolderKindThreadRuntime,
		SessionSandboxHolderKindPreview,
		SessionSandboxHolderKindSnapshot,
		SessionSandboxHolderKindOperator:
		return nil
	default:
		return fmt.Errorf("invalid SessionSandboxHolderKind: %q", k)
	}
}

type SessionSandboxHolderStatus string

const (
	SessionSandboxHolderStatusActive   SessionSandboxHolderStatus = "active"
	SessionSandboxHolderStatusDraining SessionSandboxHolderStatus = "draining"
	SessionSandboxHolderStatusReleased SessionSandboxHolderStatus = "released"
	SessionSandboxHolderStatusExpired  SessionSandboxHolderStatus = "expired"
)

func (s SessionSandboxHolderStatus) Validate() error {
	switch s {
	case SessionSandboxHolderStatusActive,
		SessionSandboxHolderStatusDraining,
		SessionSandboxHolderStatusReleased,
		SessionSandboxHolderStatusExpired:
		return nil
	default:
		return fmt.Errorf("invalid SessionSandboxHolderStatus: %q", s)
	}
}

type ThreadInboxEntry struct {
	ID               uuid.UUID                `db:"id" json:"id"`
	OrgID            uuid.UUID                `db:"org_id" json:"org_id"`
	SessionID        uuid.UUID                `db:"session_id" json:"session_id"`
	ThreadID         uuid.UUID                `db:"thread_id" json:"thread_id"`
	SequenceNo       int64                    `db:"sequence_no" json:"sequence_no"`
	MessageID        int64                    `db:"message_id" json:"message_id"`
	ClientMessageID  *string                  `db:"client_message_id" json:"client_message_id,omitempty"`
	EntryType        ThreadInboxEntryType     `db:"entry_type" json:"entry_type"`
	Payload          json.RawMessage          `db:"payload" json:"payload"`
	DeliveryState    ThreadInboxDeliveryState `db:"delivery_state" json:"delivery_state"`
	DeliveryAttempts int                      `db:"delivery_attempts" json:"delivery_attempts"`
	LastError        *string                  `db:"last_error" json:"last_error,omitempty"`
	OwnerNodeID      *string                  `db:"owner_node_id" json:"owner_node_id,omitempty"`
	RuntimeID        *uuid.UUID               `db:"runtime_id" json:"runtime_id,omitempty"`
	AcceptedAt       time.Time                `db:"accepted_at" json:"accepted_at"`
	DeliveredAt      *time.Time               `db:"delivered_at" json:"delivered_at,omitempty"`
	AckedAt          *time.Time               `db:"acked_at" json:"acked_at,omitempty"`
	CreatedAt        time.Time                `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time                `db:"updated_at" json:"updated_at"`
}

type ThreadRuntime struct {
	ID                         uuid.UUID           `db:"id" json:"id"`
	OrgID                      uuid.UUID           `db:"org_id" json:"org_id"`
	SessionID                  uuid.UUID           `db:"session_id" json:"session_id"`
	ThreadID                   uuid.UUID           `db:"thread_id" json:"thread_id"`
	SandboxID                  uuid.UUID           `db:"sandbox_id" json:"sandbox_id"`
	ContainerID                string              `db:"container_id" json:"container_id"`
	RuntimeHandleID            string              `db:"runtime_handle_id" json:"runtime_handle_id"`
	AgentType                  AgentType           `db:"agent_type" json:"agent_type"`
	Model                      *string             `db:"model" json:"model,omitempty"`
	Status                     ThreadRuntimeStatus `db:"status" json:"status"`
	OwnerNodeID                string              `db:"owner_node_id" json:"owner_node_id"`
	LeaseToken                 uuid.UUID           `db:"lease_token" json:"lease_token"`
	LastDeliveredSequence      int64               `db:"last_delivered_sequence" json:"last_delivered_sequence"`
	LastAckedSequence          int64               `db:"last_acked_sequence" json:"last_acked_sequence"`
	BaseWorkspaceGeneration    int64               `db:"base_workspace_generation" json:"base_workspace_generation"`
	CurrentWorkspaceGeneration int64               `db:"current_workspace_generation" json:"current_workspace_generation"`
	StartedAt                  time.Time           `db:"started_at" json:"started_at"`
	HeartbeatAt                *time.Time          `db:"heartbeat_at" json:"heartbeat_at,omitempty"`
	LeaseExpiresAt             *time.Time          `db:"lease_expires_at" json:"lease_expires_at,omitempty"`
	ClosedAt                   *time.Time          `db:"closed_at" json:"closed_at,omitempty"`
	StopReason                 *string             `db:"stop_reason" json:"stop_reason,omitempty"`
	LastError                  *string             `db:"last_error" json:"last_error,omitempty"`
	CreatedAt                  time.Time           `db:"created_at" json:"created_at"`
	UpdatedAt                  time.Time           `db:"updated_at" json:"updated_at"`
}

type SessionSandboxHolder struct {
	ID          uuid.UUID                  `db:"id" json:"id"`
	OrgID       uuid.UUID                  `db:"org_id" json:"org_id"`
	SessionID   uuid.UUID                  `db:"session_id" json:"session_id"`
	ContainerID string                     `db:"container_id" json:"container_id"`
	HolderKind  SessionSandboxHolderKind   `db:"holder_kind" json:"holder_kind"`
	HolderID    uuid.UUID                  `db:"holder_id" json:"holder_id"`
	OwnerNodeID string                     `db:"owner_node_id" json:"owner_node_id"`
	LeaseToken  uuid.UUID                  `db:"lease_token" json:"lease_token"`
	Status      SessionSandboxHolderStatus `db:"status" json:"status"`
	HeartbeatAt time.Time                  `db:"heartbeat_at" json:"heartbeat_at"`
	ExpiresAt   time.Time                  `db:"expires_at" json:"expires_at"`
	CreatedAt   time.Time                  `db:"created_at" json:"created_at"`
	ReleasedAt  *time.Time                 `db:"released_at" json:"released_at,omitempty"`
	UpdatedAt   time.Time                  `db:"updated_at" json:"updated_at"`
}
