package pm

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// ProjectTaskUpdater is called by the orchestrator when an agent run
// completes, to update the associated project task's status.
type ProjectTaskUpdater interface {
	OnSessionComplete(ctx context.Context, run *models.Session, status models.SessionStatus) error
}

// ProjectHooks implements ProjectTaskUpdater by updating project task
// status and project progress when agent runs finish.
type ProjectHooks struct {
	projectTasks projectTaskStore
	projects     projectStore
	logger       zerolog.Logger
}

func NewProjectHooks(projectTasks projectTaskStore, projects projectStore, logger zerolog.Logger) *ProjectHooks {
	return &ProjectHooks{
		projectTasks: projectTasks,
		projects:     projects,
		logger:       logger,
	}
}

func (h *ProjectHooks) OnSessionComplete(ctx context.Context, run *models.Session, status models.SessionStatus) error {
	if run.ProjectTaskID == nil {
		return nil
	}

	task, err := h.projectTasks.GetByID(ctx, run.OrgID, *run.ProjectTaskID)
	if err != nil {
		return fmt.Errorf("get project task %s: %w", run.ProjectTaskID.String(), err)
	}

	now := time.Now()
	switch status {
	case models.SessionStatusCompleted:
		task.Status = models.ProjectTaskStatusCompleted
		task.CompletedAt = &now
	case models.SessionStatusFailed:
		task.Status = models.ProjectTaskStatusFailed
		outcomeNote := "Agent run failed"
		task.OutcomeNotes = &outcomeNote
	case models.SessionStatusNeedsHumanGuidance:
		task.Status = models.ProjectTaskStatusFailed
		outcomeNote := "Agent run needs human guidance"
		task.OutcomeNotes = &outcomeNote
	default:
		return nil
	}

	task.SessionID = &run.ID
	if err := h.projectTasks.Update(ctx, &task); err != nil {
		return fmt.Errorf("update project task status: %w", err)
	}

	// Update the project's aggregate progress counts.
	if err := h.projects.UpdateProgress(ctx, run.OrgID, task.ProjectID); err != nil {
		h.logger.Warn().
			Err(err).
			Str("project_id", task.ProjectID.String()).
			Msg("failed to update project progress after task completion")
	}

	return nil
}

// Ensure compile-time interface compliance.
var _ ProjectTaskUpdater = (*ProjectHooks)(nil)
