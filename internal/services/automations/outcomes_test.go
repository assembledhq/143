package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeOutcomeRunStore struct {
	run models.AutomationRun
	err error
}

func (f *fakeOutcomeRunStore) GetByRunID(_ context.Context, _, _ uuid.UUID) (models.AutomationRun, error) {
	return f.run, f.err
}

type fakeOutcomeWriter struct {
	outcome *models.AutomationRunOutcome
	action  *models.AutomationRunExternalAction
	err     error
}

func (f *fakeOutcomeWriter) Create(_ context.Context, _ uuid.UUID, outcome *models.AutomationRunOutcome, action *models.AutomationRunExternalAction) (models.AutomationRunOutcome, error) {
	f.outcome = outcome
	f.action = action
	if f.err != nil {
		return models.AutomationRunOutcome{}, f.err
	}
	created := *outcome
	created.ID = uuid.New()
	created.ExternalAction = action
	return created, nil
}

func testGitHubOutcomeRun() models.AutomationRun {
	return models.AutomationRun{
		ID: uuid.New(), AutomationID: uuid.New(), OrgID: uuid.New(),
		TriggeredBy:    models.AutomationTriggeredByGitHub,
		ConfigSnapshot: json.RawMessage(`{"github":{"repository":"assembledhq/143","pull_request_number":123,"pull_request_url":"https://github.com/assembledhq/143/pull/123","pull_request_title":"Clarify outcomes","head_sha":"abc123"}}`),
	}
}

func TestOutcomeServiceReport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mutateRun   func(*models.AutomationRun)
		req         ReportOutcomeRequest
		expectedErr error
		wantAction  bool
	}{
		{
			name: "passed without external action",
			req:  ReportOutcomeRequest{Decision: models.AutomationOutcomeDecisionPassed, Reason: "No blocking issues were found."},
		},
		{
			name: "missing snapshot URL uses canonical target",
			mutateRun: func(run *models.AutomationRun) {
				run.ConfigSnapshot = json.RawMessage(`{"github":{"repository":"assembledhq/143","pull_request_number":123}}`)
			},
			req: ReportOutcomeRequest{Decision: models.AutomationOutcomeDecisionPassed, Reason: "No blocking issues were found."},
		},
		{
			name: "changes requested with linked review",
			req: ReportOutcomeRequest{
				Decision: models.AutomationOutcomeDecisionChangesRequested, Reason: "The migration is unsafe.",
				ExternalActionType: models.AutomationExternalActionGitHubReviewChangesRequested,
				ExternalActionURL:  "https://github.com/assembledhq/143/pull/123#pullrequestreview-456",
				ExternalActionID:   "456",
			},
			wantAction: true,
		},
		{
			name:        "changes requested without review is rejected",
			req:         ReportOutcomeRequest{Decision: models.AutomationOutcomeDecisionChangesRequested, Reason: "Blocking issue."},
			expectedErr: ErrOutcomeActionRequired,
		},
		{
			name: "action for another PR is rejected",
			req: ReportOutcomeRequest{
				Decision: models.AutomationOutcomeDecisionAdvisory, Reason: "Consider a test.",
				ExternalActionType: models.AutomationExternalActionGitHubComment,
				ExternalActionURL:  "https://github.com/assembledhq/143/pull/999#issuecomment-1",
			},
			expectedErr: ErrOutcomeActionInvalid,
		},
		{
			name:        "blank reason is rejected",
			req:         ReportOutcomeRequest{Decision: models.AutomationOutcomeDecisionPassed, Reason: "  "},
			expectedErr: ErrOutcomeReasonRequired,
		},
		{
			name:        "non github run is rejected",
			mutateRun:   func(run *models.AutomationRun) { run.TriggeredBy = models.AutomationTriggeredByManual },
			req:         ReportOutcomeRequest{Decision: models.AutomationOutcomeDecisionPassed, Reason: "Complete."},
			expectedErr: ErrOutcomeTargetUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			run := testGitHubOutcomeRun()
			if tt.mutateRun != nil {
				tt.mutateRun(&run)
			}
			tt.req.SessionID = uuid.New()
			tt.req.RunID = run.ID
			writer := &fakeOutcomeWriter{}
			service := NewOutcomeService(&fakeOutcomeRunStore{run: run}, writer)
			outcome, err := service.Report(context.Background(), run.OrgID, tt.req)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr, "Report should return the expected validation error")
				require.Nil(t, writer.outcome, "invalid reports should not be persisted")
				return
			}
			require.NoError(t, err, "Report should persist a valid structured outcome")
			require.Equal(t, run.ID, outcome.AutomationRunID, "outcome should remain linked to the triggering run")
			require.Equal(t, "assembledhq/143", outcome.Repository, "outcome should derive the repository from the run snapshot")
			require.Equal(t, 123, outcome.PullRequestNumber, "outcome should derive the PR number from the run snapshot")
			require.Equal(t, "https://github.com/assembledhq/143/pull/123", outcome.PullRequestURL, "outcome should preserve or reconstruct the canonical PR URL")
			require.Equal(t, tt.wantAction, outcome.ExternalAction != nil, "outcome should persist an action only when one was reported")
		})
	}
}

func TestOutcomeServiceReportMapsConflictingRetry(t *testing.T) {
	t.Parallel()
	run := testGitHubOutcomeRun()
	writer := &fakeOutcomeWriter{err: fmt.Errorf("write failed: %w", db.ErrAutomationOutcomeAlreadyReported)}
	service := NewOutcomeService(&fakeOutcomeRunStore{run: run}, writer)
	_, err := service.Report(context.Background(), run.OrgID, ReportOutcomeRequest{
		SessionID: uuid.New(), RunID: run.ID, Decision: models.AutomationOutcomeDecisionPassed, Reason: "Passed.",
	})
	require.ErrorIs(t, err, ErrOutcomeAlreadyReported, "conflicting retries should surface a stable service error")
}
