package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type SessionStreamEventType string

const (
	SessionStreamEventThreadInboxQueued          SessionStreamEventType = "thread.inbox.queued"
	SessionStreamEventThreadInboxCleared         SessionStreamEventType = "thread.inbox.cleared"
	SessionStreamEventThreadRuntimeUpdated       SessionStreamEventType = "thread.runtime.updated"
	SessionStreamEventWorkspaceGenerationChanged SessionStreamEventType = "session.workspace.generation_changed"
	SessionStreamEventActivityPhaseStarted       SessionStreamEventType = "session_activity_phase.started"
	SessionStreamEventActivityPhaseTerminal      SessionStreamEventType = "session_activity_phase.terminal"
	SessionStreamEventInboxDeliveryAcknowledged  SessionStreamEventType = "thread_inbox_delivery.acknowledged"
	SessionStreamEventInboxDeliveryStarted       SessionStreamEventType = "thread_inbox_delivery.started"
	SessionStreamEventInboxDeliveryAbandoned     SessionStreamEventType = "thread_inbox_delivery.abandoned"
)

func (t SessionStreamEventType) Validate() error {
	switch t {
	case SessionStreamEventThreadInboxQueued,
		SessionStreamEventThreadInboxCleared,
		SessionStreamEventThreadRuntimeUpdated,
		SessionStreamEventWorkspaceGenerationChanged,
		SessionStreamEventActivityPhaseStarted,
		SessionStreamEventActivityPhaseTerminal,
		SessionStreamEventInboxDeliveryAcknowledged,
		SessionStreamEventInboxDeliveryStarted,
		SessionStreamEventInboxDeliveryAbandoned:
		return nil
	default:
		return fmt.Errorf("invalid SessionStreamEventType: %q", t)
	}
}

type SessionStreamEvent struct {
	ID        uuid.UUID              `json:"id"`
	Type      SessionStreamEventType `json:"type"`
	SessionID uuid.UUID              `json:"session_id"`
	OrgID     uuid.UUID              `json:"org_id"`
	ThreadID  uuid.UUID              `json:"thread_id,omitempty"`
	EmittedAt time.Time              `json:"emitted_at"`
	Data      any                    `json:"data"`
}

type ActivityPhaseEvent struct {
	Phase SessionActivityPhase `json:"phase"`
}

type ThreadInboxDeliveryBatchEvent struct {
	Batch ThreadInboxDeliveryBatch `json:"batch"`
}

type ThreadInboxEvent struct {
	SessionID           uuid.UUID `json:"session_id"`
	ThreadID            uuid.UUID `json:"thread_id"`
	OrgID               uuid.UUID `json:"org_id"`
	PendingMessageCount int       `json:"pending_message_count"`
}

type ThreadRuntimeEvent struct {
	SessionID           uuid.UUID    `json:"session_id"`
	ThreadID            uuid.UUID    `json:"thread_id"`
	OrgID               uuid.UUID    `json:"org_id"`
	Status              ThreadStatus `json:"status"`
	AgentSessionID      *string      `json:"agent_session_id,omitempty"`
	CurrentTurn         int          `json:"current_turn"`
	PendingMessageCount int          `json:"pending_message_count"`
	LastActivityAt      *time.Time   `json:"last_activity_at,omitempty"`
	StartedAt           *time.Time   `json:"started_at,omitempty"`
	CompletedAt         *time.Time   `json:"completed_at,omitempty"`
	FailureExplanation  *string      `json:"failure_explanation"`
	FailureCategory     *string      `json:"failure_category"`
}

type SessionWorkspaceGenerationChangedEvent struct {
	SessionID                  uuid.UUID `json:"session_id"`
	OrgID                      uuid.UUID `json:"org_id"`
	WorkspaceRevision          int64     `json:"workspace_revision"`
	WorkspaceRevisionUpdatedAt time.Time `json:"workspace_revision_updated_at"`
	Reason                     string    `json:"reason,omitempty"`
}

func NewThreadInboxEvent(thread SessionThread) ThreadInboxEvent {
	return ThreadInboxEvent{
		SessionID:           thread.SessionID,
		ThreadID:            thread.ID,
		OrgID:               thread.OrgID,
		PendingMessageCount: thread.PendingMessageCount,
	}
}

func NewThreadRuntimeEvent(thread SessionThread) ThreadRuntimeEvent {
	return ThreadRuntimeEvent{
		SessionID:           thread.SessionID,
		ThreadID:            thread.ID,
		OrgID:               thread.OrgID,
		Status:              thread.Status,
		AgentSessionID:      thread.AgentSessionID,
		CurrentTurn:         thread.CurrentTurn,
		PendingMessageCount: thread.PendingMessageCount,
		LastActivityAt:      thread.LastActivityAt,
		StartedAt:           thread.StartedAt,
		CompletedAt:         thread.CompletedAt,
		FailureExplanation:  thread.FailureExplanation,
		FailureCategory:     thread.FailureCategory,
	}
}
