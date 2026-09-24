package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type assessmentFreshnessCapture struct {
	result  codereviewsvc.AssessmentInputCaptureResult
	request codereviewsvc.AssessmentInputCaptureRequest
}

func (f *assessmentFreshnessCapture) CaptureAssessmentInputs(_ context.Context, request codereviewsvc.AssessmentInputCaptureRequest) (codereviewsvc.AssessmentInputCaptureResult, error) {
	f.request = request
	return f.result, nil
}

func TestFullAssessmentCoverage(t *testing.T) {
	t.Parallel()
	policy := models.CodeReviewPolicyConfig{AgentRoster: models.CodeReviewAgentRoster{Reviewers: []models.AgentType{"claude"}, RequireReviewerQuorum: 1}}
	validReviewer := models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted, RawOutput: stringPtr("review complete"), StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{ReadOnly: true, ReviewerKey: codeReviewReviewerKey(0, "claude")})}
	synthesis := codeReviewOrchestratorSynthesis{Summary: "summary", ReviewSummary: "review summary", DescriptionAssessments: []codeReviewDescriptionAssessment{}, Findings: []codeReviewOrchestratorFinding{}, HumanReviewReasons: []codeReviewOrchestratorHumanReviewReason{}}
	validOrchestrator := models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted, StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{ThreadID: uuid.NewString(), Synthesis: synthesis, SynthesisValidated: true, ReadOnly: true})}
	tests := []struct {
		name          string
		reviewer      models.CodeReviewAgentResult
		orchestrator  models.CodeReviewAgentResult
		reuseEligible bool
		want          bool
	}{
		{"complete read-only panel", validReviewer, validOrchestrator, true, true},
		{"incomplete input manifest", validReviewer, validOrchestrator, false, false},
		{"failed reviewer", func() models.CodeReviewAgentResult {
			r := validReviewer
			r.Status = models.CodeReviewAgentResultStatusFailed
			return r
		}(), validOrchestrator, true, false},
		{"invalid synthesis", validReviewer, func() models.CodeReviewAgentResult {
			r := validOrchestrator
			r.StructuredResult = marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{Synthesis: synthesis, SynthesisValidated: false})
			return r
		}(), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := fullAssessmentCoverage(policy, []models.CodeReviewAgentResult{tt.reviewer, tt.orchestrator}, codereviewsvc.ReviewInputManifest{ReuseEligible: tt.reuseEligible})
			require.Equal(t, tt.want, got, "only complete validated input and reviewer coverage may be reused")
		})
	}
}

func TestFullAssessmentCoverageRankedFallback(t *testing.T) {
	t.Parallel()
	policy := models.CodeReviewPolicyConfig{AgentRoster: models.CodeReviewAgentRoster{Reviewers: []models.AgentType{"claude", "codex"}, ReviewerCount: 1, RequireReviewerQuorum: 1}}
	synthesis := codeReviewOrchestratorSynthesis{Summary: "summary", ReviewSummary: "review summary", DescriptionAssessments: []codeReviewDescriptionAssessment{}, Findings: []codeReviewOrchestratorFinding{}, HumanReviewReasons: []codeReviewOrchestratorHumanReviewReason{}}
	orchestrator := models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted, StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{ThreadID: uuid.NewString(), Synthesis: synthesis, SynthesisValidated: true, ReadOnly: true})}
	fallback := models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted, RawOutput: stringPtr("fallback review complete"), StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{ReadOnly: true, ReviewerKey: codeReviewReviewerKey(1, "codex")})}
	tests := []struct {
		name  string
		first models.CodeReviewAgentResult
		want  bool
	}{
		{name: "failed first candidate covered by fallback", first: models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{ReviewerKey: codeReviewReviewerKey(0, "claude"), Error: "provider unavailable"})}, want: true},
		{name: "write violation in failed candidate blocks reuse", first: models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{ReviewerKey: codeReviewReviewerKey(0, "claude"), ReadOnlyViolation: true})}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := fullAssessmentCoverage(policy, []models.CodeReviewAgentResult{tt.first, fallback, orchestrator}, codereviewsvc.ReviewInputManifest{ReuseEligible: true})
			require.Equal(t, tt.want, got, "ranked roster should reuse complete fallback coverage only when no write violation occurred")
		})
	}
}

func TestFullAssessmentCoverageRejectsUnsafeSelectedSynthesis(t *testing.T) {
	t.Parallel()
	policy := models.CodeReviewPolicyConfig{AgentRoster: models.CodeReviewAgentRoster{Reviewers: []models.AgentType{"claude"}, RequireReviewerQuorum: 1}}
	reviewer := models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted, RawOutput: stringPtr("review complete"), StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{ReadOnly: true, ReviewerKey: codeReviewReviewerKey(0, "claude")})}
	synthesis := codeReviewOrchestratorSynthesis{Summary: "summary", ReviewSummary: "review summary", DescriptionAssessments: []codeReviewDescriptionAssessment{}, Findings: []codeReviewOrchestratorFinding{}, HumanReviewReasons: []codeReviewOrchestratorHumanReviewReason{}}
	unsafe := models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted, StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{ThreadID: uuid.NewString(), Synthesis: synthesis, SynthesisValidated: true, ReadOnlyViolation: true})}
	safe := models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted, StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{ThreadID: uuid.NewString(), Synthesis: synthesis, SynthesisValidated: true, ReadOnly: true})}
	got := fullAssessmentCoverage(policy, []models.CodeReviewAgentResult{reviewer, unsafe, safe}, codereviewsvc.ReviewInputManifest{ReuseEligible: true})
	require.False(t, got, "a later safe orchestrator result cannot replace the first synthesis chosen for the full review")
}

func TestVerifyFullAssessmentFreshness(t *testing.T) {
	t.Parallel()
	orgID, repoID, prID, sessionID, assessmentID, policyID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	job := runCodeReviewPayload{OrgID: orgID, RepositoryID: repoID, PullRequestID: prID, SessionID: sessionID}
	manifest := codereviewsvc.ReviewInputManifest{InputVersion: codereviewsvc.ReviewInputManifestVersion, ReuseEligible: true, InputDigest: "all", CodeDigest: "code", ContractDigest: "contract", IntentDigest: "intent", VisualDigest: "visual", RequestDigest: "request", TextDigest: "text", GateDigest: "gates", Code: codereviewsvc.ReviewCodeInput{HeadSHA: "head", BaseSHA: "base", BaseRef: "main"}, TextEvidence: codereviewsvc.ReviewTextInput{Complete: true, SourceProvenanceComplete: true, Items: []codereviewsvc.ReviewTextEvidence{{Surface: "check_status", Content: "pending"}, {Surface: "pull_request_description", Content: "Adds a feature"}}}}
	manifestJSON, err := json.Marshal(manifest)
	require.NoError(t, err, "encode captured baseline")
	assessment := models.CodeReviewAssessment{ID: assessmentID, PolicyID: policyID, InputDigest: "all", InputManifest: manifestJSON, HeadSHA: "head", BaseSHA: "base", BaseRef: "main"}
	valid := codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: policyID}}
	tests := []struct {
		name   string
		change func(*codereviewsvc.AssessmentInputCaptureResult)
		want   error
	}{
		{name: "same captured input"},
		{name: "CI completion preserves full panel", change: func(r *codereviewsvc.AssessmentInputCaptureResult) {
			r.Manifest.InputDigest = "new"
			r.Manifest.GateDigest = "new"
			r.Manifest.TextDigest = "new"
			r.Manifest.TextEvidence.Items[0].Content = "success"
		}},
		{name: "changed visual evidence", change: func(r *codereviewsvc.AssessmentInputCaptureResult) { r.Manifest.VisualDigest = "new" }, want: errFullAssessmentInputsChanged},
		{name: "changed code", change: func(r *codereviewsvc.AssessmentInputCaptureResult) { r.Manifest.CodeDigest = "new" }, want: errFullAssessmentInputsChanged},
		{name: "changed contract", change: func(r *codereviewsvc.AssessmentInputCaptureResult) { r.Manifest.ContractDigest = "new" }, want: errFullAssessmentInputsChanged},
		{name: "changed description evidence", change: func(r *codereviewsvc.AssessmentInputCaptureResult) {
			r.Manifest.TextEvidence.Items[1].Content = "New proof"
		}, want: errFullAssessmentInputsChanged},
		{name: "changed policy", change: func(r *codereviewsvc.AssessmentInputCaptureResult) { r.Policy.ID = uuid.New() }, want: errFullAssessmentInputsChanged},
		{name: "missing manifest version", change: func(r *codereviewsvc.AssessmentInputCaptureResult) { r.Manifest.InputVersion = 0 }, want: errFullAssessmentInputsChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := valid
			result.Manifest.TextEvidence.Items = append([]codereviewsvc.ReviewTextEvidence(nil), valid.Manifest.TextEvidence.Items...)
			if tt.change != nil {
				tt.change(&result)
			}
			capture := &assessmentFreshnessCapture{result: result}
			err := verifyFullAssessmentFreshness(context.Background(), &Services{CodeReviewInputCapture: capture}, job, assessment)
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want, "changed source input should block publication")
			} else {
				require.NoError(t, err, "unchanged source input should permit publication")
			}
			require.True(t, capture.request.Fresh, "publication check must fetch fresh evidence")
			require.Equal(t, assessmentID, capture.request.AssessmentID, "freshness check must preserve assessment identity")
		})
	}
}

func TestFullAssessmentOutcomeDescriptionShape(t *testing.T) {
	t.Parallel()
	outcome, err := fullAssessmentOutcome(nil, nil, false)
	require.NoError(t, err, "early stop outcome should encode")
	var decoded struct {
		DescriptionAssessments []codeReviewDescriptionAssessment `json:"description_assessments"`
		CoverageComplete       bool                              `json:"coverage_complete"`
	}
	require.NoError(t, json.Unmarshal(outcome, &decoded), "outcome should decode with the admission contract")
	require.Equal(t, []codeReviewDescriptionAssessment{}, decoded.DescriptionAssessments, "early stop should record an empty assessment list")
	require.False(t, decoded.CoverageComplete, "early stop cannot become a reusable source baseline")
}
