package worker

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codereview"
)

func publisherRenderSignal(render models.CodeReviewUIRenderChange) *models.CodeReviewRiskTierSignal {
	return workerRiskTierSignal(models.CodeReviewRiskTierHigh, render)
}

func publisherAssessment(key string, status codeReviewDescriptionAssessmentStatus, basis models.CodeReviewDescriptionEvidenceBasis) codeReviewDescriptionAssessment {
	return codeReviewDescriptionAssessment{Key: key, Status: status, EvidenceBasis: basis, EvidenceIDs: []string{}, Reason: "the coding agent reviewed " + key}
}

func TestCodeReviewDescriptionEvaluationHonorsPublisherRenderChange(t *testing.T) {
	t.Parallel()
	policy := models.DefaultCodeReviewPolicyConfig()
	uiFiles := []codereview.PullRequestFile{{Filename: "frontend/src/app/page.tsx", Additions: 2}}
	backendFiles := []codereview.PullRequestFile{{Filename: "internal/api/review.go", Additions: 2}}
	described := publisherAssessment("description", codeReviewDescriptionAssessmentSatisfied, models.CodeReviewDescriptionEvidenceBasisPullRequestDescription)
	uiNotApplicable := publisherAssessment("ui_evidence", codeReviewDescriptionAssessmentNotApplicable, models.CodeReviewDescriptionEvidenceBasisNotApplicable)
	uiPreview := publisherAssessment("ui_evidence", codeReviewDescriptionAssessmentSatisfied, models.CodeReviewDescriptionEvidenceBasisPreviewLink)
	tests := []struct {
		name        string
		files       []codereview.PullRequestFile
		signal      *models.CodeReviewRiskTierSignal
		assessments []codeReviewDescriptionAssessment
		expected    codeReviewDescriptionEvaluation
		expectError bool
	}{
		{
			name: "true rejects a not-applicable screenshot assessment", files: backendFiles, signal: publisherRenderSignal(models.CodeReviewUIRenderChangeTrue),
			assessments: []codeReviewDescriptionAssessment{described, uiNotApplicable},
			expected: codeReviewDescriptionEvaluation{RequirementSummaries: []string{
				"Understandable description: passed (the coding agent reviewed description)",
				"Screenshots or preview link: failed (the risk-tier check reports a rendered UI change, so not applicable is not accepted: the coding agent reviewed ui_evidence)",
			}},
		},
		{
			name: "true accepts visual evidence", files: backendFiles, signal: publisherRenderSignal(models.CodeReviewUIRenderChangeTrue),
			assessments: []codeReviewDescriptionAssessment{described, uiPreview},
			expected: codeReviewDescriptionEvaluation{Passed: true, RequirementSummaries: []string{
				"Understandable description: passed (the coding agent reviewed description)",
				"Screenshots or preview link: passed (the coding agent reviewed ui_evidence)",
			}},
		},
		{
			name: "true requires an assessment even outside the fallback paths", files: backendFiles, signal: publisherRenderSignal(models.CodeReviewUIRenderChangeTrue),
			assessments: []codeReviewDescriptionAssessment{described}, expectError: true,
		},
		{
			name: "false passes UI files without screenshots", files: uiFiles, signal: publisherRenderSignal(models.CodeReviewUIRenderChangeFalse),
			assessments: []codeReviewDescriptionAssessment{described},
			expected:    codeReviewDescriptionEvaluation{Passed: true, RequirementSummaries: []string{"Understandable description: passed (the coding agent reviewed description)"}},
		},
		{
			name: "false rejects an assessment of the exempt requirement", files: uiFiles, signal: publisherRenderSignal(models.CodeReviewUIRenderChangeFalse),
			assessments: []codeReviewDescriptionAssessment{described, uiPreview}, expectError: true,
		},
		{
			name: "unknown keeps today's not-applicable judgment", files: uiFiles, signal: publisherRenderSignal(models.CodeReviewUIRenderChangeUnknown),
			assessments: []codeReviewDescriptionAssessment{described, uiNotApplicable},
			expected: codeReviewDescriptionEvaluation{Passed: true, RequirementSummaries: []string{
				"Understandable description: passed (the coding agent reviewed description)",
				"Screenshots or preview link: passed (not applicable: the coding agent reviewed ui_evidence)",
			}},
		},
		{
			name: "disabled setting keeps today's not-applicable judgment", files: uiFiles, signal: &models.CodeReviewRiskTierSignal{},
			assessments: []codeReviewDescriptionAssessment{described, uiNotApplicable},
			expected: codeReviewDescriptionEvaluation{Passed: true, RequirementSummaries: []string{
				"Understandable description: passed (the coding agent reviewed description)",
				"Screenshots or preview link: passed (not applicable: the coding agent reviewed ui_evidence)",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			evaluation, err := codeReviewDescriptionEvaluationFromSynthesis(policy, tt.files, tt.signal, codeReviewOrchestratorSynthesis{DescriptionAssessments: tt.assessments}, models.CodeReviewVisualEvidenceSnapshot{})
			if tt.expectError {
				require.Error(t, err, "the orchestrator must assess exactly the publisher-adjusted requirement set")
				return
			}
			require.NoError(t, err, "a complete assessment should evaluate")
			require.Equal(t, tt.expected, evaluation, "the description policy should follow the authoritative render change")
		})
	}
}

func TestCodeReviewDescriptionRequirementsForPromptLabelsPublisherDecisions(t *testing.T) {
	t.Parallel()
	policy := models.DefaultCodeReviewPolicyConfig()
	policy.DescriptionPolicy.Requirements = append(policy.DescriptionPolicy.Requirements, models.CodeReviewDescriptionRequirement{
		Key: "custom_visual", Title: "Custom visual", Required: true, EvidenceKind: models.CodeReviewDescriptionEvidenceKindVisual, Prompt: "Show the screen.",
		AppliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityPublisher, Fallback: &models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"**/*.tsx"}}},
	})
	uiFiles := []codereview.PullRequestFile{{Filename: "frontend/src/app/page.tsx", Additions: 2}}
	tests := []struct {
		name     string
		signal   *models.CodeReviewRiskTierSignal
		expected map[string]string
	}{
		{name: "fallback keeps today's labels", signal: nil, expected: map[string]string{"description": "all", "ui_evidence": "paths", "custom_visual": "paths"}},
		{name: "unknown keeps today's labels", signal: publisherRenderSignal(models.CodeReviewUIRenderChangeUnknown), expected: map[string]string{"description": "all", "ui_evidence": "paths", "custom_visual": "paths"}},
		{name: "true labels publisher-required requirements", signal: publisherRenderSignal(models.CodeReviewUIRenderChangeTrue), expected: map[string]string{"description": "all", "ui_evidence": "publisher_result", "custom_visual": "publisher_result"}},
		{name: "false omits publisher-exempt requirements", signal: publisherRenderSignal(models.CodeReviewUIRenderChangeFalse), expected: map[string]string{"description": "all"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			labels := map[string]string{}
			for _, requirement := range codeReviewDescriptionRequirementsForPrompt(policy, uiFiles, tt.signal) {
				labels[requirement.Key] = requirement.Applicability
			}
			require.Equal(t, tt.expected, labels, "the orchestrator prompt should list the publisher-adjusted requirements with their effective applicability")
		})
	}
}

func TestRecheckRequirementsKeepPublisherRequiredEvidence(t *testing.T) {
	t.Parallel()
	policy := models.DefaultCodeReviewPolicyConfig()
	backendFiles := []codereview.PullRequestFile{{Filename: "internal/api/review.go", Additions: 2}}
	uiFiles := []codereview.PullRequestFile{{Filename: "frontend/src/app/page.tsx", Additions: 2}}
	baseline := codeReviewOrchestratorSynthesis{DescriptionAssessments: []codeReviewDescriptionAssessment{
		publisherAssessment("description", codeReviewDescriptionAssessmentSatisfied, models.CodeReviewDescriptionEvidenceBasisPullRequestDescription),
		publisherAssessment("ui_evidence", codeReviewDescriptionAssessmentNotApplicable, models.CodeReviewDescriptionEvidenceBasisNotApplicable),
	}}
	tests := []struct {
		name     string
		files    []codereview.PullRequestFile
		signal   *models.CodeReviewRiskTierSignal
		expected []string
	}{
		{name: "true reassesses a not-applicable screenshot requirement", files: backendFiles, signal: publisherRenderSignal(models.CodeReviewUIRenderChangeTrue), expected: []string{"description", "ui_evidence"}},
		{name: "fallback skips a not-applicable screenshot requirement", files: uiFiles, signal: nil, expected: []string{"description"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requirements, err := recheckRequirements(policy, tt.files, tt.signal, baseline)
			require.NoError(t, err, "a complete baseline should yield recheck requirements")
			keys := make([]string, 0, len(requirements))
			for _, requirement := range requirements {
				keys = append(keys, requirement.Key)
			}
			require.Equal(t, tt.expected, keys, "an evidence recheck should reassess every requirement the publisher still requires")
		})
	}
}

func TestFullAssessmentOutcomeRecordsActiveRiskTierSignal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		signal   *models.CodeReviewRiskTierSignal
		expected string
	}{
		{name: "no signal keeps today's outcome", signal: nil, expected: `{"description_assessments":[],"risk_reasons":null,"coverage_complete":true}`},
		{name: "disabled signal keeps today's outcome", signal: &models.CodeReviewRiskTierSignal{}, expected: `{"description_assessments":[],"risk_reasons":null,"coverage_complete":true}`},
		{name: "active signal is recorded for recheck planning", signal: publisherRenderSignal(models.CodeReviewUIRenderChangeFalse), expected: func() string {
			encoded, err := json.Marshal(publisherRenderSignal(models.CodeReviewUIRenderChangeFalse))
			require.NoError(t, err, "signal fixture should encode")
			return `{"description_assessments":[],"risk_reasons":null,"coverage_complete":true,"risk_tier_signal":` + string(encoded) + `}`
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcome, err := fullAssessmentOutcome(nil, nil, true, tt.signal)
			require.NoError(t, err, "the outcome should encode")
			require.JSONEq(t, tt.expected, string(outcome), "the structured outcome should carry the signal only when the setting is active")
		})
	}
}

func TestEvaluateLiveCodeReviewOutcomeFollowsPublisherRenderChange(t *testing.T) {
	t.Parallel()
	policy := models.DefaultCodeReviewPolicyConfig()
	policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	uiFiles := []codereview.PullRequestFile{{Filename: "frontend/src/components/Button.tsx", Additions: 3, Deletions: 1}}
	tests := []struct {
		name     string
		signal   *models.CodeReviewRiskTierSignal
		statuses map[string]codeReviewDescriptionAssessmentStatus
		expected models.CodeReviewDecision
	}{
		{name: "false approves a UI file change without screenshots", signal: publisherRenderSignal(models.CodeReviewUIRenderChangeFalse), expected: models.CodeReviewDecisionApproved},
		{name: "true holds a not-applicable screenshot judgment", signal: publisherRenderSignal(models.CodeReviewUIRenderChangeTrue), statuses: map[string]codeReviewDescriptionAssessmentStatus{"ui_evidence": codeReviewDescriptionAssessmentNotApplicable}, expected: models.CodeReviewDecisionNeedsHumanReview},
		{name: "unknown keeps today's not-applicable judgment", signal: publisherRenderSignal(models.CodeReviewUIRenderChangeUnknown), statuses: map[string]codeReviewDescriptionAssessmentStatus{"ui_evidence": codeReviewDescriptionAssessmentNotApplicable}, expected: models.CodeReviewDecisionApproved},
		{name: "disabled setting keeps today's not-applicable judgment", signal: &models.CodeReviewRiskTierSignal{}, statuses: map[string]codeReviewDescriptionAssessmentStatus{"ui_evidence": codeReviewDescriptionAssessmentNotApplicable}, expected: models.CodeReviewDecisionApproved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			body := "Adjusts the button label.\n\nTesting: unit tests."
			input := liveCodeReviewOutcomeInput{
				Policy: policy, Job: runCodeReviewPayload{OrgID: orgID, SessionID: uuid.New(), HeadSHA: "head", RiskTierSignal: tt.signal},
				PullRequest: models.PullRequest{OrgID: orgID, Body: &body, HeadSHA: stringPtr("head"), Status: models.PullRequestStatusOpen, AuthoredBy: models.GitIdentitySourceUser},
				Health:      &models.PullRequestHealthResponse{HeadSHA: "head", Status: models.PullRequestStatusOpen, CanMerge: true, ChecksConfirmed: true, MergeState: models.PullRequestMergeStateClean},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: uiFiles, ChangedFilesAvailable: true,
			}
			input = withRiskTierSynthesisStatuses(input, tt.statuses)
			decision, _ := evaluateLiveCodeReviewOutcome(input)
			require.Equal(t, tt.expected, decision.Decision, "the screenshot requirement should follow the authoritative render change")
			if tt.expected == models.CodeReviewDecisionNeedsHumanReview {
				require.Contains(t, decision.RiskReasonDetails, models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonDescriptionFailed}, "a held screenshot requirement should fail the description policy")
			}
		})
	}
}

func withRiskTierSynthesisStatuses(input liveCodeReviewOutcomeInput, statuses map[string]codeReviewDescriptionAssessmentStatus) liveCodeReviewOutcomeInput {
	input = withApprovingRiskTierSynthesis(input)
	synthesis := input.OrchestratorSynthesis
	assessments := make([]codeReviewDescriptionAssessment, 0, len(synthesis.DescriptionAssessments))
	for _, assessment := range synthesis.DescriptionAssessments {
		if status, ok := statuses[assessment.Key]; ok && status == codeReviewDescriptionAssessmentNotApplicable {
			assessment.Status, assessment.EvidenceBasis = status, models.CodeReviewDescriptionEvidenceBasisNotApplicable
		} else if assessment.Key == "ui_evidence" {
			assessment.EvidenceBasis = models.CodeReviewDescriptionEvidenceBasisPreviewLink
		}
		assessments = append(assessments, assessment)
	}
	synthesis.DescriptionAssessments = assessments
	input.OrchestratorSynthesis = synthesis
	results := append([]models.CodeReviewAgentResult{}, input.AgentResults[:len(input.AgentResults)-1]...)
	input.AgentResults = append(results, models.CodeReviewAgentResult{
		Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted,
		StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{DescriptionInputHash: synthesis.DescriptionInputHash, SynthesisValidated: true, Synthesis: synthesis}),
	})
	return input
}

func TestLiveRescoreToRenderChangeRequiresScreenshots(t *testing.T) {
	t.Parallel()
	policy := models.DefaultCodeReviewPolicyConfig()
	policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	backendFiles := []codereview.PullRequestFile{{Filename: "internal/api/review.go", Additions: 3, Deletions: 1}}
	checkpoint := publisherRenderSignal(models.CodeReviewUIRenderChangeFalse)
	live := models.ResolveCodeReviewRiskTierSignal(workerRiskTierSetting(workerRiskTierAppID), workerRiskTierHead, []models.CodeReviewCheckRun{workerRiskTierRun(5, workerRiskTierAppID, workerRiskTierHead, "high", "true")})
	tests := []struct {
		name     string
		decision *models.CodeReviewRiskTierSignal
		expected models.CodeReviewDecision
	}{
		{name: "checkpoint alone approves without screenshots", decision: checkpoint, expected: models.CodeReviewDecisionApproved},
		{name: "live rescore to true holds the assessed synthesis", decision: func() *models.CodeReviewRiskTierSignal { combined := checkpoint.WithLive(live); return &combined }(), expected: models.CodeReviewDecisionNeedsHumanReview},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			body := "Tightens request validation.\n\nTesting: unit tests."
			input := liveCodeReviewOutcomeInput{
				Policy: policy, Job: runCodeReviewPayload{OrgID: orgID, SessionID: uuid.New(), HeadSHA: "head", RiskTierSignal: checkpoint},
				PullRequest: models.PullRequest{OrgID: orgID, Body: &body, HeadSHA: stringPtr("head"), Status: models.PullRequestStatusOpen, AuthoredBy: models.GitIdentitySourceUser},
				Health:      &models.PullRequestHealthResponse{HeadSHA: "head", Status: models.PullRequestStatusOpen, CanMerge: true, ChecksConfirmed: true, MergeState: models.PullRequestMergeStateClean},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: backendFiles, ChangedFilesAvailable: true,
			}
			input = withApprovingRiskTierSynthesis(input)
			input.Job.RiskTierSignal = tt.decision
			decision, _ := evaluateLiveCodeReviewOutcome(input)
			require.Equal(t, tt.expected, decision.Decision, "a live render-change rescore after synthesis should hold the review for screenshots")
		})
	}
}

func TestCodeReviewRiskTierRenderChangeHolds(t *testing.T) {
	t.Parallel()
	signal := func(render models.CodeReviewUIRenderChange) *models.CodeReviewRiskTierSignal {
		return publisherRenderSignal(render)
	}
	unreadable := func() *models.CodeReviewRiskTierSignal {
		combined := signal(models.CodeReviewUIRenderChangeFalse).WithLive(models.CodeReviewRiskTierLookupFailedSignal(workerRiskTierSetting(workerRiskTierAppID), workerRiskTierHead))
		return &combined
	}()
	tests := []struct {
		name    string
		staged  *models.CodeReviewRiskTierSignal
		current *models.CodeReviewRiskTierSignal
		holds   bool
	}{
		{name: "unchanged false", staged: signal(models.CodeReviewUIRenderChangeFalse), current: signal(models.CodeReviewUIRenderChangeFalse), holds: true},
		{name: "unchanged true", staged: signal(models.CodeReviewUIRenderChangeTrue), current: signal(models.CodeReviewUIRenderChangeTrue), holds: true},
		{name: "true relaxed to unknown", staged: signal(models.CodeReviewUIRenderChangeTrue), current: signal(models.CodeReviewUIRenderChangeUnknown), holds: true},
		{name: "unknown relaxed to false", staged: signal(models.CodeReviewUIRenderChangeUnknown), current: signal(models.CodeReviewUIRenderChangeFalse), holds: true},
		{name: "no staged signal and disabled now", current: &models.CodeReviewRiskTierSignal{}, holds: true},
		{name: "false rescored to true", staged: signal(models.CodeReviewUIRenderChangeFalse), current: signal(models.CodeReviewUIRenderChangeTrue)},
		{name: "unknown rescored to true", staged: signal(models.CodeReviewUIRenderChangeUnknown), current: signal(models.CodeReviewUIRenderChangeTrue)},
		{name: "false rescored to unknown", staged: signal(models.CodeReviewUIRenderChangeFalse), current: signal(models.CodeReviewUIRenderChangeUnknown)},
		{name: "false exemption becomes unreadable", staged: signal(models.CodeReviewUIRenderChangeFalse), current: unreadable},
		{name: "false exemption disabled", staged: signal(models.CodeReviewUIRenderChangeFalse), current: &models.CodeReviewRiskTierSignal{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			holds, detail := codeReviewRiskTierRenderChangeHolds(tt.staged, tt.current)
			require.Equal(t, tt.holds, holds, "a staged approval should hold only while no screenshot requirement was added or lost its exemption")
			require.Equal(t, tt.holds, detail == "", "a withheld approval should explain the change")
		})
	}
}
