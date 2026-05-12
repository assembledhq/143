package models

import "fmt"

// LinearAgentSessionState mirrors the Linear AgentSession state machine.
// Linear computes session state from the activity stream; we cache the latest
// known value in linear_agent_sessions.state for operator queries and
// idempotency checks ("is this AgentSession still alive — should we route a
// `prompted` event to it?").
//
// Vocabulary must stay in lockstep with the CHECK constraint in
// migrations/000121_linear_agent.up.sql — the
// TestLinearAgentSessionStateMigrationVocabularyMatchesGoEnum test parses the
// migration and fails if the two drift.
type LinearAgentSessionState string

const (
	// LinearAgentSessionStatePending is the row's state immediately after
	// the dispatcher upserts it from a `created` webhook, before the worker
	// has had a chance to create the 143 session.
	LinearAgentSessionStatePending LinearAgentSessionState = "pending"
	// LinearAgentSessionStateInProgress is set once run_agent has been
	// enqueued. Maps to Linear's "working" state.
	LinearAgentSessionStateInProgress LinearAgentSessionState = "in_progress"
	// LinearAgentSessionStateAwaitingInput is set when the agent has emitted
	// an elicitation activity asking the user a question.
	LinearAgentSessionStateAwaitingInput LinearAgentSessionState = "awaiting_input"
	// LinearAgentSessionStateComplete is the terminal happy-path state —
	// the agent emitted a final response activity.
	LinearAgentSessionStateComplete LinearAgentSessionState = "complete"
	// LinearAgentSessionStateError is the terminal failure state — the agent
	// emitted an error activity or run_agent failed permanently.
	LinearAgentSessionStateError LinearAgentSessionState = "error"
)

// IsTerminal reports whether the state will not progress further without
// fresh inbound activity. The `prompted` router uses this to decide between
// turn-append (non-terminal) and revision (terminal).
func (s LinearAgentSessionState) IsTerminal() bool {
	switch s {
	case LinearAgentSessionStateComplete, LinearAgentSessionStateError:
		return true
	default:
		return false
	}
}

// Validate returns an error if the state is not a recognized value.
func (s LinearAgentSessionState) Validate() error {
	switch s {
	case LinearAgentSessionStatePending,
		LinearAgentSessionStateInProgress,
		LinearAgentSessionStateAwaitingInput,
		LinearAgentSessionStateComplete,
		LinearAgentSessionStateError:
		return nil
	default:
		return fmt.Errorf("invalid linear agent session state: %q", s)
	}
}

// LinearAgentActivityType mirrors the Linear AgentActivity type vocabulary.
// Vocabulary kept in lockstep with the CHECK constraint in
// migrations/000121_linear_agent.up.sql.
type LinearAgentActivityType string

const (
	// LinearAgentActivityThought is reasoning narration. May be ephemeral.
	LinearAgentActivityThought LinearAgentActivityType = "thought"
	// LinearAgentActivityAction is a tool invocation. May be ephemeral.
	LinearAgentActivityAction LinearAgentActivityType = "action"
	// LinearAgentActivityElicitation asks the user a clarifying question and
	// flips the AgentSession state to awaitingInput.
	LinearAgentActivityElicitation LinearAgentActivityType = "elicitation"
	// LinearAgentActivityResponse is a partial or final answer.
	LinearAgentActivityResponse LinearAgentActivityType = "response"
	// LinearAgentActivityError is a hard failure that flips the AgentSession
	// state to error (terminal).
	LinearAgentActivityError LinearAgentActivityType = "error"
)

// CanBeEphemeral reports whether the activity type supports the ephemeral
// flag. Only `thought` and `action` may be ephemeral per Linear's contract;
// the writer enforces this so we don't get a runtime GraphQL rejection.
func (t LinearAgentActivityType) CanBeEphemeral() bool {
	switch t {
	case LinearAgentActivityThought, LinearAgentActivityAction:
		return true
	default:
		return false
	}
}

// Validate returns an error if the activity type is not a recognized value.
func (t LinearAgentActivityType) Validate() error {
	switch t {
	case LinearAgentActivityThought,
		LinearAgentActivityAction,
		LinearAgentActivityElicitation,
		LinearAgentActivityResponse,
		LinearAgentActivityError:
		return nil
	default:
		return fmt.Errorf("invalid linear agent activity type: %q", t)
	}
}

// Linear required-scopes for the agent feature. These are appended to the
// pre-existing `read,write` set on the upgraded OAuth flow — Linear treats
// scopes additively, so no scope is removed.
//
// Note: offline_access was attempted (PR #807) but reverted (PR #816)
// because Linear rejects it as "Invalid scope" and refuses the authorize
// redirect. Linear returns refresh_token automatically without any
// special scope, so the refresh machinery in
// internal/services/linear/refresh.go works without it.
const (
	LinearScopeRead           = "read"
	LinearScopeWrite          = "write"
	LinearScopeAppAssignable  = "app:assignable"
	LinearScopeAppMentionable = "app:mentionable"
)

// LinearAgentRequiredScopes is the canonical scope list for the agent OAuth
// flow. The OAuth start handler sends these as a comma-separated string (the
// shape Linear expects); the health probe parses Scope back from
// LinearConfig and checks the agent-specific scopes are present.
var LinearAgentRequiredScopes = []string{
	LinearScopeRead,
	LinearScopeWrite,
	LinearScopeAppAssignable,
	LinearScopeAppMentionable,
}
