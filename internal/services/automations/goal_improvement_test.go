package automations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestGoalHash(t *testing.T) {
	t.Parallel()

	require.Equal(t, GoalHash("run tests"), GoalHash("  run tests\n"), "goal hash should normalize surrounding whitespace")
	require.True(t, strings.HasPrefix(GoalHash("run tests"), "sha256:"), "goal hash should include the algorithm prefix")
}

func TestParseFastGoalImprovement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantGoal   string
		wantErr    bool
		confidence string
	}{
		{
			name:       "parses plain json",
			raw:        `{"proposed_goal":"Do the thing carefully","rationale":"clearer","changes":["added verification"],"evidence":["draft goal"],"risks":[],"confidence":"medium","warnings":[]}`,
			wantGoal:   "Do the thing carefully",
			confidence: "medium",
		},
		{
			name:       "parses fenced json and defaults confidence to medium",
			raw:        "```json\n{\"proposed_goal\":\"Do the thing carefully\",\"rationale\":\"clearer\"}\n```",
			wantGoal:   "Do the thing carefully",
			confidence: "medium",
		},
		{
			name:    "rejects invalid json",
			raw:     "not json",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseFastGoalImprovement(tt.raw)
			if tt.wantErr {
				require.Error(t, err, "invalid improvement output should return an error")
				return
			}
			require.NoError(t, err, "valid improvement output should parse")
			require.Equal(t, tt.wantGoal, got.ProposedGoal, "parser should return the proposed goal")
			require.Equal(t, tt.confidence, got.Confidence, "parser should return expected confidence")
		})
	}
}

func TestGoalImprovementCapabilitySnapshotIsReadOnly(t *testing.T) {
	t.Parallel()

	grantedAt := time.Now().UTC()
	got := goalImprovementCapabilitySnapshot(grantedAt)

	require.Equal(t, []models.AgentCapabilityID{
		models.AgentCapabilityRepoContext,
		models.AgentCapabilitySessionHistory,
		models.AgentCapabilityPRHistory,
		models.AgentCapabilityCIHistory,
		models.AgentCapabilityReviewFeedback,
	}, snapshotCapabilityIDs(got), "goal improvement sessions should receive the expected read-only context capabilities")
	for _, item := range got {
		require.Equal(t, models.AgentCapabilityAccessRead, item.AccessLevel, "goal improvement capabilities should be read-only")
		require.NotEqual(t, models.AgentCapabilityPublishing, item.ID, "goal improvement sessions should not include publishing")
		require.Equal(t, models.AgentCapabilityGrantSourceLaunchDefault, item.Source, "goal improvement capabilities should be launch-scoped")
		require.Equal(t, grantedAt, item.GrantedAt, "goal improvement capabilities should share the session creation timestamp")
	}
}

func TestGoalImprovementService_OnSessionCompleteCompletedProposalNoops(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	improvementID := uuid.New()
	store := db.NewAutomationGoalImprovementStore(mock)
	service := NewGoalImprovementService(store, nil, nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT .+ FROM automation_goal_improvements\s+WHERE org_id = @org_id AND analysis_session_id = @analysis_session_id`).
		WithArgs(orgID, sessionID).
		WillReturnRows(pgxmock.NewRows(goalImprovementRowsForServiceTest()).AddRow(
			improvementID, orgID, nil, nil, models.AutomationGoalImprovementModeDeep,
			models.AutomationGoalImprovementStatusCompleted, nil, "current goal", json.RawMessage(`{}`),
			GoalHash("current goal"), json.RawMessage(`{}`), stringPtrForGoalImprovementTest("better goal"),
			json.RawMessage(`{"rationale":"clearer"}`), stringPtrForGoalImprovementTest("medium"), json.RawMessage(`[]`),
			nil, &sessionID, nil, nil, nil, time.Now(), time.Now(),
		))

	err = service.OnSessionComplete(context.Background(), &models.Session{
		ID:     sessionID,
		OrgID:  orgID,
		Origin: models.SessionOriginAutomationGoalImprovement,
	}, models.SessionStatusCompleted)

	require.NoError(t, err, "completed sessions should not fail an already completed proposal")
	require.NoError(t, mock.ExpectationsWereMet(), "OnSessionComplete should only inspect the linked proposal")
}

func TestGoalImprovementService_ApplySavedUsesLockedTransaction(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	automationID := uuid.New()
	improvementID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	automation := models.Automation{
		ID:            automationID,
		OrgID:         orgID,
		Name:          "Nightly tests",
		Goal:          "Run tests",
		ExecutionMode: models.AutomationExecutionModeSequential,
		MaxConcurrent: 1,
		BaseBranch:    "main",
		ScheduleType:  "interval",
		Timezone:      "UTC",
		Enabled:       true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	service := NewGoalImprovementService(
		db.NewAutomationGoalImprovementStore(mock),
		db.NewAutomationStore(mock),
		nil,
		nil,
		nil,
		mock,
		nil,
	)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .+ FROM automations\s+WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL\s+FOR UPDATE`).
		WithArgs(automationID, orgID).
		WillReturnRows(addAutomationRowForGoalImprovementServiceTest(pgxmock.NewRows(automationColumnsForGoalImprovementServiceTest()), automation))
	mock.ExpectQuery(`SELECT .+ FROM automation_goal_improvements\s+WHERE id = @id AND automation_id = @automation_id AND org_id = @org_id`).
		WithArgs(improvementID, automationID, orgID).
		WillReturnRows(pgxmock.NewRows(goalImprovementRowsForServiceTest()).AddRow(
			improvementID, orgID, &automationID, nil, models.AutomationGoalImprovementModeFast,
			models.AutomationGoalImprovementStatusCompleted, nil, automation.Goal, json.RawMessage(`{}`),
			GoalHash(automation.Goal), json.RawMessage(`{}`), stringPtrForGoalImprovementTest("Run tests and report failures"),
			json.RawMessage(`{"rationale":"clearer"}`), stringPtrForGoalImprovementTest("medium"), json.RawMessage(`[]`),
			nil, nil, nil, nil, nil, now, now,
		))
	mock.ExpectExec(`UPDATE automations SET`).
		WithArgs(anyGoalImprovementServiceArgs(30)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE automation_goal_improvements\s+SET applied_by = @applied_by, applied_at = now\(\), updated_at = now\(\)\s+WHERE id = @id AND org_id = @org_id`).
		WithArgs(&userID, improvementID, orgID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	got, err := service.ApplySaved(context.Background(), orgID, ApplySavedGoalImprovementRequest{
		AutomationID:         automationID,
		ImprovementID:        improvementID,
		ExpectedBaseGoalHash: GoalHash(automation.Goal),
		ProposedGoal:         "Run tests and report failures",
		AppliedBy:            &userID,
	})

	require.NoError(t, err, "ApplySaved should apply inside one locked transaction")
	require.Equal(t, "Run tests", got.Before.Goal, "result should expose the original goal for audit diffing")
	require.Equal(t, "Run tests and report failures", got.Automation.Goal, "result should return the updated automation")
	require.Equal(t, improvementID, got.Improvement.ID, "result should include the applied proposal")
	require.NoError(t, mock.ExpectationsWereMet(), "ApplySaved should lock, update, mark applied, and commit")
}

func snapshotCapabilityIDs(snapshot []models.AgentCapabilitySnapshotItem) []models.AgentCapabilityID {
	ids := make([]models.AgentCapabilityID, 0, len(snapshot))
	for _, item := range snapshot {
		ids = append(ids, item.ID)
	}
	return ids
}

func goalImprovementRowsForServiceTest() []string {
	return []string{
		"id", "org_id", "automation_id", "repository_id", "mode", "status",
		"input_name", "input_goal", "input_config", "base_goal_hash", "evidence_snapshot",
		"proposed_goal", "proposal", "confidence", "warnings", "error_message",
		"analysis_session_id", "created_by", "applied_by", "applied_at", "created_at", "updated_at",
	}
}

func automationColumnsForGoalImprovementServiceTest() []string {
	return []string{
		"id", "org_id", "repository_id", "name", "goal", "scope",
		"icon_type", "icon_value",
		"agent_type", "model_override", "reasoning_effort", "execution_mode", "max_concurrent", "base_branch",
		"identity_scope", "pre_pr_review_loops",
		"schedule_type", "interval_value", "interval_unit", "interval_run_at", "cron_expression", "timezone",
		"github_event_triggers", "github_event_filters",
		"next_run_at", "last_run_at", "enabled", "created_by", "paused_by", "paused_at",
		"priority", "external_metadata", "created_at", "updated_at", "deleted_at",
	}
}

func addAutomationRowForGoalImprovementServiceTest(rows *pgxmock.Rows, a models.Automation) *pgxmock.Rows {
	metadata := a.ExternalMetadata
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}
	githubEventFilters := a.GitHubEventFilters
	if len(githubEventFilters) == 0 {
		githubEventFilters = []byte(`{}`)
	}
	return rows.AddRow(
		a.ID, a.OrgID, a.RepositoryID, a.Name, a.Goal, a.Scope,
		a.IconType.OrDefault(), a.IconValue,
		a.AgentType, a.ModelOverride, a.ReasoningEffort, a.ExecutionMode, a.MaxConcurrent, a.BaseBranch,
		a.IdentityScope.OrDefault(), a.PrePRReviewLoops,
		a.ScheduleType, a.IntervalValue, a.IntervalUnit, a.IntervalRunAt, a.CronExpression, a.Timezone,
		nil, githubEventFilters,
		a.NextRunAt, a.LastRunAt, a.Enabled, a.CreatedBy, a.PausedBy, a.PausedAt,
		a.Priority, metadata, a.CreatedAt, a.UpdatedAt, a.DeletedAt,
	)
}

func stringPtrForGoalImprovementTest(value string) *string {
	return &value
}

func anyGoalImprovementServiceArgs(count int) []any {
	args := make([]any, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}
