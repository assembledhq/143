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
	UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string, completedAt *time.Time, resultSummary *string) error
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
func (h *AutomationHooks) OnSessionComplete(ctx context.Context, run *models.Session, status string) error {
	if run.AutomationRunID == nil {
		return nil
	}

	var runStatus string
	switch status {
	case "completed":
		runStatus = models.AutomationRunStatusCompleted
	case "failed":
		runStatus = models.AutomationRunStatusFailed
	default:
		// Ignore non-terminal updates; the automation_run will be updated
		// when the session eventually lands in completed/failed.
		return nil
	}

	now := time.Now()
	summary := deriveSummary(run, status)
	if err := h.runs.UpdateStatus(ctx, run.OrgID, *run.AutomationRunID, runStatus, &now, summary); err != nil {
		return fmt.Errorf("update automation run status: %w", err)
	}
	return nil
}

// deriveSummary picks the most useful single-line summary from the session's
// terminal fields. Prefers the orchestrator's result summary; falls back to
// the error string on failure; finally to a generic status label so the row
// never lands with an empty result_summary.
func deriveSummary(run *models.Session, status string) *string {
	if run.ResultSummary != nil && *run.ResultSummary != "" {
		s := *run.ResultSummary
		return &s
	}
	if status == "failed" && run.Error != nil && *run.Error != "" {
		s := *run.Error
		return &s
	}
	var s string
	switch status {
	case "completed":
		s = "Agent session completed."
	case "failed":
		s = "Agent session failed."
	default:
		s = fmt.Sprintf("Agent session ended with status %q.", status)
	}
	return &s
}
