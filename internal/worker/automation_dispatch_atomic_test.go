package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAutomationRunHandler_SessionCreateFailureRollsBackClaim(t *testing.T) {
	t.Parallel()
	stores, mock := newTestStores(t)
	defer mock.Close()
	stores.Automations = db.NewAutomationStore(mock)
	stores.AutomationRuns = db.NewAutomationRunStore(mock)
	orgID, automationID, runID, repoID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id`).WithArgs(runID, automationID, orgID).WillReturnRows(automationRunRows(runID, automationID, orgID, now, models.AutomationTriggeredBySchedule, nil, nil, nil, nil, nil, []byte(`{}`), "goal", []byte(`{}`), models.AutomationRunStatusPending, nil, nil, nil, now, now))
	mock.ExpectQuery(`SELECT .+ FROM automations WHERE id = @id`).WithArgs(automationID, orgID).WillReturnRows(automationRows(
		automationID, orgID, &repoID, "nightly", "cleanup", nil, models.AutomationIconTypeEmoji, "⚙️",
		nil, nil, nil, models.AutomationFallbackModels{}, "sequential", 1, "main", models.AutomationIdentityScopeOrg, models.AutomationPublishPolicyPullRequest, 0,
		models.AutomationScheduleInterval, nil, nil, nil, nil, "UTC", []string{}, []byte(`{}`), nil, nil, true, nil, nil, nil, 50, []byte(`{}`), now, now, nil,
	))
	expectAutomationRunSessionAttempts(mock)
	mock.ExpectBegin()
	expectCurrentAutomationSession(mock, models.AutomationRunStatusPending, uuid.Nil)
	mock.ExpectExec(`UPDATE automation_runs[\s\S]+WHERE id = @id AND org_id = @org_id[\s\S]+AND status = 'pending'`).WithArgs(runID, orgID).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	createErr := errors.New("session insert unavailable")
	mock.ExpectQuery(`INSERT INTO sessions`).WithArgs(workerAnyArgs(39)...).WillReturnError(createErr)
	mock.ExpectRollback()
	payload, err := json.Marshal(map[string]string{"org_id": orgID.String(), "automation_id": automationID.String(), "automation_run_id": runID.String()})
	require.NoError(t, err, "automation payload should encode")
	err = newAutomationRunHandler(stores, nil, zerolog.Nop())(context.Background(), models.JobTypeAutomationRun, payload)
	require.ErrorIs(t, err, createErr, "failed session creation must remain retryable")
	require.NoError(t, mock.ExpectationsWereMet(), "failed session insertion must roll back the running claim without enqueueing an orphan agent")
}
