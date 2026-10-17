package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRunAgentHandler_AutomationParentBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		age                 time.Duration
		status              models.AutomationRunStatus
		superseded, proceed bool
	}{
		{name: "active run caps long session and extensions", age: 30 * time.Minute, status: models.AutomationRunStatusRunning, proceed: true},
		{name: "queue delay consumed execution budget", age: 59 * time.Minute, status: models.AutomationRunStatusRunning},
		{name: "parent already terminal", age: 10 * time.Minute, status: models.AutomationRunStatusFailed},
		{name: "old queued attempt was superseded", age: 10 * time.Minute, status: models.AutomationRunStatusRunning, superseded: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.AutomationRuns = db.NewAutomationRunStore(mock)
			orgID, automationID, runID, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Now().UTC()
			triggeredAt := now.Add(-tt.age)
			sessionRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusPending, 0, nil, nil)
			setWorkerSessionColumnValue(sessionRow, "automation_run_id", &runID)
			setWorkerSessionColumnValue(sessionRow, "interaction_mode", string(models.SessionInteractionModeSingleRun))
			mock.ExpectQuery("SELECT .* FROM sessions").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
			mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id`).WithArgs(runID, orgID).WillReturnRows(automationRunRows(runID, automationID, orgID, triggeredAt, models.AutomationTriggeredBySchedule, nil, nil, nil, nil, nil, []byte(`{}`), "goal", []byte(`{}`), tt.status, nil, nil, nil, now, now))
			latestID := sessionID
			if tt.superseded {
				latestID = uuid.New()
			}
			expectAutomationRunSessionAttempts(mock, models.AutomationRunAttempt{SessionID: latestID})
			if !tt.proceed {
				mock.ExpectQuery("UPDATE sessions").WithArgs(workerAnyArgs(15)...).WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(workerSessionRowWithStatus(sessionRow, models.SessionStatusFailed)...))
				mock.ExpectBegin()
				if tt.status == models.AutomationRunStatusRunning {
					expectCurrentAutomationSession(mock, tt.status, latestID)
					if !tt.superseded {
						mock.ExpectExec(`UPDATE automation_runs[\s\S]+WHERE id = @id AND org_id = @org_id AND status = @from_status`).WithArgs(workerAnyArgs(6)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
						mock.ExpectCommit()
					} else {
						mock.ExpectRollback()
					}
				} else {
					mock.ExpectQuery(`SELECT status FROM automation_runs WHERE id = @id AND org_id = @org_id FOR UPDATE`).WithArgs(runID, orgID).WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow(tt.status))
					mock.ExpectRollback()
				}
			}
			orch := &orchestratorServiceStub{runtimeCeiling: 90 * time.Minute, sessionTimeout: 45 * time.Minute}
			orch.runAgentFn = func(ctx context.Context, _ *models.Session) error {
				deadline, ok := ctx.Deadline()
				require.True(t, ok, "automation agent execution must have a handler deadline")
				require.Equal(t, triggeredAt.Add(models.AutomationRunExecutionBudget), deadline, "handler deadline must preserve original parent trigger time after queueing")
				runtimeDeadline, ok := agent.RuntimeDeadlineFromContext(ctx)
				require.True(t, ok, "runtime controller must receive the same parent deadline")
				require.Equal(t, deadline, runtimeDeadline, "automatic extensions must share the handler parent deadline")
				return nil
			}
			dispatcher := &fakeSessionExecutorDispatcher{}
			services := &Services{Orchestrator: orch}
			if !tt.proceed {
				services.SessionExecutorDispatcher = dispatcher
			}
			handler := newRunAgentHandler(stores, services, zerolog.Nop())
			payload, err := json.Marshal(map[string]string{"org_id": orgID.String(), "session_id": sessionID.String()})
			require.NoError(t, err, "automation job payload should encode")
			require.NoError(t, handler(context.Background(), "run_agent", payload), "automation execution should respect current parent ownership and budget")
			expectedCalls := 0
			if tt.proceed {
				expectedCalls = 1
			}
			require.Equal(t, expectedCalls, orch.runAgentCalls, "only an active current attempt with execution time left may start")
			require.Equal(t, 0, dispatcher.calls, "expired or superseded work must not launch a session executor")
			require.NoError(t, mock.ExpectationsWereMet(), "parent checks and expired-child settlement should be durable")
		})
	}
}
