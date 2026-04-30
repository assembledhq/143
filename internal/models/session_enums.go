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

// SessionOrigin captures how a session was created. This is provenance only;
// runtime behavior is controlled by explicit policy fields.
type SessionOrigin string

const (
	SessionOriginIssueTrigger SessionOrigin = "issue_trigger"
	SessionOriginManual       SessionOrigin = "manual"
	SessionOriginProject      SessionOrigin = "project"
	SessionOriginAutomation   SessionOrigin = "automation"
	SessionOriginRevision     SessionOrigin = "revision"
)

func (o SessionOrigin) Validate() error {
	switch o {
	case SessionOriginIssueTrigger,
		SessionOriginManual,
		SessionOriginProject,
		SessionOriginAutomation,
		SessionOriginRevision:
		return nil
	default:
		return fmt.Errorf("invalid SessionOrigin: %q", o)
	}
}

// SessionInteractionMode captures whether the session is interactive across
// turns or expected to finish in one execution pass.
type SessionInteractionMode string

const (
	SessionInteractionModeInteractive SessionInteractionMode = "interactive"
	SessionInteractionModeSingleRun   SessionInteractionMode = "single_run"
)

func (m SessionInteractionMode) Validate() error {
	switch m {
	case SessionInteractionModeInteractive, SessionInteractionModeSingleRun:
		return nil
	default:
		return fmt.Errorf("invalid SessionInteractionMode: %q", m)
	}
}

// SessionValidationPolicy captures when validation should run for a session.
type SessionValidationPolicy string

const (
	SessionValidationPolicyOnTurnComplete SessionValidationPolicy = "on_turn_complete"
	SessionValidationPolicyOnSessionEnd   SessionValidationPolicy = "on_session_end"
	SessionValidationPolicySkip           SessionValidationPolicy = "skip"
)

func (p SessionValidationPolicy) Validate() error {
	switch p {
	case SessionValidationPolicyOnTurnComplete,
		SessionValidationPolicyOnSessionEnd,
		SessionValidationPolicySkip:
		return nil
	default:
		return fmt.Errorf("invalid SessionValidationPolicy: %q", p)
	}
}

// LinearPrepareState gates turn 1 of a session against pre-start Linear
// resolution. "none" means there is no Linear primary to wait for. "pending"
// holds the session in a recoverable preparing state. "ready" lets the
// orchestrator start the agent. "failed" means we could not fetch the
// promised Linear context — the user can retry; we never start blind.
type LinearPrepareState string

const (
	LinearPrepareStateNone    LinearPrepareState = "none"
	LinearPrepareStatePending LinearPrepareState = "pending"
	LinearPrepareStateReady   LinearPrepareState = "ready"
	LinearPrepareStateFailed  LinearPrepareState = "failed"
)

// AllLinearPrepareStates is the canonical, ordered list of valid
// LinearPrepareState values. Validate() and the
// chk_sessions_linear_prepare_state CHECK constraint in
// migrations/000104_linear_session_linking.up.sql both consume this
// vocabulary; TestLinearPrepareStateMigrationVocabularyMatchesGoEnum parses
// the migration and pins the two together so a value added in one place
// without the other breaks the build instead of the database.
func AllLinearPrepareStates() []LinearPrepareState {
	return []LinearPrepareState{
		LinearPrepareStateNone,
		LinearPrepareStatePending,
		LinearPrepareStateReady,
		LinearPrepareStateFailed,
	}
}

func (s LinearPrepareState) Validate() error {
	for _, valid := range AllLinearPrepareStates() {
		if s == valid {
			return nil
		}
	}
	return fmt.Errorf("invalid LinearPrepareState: %q", s)
}

// SessionIssueLinkRole captures whether a linked issue owns lifecycle
// transitions for the session or is contextual-only related context.
type SessionIssueLinkRole string

const (
	SessionIssueLinkRolePrimary SessionIssueLinkRole = "primary"
	SessionIssueLinkRoleRelated SessionIssueLinkRole = "related"
)

func (r SessionIssueLinkRole) Validate() error {
	switch r {
	case SessionIssueLinkRolePrimary, SessionIssueLinkRoleRelated:
		return nil
	default:
		return fmt.Errorf("invalid SessionIssueLinkRole: %q", r)
	}
}

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
