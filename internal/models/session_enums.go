package models

import "fmt"

// SessionStatus captures the lifecycle of an agent run.
type SessionStatus string

const (
	SessionStatusPending            SessionStatus = "pending"
	SessionStatusRunning            SessionStatus = "running"
	SessionStatusIdle               SessionStatus = "idle"
	SessionStatusAwaitingInput      SessionStatus = "awaiting_input"
	SessionStatusNeedsHumanGuidance SessionStatus = "needs_human_guidance"
	SessionStatusCompleted          SessionStatus = "completed"
	SessionStatusPRCreated          SessionStatus = "pr_created"
	SessionStatusFailed             SessionStatus = "failed"
	SessionStatusCancelled          SessionStatus = "cancelled"
	SessionStatusSkipped            SessionStatus = "skipped"
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
	// ActiveStatuses are sessions that are in-progress or need attention.
	ActiveStatuses = []SessionStatus{SessionStatusPending, SessionStatusRunning, SessionStatusIdle, SessionStatusAwaitingInput, SessionStatusNeedsHumanGuidance}
	// DoneStatuses are terminal statuses.
	DoneStatuses = []SessionStatus{SessionStatusCompleted, SessionStatusPRCreated, SessionStatusFailed, SessionStatusCancelled, SessionStatusSkipped}
)

// PRCreationState captures whether the user has initiated PR creation for a
// session and, if so, where that async push is in its lifecycle. It is
// orthogonal to SessionStatus: the agent run can be `completed` while PR
// creation is still `idle` (waiting on a user click), `pushing`, `succeeded`,
// or `failed`. Once `succeeded` the PullRequest row is the source of truth.
type PRCreationState string

const (
	PRCreationStateIdle      PRCreationState = "idle"
	PRCreationStateQueued    PRCreationState = "queued"
	PRCreationStatePushing   PRCreationState = "pushing"
	PRCreationStateSucceeded PRCreationState = "succeeded"
	PRCreationStateFailed    PRCreationState = "failed"
)

func (s PRCreationState) Validate() error {
	switch s {
	case PRCreationStateIdle,
		PRCreationStateQueued,
		PRCreationStatePushing,
		PRCreationStateSucceeded,
		PRCreationStateFailed:
		return nil
	default:
		return fmt.Errorf("invalid PRCreationState: %q", s)
	}
}

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

// ThreadStatus captures the lifecycle of a thread within a session.
type ThreadStatus string

const (
	ThreadStatusPending       ThreadStatus = "pending"
	ThreadStatusRunning       ThreadStatus = "running"
	ThreadStatusIdle          ThreadStatus = "idle"
	ThreadStatusAwaitingInput ThreadStatus = "awaiting_input"
	ThreadStatusCompleted     ThreadStatus = "completed"
	ThreadStatusFailed        ThreadStatus = "failed"
	ThreadStatusCancelled     ThreadStatus = "cancelled"
)

func (s ThreadStatus) Validate() error {
	switch s {
	case ThreadStatusPending,
		ThreadStatusRunning,
		ThreadStatusIdle,
		ThreadStatusAwaitingInput,
		ThreadStatusCompleted,
		ThreadStatusFailed,
		ThreadStatusCancelled:
		return nil
	default:
		return fmt.Errorf("invalid ThreadStatus: %q", s)
	}
}

// MaxThreadsPerSession is the maximum number of threads allowed in a single session.
const MaxThreadsPerSession = 4

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
