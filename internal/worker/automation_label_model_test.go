package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAutomationRunHandler_FrozenLabelModel(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, currentAgent, model string
		selectedAgent             models.AgentType
	}{
		{"codex", "claude_code", models.CodexModelGPT6Astra, models.AgentTypeCodex},
		{"claude", "codex", models.ClaudeCodeModelFable5, models.AgentTypeClaudeCode},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.Automations = db.NewAutomationStore(mock)
			stores.AutomationRuns = db.NewAutomationRunStore(mock)

			orgID := uuid.New()
			automationID := uuid.New()
			runID := uuid.New()
			sessionID := uuid.New()
			jobID := uuid.New()
			now := time.Now()
			previousRunAt := time.Date(2026, 6, 26, 9, 30, 0, 0, time.UTC)
			agentType := tt.currentAgent
			frozenModel := tt.model
			reasoningEffort := models.ReasoningEffortXHigh
			repoID := uuid.New()

			payload, err := json.Marshal(map[string]string{
				"org_id":            orgID.String(),
				"automation_id":     automationID.String(),
				"automation_run_id": runID.String(),
			})
			require.NoError(t, err, "frozen model execution should succeed")

			mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(automationRunRows(
					runID, automationID, orgID, now, models.AutomationTriggeredBySchedule,
					nil, nil, nil, nil, nil, []byte("{}"), "goal", []byte(`{"previous_run_at":"2026-06-26T09:30:00Z","label_model_selection":{"reasoning_frozen":true,"reasoning_effort":"high","label":"review-deep","model":"`+frozenModel+`","agent_type":"`+string(tt.selectedAgent)+`","labels":["REVIEW-DEEP"]}}`),
					models.AutomationRunStatusPending, nil, nil, nil, now, now,
				))

			mock.ExpectQuery(`SELECT .+ FROM automations WHERE id = @id`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(automationRows(
					automationID, orgID, &repoID, "nightly", "cleanup", nil,
					models.AutomationIconTypeEmoji, "⚙️",
					&agentType, nil, &reasoningEffort, "sequential", 1, "main", models.AutomationIdentityScopeOrg, models.AutomationPublishPolicyPullRequest, 0,
					models.AutomationScheduleInterval, nil, nil, nil, nil, "UTC",
					[]string{}, []byte("{}"),
					nil, nil, true, nil, nil, nil,
					50, []byte("{}"), now, now, nil,
				))

			mock.ExpectExec(`UPDATE automation_runs\s+SET status = 'running',\s+dispatch_state = NULL.+WHERE id = @id AND org_id = @org_id\s+AND status = 'pending'`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			expectedGoal := fmt.Sprintf("goal\n\nAutomation run context\n- Current automation run triggered at: %s\n- Previous automation run: %s",
				now.UTC().Format(time.RFC3339), previousRunAt.Format(time.RFC3339))
			expectedReasoning := models.ReasoningEffortHigh
			createSessionArgs := workerAnyArgs(39)
			createSessionArgs[1] = tt.selectedAgent
			createSessionArgs[9] = &frozenModel
			createSessionArgs[10] = &expectedReasoning
			createSessionArgs[19] = &expectedGoal
			createSessionArgs[23] = &runID
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO sessions`).
				WithArgs(createSessionArgs...).
				WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).AddRow(sessionID, now, now))
			mock.ExpectQuery(`INSERT INTO session_threads`).
				WithArgs(workerAnyArgs(8)...).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			mock.ExpectCommit()

			mock.ExpectQuery(`INSERT INTO jobs`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

			handler := newAutomationRunHandler(stores, nil, zerolog.Nop())
			err = handler(context.Background(), models.JobTypeAutomationRun, payload)
			require.NoError(t, err, "frozen model execution should succeed")
			require.NoError(t, mock.ExpectationsWereMet(), "session and execution queue should use frozen settings")
		})
	}
}

func TestAutomationRunHandler_InvalidFrozenLabelModelStopsExecution(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	stores.Automations = db.NewAutomationStore(mock)
	stores.AutomationRuns = db.NewAutomationRunStore(mock)

	orgID := uuid.New()
	automationID := uuid.New()
	runID := uuid.New()
	now := time.Now()
	agentType := "claude_code"
	frozenModel := "removed-model"
	reasoningEffort := models.ReasoningEffortXHigh
	repoID := uuid.New()

	payload, err := json.Marshal(map[string]string{
		"org_id":            orgID.String(),
		"automation_id":     automationID.String(),
		"automation_run_id": runID.String(),
	})
	require.NoError(t, err, "frozen model execution should succeed")

	mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(automationRunRows(
			runID, automationID, orgID, now, models.AutomationTriggeredBySchedule,
			nil, nil, nil, nil, nil, []byte("{}"), "goal", []byte(`{"previous_run_at":"2026-06-26T09:30:00Z","label_model_selection":{"label":"review-deep","model":"`+frozenModel+`","agent_type":"codex","labels":["REVIEW-DEEP"]}}`),
			models.AutomationRunStatusPending, nil, nil, nil, now, now,
		))

	mock.ExpectQuery(`SELECT .+ FROM automations WHERE id = @id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(automationRows(
			automationID, orgID, &repoID, "nightly", "cleanup", nil,
			models.AutomationIconTypeEmoji, "⚙️",
			&agentType, nil, &reasoningEffort, "sequential", 1, "main", models.AutomationIdentityScopeOrg, models.AutomationPublishPolicyPullRequest, 0,
			models.AutomationScheduleInterval, nil, nil, nil, nil, "UTC",
			[]string{}, []byte("{}"),
			nil, nil, true, nil, nil, nil,
			50, []byte("{}"), now, now, nil,
		))

	handler := newAutomationRunHandler(stores, nil, zerolog.Nop())
	err = handler(context.Background(), models.JobTypeAutomationRun, payload)
	require.ErrorContains(t, err, "resolve automation run label model", "invalid frozen model must fail before model execution")
	require.NoError(t, mock.ExpectationsWereMet(), "invalid selection must not claim, create a session or enqueue execution")
}
