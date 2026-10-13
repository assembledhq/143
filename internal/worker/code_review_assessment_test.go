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
	result   codereviewsvc.AssessmentInputCaptureResult
	request  codereviewsvc.AssessmentInputCaptureRequest
	requests []codereviewsvc.AssessmentInputCaptureRequest
}

func (f *assessmentFreshnessCapture) CaptureAssessmentInputs(_ context.Context, request codereviewsvc.AssessmentInputCaptureRequest) (codereviewsvc.AssessmentInputCaptureResult, error) {
	f.request = request
	f.requests = append(f.requests, request)
	return f.result, nil
}

type assessmentVisualCheckpoint struct {
	snapshot models.CodeReviewVisualEvidenceSnapshot
	requests []codereviewsvc.CaptureVisualEvidenceInput
}

func (f *assessmentVisualCheckpoint) Capture(_ context.Context, request codereviewsvc.CaptureVisualEvidenceInput) (models.CodeReviewVisualEvidenceSnapshot, error) {
	f.requests = append(f.requests, request)
	return f.snapshot, nil
}

func TestFullAssessmentAvailabilityFreshness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                                string
		before, after                                                       models.CodeReviewVisualEvidenceFetchStatus
		change                                                              func(*codereviewsvc.AssessmentInputCaptureResult)
		retry                                                               bool
		unchanged, corruptCheckpoint, missingCheckpoint, simultaneousChange bool
	}{
		{name: "available bytes temporarily unavailable", before: "available", after: "unavailable", retry: true},
		{name: "unavailable failure reason changes", before: "unavailable", after: "unavailable", retry: true},
		{name: "same unavailable reason preserves captured input", before: "unavailable", after: "unavailable", unchanged: true},
		{name: "restored original bytes preserve captured input", before: "available", after: "available", unchanged: true},
		{name: "corrupt immutable checkpoint remains bounded", before: "available", after: "unavailable", corruptCheckpoint: true, retry: true},
		{name: "missing immutable checkpoint remains bounded", before: "available", after: "unavailable", missingCheckpoint: true, retry: true},
		{name: "known available content change wins over another unavailable image", before: "available", after: "unavailable", simultaneousChange: true},
		{name: "newly available bytes require new analysis", before: "unavailable", after: "available"},
		{name: "different available bytes require new analysis", before: "available", after: "available"},
		{name: "changed caption alongside failure requires new analysis", before: "available", after: "unavailable", change: func(r *codereviewsvc.AssessmentInputCaptureResult) {
			r.Manifest.Visual.Images[0].SourceText = "New caption"
		}},
		{name: "changed alt text requires new analysis", before: "available", after: "unavailable", change: func(r *codereviewsvc.AssessmentInputCaptureResult) { r.Manifest.Visual.Images[0].AltText = "New alt" }},
		{name: "changed source requires new analysis", before: "available", after: "unavailable", change: func(r *codereviewsvc.AssessmentInputCaptureResult) {
			r.Manifest.Visual.Images[0].SourceURL = "https://example.test/new"
		}},
		{name: "changed code alongside failure requires new analysis", before: "available", after: "unavailable", change: func(r *codereviewsvc.AssessmentInputCaptureResult) { r.Manifest.CodeDigest = "changed" }},
		{name: "changed request alongside failure requires new analysis", before: "available", after: "unavailable", change: func(r *codereviewsvc.AssessmentInputCaptureResult) { r.Manifest.RequestDigest = "changed" }},
		{name: "changed policy alongside failure requires new analysis", before: "available", after: "unavailable", change: func(r *codereviewsvc.AssessmentInputCaptureResult) { r.Policy.ID = uuid.New() }},
		{name: "changed discussion alongside failure requires new analysis", before: "available", after: "unavailable", change: func(r *codereviewsvc.AssessmentInputCaptureResult) {
			r.Manifest.TextEvidence.Items[0].Content = "New request"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, policyID := uuid.New(), uuid.New()
			manifest := codereviewsvc.ReviewInputManifest{InputVersion: codereviewsvc.ReviewInputManifestVersion, ReuseEligible: true, InputDigest: "input", CodeDigest: "code", ContractDigest: "contract", IntentDigest: "intent", VisualDigest: "visual", RequestDigest: "request", Code: codereviewsvc.ReviewCodeInput{HeadSHA: "head", BaseSHA: "base", BaseRef: "main"}, Visual: codereviewsvc.ReviewVisualInput{CaptureComplete: true, SourceProvenanceComplete: true, Images: []codereviewsvc.ReviewVisualImage{{SourceID: "image-1", SourceURL: "https://example.test/image?token=secret", SourceText: "Original caption", AltText: "Original alt", ContentDigest: "original"}}}, TextEvidence: codereviewsvc.ReviewTextInput{Items: []codereviewsvc.ReviewTextEvidence{{Surface: "issue_comment", Content: "Original request"}}}}
			visual := models.CodeReviewVisualEvidenceSnapshot{AssessmentID: &id, Complete: true, HeadSHA: "head", Evidence: []models.CodeReviewVisualEvidence{{Source: models.CodeReviewVisualEvidenceSource{SourceID: "image-1", SourceURL: "https://example.test/image?token=secret", ContextText: "Original caption", AltText: "Original alt"}, ContentSHA256: "original", Status: tt.before}}}
			if tt.simultaneousChange {
				visual.Evidence = append(visual.Evidence, models.CodeReviewVisualEvidence{Source: models.CodeReviewVisualEvidenceSource{SourceID: "image-2", SourceURL: "https://example.test/second"}, ContentSHA256: "original-second", Status: "available"})
			}
			manifest.Visual = codereviewsvc.AssessmentVisualInput(visual)
			raw, err := json.Marshal(manifest)
			require.NoError(t, err, "encode immutable analysis input")
			assessment := models.CodeReviewAssessment{ID: id, PolicyID: policyID, InputManifest: raw, InputDigest: "input", HeadSHA: "head", BaseSHA: "base", BaseRef: "main"}
			checkpoint := codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: policyID}, VisualEvidence: visual}
			fresh := checkpoint
			fresh.Manifest.Visual.Images = append([]codereviewsvc.ReviewVisualImage(nil), manifest.Visual.Images...)
			fresh.Manifest.TextEvidence.Items = append([]codereviewsvc.ReviewTextEvidence(nil), manifest.TextEvidence.Items...)
			fresh.Manifest.VisualDigest = "fresh-visual"
			fresh.Manifest.Visual.Images[0].ContentDigest = "fresh-content"
			fresh.VisualEvidence.Evidence = append([]models.CodeReviewVisualEvidence(nil), checkpoint.VisualEvidence.Evidence...)
			fresh.VisualEvidence.Evidence[0].Status = tt.after
			if tt.unchanged {
				fresh.Manifest.VisualDigest = manifest.VisualDigest
				fresh.Manifest.Visual.Images[0].ContentDigest = manifest.Visual.Images[0].ContentDigest
			}
			if tt.simultaneousChange {
				fresh.Manifest.Visual.Images[1].ContentDigest = "different-available-bytes"
			}
			if tt.change != nil {
				tt.change(&fresh)
			}
			capture := &assessmentFreshnessCapture{result: fresh}
			visualCapture := &assessmentVisualCheckpoint{snapshot: visual}
			if tt.corruptCheckpoint {
				visualCapture.snapshot.AssessmentID = new(uuid.New())
			}
			services := &Services{CodeReviewInputCapture: capture, CodeReviewVisualEvidence: visualCapture}
			if tt.missingCheckpoint {
				services.CodeReviewVisualEvidence = nil
			}
			err = verifyFullAssessmentFreshness(context.Background(), services, runCodeReviewPayload{}, assessment)
			if tt.retry {
				var retry *RetryableError
				require.ErrorAs(t, err, &retry, "temporary loss of existing bytes must retry the same controller")
				require.True(t, retry.ConsumeAttempt, "evidence failures must spend the controller attempt budget")
				require.NotNil(t, retry.MaxRetryDuration, "evidence recovery must have an explicit durable time bound")
				require.False(t, retry.BypassMaxRetryDuration, "evidence recovery must never bypass its deadline")
				require.Len(t, capture.requests, 1, "classification must not refresh unrelated PR inputs twice")
				if !tt.missingCheckpoint {
					require.Len(t, visualCapture.requests, 1, "classification must read one immutable visual checkpoint")
					require.False(t, visualCapture.requests[0].Fresh, "checkpoint read must preserve original captured evidence")
					require.Equal(t, &id, visualCapture.requests[0].AssessmentID, "checkpoint read must bind the exact assessment identity")
				}
				require.NotContains(t, err.Error(), "secret", "freshness diagnostics must not disclose attachment tokens")
			} else if tt.unchanged {
				require.NoError(t, err, "exactly restored or unchanged captures must not restart analysis")
				require.Empty(t, visualCapture.requests, "matching visual identity does not need checkpoint classification")
			} else {
				require.ErrorIs(t, err, errFullAssessmentInputsChanged, "genuine analysis changes must require a new assessment")
			}
		})
	}
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
