package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestVerifyFullAssessmentApprovalUsesFreshGatesAndOriginalFindings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		checkStatus models.PullRequestCheckStatus
		finding     bool
		wantError   bool
	}{
		{name: "green current checks preserve staged approval", checkStatus: models.PullRequestCheckStatusPassed},
		{name: "fresh failing check blocks staged approval", checkStatus: models.PullRequestCheckStatusFailed, wantError: true},
		{name: "original high finding blocks staged approval", checkStatus: models.PullRequestCheckStatusPassed, finding: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID := uuid.New(), uuid.New()
			prBody := "Fixes invoice rounding. Testing: go test ./..."
			pr := models.PullRequest{OrgID: orgID, Body: &prBody, HeadSHA: stringPtr("head"), Status: models.PullRequestStatusOpen}
			visual := models.CodeReviewVisualEvidenceSnapshot{Complete: true}
			policy := models.DefaultCodeReviewPolicyConfig()
			policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
			policy.RiskPolicy.RequirePassingChecks = true
			policy.AgentRoster.Reviewers = []models.AgentType{models.AgentTypeCodex}
			policy.AgentRoster.ReviewerCount = 1
			policy.AgentRoster.RequireReviewerQuorum = 1
			policy.DescriptionPolicy.Requirements = []models.CodeReviewDescriptionRequirement{{Key: "description", Title: "Description", Required: true, EvidenceKind: models.CodeReviewDescriptionEvidenceKindGeneral, Prompt: "Explain the change"}}
			synthesis := codeReviewOrchestratorSynthesis{
				Summary: "Code is sound", ReviewSummary: "No blocking issues", ApprovalRecommended: true,
				DescriptionInputHash:   codeReviewDescriptionInputHash(pr, visual),
				DescriptionAssessments: []codeReviewDescriptionAssessment{{Key: "description", Status: codeReviewDescriptionAssessmentSatisfied, EvidenceBasis: models.CodeReviewDescriptionEvidenceBasisPullRequestDescription, Reason: "The description explains the rounding change"}},
				RiskNotes:              []string{}, Findings: []codeReviewOrchestratorFinding{}, HumanReviewReasons: []codeReviewOrchestratorHumanReviewReason{},
			}
			structured := marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{DescriptionInputHash: synthesis.DescriptionInputHash, SynthesisValidated: true, Synthesis: synthesis})
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated database mock")
			defer mock.Close()
			now := time.Now().UTC()
			resultRows := pgxmock.NewRows([]string{"id", "org_id", "session_id", "agent_provider", "agent_model", "role", "status", "raw_output", "structured_result", "created_at"}).
				AddRow(uuid.New(), orgID, sessionID, "codex", nil, string(models.CodeReviewAgentRoleReviewer), string(models.CodeReviewAgentResultStatusCompleted), nil, []byte(`{}`), now).
				AddRow(uuid.New(), orgID, sessionID, "codex", nil, string(models.CodeReviewAgentRoleOrchestrator), string(models.CodeReviewAgentResultStatusCompleted), nil, []byte(structured), now.Add(time.Second))
			mock.ExpectQuery("FROM code_review_agent_results").WithArgs(orgID, sessionID).WillReturnRows(resultRows)
			findingRows := pgxmock.NewRows([]string{"id", "org_id", "session_id", "agent_result_id", "dedupe_key", "severity", "confidence", "path", "start_line", "end_line", "summary", "body", "selected_for_inline", "github_comment_id", "created_at"})
			if tt.finding {
				findingRows.AddRow(uuid.New(), orgID, sessionID, nil, "high-finding", "high", "high", nil, nil, nil, "Unresolved correctness issue", "Original reviewer concern", false, nil, now)
			}
			mock.ExpectQuery("FROM code_review_findings").WithArgs(orgID, sessionID, false).WillReturnRows(findingRows)
			fresh := codereviewsvc.AssessmentInputCaptureResult{
				Policy:         models.CodeReviewPolicyRecord{Enabled: true, ApprovalMode: policy.ApprovalMode, RiskPolicy: policy.RiskPolicy, AgentRoster: policy.AgentRoster, DescriptionPolicy: policy.DescriptionPolicy},
				PullRequest:    pr,
				Health:         &models.PullRequestHealthResponse{HeadSHA: "head", Status: models.PullRequestStatusOpen, CanMerge: true, ChecksConfirmed: true, Checks: []models.PullRequestCheckSummary{{Name: "tests", Status: tt.checkStatus}}, MergeState: models.PullRequestMergeStateClean},
				Files:          []codereviewsvc.PullRequestFile{{Filename: "internal/invoices.go", Additions: 3, Deletions: 1}},
				VisualEvidence: visual,
			}
			stores := &Stores{CodeReviews: db.NewCodeReviewStore(mock)}
			job := runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 1, HeadSHA: "head"}
			err = verifyFullAssessmentApproval(context.Background(), stores, &Services{}, job, fresh)
			if tt.wantError {
				require.ErrorIs(t, err, errFullAssessmentInputsChanged, "fresh backend gate or original blocker must prevent staged approval")
			} else {
				require.False(t, errors.Is(err, errFullAssessmentInputsChanged), "clean original panel and fresh gates must keep approval eligible")
				require.NoError(t, err, "clean original panel should be approvable at publication")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all original result and finding reads should be tenant scoped")
		})
	}
}
