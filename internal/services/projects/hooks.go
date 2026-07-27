// Package projects contains service-layer behavior for human-authored projects.
package projects

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

type projectTaskStore interface {
	GetByID(ctx context.Context, orgID, taskID uuid.UUID) (models.ProjectTask, error)
	Update(ctx context.Context, task *models.ProjectTask) error
}

type projectStore interface {
	UpdateProgress(ctx context.Context, orgID, projectID uuid.UUID) error
}

// Hooks updates project task status and aggregate progress when a linked
// coding session finishes.
type Hooks struct {
	projectTasks projectTaskStore
	projects     projectStore
	logger       zerolog.Logger
}

func NewHooks(projectTasks projectTaskStore, projects projectStore, logger zerolog.Logger) *Hooks {
	return &Hooks{projectTasks: projectTasks, projects: projects, logger: logger}
}

func (h *Hooks) OnSessionComplete(ctx context.Context, session *models.Session, status models.SessionStatus) error {
	if session.ProjectTaskID == nil {
		return nil
	}

	task, err := h.projectTasks.GetByID(ctx, session.OrgID, *session.ProjectTaskID)
	if err != nil {
		return fmt.Errorf("get project task %s: %w", session.ProjectTaskID.String(), err)
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

	task.SessionID = &session.ID
	if err := h.projectTasks.Update(ctx, &task); err != nil {
		return fmt.Errorf("update project task status: %w", err)
	}
	if err := h.projects.UpdateProgress(ctx, session.OrgID, task.ProjectID); err != nil {
		h.logger.Warn().Err(err).Str("project_id", task.ProjectID.String()).
			Msg("failed to update project progress after task completion")
	}
	return nil
}
