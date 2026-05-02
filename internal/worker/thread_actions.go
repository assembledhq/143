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
// Implementation note: we create the new session row and post an assistant
// message into the source thread that links to the fork. Cloning the source
// snapshot into the new session's sandbox happens lazily on the new
// session's first user message (the existing run_agent path handles the
// hydrate). This keeps the fork operation cheap and recoverable.
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
			Status:        string(models.SessionStatusIdle),
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
			msg := &models.SessionMessage{
				SessionID:  sessionID,
				OrgID:      orgID,
				ThreadID:   &threadID,
				TurnNumber: thread.CurrentTurn,
				Role:       models.MessageRoleAssistant,
				Content: fmt.Sprintf(
					"Forked this tab into a new session: **%s**.\n\n"+
						"The new session shares the same repository (`%s` branch) but starts with a fresh sandbox so risky divergent work doesn't touch this branch.",
					label,
					branchOrDefault(source),
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

// newRevertSessionThreadHandler attempts to apply the thread's diff in
// reverse against the shared sandbox. The orchestrator owns the actual git
// exec (it is the only component with a live sandbox handle); when no
// orchestrator is configured we fall back to an assistant message that
// instructs the user to apply the reverse patch by hand.
//
// The handler always records a user-visible audit message in the source
// thread so the action is legible regardless of execution path.
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

		// We post an assistant message describing the revert request and
		// containing the patch the user can apply locally. The actual
		// in-sandbox `git apply -R` step is left to a Phase 4.5 follow-up
		// that owns sandbox exec sequencing — this handler ensures the
		// user-facing artifact is durable today.
		if stores.SessionMessages != nil {
			msg := &models.SessionMessage{
				SessionID:  sessionID,
				OrgID:      orgID,
				ThreadID:   &threadID,
				TurnNumber: thread.CurrentTurn,
				Role:       models.MessageRoleAssistant,
				Content: fmt.Sprintf(
					"Revert prepared. Apply this patch in reverse to undo this tab's changes:\n\n"+
						"```diff\n%s\n```\n\n"+
						"Tip: `git apply -R` against this patch from the workspace root will roll back the listed paths.",
					truncateDiffForDisplay(*thread.Diff),
				),
			}
			if err := stores.SessionMessages.Create(ctx, msg); err != nil {
				logger.Warn().Err(err).Msg("failed to post revert message")
			}
		}
		logger.Info().
			Str("session_id", sessionID.String()).
			Str("thread_id", threadID.String()).
			Msg("revert thread artifact prepared")
		return nil
	}
}

func branchOrDefault(s models.Session) string {
	if s.TargetBranch != nil && *s.TargetBranch != "" {
		return *s.TargetBranch
	}
	return "default"
}

// truncateDiffForDisplay caps the inline patch posted into the chat so a
// large diff doesn't blow up the message column. The full diff remains on
// the thread row for download.
func truncateDiffForDisplay(diff string) string {
	const limit = 12_000
	if len(diff) <= limit {
		return diff
	}
	return diff[:limit] + "\n…\n[diff truncated for display — full patch is available on the tab's diff view]"
}
