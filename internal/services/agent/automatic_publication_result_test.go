package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type automaticPublicationResultFixture struct {
	SessionStore
	atomic, ordinary int
	err              error
}

func (s *automaticPublicationResultFixture) UpdateResult(context.Context, uuid.UUID, uuid.UUID, models.SessionStatus, *models.SessionResult) error {
	s.ordinary++
	return s.err
}

func (s *automaticPublicationResultFixture) UpdateResultWithAutomaticPublication(context.Context, uuid.UUID, uuid.UUID, models.SessionStatus, *models.SessionResult) error {
	s.atomic++
	return s.err
}

func TestPersistRunResultDurablySchedulesOnlyAutomationPublication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		edit                func(*models.Session, *models.SessionResult)
		status              models.SessionStatus
		failure, wantAtomic bool
	}{
		{name: "automation completed with diff", status: models.SessionStatusCompleted, wantAtomic: true},
		{name: "atomic failure propagates", status: models.SessionStatusCompleted, failure: true, wantAtomic: true},
		{name: "manual session", status: models.SessionStatusCompleted, edit: func(s *models.Session, _ *models.SessionResult) { s.AutomationRunID = nil }},
		{name: "no current diff", status: models.SessionStatusCompleted, edit: func(_ *models.Session, r *models.SessionResult) { r.Diff = nil }},
		{name: "failed automation", status: models.SessionStatusFailed},
		{name: "interactive turn", status: models.SessionStatusCompleted, edit: func(s *models.Session, _ *models.SessionResult) {
			s.InteractionMode = models.SessionInteractionModeInteractive
		}},
		{name: "goal improvement", status: models.SessionStatusCompleted, edit: func(s *models.Session, _ *models.SessionResult) {
			s.Origin = models.SessionOriginAutomationGoalImprovement
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			automationRun := uuid.New()
			session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), AutomationRunID: &automationRun, Origin: models.SessionOriginAutomation}
			diff := "synthetic change"
			result := &models.SessionResult{Diff: &diff}
			if tt.edit != nil {
				tt.edit(session, result)
			}
			store := &automaticPublicationResultFixture{}
			if tt.failure {
				store.err = errors.New("coordination enqueue unavailable")
			}
			queued, err := (&Orchestrator{sessions: store}).persistRunResult(context.Background(), session, tt.status, result)
			require.Equal(t, tt.wantAtomic, queued, "only successful automation results use the atomic publication producer")
			require.ErrorIs(t, err, store.err, "result and publication scheduling failures must propagate together")
			expectedAtomic, expectedOrdinary := 0, 1
			if tt.wantAtomic {
				expectedAtomic, expectedOrdinary = 1, 0
			}
			require.Equal(t, expectedAtomic, store.atomic, "eligible result must be written through the durable producer exactly once")
			require.Equal(t, expectedOrdinary, store.ordinary, "ordinary session behavior must retain the existing result path")
		})
	}
}
