package linear

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// HandleAgentMilestone fans a milestone event out to the Linear AgentSession
// stream, when the underlying 143 session was triggered by an inbound agent
// assignment / @-mention. No-op for sessions whose origin is the existing
// manual / project / automation paths — those sessions don't have a Linear
// AgentSession to write to.
//
// Kill-switch / drain semantics: LINEAR_AGENT_ENABLED gates the dispatcher
// (api/handlers/linear_agent_webhook.go) and the worker handler registration
// (cmd/server/main.go). It does NOT gate this milestone fan-out: once a
// `linear_agent_sessions` row exists, the milestone path keeps emitting
// activities until the session reaches a terminal state, even after an
// operator flips the kill switch off. This is intentional — the kill switch
// is "stop accepting new sessions", not "kill in-flight sessions" — and lets
// us toggle the feature during an incident without stranding running PRs.
//
// Best-effort: errors are logged but not surfaced to the caller. The
// canonical record of session lifecycle still lives in the durable
// attachment + rolling comment that HandleMilestone already wrote; agent
// activities are a UX enhancement, not a correctness gate. We never want a
// transient Linear error on the agent surface to fail a milestone job that
// already updated the durable handles successfully.
//
// Idempotent: the underlying activity log enforces (agent_session_row_id,
// idem_key) UNIQUE so concurrent fan-outs and replays are safe.
func (s *Service) HandleAgentMilestone(ctx context.Context, in MilestoneInput) error {
	if s == nil || s.agentSessions == nil || s.agentActivities == nil {
		// Feature dark — silently skip.
		return nil
	}
	if in.Session == nil {
		return errors.New("nil session")
	}
	if in.Link.Role != models.SessionIssueLinkRolePrimary {
		// Related issues never participate in the agent stream — only the
		// primary link drives the AgentSession lifecycle.
		return nil
	}
	if in.Session.LinearPrivate {
		// Private sessions suppress all Linear writes including agent
		// activities. Don't even look up the agent_session row.
		return nil
	}

	row, err := s.agentSessions.LookupBySessionID(ctx, in.Session.OrgID, in.Session.ID)
	switch {
	case errors.Is(err, db.ErrLinearAgentSessionNotFound):
		// Session wasn't triggered through the agent path — silent skip.
		return nil
	case err != nil:
		s.logger.Warn().Err(err).
			Str("session_id", in.Session.ID.String()).
			Msg("agent milestone: failed to look up linear_agent_sessions row")
		return nil
	}

	activity, ok := MilestoneActivity(in.Event, in.PRNumber)
	if !ok {
		// This milestone has no agent-side echo (e.g. MilestoneLinked,
		// which is suppressed because the dispatcher already emitted a
		// bootstrap thought).
		return nil
	}

	_, token, err := s.integrationFor(ctx, in.Session.OrgID)
	if err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", in.Session.ID.String()).
			Msg("agent milestone: failed to resolve linear integration")
		return nil
	}
	client, err := s.clientFactory(ctx, token)
	if err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", in.Session.ID.String()).
			Msg("agent milestone: failed to build linear client")
		return nil
	}

	writer := NewAgentActivityWriter(client, s.agentActivities, s.agentMetrics, s.logger)
	if _, err := writer.Emit(ctx, EmitInput{
		OrgID:             in.Session.OrgID,
		AgentSessionRowID: row.ID,
		AgentSessionID:    row.LinearAgentSessionID,
		Activity:          activity,
	}); err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", in.Session.ID.String()).
			Str("agent_session_id", row.LinearAgentSessionID).
			Str("idem_key", activity.IdemKey).
			Msg("agent milestone: emit failed; reservation kept to prevent duplicate")
		return nil
	}

	// Pin externalUrls on every milestone where we have a stable 143
	// session id to surface. AgentSessionUpdate is idempotent on Linear's
	// side (same id+url combination → no-op), so re-pinning is free and
	// it guarantees the deep link survives even if the MilestoneStarted
	// emit succeeded but the subsequent UpdateSession call failed.
	//
	// We only ship the 143 session URL today; the PR URL itself is in
	// the activity body. A future iteration can resolve the GitHub PR
	// URL via the PR store and add it here as a second entry, at which
	// point the slice shape carries its weight.
	if pinExternalURLsForEvent(in.Event) {
		if err := client.AgentSessionUpdate(ctx, AgentSessionUpdateInput{
			AgentSessionID: row.LinearAgentSessionID,
			ExternalURLs: []AgentSessionExternalURL{
				{URL: s.SessionURL(in.Session.ID), Title: "143 session"},
			},
		}); err != nil {
			s.logger.Warn().Err(err).
				Str("session_id", in.Session.ID.String()).
				Str("agent_session_id", row.LinearAgentSessionID).
				Str("event", string(in.Event)).
				Msg("agent milestone: failed to pin external URLs on AgentSession")
		}
	}

	// Update the cached state for terminal events so the dispatcher's
	// `prompted` router can decide turn-append vs revision without
	// round-tripping to Linear.
	if cached := terminalStateFor(in.Event); cached != "" {
		if err := s.agentSessions.SetState(ctx, in.Session.OrgID, row.ID, cached); err != nil {
			s.logger.Warn().Err(err).
				Str("agent_session_row_id", row.ID.String()).
				Msg("agent milestone: failed to update cached state")
		}
	}

	return nil
}

// pinExternalURLsForEvent returns true for milestone events where we want
// the AgentSession header to point at the 143 session URL. We pin on the
// first concrete progress event (Started) and on the PR-open event so the
// header gets populated as early as possible AND has a second chance to
// land if the first pin attempt failed. Terminal events are deliberately
// excluded: the session header already shows whatever was last pinned, and
// re-pinning right before close is purely overhead.
func pinExternalURLsForEvent(event MilestoneEvent) bool {
	switch event {
	case MilestoneStarted, MilestonePROpened:
		return true
	}
	return false
}

// terminalStateFor maps a milestone event to the cached state we should
// record on the linear_agent_sessions row. Returns "" for non-terminal
// events.
func terminalStateFor(event MilestoneEvent) models.LinearAgentSessionState {
	switch event {
	case MilestonePRMerged, MilestoneEndedNoPR:
		return models.LinearAgentSessionStateComplete
	case MilestoneFailed:
		return models.LinearAgentSessionStateError
	}
	return ""
}
