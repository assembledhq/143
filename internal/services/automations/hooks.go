// Package automations contains service-layer glue for the automations feature.
// It lives alongside other service packages (pm, validation, etc.) so the
// orchestrator's completion hooks can depend on it without pulling the db
// layer into the agent package directly.
package automations

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// automationRunStore is the minimal surface AutomationHooks needs. Defined as
// an interface here (rather than taking *db.AutomationRunStore) so hook tests
// don't have to stand up a Postgres pool.
type automationRunStore interface {
	TransitionStatusIf(ctx context.Context, orgID, runID uuid.UUID, fromStatus, toStatus models.AutomationRunStatus, completedAt *time.Time, resultSummary *string) (bool, error)
}

// AutomationHooks implements agent.AutomationRunUpdater by mapping a session's
// terminal status back onto its owning automation_runs row.
type AutomationHooks struct {
	runs   automationRunStore
	logger zerolog.Logger
}

func NewAutomationHooks(runs automationRunStore, logger zerolog.Logger) *AutomationHooks {
	return &AutomationHooks{runs: runs, logger: logger}
}

// OnSessionComplete maps a session's terminal status to the automation_run
// row. Non-terminal statuses (awaiting_input, cancelled, etc.) are ignored —
// the automation_run stays "running" until the session reaches a terminal
// state, matching the reaper contract (stuck pending/running rows get failed
// after the threshold).
//
// Transition is conditional on the current automation_run status being
// "running" so that if both the orchestrator's success path and failRun fire
// for the same session (or the reaper has already written a terminal status),
// a second call here cannot overwrite an already-terminal row.
func (h *AutomationHooks) OnSessionComplete(ctx context.Context, run *models.Session, status models.SessionStatus) error {
	if run.AutomationRunID == nil {
		return nil
	}

	var runStatus models.AutomationRunStatus
	switch status {
	case models.SessionStatusCompleted:
		runStatus = models.AutomationRunStatusCompleted
	case models.SessionStatusFailed, models.SessionStatusNeedsHumanGuidance:
		// needs_human_guidance is terminal from the orchestrator's
		// perspective — a human response starts a fresh session rather than
		// re-entering this hook — so map it onto failed here too, matching
		// pm.ProjectHooks. Without this the automation_run would stay
		// "running" until the 1-hour reaper swept it.
		runStatus = models.AutomationRunStatusFailed
	default:
		// Ignore non-terminal updates; the automation_run will be updated
		// when the session eventually lands in a terminal status.
		return nil
	}

	now := time.Now().UTC()
	summary := deriveSummary(run, status)
	transitioned, err := h.runs.TransitionStatusIf(ctx, run.OrgID, *run.AutomationRunID, models.AutomationRunStatusRunning, runStatus, &now, summary)
	if err != nil {
		return fmt.Errorf("update automation run status: %w", err)
	}
	if !transitioned {
		// Row was already non-running (terminal, or never claimed by the
		// worker handler). No-op — the earlier writer's status stands.
		h.logger.Debug().
			Str("automation_run_id", run.AutomationRunID.String()).
			Str("attempted_status", string(runStatus)).
			Msg("automation run already non-running; hook update skipped")
	}
	return nil
}

// deriveSummary picks the most useful single-line summary from the session's
// terminal fields. Prefers the orchestrator's result summary; falls back to
// the error string on failure; finally to a generic status label so the row
// never lands with an empty result_summary.
func deriveSummary(run *models.Session, status models.SessionStatus) *string {
	if run.ResultSummary != nil && *run.ResultSummary != "" {
		s := *run.ResultSummary
		return &s
	}
	sessionStatus := models.SessionStatus(status)
	if sessionStatus == models.SessionStatusFailed && run.Error != nil && *run.Error != "" {
		s := *run.Error
		return &s
	}
	var s string
	switch sessionStatus {
	case models.SessionStatusCompleted:
		s = "Agent session completed."
	case models.SessionStatusFailed:
		s = "Agent session failed."
	case models.SessionStatusNeedsHumanGuidance:
		s = "Agent run needs human guidance."
	default:
		s = fmt.Sprintf("Agent session ended with status %q.", status)
	}
	return &s
}
