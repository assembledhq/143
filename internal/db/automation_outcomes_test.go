package db

import (
	"context"
	"encoding/json"
	"os"
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
		"attempt_outcomes",
	}
}

func automationOutcomeRowColumns() []string {
	return []string{
		"id", "org_id", "automation_id", "automation_run_id", "session_id",
		"repository", "pull_request_number", "pull_request_url", "pull_request_title", "head_sha",
		"decision", "reason", "source", "reported_at", "created_at",
	}
}

func automationOutcomeRow(outcome models.AutomationRunOutcome) *pgxmock.Rows {
	return pgxmock.NewRows(automationOutcomeRowColumns()).AddRow(
		outcome.ID, outcome.OrgID, outcome.AutomationID, outcome.AutomationRunID, outcome.SessionID,
		outcome.Repository, outcome.PullRequestNumber, outcome.PullRequestURL, outcome.PullRequestTitle, outcome.HeadSHA,
		outcome.Decision, outcome.Reason, outcome.Source, outcome.ReportedAt, outcome.CreatedAt,
	)
}

func TestAutomationOutcomeStoreCreateRetrySemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		conflict  bool
		setupMock func(pgxmock.PgxPoolIface, models.AutomationRunOutcome)
	}{
		{
			name: "creates the first report transactionally",
			setupMock: func(mock pgxmock.PgxPoolIface, existing models.AutomationRunOutcome) {
				mock.ExpectBegin()
				mock.ExpectQuery(`INSERT INTO automation_run_outcomes`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(automationOutcomeRow(existing))
				mock.ExpectCommit()
			},
		},
		{
			name: "accepts an identical retry",
			setupMock: func(mock pgxmock.PgxPoolIface, existing models.AutomationRunOutcome) {
				mock.ExpectBegin()
				mock.ExpectQuery(`INSERT INTO automation_run_outcomes`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(automationOutcomeRowColumns()))
				mock.ExpectQuery(`SELECT[\s\S]*FROM automation_run_outcomes`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(automationOutcomeRow(existing))
				mock.ExpectQuery(`SELECT id, org_id, outcome_id[\s\S]*FROM automation_run_external_actions`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "outcome_id", "provider", "action_type", "external_id", "url", "verification_status", "created_at"}))
				mock.ExpectCommit()
			},
		},
		{
			name:     "rejects a conflicting retry",
			conflict: true,
			setupMock: func(mock pgxmock.PgxPoolIface, existing models.AutomationRunOutcome) {
				mock.ExpectBegin()
				mock.ExpectQuery(`INSERT INTO automation_run_outcomes`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(automationOutcomeRowColumns()))
				mock.ExpectQuery(`SELECT[\s\S]*FROM automation_run_outcomes`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(automationOutcomeRow(existing))
				mock.ExpectQuery(`SELECT id, org_id, outcome_id[\s\S]*FROM automation_run_external_actions`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "outcome_id", "provider", "action_type", "external_id", "url", "verification_status", "created_at"}))
				mock.ExpectRollback()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgx mock should initialize")
			defer mock.Close()
			now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
			existing := models.AutomationRunOutcome{
				ID: uuid.New(), OrgID: uuid.New(), AutomationID: uuid.New(), AutomationRunID: uuid.New(), SessionID: uuid.New(),
				Repository: "assembledhq/143", PullRequestNumber: 123, PullRequestURL: "https://github.com/assembledhq/143/pull/123",
				Decision: models.AutomationOutcomeDecisionPassed, Reason: "No blocking issues were found.",
				Source: models.AutomationOutcomeSourceAgentReported, ReportedAt: now, CreatedAt: now,
			}
			requested := existing
			requested.ID = uuid.Nil
			requested.ReportedAt = time.Time{}
			requested.CreatedAt = time.Time{}
			if tt.conflict {
				requested.Reason = "A different result was reported."
			}
			tt.setupMock(mock, existing)

			created, err := NewAutomationOutcomeStore(mock).Create(context.Background(), existing.OrgID, &requested, nil)
			if tt.conflict {
				require.ErrorIs(t, err, ErrAutomationOutcomeAlreadyReported, "conflicting retry should preserve the original audit record")
			} else {
				require.NoError(t, err, "first reports and identical retries should succeed")
				require.Equal(t, existing, created, "store should return the durable outcome record")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "transaction and retry expectations should be met")
		})
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
	expectedOutcome := models.AutomationRunOutcome{
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
	}
	attemptOutcomesJSON, err := json.Marshal([]models.AutomationRunOutcome{expectedOutcome})
	require.NoError(t, err, "attempt outcome history should encode as JSON")

	rows := pgxmock.NewRows(automationDecisionColumns()).AddRow(
		automationID, runID, &sessionID,
		"assembledhq/143", 123, "https://github.com/assembledhq/143/pull/123", stringPointer("Clarify outcomes"), stringPointer("abc123"),
		models.AutomationRunStatusCompleted, now, &now, int64(3),
		&outcomeID, &orgID, &automationID, &runID, &sessionID,
		stringPointer("assembledhq/143"), intPointer(123), stringPointer("https://github.com/assembledhq/143/pull/123"), stringPointer("Clarify outcomes"), stringPointer("abc123"),
		stringPointer(string(models.AutomationOutcomeDecisionChangesRequested)), stringPointer("A blocking migration issue was found."), stringPointer(string(models.AutomationOutcomeSourceAgentReported)), &now, &now,
		&actionID, &orgID, &outcomeID, stringPointer("github"), stringPointer(string(models.AutomationExternalActionGitHubReviewChangesRequested)), stringPointer("456"),
		stringPointer("https://github.com/assembledhq/143/pull/123#pullrequestreview-456"), stringPointer(string(models.AutomationExternalActionVerificationReported)), &now,
		attemptOutcomesJSON,
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
		Outcome:         &expectedOutcome,
		AttemptOutcomes: []models.AutomationRunOutcome{expectedOutcome},
	}}, decisions, "ListDecisions should preserve target, lifecycle, outcome, and external action as separate fields")
	require.Contains(t, automationDecisionTargetCTEs, "'https://github.com/'", "decision targets should reconstruct canonical URLs for historical snapshots")
	require.Contains(t, automationDecisionTargetCTEs, "target_outcome.head_sha", "decision grouping should use the agent-reported head SHA when the trigger snapshot predates revision capture")
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
	mock.ExpectQuery(`WITH raw_targeted AS[\s\S]*o.id IS NULL AND l.execution_status IN \('pending', 'running'\)`).
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

func TestAutomationOutcomeStoreListDecisionsOutcomeNotReportedFilter(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()
	orgID := uuid.New()
	automationID := uuid.New()
	mock.ExpectQuery(`WITH raw_targeted AS[\s\S]*o.id IS NULL AND l.execution_status NOT IN \('pending', 'running', 'failed'\)`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "automation_id": automationID}).
		WillReturnRows(pgxmock.NewRows(automationDecisionColumns()))

	decisions, err := NewAutomationOutcomeStore(mock).ListDecisions(context.Background(), orgID, automationID, AutomationDecisionFilters{
		Limit: 25, OutcomeNotReported: true,
	})
	require.NoError(t, err, "unreported-outcome filter should execute successfully")
	require.Equal(t, []models.AutomationDecision{}, decisions, "unreported-outcome filter should return only terminal runs without an outcome")
	require.NoError(t, mock.ExpectationsWereMet(), "unreported-outcome query should use the exclusive display-state predicate")
}

func TestAutomationOutcomeStoreListDecisionsDecisionFilterMatchesAnyAttempt(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()
	orgID := uuid.New()
	automationID := uuid.New()
	decision := models.AutomationOutcomeDecisionChangesRequested
	mock.ExpectQuery(`WITH raw_targeted AS[\s\S]*EXISTS \([\s\S]*filter_outcome.decision = @decision`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "automation_id": automationID, "decision": decision}).
		WillReturnRows(pgxmock.NewRows(automationDecisionColumns()))

	decisions, err := NewAutomationOutcomeStore(mock).ListDecisions(context.Background(), orgID, automationID, AutomationDecisionFilters{
		Limit: 25, Decision: &decision,
	})
	require.NoError(t, err, "decision filter should execute successfully")
	require.Equal(t, []models.AutomationDecision{}, decisions, "decision filter should search all reported attempts for each revision")
	require.NoError(t, mock.ExpectationsWereMet(), "decision filter should use the attempt-history predicate")
}

func TestAutomationOutcomeMigrationRequiresMatchingSummaryPR(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000248_automation_run_outcomes.up.sql")
	require.NoError(t, err, "structured outcome migration should be readable")
	sql := string(body)
	require.Contains(t, sql, "AS summary_pull_request_number", "backfill should retain the PR number parsed from the legacy summary")
	require.Contains(t, sql, "summary_pull_request_number = pull_request_number::text", "backfill should reject summaries for a different PR")
}

func stringPointer(value string) *string {
	return &value
}

func intPointer(value int) *int {
	return &value
}
