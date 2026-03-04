package models

import "fmt"

// AgentRunStatus captures the lifecycle of an agent run.
type AgentRunStatus string

const (
	AgentRunStatusPending             AgentRunStatus = "pending"
	AgentRunStatusRunning             AgentRunStatus = "running"
	AgentRunStatusAwaitingInput       AgentRunStatus = "awaiting_input"
	AgentRunStatusNeedsHumanGuidance  AgentRunStatus = "needs_human_guidance"
	AgentRunStatusCompleted           AgentRunStatus = "completed"
	AgentRunStatusPRCreated           AgentRunStatus = "pr_created"
	AgentRunStatusFailed              AgentRunStatus = "failed"
	AgentRunStatusCancelled           AgentRunStatus = "cancelled"
	AgentRunStatusSkipped             AgentRunStatus = "skipped"
)

func (s AgentRunStatus) Validate() error {
	switch s {
	case AgentRunStatusPending,
		AgentRunStatusRunning,
		AgentRunStatusAwaitingInput,
		AgentRunStatusNeedsHumanGuidance,
		AgentRunStatusCompleted,
		AgentRunStatusPRCreated,
		AgentRunStatusFailed,
		AgentRunStatusCancelled,
		AgentRunStatusSkipped:
		return nil
	default:
		return fmt.Errorf("invalid AgentRunStatus: %q", s)
	}
}
