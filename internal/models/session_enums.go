package models

import "fmt"

// AgentSessionType distinguishes plan-driven sessions from ad-hoc runs.
type AgentSessionType string

const (
	AgentSessionTypePlan   AgentSessionType = "plan"
	AgentSessionTypeManual AgentSessionType = "manual"
)

func (s AgentSessionType) Validate() error {
	switch s {
	case AgentSessionTypePlan, AgentSessionTypeManual:
		return nil
	default:
		return fmt.Errorf("invalid AgentSessionType: %q", s)
	}
}

// AgentSessionStatus is the computed lifecycle state of a session.
type AgentSessionStatus string

const (
	AgentSessionStatusActive    AgentSessionStatus = "active"
	AgentSessionStatusCompleted AgentSessionStatus = "completed"
	AgentSessionStatusFailed    AgentSessionStatus = "failed"
)

func (s AgentSessionStatus) Validate() error {
	switch s {
	case AgentSessionStatusActive, AgentSessionStatusCompleted, AgentSessionStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid AgentSessionStatus: %q", s)
	}
}

// AgentSessionTriggeredBy indicates how the session was initiated.
type AgentSessionTriggeredBy string

const (
	AgentSessionTriggeredByScheduled AgentSessionTriggeredBy = "scheduled"
	AgentSessionTriggeredByManual    AgentSessionTriggeredBy = "manual"
	AgentSessionTriggeredByFixThis   AgentSessionTriggeredBy = "fix_this"
)

func (s AgentSessionTriggeredBy) Validate() error {
	switch s {
	case AgentSessionTriggeredByScheduled, AgentSessionTriggeredByManual, AgentSessionTriggeredByFixThis:
		return nil
	default:
		return fmt.Errorf("invalid AgentSessionTriggeredBy: %q", s)
	}
}
