package models

import "fmt"

// SessionStatus captures the lifecycle of an agent run.
type SessionStatus string

const (
	SessionStatusPending             SessionStatus = "pending"
	SessionStatusRunning             SessionStatus = "running"
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
