package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func automationDecisionColumns() []string {
	return []string{
		"automation_id", "run_id", "session_id",
		"repository", "pull_request_number", "pull_request_url", "pull_request_title", "head_sha",
		"execution_status", "triggered_at", "completed_at", "attempt_count",
		"outcome_id", "outcome_org_id", "outcome_automation_id", "outcome_run_id", "outcome_session_id",
		"outcome_repository", "outcome_pr_number", "outcome_url", "outcome_title", "outcome_head_sha",
		"outcome_decision", "outcome_reason", "outcome_source", "outcome_reported_at", "outcome_created_at",
		"action_id", "action_org_id", "action_outcome_id", "action_provider", "action_type", "action_external_id", "action_url", "action_verification", "action_created_at",
	}
}

func TestAutomationOutcomeStoreListDecisions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()
	store := NewAutomationOutcomeStore(mock)
	orgID := uuid.New()
	automationID := uuid.New()
	runID := uuid.New()
	sessionID := uuid.New()
	outcomeID := uuid.New()
	actionID := uuid.New()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	rows := pgxmock.NewRows(automationDecisionColumns()).AddRow(
		automationID, runID, &sessionID,
		"assembledhq/143", 123, "https://github.com/assembledhq/143/pull/123", stringPointer("Clarify outcomes"), stringPointer("abc123"),
		models.AutomationRunStatusCompleted, now, &now, int64(3),
		&outcomeID, &orgID, &automationID, &runID, &sessionID,
		stringPointer("assembledhq/143"), intPointer(123), stringPointer("https://github.com/assembledhq/143/pull/123"), stringPointer("Clarify outcomes"), stringPointer("abc123"),
		stringPointer(string(models.AutomationOutcomeDecisionChangesRequested)), stringPointer("A blocking migration issue was found."), stringPointer(string(models.AutomationOutcomeSourceAgentReported)), &now, &now,
		&actionID, &orgID, &outcomeID, stringPointer("github"), stringPointer(string(models.AutomationExternalActionGitHubReviewChangesRequested)), stringPointer("456"),
		stringPointer("https://github.com/assembledhq/143/pull/123#pullrequestreview-456"), stringPointer(string(models.AutomationExternalActionVerificationReported)), &now,
	)
	mock.ExpectQuery(`WITH raw_targeted AS[\s\S]*COALESCE\([\s\S]*https://github.com/`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "automation_id": automationID}).
		WillReturnRows(rows)

	decisions, err := store.ListDecisions(context.Background(), orgID, automationID, AutomationDecisionFilters{Limit: 25})
	require.NoError(t, err, "ListDecisions should return the grouped decision")
	require.Equal(t, []models.AutomationDecision{{
		AutomationID: automationID,
		RunID:        runID,
		SessionID:    &sessionID,
		Target: models.AutomationDecisionTarget{
			Repository: "assembledhq/143", PullRequestNumber: 123,
			PullRequestURL:   "https://github.com/assembledhq/143/pull/123",
			PullRequestTitle: stringPointer("Clarify outcomes"), HeadSHA: stringPointer("abc123"),
		},
		ExecutionStatus: models.AutomationRunStatusCompleted,
		TriggeredAt:     now,
		CompletedAt:     &now,
		AttemptCount:    3,
		Outcome: &models.AutomationRunOutcome{
			ID: outcomeID, OrgID: orgID, AutomationID: automationID, AutomationRunID: runID, SessionID: sessionID,
			Repository: "assembledhq/143", PullRequestNumber: 123, PullRequestURL: "https://github.com/assembledhq/143/pull/123",
			PullRequestTitle: stringPointer("Clarify outcomes"), HeadSHA: stringPointer("abc123"),
			Decision: models.AutomationOutcomeDecisionChangesRequested, Reason: "A blocking migration issue was found.",
			Source: models.AutomationOutcomeSourceAgentReported, ReportedAt: now, CreatedAt: now,
			ExternalAction: &models.AutomationRunExternalAction{
				ID: actionID, OrgID: orgID, OutcomeID: outcomeID, Provider: "github",
				ActionType: models.AutomationExternalActionGitHubReviewChangesRequested,
				ExternalID: stringPointer("456"), URL: "https://github.com/assembledhq/143/pull/123#pullrequestreview-456",
				VerificationStatus: models.AutomationExternalActionVerificationReported, CreatedAt: now,
			},
		},
	}}, decisions, "ListDecisions should preserve target, lifecycle, outcome, and external action as separate fields")
	require.Contains(t, automationDecisionTargetCTEs, "'https://github.com/'", "decision targets should reconstruct canonical URLs for historical snapshots")
	require.NoError(t, mock.ExpectationsWereMet(), "ListDecisions should keep every query org scoped")
}

func TestAutomationOutcomeStoreGetDecisionStats(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()
	store := NewAutomationOutcomeStore(mock)
	orgID := uuid.New()
	automationID := uuid.New()
	expected := models.AutomationDecisionStats{
		UniquePullRequests: 4, UniqueRevisions: 5, TotalRuns: 12, Evaluating: 1,
		Passed: 2, ChangesRequested: 1, Advisory: 1, NotApplicable: 0,
		OutcomeNotReported: 0, ExecutionFailed: 1,
	}
	mock.ExpectQuery(`WITH raw_targeted AS`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "automation_id": automationID}).
		WillReturnRows(pgxmock.NewRows([]string{
			"unique_pull_requests", "unique_revisions", "total_runs", "evaluating", "passed",
			"changes_requested", "advisory", "not_applicable", "outcome_not_reported", "execution_failed",
		}).AddRow(4, 5, 12, 1, 2, 1, 1, 0, 0, 1))

	stats, err := store.GetDecisionStats(context.Background(), orgID, automationID)
	require.NoError(t, err, "GetDecisionStats should return aggregate outcome counts")
	require.Equal(t, expected, stats, "GetDecisionStats should distinguish decisions from execution states")
	require.NoError(t, mock.ExpectationsWereMet(), "GetDecisionStats should filter by org and automation")
}

func stringPointer(value string) *string {
	return &value
}

func intPointer(value int) *int {
	return &value
}
