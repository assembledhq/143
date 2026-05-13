package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ThreadInboxEntryType string

const (
	ThreadInboxEntryTypeUserMessage   ThreadInboxEntryType = "user_message"
	ThreadInboxEntryTypeSystemControl ThreadInboxEntryType = "system_control"
	ThreadInboxEntryTypeToolReply     ThreadInboxEntryType = "tool_reply"
)

func (t ThreadInboxEntryType) Validate() error {
	switch t {
	case ThreadInboxEntryTypeUserMessage, ThreadInboxEntryTypeSystemControl, ThreadInboxEntryTypeToolReply:
		return nil
	default:
		return fmt.Errorf("invalid ThreadInboxEntryType: %q", t)
	}
}

type ThreadInboxDeliveryState string

const (
	ThreadInboxDeliveryStatePending    ThreadInboxDeliveryState = "pending"
	ThreadInboxDeliveryStateDelivered  ThreadInboxDeliveryState = "delivered"
	ThreadInboxDeliveryStateAcked      ThreadInboxDeliveryState = "acked"
	ThreadInboxDeliveryStateDeadLetter ThreadInboxDeliveryState = "dead_letter"
)

func (s ThreadInboxDeliveryState) Validate() error {
	switch s {
	case ThreadInboxDeliveryStatePending, ThreadInboxDeliveryStateDelivered, ThreadInboxDeliveryStateAcked, ThreadInboxDeliveryStateDeadLetter:
		return nil
	default:
		return fmt.Errorf("invalid ThreadInboxDeliveryState: %q", s)
	}
}

type ThreadRuntimeStatus string

const (
	ThreadRuntimeStatusStarting ThreadRuntimeStatus = "starting"
	ThreadRuntimeStatusLive     ThreadRuntimeStatus = "live"
	ThreadRuntimeStatusDraining ThreadRuntimeStatus = "draining"
	ThreadRuntimeStatusLost     ThreadRuntimeStatus = "lost"
	ThreadRuntimeStatusClosed   ThreadRuntimeStatus = "closed"
)

func (s ThreadRuntimeStatus) Validate() error {
	switch s {
	case ThreadRuntimeStatusStarting, ThreadRuntimeStatusLive, ThreadRuntimeStatusDraining, ThreadRuntimeStatusLost, ThreadRuntimeStatusClosed:
		return nil
	default:
		return fmt.Errorf("invalid ThreadRuntimeStatus: %q", s)
	}
}

type ThreadInboxEntry struct {
	ID               uuid.UUID                `db:"id" json:"id"`
	OrgID            uuid.UUID                `db:"org_id" json:"org_id"`
	SessionID        uuid.UUID                `db:"session_id" json:"session_id"`
	ThreadID         uuid.UUID                `db:"thread_id" json:"thread_id"`
	SequenceNo       int64                    `db:"sequence_no" json:"sequence_no"`
	MessageID        int64                    `db:"message_id" json:"message_id"`
	EntryType        ThreadInboxEntryType     `db:"entry_type" json:"entry_type"`
	DeliveryState    ThreadInboxDeliveryState `db:"delivery_state" json:"delivery_state"`
	AcceptedAt       time.Time                `db:"accepted_at" json:"accepted_at"`
	DeliveredAt      *time.Time               `db:"delivered_at" json:"delivered_at,omitempty"`
	AckedAt          *time.Time               `db:"acked_at" json:"acked_at,omitempty"`
	OwnerNodeID      *string                  `db:"owner_node_id" json:"owner_node_id,omitempty"`
	DeliveryAttempts int                      `db:"delivery_attempts" json:"delivery_attempts"`
	LastError        *string                  `db:"last_error" json:"last_error,omitempty"`
}

type ThreadRuntime struct {
	ThreadID              uuid.UUID           `db:"thread_id" json:"thread_id"`
	OrgID                 uuid.UUID           `db:"org_id" json:"org_id"`
	SessionID             uuid.UUID           `db:"session_id" json:"session_id"`
	RuntimeID             string              `db:"runtime_id" json:"runtime_id"`
	OwnerNodeID           string              `db:"owner_node_id" json:"owner_node_id"`
	LeaseToken            uuid.UUID           `db:"lease_token" json:"lease_token"`
	LeaseExpiresAt        time.Time           `db:"lease_expires_at" json:"lease_expires_at"`
	Status                ThreadRuntimeStatus `db:"status" json:"status"`
	SandboxID             *string             `db:"sandbox_id" json:"sandbox_id,omitempty"`
	AgentType             AgentType           `db:"agent_type" json:"agent_type"`
	Model                 *string             `db:"model" json:"model,omitempty"`
	LastDeliveredSequence int64               `db:"last_delivered_sequence" json:"last_delivered_sequence"`
	LastAckedSequence     int64               `db:"last_acked_sequence" json:"last_acked_sequence"`
	LastHeartbeatAt       time.Time           `db:"last_heartbeat_at" json:"last_heartbeat_at"`
	StartedAt             time.Time           `db:"started_at" json:"started_at"`
	ClosedAt              *time.Time          `db:"closed_at" json:"closed_at,omitempty"`
}
