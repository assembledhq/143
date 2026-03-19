package models

import "fmt"

// SessionStatus captures the lifecycle of an agent run.
type SessionStatus string

const (
	SessionStatusPending             SessionStatus = "pending"
	SessionStatusRunning             SessionStatus = "running"
	SessionStatusIdle                SessionStatus = "idle"
	SessionStatusAwaitingInput       SessionStatus = "awaiting_input"
	SessionStatusNeedsHumanGuidance  SessionStatus = "needs_human_guidance"
	SessionStatusCompleted           SessionStatus = "completed"
	SessionStatusPRCreated           SessionStatus = "pr_created"
	SessionStatusFailed              SessionStatus = "failed"
	SessionStatusCancelled           SessionStatus = "cancelled"
	SessionStatusSkipped             SessionStatus = "skipped"
)

func (s SessionStatus) Validate() error {
	switch s {
	case SessionStatusPending,
		SessionStatusRunning,
		SessionStatusIdle,
		SessionStatusAwaitingInput,
		SessionStatusNeedsHumanGuidance,
		SessionStatusCompleted,
		SessionStatusPRCreated,
		SessionStatusFailed,
		SessionStatusCancelled,
		SessionStatusSkipped:
		return nil
	default:
		return fmt.Errorf("invalid SessionStatus: %q", s)
	}
}

// Status groups used by the frontend filter tabs. Defined here so the backend
// is the source of truth; the frontend arrays must stay in sync.
var (
	// NeedsAttentionStatuses are statuses where user action is required.
	NeedsAttentionStatuses = []SessionStatus{SessionStatusAwaitingInput, SessionStatusNeedsHumanGuidance, SessionStatusFailed}
	// WorkingStatuses are statuses where the agent is actively executing.
	WorkingStatuses = []SessionStatus{SessionStatusPending, SessionStatusRunning}
	// DoneStatuses are terminal or parked statuses.
	DoneStatuses = []SessionStatus{SessionStatusCompleted, SessionStatusPRCreated, SessionStatusCancelled, SessionStatusSkipped, SessionStatusIdle}
)

// SandboxState tracks the lifecycle of a session's sandbox.
type SandboxState string

const (
	SandboxStateNone        SandboxState = "none"
	SandboxStateRunning     SandboxState = "running"
	SandboxStateSnapshotted SandboxState = "snapshotted"
	SandboxStateDestroyed   SandboxState = "destroyed"
)

func (s SandboxState) Validate() error {
	switch s {
	case SandboxStateNone, SandboxStateRunning, SandboxStateSnapshotted, SandboxStateDestroyed:
		return nil
	default:
		return fmt.Errorf("invalid SandboxState: %q", s)
	}
}

// MessageRole identifies who sent a session message.
type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

func (r MessageRole) Validate() error {
	switch r {
	case MessageRoleUser, MessageRoleAssistant:
		return nil
	default:
		return fmt.Errorf("invalid MessageRole: %q", r)
	}
}
