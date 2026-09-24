package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func recheckResponseFixture() (codeReviewRecheckValidationInput, codeReviewRecheckResponse) {
	baselineID, blockerID, advisoryID := uuid.New(), uuid.New(), uuid.New()
	input := codeReviewRecheckValidationInput{
		BaselineID: baselineID, InputDigest: strings.Repeat("a", 64),
		Requirements: []models.CodeReviewDescriptionRequirement{{Key: "screenshot", EvidenceKind: models.CodeReviewDescriptionEvidenceKindVisual}, {Key: "test-plan", EvidenceKind: models.CodeReviewDescriptionEvidenceKindGeneral}},
		BaselineSynthesis: codeReviewOrchestratorSynthesis{Summary: "Original code summary", ReviewSummary: "Original decision", ScopeMismatch: true,
			DescriptionAssessments: []codeReviewDescriptionAssessment{{Key: "screenshot", Status: codeReviewDescriptionAssessmentMissing, EvidenceBasis: models.CodeReviewDescriptionEvidenceBasisMissing, Reason: "needs screenshot"}, {Key: "test-plan", Status: codeReviewDescriptionAssessmentMissing, EvidenceBasis: models.CodeReviewDescriptionEvidenceBasisMissing, Reason: "needs test output"}},
			Findings:               []codeReviewOrchestratorFinding{}, HumanReviewReasons: []codeReviewOrchestratorHumanReviewReason{}},
		BaselineFindings: []models.CodeReviewFinding{{ID: blockerID, Severity: models.CodeReviewFindingSeverityHigh, Summary: "Coverage unproven"}, {ID: advisoryID, Severity: models.CodeReviewFindingSeverityLow, Summary: "Optional refactor"}},
		BaselineManifest: codereviewsvc.ReviewInputManifest{Visual: codereviewsvc.ReviewVisualInput{Images: []codereviewsvc.ReviewVisualImage{}}, TextEvidence: codereviewsvc.ReviewTextInput{Items: []codereviewsvc.ReviewTextEvidence{}}},
		CurrentManifest:  codereviewsvc.ReviewInputManifest{Visual: codereviewsvc.ReviewVisualInput{Images: []codereviewsvc.ReviewVisualImage{{SourceID: "image-source", ContentDigest: strings.Repeat("b", 64)}}}, TextEvidence: codereviewsvc.ReviewTextInput{Items: []codereviewsvc.ReviewTextEvidence{{EvidenceID: "text-new", Content: "Test run on this commit: PASS", ContentDigest: strings.Repeat("c", 64)}}}},
		VisualEvidence:   models.CodeReviewVisualEvidenceSnapshot{Complete: true, Evidence: []models.CodeReviewVisualEvidence{{EvidenceID: "image-new", Source: models.CodeReviewVisualEvidenceSource{SourceID: "image-source"}, Status: models.CodeReviewVisualEvidenceFetchStatusAvailable, StoredURL: "https://first-party.test/image", ContentSHA256: strings.Repeat("b", 64)}}},
	}
	no := false
	requirements := []models.CodeReviewRequirementReassessment{
		{Key: "screenshot", Status: models.CodeReviewRequirementSatisfied, Reason: "Shows current UI", EvidenceCitations: []models.CodeReviewEvidenceCitation{{EvidenceID: "image-new"}}},
		{Key: "test-plan", Status: models.CodeReviewRequirementSatisfied, Reason: "Shows passing test output", EvidenceCitations: []models.CodeReviewEvidenceCitation{{EvidenceID: "text-new", Quote: "Test run on this commit: PASS"}}},
	}
	findings := []models.CodeReviewFindingReassessment{
		{FindingID: blockerID, Status: models.CodeReviewFindingResolved, Reason: "Current test output covers the failure", EvidenceCitations: []models.CodeReviewEvidenceCitation{{EvidenceID: "text-new", Quote: "Test run on this commit: PASS"}}},
		{FindingID: advisoryID, Status: models.CodeReviewFindingRetained, Reason: "Refactor remains optional", EvidenceCitations: []models.CodeReviewEvidenceCitation{}},
	}
	return input, codeReviewRecheckResponse{BaselineAssessmentID: baselineID, InputDigest: input.InputDigest, EscalateFullReview: &no, Requirements: &requirements, Findings: &findings}
}

func TestValidateCodeReviewRecheckResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*codeReviewRecheckValidationInput, *codeReviewRecheckResponse)
		valid  bool
	}{
		{"current text and image resolve an original finding", nil, true},
		{"retained high finding stays in effective set", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			(*r.Findings)[0].Status = models.CodeReviewFindingRetained
		}, true},
		{"unknown finding ID", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			(*r.Findings)[0].FindingID = uuid.New()
		}, false},
		{"duplicate finding ID", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			(*r.Findings)[1].FindingID = (*r.Findings)[0].FindingID
		}, false},
		{"omitted finding ID", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			*r.Findings = (*r.Findings)[:1]
		}, false},
		{"resolved finding lacks citation", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			(*r.Findings)[0].EvidenceCitations = nil
		}, false},
		{"stale text quote", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			(*r.Findings)[0].EvidenceCitations[0].Quote = "another result"
		}, false},
		{"unchanged old text cannot resolve", func(in *codeReviewRecheckValidationInput, _ *codeReviewRecheckResponse) {
			in.BaselineManifest.TextEvidence.Items = append([]codereviewsvc.ReviewTextEvidence(nil), in.CurrentManifest.TextEvidence.Items...)
		}, false},
		{"unrelated text edit cannot validate old quote", func(in *codeReviewRecheckValidationInput, _ *codeReviewRecheckResponse) {
			in.BaselineManifest.TextEvidence.Items = []codereviewsvc.ReviewTextEvidence{{EvidenceID: "old-text", Content: "Test run on this commit: PASS\nAn older note", ContentDigest: strings.Repeat("d", 64)}}
		}, false},
		{"rehosted identical screenshot is not new evidence", func(in *codeReviewRecheckValidationInput, _ *codeReviewRecheckResponse) {
			in.BaselineManifest.Visual.Images = []codereviewsvc.ReviewVisualImage{{SourceID: "old-image", ContentDigest: strings.Repeat("b", 64)}}
		}, false},
		{"new text cannot make an old screenshot satisfy a missing visual requirement", func(in *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			in.BaselineManifest.Visual.Images = []codereviewsvc.ReviewVisualImage{{SourceID: "old-image", ContentDigest: strings.Repeat("b", 64)}}
			(*r.Requirements)[0].EvidenceCitations = append((*r.Requirements)[0].EvidenceCitations, models.CodeReviewEvidenceCitation{EvidenceID: "text-new", Quote: "Test run on this commit: PASS"})
		}, false},
		{"fetched screenshot must match captured image digest", func(in *codeReviewRecheckValidationInput, _ *codeReviewRecheckResponse) {
			in.VisualEvidence.Evidence[0].ContentSHA256 = strings.Repeat("e", 64)
		}, false},
		{"unavailable image cannot satisfy", func(in *codeReviewRecheckValidationInput, _ *codeReviewRecheckResponse) {
			in.VisualEvidence.Evidence[0].Status = models.CodeReviewVisualEvidenceFetchStatusUnavailable
		}, false},
		{"missing requirement reassessment", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			*r.Requirements = (*r.Requirements)[:1]
		}, false},
		{"duplicate requirement key", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			(*r.Requirements)[1].Key = "screenshot"
		}, false},
		{"wrong baseline identity", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			r.BaselineAssessmentID = uuid.New()
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input, response := recheckResponseFixture()
			if tt.change != nil {
				tt.change(&input, &response)
			}
			raw, err := json.Marshal(response)
			require.NoError(t, err, "fixture response should encode")
			input.Raw = string(raw)
			result, err := validateCodeReviewRecheckResponse(input)
			if !tt.valid {
				require.Error(t, err, "unverifiable dispositions must fail closed")
				require.Equal(t, input.BaselineSynthesis, result.Synthesis, "invalid output must preserve the original synthesis")
				return
			}
			require.NoError(t, err, "complete exact dispositions should validate")
			require.True(t, result.Synthesis.ScopeMismatch, "nonfinding blocker must remain intact")
			if tt.name == "retained high finding stays in effective set" {
				require.Equal(t, input.BaselineFindings, result.EffectiveFindings, "retained blocker must still be counted")
			} else {
				require.Equal(t, input.BaselineFindings[1:], result.EffectiveFindings, "only explicitly resolved source finding may leave risk input")
			}
			require.Equal(t, models.CodeReviewDescriptionEvidenceBasisCapturedText, result.Synthesis.DescriptionAssessments[1].EvidenceBasis, "validated text citation should have backend-derived basis")
		})
	}
}

func TestRecheckResponseEscalationAndMalformed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*codeReviewRecheckValidationInput, *codeReviewRecheckResponse)
		valid  bool
	}{
		{"new concern retains reason", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			yes := true
			r.EscalateFullReview = &yes
			r.EscalationReason = "The log shows a new data-loss path"
		}, true},
		{"empty escalation", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			yes := true
			r.EscalateFullReview = &yes
		}, false},
		{"oversized escalation", func(_ *codeReviewRecheckValidationInput, r *codeReviewRecheckResponse) {
			yes := true
			r.EscalateFullReview = &yes
			r.EscalationReason = strings.Repeat("x", maxCodeReviewRecheckEscalationReasonBytes+1)
		}, false},
		{"trailing response", func(in *codeReviewRecheckValidationInput, _ *codeReviewRecheckResponse) { in.Raw = "{} {}" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input, response := recheckResponseFixture()
			if tt.change != nil {
				tt.change(&input, &response)
			}
			if input.Raw == "" {
				raw, err := json.Marshal(response)
				require.NoError(t, err, "fixture should encode")
				input.Raw = string(raw)
			}
			result, err := validateCodeReviewRecheckResponse(input)
			if !tt.valid {
				require.Error(t, err, "invalid response must fail closed")
				return
			}
			require.NoError(t, err, "bounded escalation should validate")
			require.Equal(t, "The log shows a new data-loss path", result.EscalationReason, "exact concern should survive for durable fallback")
			require.Equal(t, input.BaselineSynthesis, result.Synthesis, "escalation cannot change baseline")
		})
	}
}
