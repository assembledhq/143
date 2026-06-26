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

func (s SessionStatus) IsTerminal() bool {
	switch s {
	case SessionStatusCompleted,
		SessionStatusPRCreated,
		SessionStatusFailed,
		SessionStatusCancelled,
		SessionStatusSkipped:
		return true
	default:
		return false
	}
}

func (s SessionStatus) IsResumable() bool {
	for _, resumable := range ResumableSessionStatuses {
		if s == resumable {
			return true
		}
	}
	return false
}

func (s SessionStatus) CanAddThread() bool {
	return !s.IsTerminal() || s.IsResumable()
}

// Status groups used by the frontend filter tabs. Defined here so the backend
// is the source of truth; the frontend arrays must stay in sync.
var (
	// ActiveStatuses are sessions that are in-progress or need attention.
	ActiveStatuses = []SessionStatus{SessionStatusPending, SessionStatusRunning, SessionStatusIdle, SessionStatusAwaitingInput, SessionStatusNeedsHumanGuidance}
	// DoneStatuses are terminal statuses.
	DoneStatuses = []SessionStatus{SessionStatusCompleted, SessionStatusPRCreated, SessionStatusFailed, SessionStatusCancelled, SessionStatusSkipped}

	// ResumableSessionStatuses are the non-idle statuses from which a session
	// can be re-claimed via SessionStore.ClaimForResume to continue a follow-up
	// message. Mirrors the SQL status list inline in ClaimForResume; both must
	// stay in sync.
	ResumableSessionStatuses = []SessionStatus{
		SessionStatusCompleted,
		SessionStatusPRCreated,
		SessionStatusFailed,
		SessionStatusCancelled,
		SessionStatusAwaitingInput,
		SessionStatusNeedsHumanGuidance,
	}
)

// SessionOrigin captures how a session was created. This is provenance only;
// runtime behavior is controlled by explicit policy fields.
type SessionOrigin string

const (
	SessionOriginIssueTrigger              SessionOrigin = "issue_trigger"
	SessionOriginManual                    SessionOrigin = "manual"
	SessionOriginProject                   SessionOrigin = "project"
	SessionOriginAutomation                SessionOrigin = "automation"
	SessionOriginRevision                  SessionOrigin = "revision"
	SessionOriginSlack                     SessionOrigin = "slack"
	SessionOriginExternalAPI               SessionOrigin = "external_api"
	SessionOriginEvalBootstrap             SessionOrigin = "eval_bootstrap"
	SessionOriginEvalRun                   SessionOrigin = "eval_run"
	SessionOriginAutomationGoalImprovement SessionOrigin = "automation_goal_improvement"
	SessionOriginCodeReview                SessionOrigin = "code_review"
)

func (o SessionOrigin) Validate() error {
	switch o {
	case SessionOriginIssueTrigger,
		SessionOriginManual,
		SessionOriginProject,
		SessionOriginAutomation,
		SessionOriginRevision,
		SessionOriginSlack,
		SessionOriginExternalAPI,
		SessionOriginEvalBootstrap,
		SessionOriginEvalRun,
		SessionOriginAutomationGoalImprovement,
		SessionOriginCodeReview:
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

// SessionRetryMode controls how a failed session retry is dispatched.
type SessionRetryMode string

const (
	SessionRetryModeCheckpoint SessionRetryMode = "checkpoint"
	SessionRetryModeStartOver  SessionRetryMode = "start_over"
)

func (m SessionRetryMode) Validate() error {
	switch m {
	case SessionRetryModeCheckpoint, SessionRetryModeStartOver:
		return nil
	default:
		return fmt.Errorf("invalid SessionRetryMode: %q", m)
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
// chk_session_linear_context_prepare_state CHECK constraint in
// migrations/000229_session_metadata_side_tables.up.sql both consume this
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

// PRPushState mirrors PRCreationState but tracks the "Push changes" follow-up
// action that pushes new commits to an already-open PR. Kept separate so the
// two operations can be in flight independently and so the UI does not have to
// disambiguate "succeeded" between "PR opened" and "changes pushed".
type PRPushState string

const (
	PRPushStateIdle      PRPushState = "idle"
	PRPushStateQueued    PRPushState = "queued"
	PRPushStatePushing   PRPushState = "pushing"
	PRPushStateSucceeded PRPushState = "succeeded"
	PRPushStateFailed    PRPushState = "failed"
)

func (s PRPushState) Validate() error {
	switch s {
	case PRPushStateIdle,
		PRPushStateQueued,
		PRPushStatePushing,
		PRPushStateSucceeded,
		PRPushStateFailed:
		return nil
	default:
		return fmt.Errorf("invalid PRPushState: %q", s)
	}
}

// BranchCreationState tracks the branch-only publish action. It mirrors the
// PR creation states but stays independent so creating a branch does not block
// a later PR creation from the same session.
type BranchCreationState string

const (
	BranchCreationStateIdle      BranchCreationState = "idle"
	BranchCreationStateQueued    BranchCreationState = "queued"
	BranchCreationStatePushing   BranchCreationState = "pushing"
	BranchCreationStateSucceeded BranchCreationState = "succeeded"
	BranchCreationStateFailed    BranchCreationState = "failed"
)

func (s BranchCreationState) Validate() error {
	switch s {
	case BranchCreationStateIdle,
		BranchCreationStateQueued,
		BranchCreationStatePushing,
		BranchCreationStateSucceeded,
		BranchCreationStateFailed:
		return nil
	default:
		return fmt.Errorf("invalid BranchCreationState: %q", s)
	}
}

// SessionAutonomy is the per-run autonomy knob stored in
// sessions.autonomy_level. It is intentionally distinct from the org-level
// AutonomyLevel (manual / auto_simple / auto_all) which controls when the PM
// auto-triggers runs; conflating the two writes the wrong vocabulary into the
// column and trips chk_sessions_autonomy_level on insert.
type SessionAutonomy string

const (
	SessionAutonomyFull       SessionAutonomy = "full"
	SessionAutonomySemi       SessionAutonomy = "semi"
	SessionAutonomySupervised SessionAutonomy = "supervised"
)

// AllSessionAutonomies is the canonical list of valid SessionAutonomy values.
// Validate() and the chk_sessions_autonomy_level CHECK constraint in
// migrations/000035_check_constraints.up.sql consume this vocabulary;
// TestSessionAutonomyMigrationVocabularyMatchesGoEnum pins the two together.
func AllSessionAutonomies() []SessionAutonomy {
	return []SessionAutonomy{
		SessionAutonomyFull,
		SessionAutonomySemi,
		SessionAutonomySupervised,
	}
}

func (a SessionAutonomy) Validate() error {
	for _, valid := range AllSessionAutonomies() {
		if a == valid {
			return nil
		}
	}
	return fmt.Errorf("invalid SessionAutonomy: %q", a)
}

// DefaultSessionAutonomy is the autonomy level applied when a session is
// created without an explicit value (API session-create, PM-spawned runs,
// automation-triggered runs).
const DefaultSessionAutonomy = SessionAutonomySemi

type SessionTokenMode string

const (
	SessionTokenModeLow  SessionTokenMode = "low"
	SessionTokenModeHigh SessionTokenMode = "high"
)

func (m SessionTokenMode) Validate() error {
	switch m {
	case SessionTokenModeLow, SessionTokenModeHigh:
		return nil
	default:
		return fmt.Errorf("invalid SessionTokenMode: %q", m)
	}
}

// DefaultSessionTokenMode is the token mode applied when a session is created
// without an explicit token-mode override.
const DefaultSessionTokenMode = SessionTokenModeLow

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

type GitIdentitySource string

const (
	GitIdentitySourceUser GitIdentitySource = "user"
	GitIdentitySourceApp  GitIdentitySource = "app"
)

func (s GitIdentitySource) Validate() error {
	switch s {
	case GitIdentitySourceUser, GitIdentitySourceApp:
		return nil
	default:
		return fmt.Errorf("invalid GitIdentitySource: %q", s)
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

// ResumableThreadStatuses are the non-idle thread statuses from which a thread
// can be re-claimed to continue a follow-up message. Intentionally narrower
// than ResumableSessionStatuses: threads do not have pr_created or
// needs_human_guidance counterparts. Kept here so the thread store and service
// share one source of truth, mirroring how ResumableSessionStatuses is wired
// into the session path.
var ResumableThreadStatuses = []ThreadStatus{
	ThreadStatusCompleted,
	ThreadStatusFailed,
	ThreadStatusCancelled,
	ThreadStatusAwaitingInput,
}

// MaxThreadsPerSession is the maximum number of threads allowed in a single session.
const MaxThreadsPerSession = 4

// MaxRunningThreadsPerSession caps how many threads inside one sandbox can be
// in an active state (pending/running/awaiting_input) at the same time. Lower
// than MaxThreadsPerSession so a user can keep idle "lanes" parked while
// limiting concurrent filesystem writers and live cost burn. Mirrors the
// "max running threads per session: 3" guidance in
// docs/design/68-sandbox-agent-tabs-and-threads.md.
const MaxRunningThreadsPerSession = 3

// FileEventTypeCreated, FileEventTypeModified, FileEventTypeDeleted are the
// canonical event_type values for session_thread_file_events. The orchestrator
// classifies git status output into these three buckets. Renames are recorded
// as a delete + create pair so each path's history is independent.
const (
	FileEventTypeCreated  SessionThreadFileEventType = "created"
	FileEventTypeModified SessionThreadFileEventType = "modified"
	FileEventTypeDeleted  SessionThreadFileEventType = "deleted"
)

type SessionQuestionStatus string

const (
	SessionQuestionStatusPending  SessionQuestionStatus = "pending"
	SessionQuestionStatusAnswered SessionQuestionStatus = "answered"
	SessionQuestionStatusTimedOut SessionQuestionStatus = "timed_out"
	SessionQuestionStatusSkipped  SessionQuestionStatus = "skipped"
)

func (s SessionQuestionStatus) Validate() error {
	switch s {
	case SessionQuestionStatusPending, SessionQuestionStatusAnswered, SessionQuestionStatusTimedOut, SessionQuestionStatusSkipped:
		return nil
	default:
		return fmt.Errorf("invalid SessionQuestionStatus: %q", s)
	}
}

type SessionLogLevel string

const (
	SessionLogLevelDebug    SessionLogLevel = "debug"
	SessionLogLevelInfo     SessionLogLevel = "info"
	SessionLogLevelWarn     SessionLogLevel = "warn"
	SessionLogLevelError    SessionLogLevel = "error"
	SessionLogLevelOutput   SessionLogLevel = "output"
	SessionLogLevelToolUse  SessionLogLevel = "tool_use"
	SessionLogLevelQuestion SessionLogLevel = "question"
)

func (l SessionLogLevel) Validate() error {
	switch l {
	case SessionLogLevelDebug, SessionLogLevelInfo, SessionLogLevelWarn, SessionLogLevelError,
		SessionLogLevelOutput, SessionLogLevelToolUse, SessionLogLevelQuestion:
		return nil
	default:
		return fmt.Errorf("invalid SessionLogLevel: %q", l)
	}
}

type SessionThreadFileEventType string

const (
	SessionThreadFileEventTypeCreated  SessionThreadFileEventType = "created"
	SessionThreadFileEventTypeModified SessionThreadFileEventType = "modified"
	SessionThreadFileEventTypeDeleted  SessionThreadFileEventType = "deleted"
)

func (t SessionThreadFileEventType) Validate() error {
	switch t {
	case SessionThreadFileEventTypeCreated, SessionThreadFileEventTypeModified, SessionThreadFileEventTypeDeleted:
		return nil
	default:
		return fmt.Errorf("invalid SessionThreadFileEventType: %q", t)
	}
}

type SessionDiffSource string

const (
	SessionDiffSourceTurnComplete SessionDiffSource = "turn_complete"
	SessionDiffSourceReview       SessionDiffSource = "review"
)

func (s SessionDiffSource) Validate() error {
	switch s {
	case SessionDiffSourceTurnComplete, SessionDiffSourceReview:
		return nil
	default:
		return fmt.Errorf("invalid SessionDiffSource: %q", s)
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
