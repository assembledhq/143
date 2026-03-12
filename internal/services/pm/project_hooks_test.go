package pm

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestProjectHooks_OnSessionComplete_NilProjectTaskID(t *testing.T) {
	t.Parallel()

	hooks := NewProjectHooks(nil, nil, zerolog.Nop())
	run := &models.Session{ProjectTaskID: nil}

	err := hooks.OnSessionComplete(context.Background(), run, "completed")
	require.NoError(t, err, "should return nil when no project task ID")
}

func TestProjectHooks_OnSessionComplete_Completed(t *testing.T) {
	t.Parallel()

	taskID := uuid.New()
	projectID := uuid.New()
	orgID := uuid.New()
	runID := uuid.New()

	task := &models.ProjectTask{
		ID:        taskID,
		ProjectID: projectID,
		OrgID:     orgID,
		Status:    models.ProjectTaskStatusRunning,
	}
	pts := &mockProjectTaskStore{tasks: []*models.ProjectTask{task}}
	ps := newMockProjectStore(models.Project{ID: projectID, OrgID: orgID})

	hooks := NewProjectHooks(pts, ps, zerolog.Nop())

	run := &models.Session{
		ID:            runID,
		OrgID:         orgID,
		ProjectTaskID: &taskID,
	}

	err := hooks.OnSessionComplete(context.Background(), run, "completed")
	require.NoError(t, err)

	require.Equal(t, models.ProjectTaskStatusCompleted, pts.tasks[0].Status)
	require.NotNil(t, pts.tasks[0].CompletedAt)
	require.Equal(t, &runID, pts.tasks[0].SessionID)
}

func TestProjectHooks_OnSessionComplete_Failed(t *testing.T) {
	t.Parallel()

	taskID := uuid.New()
	projectID := uuid.New()
	orgID := uuid.New()

	task := &models.ProjectTask{
		ID:        taskID,
		ProjectID: projectID,
		OrgID:     orgID,
		Status:    models.ProjectTaskStatusRunning,
	}
	pts := &mockProjectTaskStore{tasks: []*models.ProjectTask{task}}
	ps := newMockProjectStore(models.Project{ID: projectID, OrgID: orgID})

	hooks := NewProjectHooks(pts, ps, zerolog.Nop())

	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         orgID,
		ProjectTaskID: &taskID,
	}

	err := hooks.OnSessionComplete(context.Background(), run, "failed")
	require.NoError(t, err)

	require.Equal(t, models.ProjectTaskStatusFailed, pts.tasks[0].Status)
	require.NotNil(t, pts.tasks[0].OutcomeNotes)
	require.Equal(t, "Agent run failed", *pts.tasks[0].OutcomeNotes)
}

func TestProjectHooks_OnSessionComplete_NeedsHumanGuidance(t *testing.T) {
	t.Parallel()

	taskID := uuid.New()
	projectID := uuid.New()
	orgID := uuid.New()

	task := &models.ProjectTask{
		ID:        taskID,
		ProjectID: projectID,
		OrgID:     orgID,
		Status:    models.ProjectTaskStatusRunning,
	}
	pts := &mockProjectTaskStore{tasks: []*models.ProjectTask{task}}
	ps := newMockProjectStore(models.Project{ID: projectID, OrgID: orgID})

	hooks := NewProjectHooks(pts, ps, zerolog.Nop())

	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         orgID,
		ProjectTaskID: &taskID,
	}

	err := hooks.OnSessionComplete(context.Background(), run, "needs_human_guidance")
	require.NoError(t, err)

	require.Equal(t, models.ProjectTaskStatusFailed, pts.tasks[0].Status)
	require.Contains(t, *pts.tasks[0].OutcomeNotes, "human guidance")
}

func TestProjectHooks_OnSessionComplete_UnknownStatus(t *testing.T) {
	t.Parallel()

	taskID := uuid.New()
	orgID := uuid.New()

	task := &models.ProjectTask{
		ID:        taskID,
		ProjectID: uuid.New(),
		OrgID:     orgID,
		Status:    models.ProjectTaskStatusRunning,
	}
	pts := &mockProjectTaskStore{tasks: []*models.ProjectTask{task}}

	hooks := NewProjectHooks(pts, nil, zerolog.Nop())

	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         orgID,
		ProjectTaskID: &taskID,
	}

	err := hooks.OnSessionComplete(context.Background(), run, "unknown_status")
	require.NoError(t, err)

	// Status should remain unchanged.
	require.Equal(t, models.ProjectTaskStatusRunning, pts.tasks[0].Status)
}
