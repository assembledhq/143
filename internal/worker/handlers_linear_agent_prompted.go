package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// promptedAwaitingCreateBackoff is the fixed wait used when a `prompted`
// event lands before its sibling `created` handler has finished attaching
// the 143 session. Linear delivered the prompted webhook to us and we
// already 200'd the dispatcher, so Linear won't redeliver — the only way
// the prompted survives is for the worker to retry. Picked to be long
// enough that the typical created path (FetchIssue + session create + a
// few inserts) completes, short enough that human-perceived latency on
// the follow-up comment stays bounded.
var promptedAwaitingCreateBackoff = 5 * time.Second

// handleLinearAgentPrompted processes a `prompted` AgentSessionEvent.
// Linear delivers this when a follow-up @mention or comment lands on an
// issue whose AgentSession is already alive. We turn-append the comment
// onto the existing 143 session (or spawn a revision if the session has
// reached terminal state).
//
// Steps:
//  1. Look up the linked 143 session via the agent_session_row.
//  2. Fetch the Linear comment body so we can include it as the user
//     message on the next turn. Falls back to a placeholder if the
//     fetch fails — better to advance the session with a partial
//     message than get stuck waiting for Linear.
//  3. ClaimIdle (or ClaimForResume for terminal sessions). Idle is the
//     happy path; resumable is the one-PR-already-merged-now-the-user-
//     wants-tweaks case.
//  4. Append a session_messages row with role=user, the Linear comment
//     body as content, and a TurnNumber bumped from session.CurrentTurn.
//  5. Enqueue continue_session with the same dedupe shape the manual
//     send-message path uses.
//
// Failure model: any step's error short-circuits and returns the err
// to the worker for retry. Linear-side noise (rate limits, transient
// FetchIssue failures) is mapped to RetryableError upstream.
func handleLinearAgentPrompted(ctx context.Context, deps LinearAgentEventHandlerDeps, agentSessions *db.LinearAgentSessionStore, payload linearAgentEventPayload, logger zerolog.Logger) error {
	orgID, err := uuid.Parse(payload.OrgID)
	if err != nil {
		return fmt.Errorf("invalid org_id: %w", err)
	}
	row, err := agentSessions.Lookup(ctx, orgID, payload.LinearAgentSessionID)
	if err != nil {
		return fmt.Errorf("lookup agent session: %w", err)
	}
	if row.SessionID == nil {
		// `created` hasn't completed yet. The dispatcher already 200'd
		// Linear so Linear won't redeliver — only the worker's retry
		// loop can keep this prompted alive. Return a retryable error
		// with a fixed short wait so we don't busy-loop or fall into
		// exponential backoff that would defer the follow-up turn for
		// minutes.
		logger.Warn().Str("agent_session_id", payload.LinearAgentSessionID).
			Msg("prompted received but 143 session not yet created; retrying shortly")
		return &RetryableError{
			Err:        errors.New("linear_agent_event: prompted arrived before created handler attached session_id"),
			RetryAfter: &promptedAwaitingCreateBackoff,
		}
	}
	sessionID := *row.SessionID

	commentBody := resolvePromptedCommentBody(ctx, deps, payload, orgID, logger)

	// Claim the session for a follow-up turn. ClaimIdle is the happy
	// path; if the session has already wrapped up (PR merged + completed)
	// we fall back to ClaimForResume which lifts terminal sessions back
	// to running. The user's intent is "respond to my new message",
	// regardless of whether the prior run technically finished.
	session, err := deps.Stores.Sessions.ClaimIdle(ctx, orgID, sessionID)
	if err != nil {
		session, err = deps.Stores.Sessions.ClaimForResume(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("claim session for prompted turn: %w", err)
		}
	}

	if deps.Stores.SessionMessages == nil {
		return errors.New("session_messages store unavailable")
	}
	msg := &models.SessionMessage{
		SessionID:  session.ID,
		OrgID:      orgID,
		TurnNumber: session.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    commentBody,
	}
	if err := deps.Stores.SessionMessages.Create(ctx, msg); err != nil {
		// Best-effort revert: the session is now claimed (running) but
		// we couldn't persist the message. Falling back to "idle" keeps
		// the system from showing a "running" session that nobody is
		// actually working on.
		_ = deps.Stores.Sessions.UpdateStatus(ctx, orgID, sessionID, "idle")
		return fmt.Errorf("create session message: %w", err)
	}

	if err := enqueueContinueForLinearAgent(ctx, deps.Stores, orgID, sessionID); err != nil {
		_ = deps.Stores.Sessions.UpdateStatus(ctx, orgID, sessionID, "idle")
		return fmt.Errorf("enqueue continue_session: %w", err)
	}

	logger.Info().
		Str("agent_session_id", payload.LinearAgentSessionID).
		Str("session_id", sessionID.String()).
		Int("turn_number", msg.TurnNumber).
		Msg("linear_agent_event: prompted -> turn appended + continue_session enqueued")
	return nil
}

// resolvePromptedCommentBody fetches the user's follow-up Linear comment
// so we can surface it verbatim into the 143 session's next turn. Returns
// a non-empty body in all cases — failures fall back to a deterministic
// placeholder so the session still advances rather than getting stuck
// waiting on Linear.
func resolvePromptedCommentBody(ctx context.Context, deps LinearAgentEventHandlerDeps, payload linearAgentEventPayload, orgID uuid.UUID, logger zerolog.Logger) string {
	const placeholder = "(Linear follow-up — see the linked issue for the full message.)"
	if payload.LinearCommentID == "" {
		return placeholder
	}
	client, err := deps.ClientForOrg(ctx, orgID)
	if err != nil {
		logger.Warn().Err(err).Msg("prompted: failed to resolve linear client; using placeholder comment text")
		return placeholder
	}
	body, err := fetchLinearComment(ctx, client, payload.LinearCommentID)
	if err != nil {
		logger.Warn().Err(err).Str("comment_id", payload.LinearCommentID).
			Msg("prompted: failed to fetch linear comment; using placeholder")
		return placeholder
	}
	if body == "" {
		return placeholder
	}
	return body
}

// fetchLinearComment pulls the body of a single Linear comment so we can
// surface the user's follow-up message verbatim into the 143 session's
// turn message. Returns the comment body on success, an empty string on
// any failure (caller falls back to a placeholder so the session still
// advances).
func fetchLinearComment(ctx context.Context, client linear.Client, commentID string) (string, error) {
	if commentID == "" {
		return "", errors.New("comment_id is empty")
	}
	comment, err := client.FetchComment(ctx, commentID)
	if err != nil {
		return "", err
	}
	if comment == nil {
		return "", linear.ErrCommentNotFound
	}
	return comment.Body, nil
}

// enqueueContinueForLinearAgent fires continue_session for a follow-up
// Linear-driven turn. Uses the same dedupe shape as the user-driven
// path so retries collapse cleanly. Different queue ("agent" vs the
// dispatcher's "linear") because continue_session is owned by the
// agent worker bundle.
func enqueueContinueForLinearAgent(ctx context.Context, stores *Stores, orgID, sessionID uuid.UUID) error {
	dedupe := "continue_session:" + sessionID.String()
	_, err := stores.Jobs.Enqueue(ctx, orgID, "agent", "continue_session", map[string]any{
		"org_id":     orgID.String(),
		"session_id": sessionID.String(),
	}, 5, &dedupe)
	return err
}
