package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// fork_session_thread payload — keeps the schema explicit so the API,
// orchestrator, and worker all agree on the wire format.
type forkSessionThreadInput struct {
	SourceSessionID string `json:"source_session_id"`
	SourceThreadID  string `json:"source_thread_id"`
	OrgID           string `json:"org_id"`
	UserID          string `json:"user_id,omitempty"`
	Label           string `json:"label,omitempty"`
}

// newForkSessionThreadHandler creates a handler that copies a tab into a new
// session whose sandbox is independent from the source. The new session
// inherits the source repo, target branch, agent, and model so the user
// keeps continuity. We deliberately do not seed the new session with the
// source thread's transcript — the design doc is explicit that a fresh tab
// starts blank, and a fork inherits the same property.
//
// Snapshot policy: the forked session does NOT inherit the source's
// snapshot_key. The first turn on the fork runs through the standard
// run_agent path which clones the repo at HEAD of the target branch. This
// makes fork ownership of storage objects unambiguous (each session owns
// its own snapshots) at the cost of losing in-progress edits that hadn't
// been committed in the source. Fork is the right primitive for "diverge
// safely from this branch state"; it is not a rollback or a checkpoint
// share.
func newForkSessionThreadHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input forkSessionThreadInput
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal fork_session_thread payload: %w", err)
		}
		orgID, err := uuid.Parse(input.OrgID)
		if err != nil {
			return fmt.Errorf("parse org id: %w", err)
		}
		sessionID, err := uuid.Parse(input.SourceSessionID)
		if err != nil {
			return fmt.Errorf("parse session id: %w", err)
		}
		threadID, err := uuid.Parse(input.SourceThreadID)
		if err != nil {
			return fmt.Errorf("parse thread id: %w", err)
		}

		thread, err := stores.SessionThreads.GetByID(ctx, orgID, threadID)
		if err != nil {
			return fmt.Errorf("fetch source thread: %w", err)
		}
		if thread.SessionID != sessionID {
			return fmt.Errorf("source thread does not belong to source session")
		}
		source, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("fetch source session: %w", err)
		}

		label := strings.TrimSpace(input.Label)
		if label == "" {
			label = "Fork of " + thread.Label
		}
		title := label

		newSession := &models.Session{
			OrgID:         orgID,
			RepositoryID:  source.RepositoryID,
			AgentType:     thread.AgentType,
			ModelOverride: thread.ModelOverride,
			Status:        models.SessionStatusIdle,
			Origin:        models.SessionOriginManual,
			TargetBranch:  source.TargetBranch,
			Title:         &title,
		}
		if input.UserID != "" {
			if uid, err := uuid.Parse(input.UserID); err == nil {
				newSession.TriggeredByUserID = &uid
			}
		}

		if err := stores.Sessions.Create(ctx, newSession); err != nil {
			return fmt.Errorf("create forked session: %w", err)
		}

		// Post an assistant message into the source thread linking to the
		// fork. The user sees this in the source tab so the action feels
		// observable.
		if stores.SessionMessages != nil {
			branchHint := ""
			if source.TargetBranch != nil && *source.TargetBranch != "" {
				branchHint = fmt.Sprintf(" on the `%s` branch", *source.TargetBranch)
			}
			msg := &models.SessionMessage{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  &threadID,
				// Stamp the next-turn number so the confirmation lands
				// AFTER the user's most recent message in the timeline,
				// not interleaved with it. timeline order is (turn_number,
				// id) so a fork message at turn N+1 sits cleanly at the
				// end of the thread's history.
				TurnNumber: thread.CurrentTurn + 1,
				Role:       models.MessageRoleAssistant,
				Content: fmt.Sprintf(
					"Forked this tab out as **%s**.\n\n"+
						"The fork shares the same repository%s but starts with a fresh sandbox so risky divergent work doesn't touch this branch.",
					label,
					branchHint,
				),
			}
			if err := stores.SessionMessages.Create(ctx, msg); err != nil {
				logger.Warn().Err(err).Msg("failed to post fork-confirmation message")
			}
		}

		logger.Info().
			Str("source_session_id", sessionID.String()).
			Str("source_thread_id", threadID.String()).
			Str("new_session_id", newSession.ID.String()).
			Msg("forked session thread into new session")
		return nil
	}
}

// revert_session_thread payload.
type revertSessionThreadInput struct {
	SessionID string `json:"session_id"`
	ThreadID  string `json:"thread_id"`
	OrgID     string `json:"org_id"`
	UserID    string `json:"user_id,omitempty"`
}

// newRevertSessionThreadHandler applies a thread's stored diff in reverse
// against the latest durable session snapshot through the orchestrator, then
// posts a user-visible confirmation back into the source thread.
func newRevertSessionThreadHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input revertSessionThreadInput
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("unmarshal revert_session_thread payload: %w", err)
		}
		orgID, err := uuid.Parse(input.OrgID)
		if err != nil {
			return fmt.Errorf("parse org id: %w", err)
		}
		sessionID, err := uuid.Parse(input.SessionID)
		if err != nil {
			return fmt.Errorf("parse session id: %w", err)
		}
		threadID, err := uuid.Parse(input.ThreadID)
		if err != nil {
			return fmt.Errorf("parse thread id: %w", err)
		}
		thread, err := stores.SessionThreads.GetByID(ctx, orgID, threadID)
		if err != nil {
			return fmt.Errorf("fetch thread: %w", err)
		}
		if thread.SessionID != sessionID {
			return fmt.Errorf("thread does not belong to session")
		}
		if thread.Diff == nil || strings.TrimSpace(*thread.Diff) == "" {
			return fmt.Errorf("thread has no diff to revert")
		}
		if stores.Sessions == nil {
			return fmt.Errorf("session store is unavailable")
		}
		if services == nil || services.Orchestrator == nil {
			return fmt.Errorf("orchestrator is unavailable")
		}
		session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("fetch session: %w", err)
		}
		if err := services.Orchestrator.RevertThread(ctx, &session, &thread); err != nil {
			return fmt.Errorf("revert thread: %w", err)
		}

		if stores.SessionMessages != nil {
			msg := &models.SessionMessage{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  &threadID,
				// Same next-turn rationale as the fork confirmation: keep
				// the assistant message ordered after the user's last
				// message in the thread timeline.
				TurnNumber: thread.CurrentTurn + 1,
				Role:       models.MessageRoleAssistant,
				Content:    fmt.Sprintf("Reverted this tab's changes and refreshed the session snapshot.\n\n````diff\n%s\n````", truncateDiffForDisplay(*thread.Diff)),
			}
			if err := stores.SessionMessages.Create(ctx, msg); err != nil {
				logger.Warn().Err(err).Msg("failed to post revert message")
			}
		}
		logger.Info().
			Str("session_id", sessionID.String()).
			Str("thread_id", threadID.String()).
			Msg("reverted session thread")
		return nil
	}
}

// truncateDiffForDisplay caps the inline patch posted into the chat so a
// large diff doesn't blow up the message column. The full diff remains on
// the thread row for download.
// revertDiffDisplayLimit caps how many bytes of a thread's diff we inline
// into the revert-confirmation chat message. A larger diff gets truncated
// with a pointer back to the full patch on the tab's diff view; this keeps
// the chat column readable while preserving the full artifact elsewhere.
const revertDiffDisplayLimit = 12_000

func truncateDiffForDisplay(diff string) string {
	if len(diff) <= revertDiffDisplayLimit {
		return diff
	}
	return diff[:revertDiffDisplayLimit] + "\n…\n[diff truncated for display — full patch is available on the tab's diff view]"
}
