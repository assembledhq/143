package projects

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type taskStoreStub struct {
	task      models.ProjectTask
	getErr    error
	updateErr error
	updated   *models.ProjectTask
}

func (s *taskStoreStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.ProjectTask, error) {
	return s.task, s.getErr
}

func (s *taskStoreStub) Update(_ context.Context, task *models.ProjectTask) error {
	copy := *task
	s.updated = &copy
	return s.updateErr
}

type projectStoreStub struct {
	err     error
	updated bool
}

func (s *projectStoreStub) UpdateProgress(context.Context, uuid.UUID, uuid.UUID) error {
	s.updated = true
	return s.err
}

func TestHooks_OnSessionComplete(t *testing.T) {
	t.Parallel()

	errStore := errors.New("store failed")
	tests := []struct {
		name           string
		status         models.SessionStatus
		linked         bool
		getErr         error
		updateErr      error
		progressErr    error
		wantErr        bool
		wantStatus     models.ProjectTaskStatus
		wantOutcome    string
		wantProgress   bool
		wantCompleted  bool
		wantTaskUpdate bool
	}{
		{name: "ignores unlinked session", status: models.SessionStatusCompleted},
		{name: "marks completed task", linked: true, status: models.SessionStatusCompleted, wantStatus: models.ProjectTaskStatusCompleted, wantProgress: true, wantCompleted: true, wantTaskUpdate: true},
		{name: "marks failed task", linked: true, status: models.SessionStatusFailed, wantStatus: models.ProjectTaskStatusFailed, wantOutcome: "Agent run failed", wantProgress: true, wantTaskUpdate: true},
		{name: "marks guidance task failed", linked: true, status: models.SessionStatusNeedsHumanGuidance, wantStatus: models.ProjectTaskStatusFailed, wantOutcome: "Agent run needs human guidance", wantProgress: true, wantTaskUpdate: true},
		{name: "ignores nonterminal status", linked: true, status: models.SessionStatusRunning},
		{name: "returns task lookup error", linked: true, status: models.SessionStatusCompleted, getErr: errStore, wantErr: true},
		{name: "returns task update error", linked: true, status: models.SessionStatusCompleted, updateErr: errStore, wantErr: true, wantStatus: models.ProjectTaskStatusCompleted, wantCompleted: true, wantTaskUpdate: true},
		{name: "progress update is best effort", linked: true, status: models.SessionStatusCompleted, progressErr: errStore, wantStatus: models.ProjectTaskStatusCompleted, wantProgress: true, wantCompleted: true, wantTaskUpdate: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID, sessionID, taskID, projectID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			tasks := &taskStoreStub{task: models.ProjectTask{ID: taskID, OrgID: orgID, ProjectID: projectID, Status: models.ProjectTaskStatusRunning}, getErr: tt.getErr, updateErr: tt.updateErr}
			projects := &projectStoreStub{err: tt.progressErr}
			hooks := NewHooks(tasks, projects, zerolog.Nop())
			session := &models.Session{ID: sessionID, OrgID: orgID}
			if tt.linked {
				session.ProjectTaskID = &taskID
			}

			err := hooks.OnSessionComplete(context.Background(), session, tt.status)
			if tt.wantErr {
				require.Error(t, err, "hook should propagate required store failures")
				return
			}
			require.NoError(t, err, "hook should complete without an error")
			require.Equal(t, tt.wantProgress, projects.updated, "project progress update should match terminal behavior")
			if !tt.wantTaskUpdate {
				require.Nil(t, tasks.updated, "task should remain unchanged")
				return
			}
			require.NotNil(t, tasks.updated, "terminal session should update its task")
			require.Equal(t, tt.wantStatus, tasks.updated.Status, "task status should reflect the session outcome")
			require.Equal(t, &sessionID, tasks.updated.SessionID, "task should retain the completing session")
			require.Equal(t, tt.wantCompleted, tasks.updated.CompletedAt != nil, "completion timestamp should only be set for success")
			if tt.wantOutcome != "" {
				require.NotNil(t, tasks.updated.OutcomeNotes, "failed task should include outcome notes")
				require.Equal(t, tt.wantOutcome, *tasks.updated.OutcomeNotes, "outcome notes should explain the terminal state")
			}
		})
	}
}
