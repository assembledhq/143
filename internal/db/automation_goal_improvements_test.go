package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func automationGoalImprovementRows() []string {
	return []string{
		"id", "org_id", "automation_id", "repository_id", "mode", "status",
		"input_name", "input_goal", "input_config", "base_goal_hash", "evidence_snapshot",
		"proposed_goal", "proposal", "confidence", "warnings", "error_message",
		"analysis_session_id", "created_by", "applied_by", "applied_at", "created_at", "updated_at",
	}
}

func TestAutomationGoalImprovementStore_GetQueriesAreOrgScoped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, automationID, improvementID uuid.UUID)
	}{
		{
			name: "get by id filters by org",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, automationID, improvementID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery(`SELECT .+ FROM automation_goal_improvements\s+WHERE id = @id AND org_id = @org_id`).
					WithArgs(improvementID, orgID).
					WillReturnRows(pgxmock.NewRows(automationGoalImprovementRows()).AddRow(
						improvementID, orgID, &automationID, nil, models.AutomationGoalImprovementModeFast,
						models.AutomationGoalImprovementStatusCompleted, nil, "goal", json.RawMessage(`{}`),
						"sha256:abc", json.RawMessage(`{}`), improvementStringPtr("better"), json.RawMessage(`{}`),
						improvementStringPtr("medium"), json.RawMessage(`[]`), nil, nil, nil, nil, nil, now, now,
					))
				_, err := store.GetByID(context.Background(), orgID, improvementID)
				require.NoError(t, err, "GetByID should scan an org-scoped improvement row")
			},
		},
		{
			name: "get by automation filters by org and automation",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, automationID, improvementID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery(`SELECT .+ FROM automation_goal_improvements\s+WHERE id = @id AND automation_id = @automation_id AND org_id = @org_id`).
					WithArgs(improvementID, automationID, orgID).
					WillReturnRows(pgxmock.NewRows(automationGoalImprovementRows()).AddRow(
						improvementID, orgID, &automationID, nil, models.AutomationGoalImprovementModeFast,
						models.AutomationGoalImprovementStatusCompleted, nil, "goal", json.RawMessage(`{}`),
						"sha256:abc", json.RawMessage(`{}`), improvementStringPtr("better"), json.RawMessage(`{}`),
						improvementStringPtr("medium"), json.RawMessage(`[]`), nil, nil, nil, nil, nil, now, now,
					))
				_, err := store.GetByAutomation(context.Background(), orgID, automationID, improvementID)
				require.NoError(t, err, "GetByAutomation should scan an org-scoped improvement row")
			},
		},
		{
			name: "get by analysis session filters by org and session",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, automationID, improvementID uuid.UUID) {
				now := time.Now()
				sessionID := uuid.New()
				mock.ExpectQuery(`SELECT .+ FROM automation_goal_improvements\s+WHERE org_id = @org_id AND analysis_session_id = @analysis_session_id`).
					WithArgs(orgID, sessionID).
					WillReturnRows(pgxmock.NewRows(automationGoalImprovementRows()).AddRow(
						improvementID, orgID, &automationID, nil, models.AutomationGoalImprovementModeDeep,
						models.AutomationGoalImprovementStatusFailed, nil, "goal", json.RawMessage(`{}`),
						"sha256:abc", json.RawMessage(`{}`), nil, json.RawMessage(`{}`),
						nil, json.RawMessage(`[]`), improvementStringPtr("session failed"), &sessionID, nil, nil, nil, now, now,
					))

				got, err := store.GetByAnalysisSession(context.Background(), orgID, sessionID)
				require.NoError(t, err, "GetByAnalysisSession should scan an org-scoped improvement row")
				require.Equal(t, sessionID, *got.AnalysisSessionID, "GetByAnalysisSession should return the proposal linked to the session")
			},
		},
		{
			name: "running deep check filters by org automation mode and active status",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, automationID, improvementID uuid.UUID) {
				mock.ExpectQuery(`SELECT EXISTS \(\s+SELECT 1 FROM automation_goal_improvements\s+WHERE org_id = @org_id\s+AND automation_id = @automation_id\s+AND mode = 'deep'\s+AND status IN \('pending', 'running'\)\s+\)`).
					WithArgs(orgID, automationID).
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

				got, err := store.HasRunningDeepByAutomation(context.Background(), orgID, automationID)
				require.NoError(t, err, "HasRunningDeepByAutomation should query the org-scoped active deep proposal set")
				require.True(t, got, "HasRunningDeepByAutomation should return the existence result")
			},
		},
		{
			name: "list by automation filters by org automation and clamps limit",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, automationID, improvementID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery(`SELECT .+ FROM automation_goal_improvements\s+WHERE org_id = @org_id AND automation_id = @automation_id\s+ORDER BY created_at DESC\s+LIMIT @limit`).
					WithArgs(orgID, automationID, 25).
					WillReturnRows(pgxmock.NewRows(automationGoalImprovementRows()).AddRow(
						improvementID, orgID, &automationID, nil, models.AutomationGoalImprovementModeDeep,
						models.AutomationGoalImprovementStatusRunning, nil, "goal", json.RawMessage(`{}`),
						"sha256:abc", json.RawMessage(`{}`), nil, json.RawMessage(`{}`),
						nil, json.RawMessage(`[]`), nil, nil, nil, nil, nil, now, now,
					))

				got, err := store.ListByAutomation(context.Background(), orgID, automationID, 100)
				require.NoError(t, err, "ListByAutomation should scan org-scoped proposal rows")
				require.Equal(t, []models.AutomationGoalImprovement{{
					ID:               improvementID,
					OrgID:            orgID,
					AutomationID:     &automationID,
					Mode:             models.AutomationGoalImprovementModeDeep,
					Status:           models.AutomationGoalImprovementStatusRunning,
					InputGoal:        "goal",
					InputConfig:      json.RawMessage(`{}`),
					BaseGoalHash:     "sha256:abc",
					EvidenceSnapshot: json.RawMessage(`{}`),
					Proposal:         json.RawMessage(`{}`),
					Warnings:         json.RawMessage(`[]`),
					CreatedAt:        now,
					UpdatedAt:        now,
				}}, got, "ListByAutomation should return the expected proposal history")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "mock pool should be created")
			defer mock.Close()
			store := NewAutomationGoalImprovementStore(mock)
			tt.run(t, store, mock, uuid.New(), uuid.New(), uuid.New())
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAutomationGoalImprovementStore_StateTransitionsAreOrgScoped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, improvementID, sessionID uuid.UUID)
	}{
		{
			name: "attach analysis session filters by org",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, improvementID, sessionID uuid.UUID) {
				mock.ExpectExec(`UPDATE automation_goal_improvements\s+SET analysis_session_id = @analysis_session_id, updated_at = now\(\)\s+WHERE id = @id AND org_id = @org_id`).
					WithArgs(sessionID, improvementID, orgID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				err := store.AttachAnalysisSession(context.Background(), orgID, improvementID, sessionID)
				require.NoError(t, err, "AttachAnalysisSession should update the org-scoped row")
			},
		},
		{
			name: "complete filters by org and running status",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, improvementID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery(`UPDATE automation_goal_improvements\s+SET status = @status, proposed_goal = @proposed_goal, proposal = @proposal, confidence = @confidence, warnings = @warnings, error_message = NULL, updated_at = now\(\)\s+WHERE id = @id AND org_id = @org_id AND status = 'running'\s+RETURNING`).
					WithArgs(models.AutomationGoalImprovementStatusCompleted, "better goal", json.RawMessage(`{"rationale":"clearer"}`), improvementStringPtr("high"), json.RawMessage(`[]`), improvementID, orgID).
					WillReturnRows(pgxmock.NewRows(automationGoalImprovementRows()).AddRow(
						improvementID, orgID, nil, nil, models.AutomationGoalImprovementModeDeep,
						models.AutomationGoalImprovementStatusCompleted, nil, "goal", json.RawMessage(`{}`),
						"sha256:abc", json.RawMessage(`{}`), improvementStringPtr("better goal"), json.RawMessage(`{"rationale":"clearer"}`),
						improvementStringPtr("high"), json.RawMessage(`[]`), nil, &sessionID, nil, nil, nil, now, now,
					))

				got, err := store.Complete(context.Background(), orgID, improvementID, "better goal", json.RawMessage(`{"rationale":"clearer"}`), improvementStringPtr("high"), json.RawMessage(`[]`))
				require.NoError(t, err, "Complete should scan the completed org-scoped row")
				require.Equal(t, models.AutomationGoalImprovementStatusCompleted, got.Status, "Complete should mark the proposal completed")
			},
		},
		{
			name: "fail filters by org and running status",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, improvementID, sessionID uuid.UUID) {
				mock.ExpectExec(`UPDATE automation_goal_improvements\s+SET status = @status, error_message = @error_message, updated_at = now\(\)\s+WHERE id = @id AND org_id = @org_id AND status IN \('pending', 'running'\)`).
					WithArgs(models.AutomationGoalImprovementStatusFailed, "judge rejected proposal", improvementID, orgID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				err := store.Fail(context.Background(), orgID, improvementID, "judge rejected proposal")
				require.NoError(t, err, "Fail should update only the org-scoped pending or running row")
			},
		},
		{
			name: "cancel filters by org and running status",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, improvementID, sessionID uuid.UUID) {
				mock.ExpectExec(`UPDATE automation_goal_improvements\s+SET status = @status, error_message = @error_message, updated_at = now\(\)\s+WHERE id = @id AND org_id = @org_id AND status IN \('pending', 'running'\)`).
					WithArgs(models.AutomationGoalImprovementStatusCanceled, "user canceled", improvementID, orgID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				err := store.Cancel(context.Background(), orgID, improvementID, "user canceled")
				require.NoError(t, err, "Cancel should update only the org-scoped pending or running row")
			},
		},
		{
			name: "expire drafts filters by org draft active status and age",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, improvementID, sessionID uuid.UUID) {
				before := time.Now().Add(-7 * 24 * time.Hour)
				mock.ExpectExec(`UPDATE automation_goal_improvements\s+SET status = @status, error_message = @error_message, updated_at = now\(\)\s+WHERE org_id = @org_id\s+AND automation_id IS NULL\s+AND status IN \('pending', 'running'\)\s+AND created_at < @before`).
					WithArgs(models.AutomationGoalImprovementStatusCanceled, "draft proposal expired", orgID, before).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				err := store.ExpireDrafts(context.Background(), orgID, before)
				require.NoError(t, err, "ExpireDrafts should cancel only stale active draft proposals for the org")
			},
		},
		{
			name: "fail by analysis session filters by org and running status",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, improvementID, sessionID uuid.UUID) {
				mock.ExpectExec(`UPDATE automation_goal_improvements\s+SET status = @status, error_message = @error_message, updated_at = now\(\)\s+WHERE org_id = @org_id\s+AND analysis_session_id = @analysis_session_id\s+AND mode = 'deep'\s+AND status IN \('pending', 'running'\)`).
					WithArgs(models.AutomationGoalImprovementStatusFailed, "session exited", orgID, sessionID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				err := store.FailByAnalysisSession(context.Background(), orgID, sessionID, "session exited")
				require.NoError(t, err, "FailByAnalysisSession should update only the org-scoped pending or running proposal attached to the session")
			},
		},
		{
			name: "cancel by analysis session filters by org and running status",
			run: func(t *testing.T, store *AutomationGoalImprovementStore, mock pgxmock.PgxPoolIface, orgID, improvementID, sessionID uuid.UUID) {
				mock.ExpectExec(`UPDATE automation_goal_improvements\s+SET status = @status, error_message = @error_message, updated_at = now\(\)\s+WHERE org_id = @org_id\s+AND analysis_session_id = @analysis_session_id\s+AND mode = 'deep'\s+AND status IN \('pending', 'running'\)`).
					WithArgs(models.AutomationGoalImprovementStatusCanceled, "session canceled", orgID, sessionID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				err := store.CancelByAnalysisSession(context.Background(), orgID, sessionID, "session canceled")
				require.NoError(t, err, "CancelByAnalysisSession should update only the org-scoped pending or running proposal attached to the session")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "mock pool should be created")
			defer mock.Close()
			store := NewAutomationGoalImprovementStore(mock)
			tt.run(t, store, mock, uuid.New(), uuid.New(), uuid.New())
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func improvementStringPtr(value string) *string {
	return &value
}
