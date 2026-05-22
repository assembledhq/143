package linear

import (
	"fmt"

	"github.com/assembledhq/143/internal/models"
)

// AgentMilestoneActivity is the rendered AgentActivity for one session
// lifecycle moment. Pure data — `agent_state.go` keeps the mapping side
// free of I/O so it's trivially unit-testable and so callers can mock it
// in tests without standing up the full writer.
type AgentMilestoneActivity struct {
	// Type maps onto Linear's AgentActivity type vocabulary. Use the typed
	// constants in models.LinearAgentActivityType* to construct it.
	Type models.LinearAgentActivityType
	// Body is the rendered prose. For thought/elicitation/response/error
	// this is shown directly. Action activities use Parameter/Result
	// instead because Linear rejects body on action content.
	Body string
	// Action is set only on Type=action — the human-readable name of the
	// tool the agent invoked. Linear's GraphQL schema rejects this field
	// on other types.
	Action string
	// Parameter is set only on Type=action — the action input Linear
	// requires for action content.
	Parameter string
	// Ephemeral marks the activity as transient (it scrolls out of the
	// activity feed quickly). Linear only honors this for thought/action;
	// the writer enforces this so we don't get a runtime GraphQL rejection.
	Ephemeral bool
	// IdemKey is the per-AgentSession dedupe slot. The writer reserves a
	// row in linear_agent_activity_log under this key before calling
	// Linear, and concurrent emits collide on UNIQUE — see
	// (LinearAgentActivityLogStore).Reserve.
	//
	// Stable across replays of the same milestone. The format is
	// `milestone:<event>` so an operator can read the activity log without
	// cross-referencing — no opaque hashes.
	IdemKey string
	// PinSessionState is set when emitting this activity should also pin
	// Linear's AgentSession state explicitly (rather than letting Linear
	// derive it from the activity stream). Empty for the common case.
	// Values use Linear's own state vocabulary, not 143's.
	PinSessionState string
}

// MilestoneActivity returns the AgentActivity that should accompany the
// given milestone, or false if the milestone has no agent-side echo.
//
// The mapping is intentionally conservative: only milestones a Linear user
// would actually want to see emit activities, and ephemeral=true keeps the
// activity feed readable. Permanent surface (the durable attachment + the
// rolling comment) carries the comprehensive log; activities are the
// "what's happening right now" stream.
//
// The session URL is *not* threaded into the activity body — Linear renders
// it via agentSessionUpdate.externalUrls (set by HandleAgentMilestone on
// MilestonePROpened), which produces a clickable header chip rather than
// inline text. Kept as a separate concern so milestone bodies stay terse.
func MilestoneActivity(event MilestoneEvent, prNumber int) (AgentMilestoneActivity, bool) {
	switch event {
	case MilestoneLinked:
		// Suppressed — the dispatcher/worker bootstrap path already
		// emitted a "Reading {KEY}…" thought. A second "Linked" thought
		// would be redundant noise.
		return AgentMilestoneActivity{}, false

	case MilestoneStarted:
		return AgentMilestoneActivity{
			Type:            models.LinearAgentActivityAction,
			Action:          "start_coding_session",
			Parameter:       "Starting coding session",
			Ephemeral:       false,
			IdemKey:         milestoneIdemKey(event),
			PinSessionState: "active",
		}, true

	case MilestonePROpened:
		body := "Opened PR."
		if prNumber > 0 {
			body = fmt.Sprintf("Opened PR #%d.", prNumber)
		}
		return AgentMilestoneActivity{
			Type:    models.LinearAgentActivityResponse,
			Body:    body,
			IdemKey: milestoneIdemKey(event),
		}, true

	case MilestonePRMerged:
		body := "PR merged."
		if prNumber > 0 {
			body = fmt.Sprintf("PR #%d merged.", prNumber)
		}
		return AgentMilestoneActivity{
			Type:            models.LinearAgentActivityAction,
			Action:          "pr_merged",
			Parameter:       body,
			Ephemeral:       false,
			IdemKey:         milestoneIdemKey(event),
			PinSessionState: "complete",
		}, true

	case MilestoneEndedNoPR:
		return AgentMilestoneActivity{
			Type:            models.LinearAgentActivityResponse,
			Body:            "Done — no code changes were needed.",
			IdemKey:         milestoneIdemKey(event),
			PinSessionState: "complete",
		}, true

	case MilestoneFailed:
		return AgentMilestoneActivity{
			Type:            models.LinearAgentActivityError,
			Body:            "Session failed. See the 143 deep link for details.",
			IdemKey:         milestoneIdemKey(event),
			PinSessionState: "error",
		}, true
	}
	return AgentMilestoneActivity{}, false
}

// BootstrapActivity is the very first activity emitted for a `created`
// AgentSessionEvent. The dispatcher emits it to satisfy Linear's 10s
// first-activity SLA; the worker re-emits with the same idem key so a
// transient dispatcher-side Linear write failure can recover before the
// live issue fetch or run_agent enqueue path does more work.
//
// IdemKey "bootstrap:opened" is single-fire across the whole AgentSession
// lifecycle; the second-arrival writer short-circuits.
func BootstrapActivity(issueIdentifier string) AgentMilestoneActivity {
	body := "Reading the issue and resolving the right repo…"
	if issueIdentifier != "" {
		body = fmt.Sprintf("Reading %s and resolving the right repo…", issueIdentifier)
	}
	return AgentMilestoneActivity{
		Type:      models.LinearAgentActivityThought,
		Body:      body,
		Ephemeral: true,
		IdemKey:   "bootstrap:opened",
	}
}

// UnmappedRepoActivity is what the dispatcher emits when the repo resolver
// can't pick a repo for an inbound AgentSession. It explains the gap so the
// admin (who is typically the Linear-side reader) knows exactly what to
// configure. Closes the AgentSession with state=complete because a missing
// mapping is a benign user state, not an error.
func UnmappedRepoActivity(teamName string) AgentMilestoneActivity {
	body := "I don't have a repository configured for this Linear team yet. Ask an admin to set up a mapping at Settings → Integrations → Linear → Agent."
	if teamName != "" {
		body = fmt.Sprintf("I don't have a repository configured for the %q team yet. Ask an admin to set up a mapping at Settings → Integrations → Linear → Agent.", teamName)
	}
	return AgentMilestoneActivity{
		Type:            models.LinearAgentActivityResponse,
		Body:            body,
		IdemKey:         "bootstrap:unmapped_repo",
		PinSessionState: "complete",
	}
}

// milestoneIdemKey is the canonical idempotency-key shape for milestone
// activities. Centralized so the dispatcher's bootstrap key, the writer's
// emit key, and the activity-log row all agree.
func milestoneIdemKey(event MilestoneEvent) string {
	return "milestone:" + string(event)
}
