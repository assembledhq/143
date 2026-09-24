package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateCodeReviewRecheckResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, escalationReason string
		mutate                 func(map[string]any)
		invalid, missing       bool
	}{
		{name: "current image satisfies visual requirement"},
		{name: "missing stays missing", missing: true},
		{name: "unknown image", invalid: true, mutate: func(m map[string]any) {
			m["description_assessments"].([]any)[0].(map[string]any)["evidence_ids"] = []string{"old-image"}
		}},
		{name: "omitted requirement", invalid: true, mutate: func(m map[string]any) { m["description_assessments"] = []any{} }},
		{name: "duplicate requirement", invalid: true, mutate: func(m map[string]any) {
			a := m["description_assessments"].([]any)
			m["description_assessments"] = append(a, a[0])
		}},
		{name: "cannot waive requirement", invalid: true, mutate: func(m map[string]any) {
			a := m["description_assessments"].([]any)[0].(map[string]any)
			a["status"] = "not_applicable"
			a["evidence_basis"] = "not_applicable"
			a["evidence_ids"] = []string{}
		}},
		{name: "cannot use prose as image", invalid: true, mutate: func(m map[string]any) {
			a := m["description_assessments"].([]any)[0].(map[string]any)
			a["evidence_basis"] = "pull_request_description"
			a["evidence_ids"] = []string{}
		}},
		{name: "stale input", invalid: true, mutate: func(m map[string]any) { m["input_digest"] = "stale" }},
		{name: "wrong baseline", invalid: true, mutate: func(m map[string]any) { m["baseline_assessment_id"] = uuid.New().String() }},
		{name: "unknown field", invalid: true, mutate: func(m map[string]any) { m["findings"] = []any{} }},
		{name: "missing escalation flag", invalid: true, mutate: func(m map[string]any) { delete(m, "escalate_full_review") }},
		{name: "escalation is preserved", escalationReason: "Screenshot reveals a broken save action", mutate: func(m map[string]any) {
			m["escalate_full_review"] = true
			m["escalation_reason"] = "Screenshot reveals a broken save action"
		}},
		{name: "empty escalation is invalid", invalid: true, mutate: func(m map[string]any) { m["escalate_full_review"] = true }},
		{name: "escalation exceeding fallback context budget is invalid", invalid: true, mutate: func(m map[string]any) {
			m["escalate_full_review"] = true
			m["escalation_reason"] = strings.Repeat("x", maxCodeReviewRecheckEscalationReasonBytes+1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id := uuid.New()
			digest := strings.Repeat("a", 64)
			baseline := codeReviewOrchestratorSynthesis{Summary: "Preserved code summary", ScopeMismatch: true, DescriptionAssessments: []codeReviewDescriptionAssessment{{Key: "screenshot", Status: codeReviewDescriptionAssessmentMissing, EvidenceBasis: models.CodeReviewDescriptionEvidenceBasisMissing, Reason: "needs screenshot"}, {Key: "test-plan", Status: codeReviewDescriptionAssessmentSatisfied, EvidenceBasis: models.CodeReviewDescriptionEvidenceBasisRepository, Reason: "verified tests"}}, Findings: []codeReviewOrchestratorFinding{{Summary: "Advisory code finding"}}}
			assessment := map[string]any{"key": "screenshot", "status": "satisfied", "evidence_basis": "image", "evidence_ids": []string{"current-image"}, "reason": "Image shows the new interaction"}
			if tt.missing {
				assessment["status"] = "missing"
				assessment["evidence_basis"] = "missing"
				assessment["evidence_ids"] = []string{}
			}
			output := map[string]any{"baseline_assessment_id": id.String(), "input_digest": digest, "escalate_full_review": false, "escalation_reason": "", "description_assessments": []any{assessment}}
			if tt.mutate != nil {
				tt.mutate(output)
			}
			raw, err := json.Marshal(output)
			require.NoError(t, err, "test response should encode")
			visual := models.CodeReviewVisualEvidenceSnapshot{Complete: true, Evidence: []models.CodeReviewVisualEvidence{{EvidenceID: "current-image", Status: models.CodeReviewVisualEvidenceFetchStatusAvailable, StoredURL: "https://first-party.test/image", ContentSHA256: strings.Repeat("b", 64)}}}
			updated, escalationReason, err := validateCodeReviewRecheckResponse(string(raw), id, digest, []string{"screenshot"}, baseline, visual)
			if tt.invalid {
				require.Error(t, err, "invalid narrow response must not reach decision evaluation")
				require.Equal(t, baseline, updated, "invalid output must preserve every baseline blocker")
				return
			}
			require.NoError(t, err, "valid narrow response should be accepted")
			require.Equal(t, tt.escalationReason, escalationReason, "new concerns must retain the bounded reason for full review")
			if tt.escalationReason != "" {
				require.Equal(t, baseline, updated, "escalation must preserve baseline")
				return
			}
			expected := baseline
			expected.DescriptionAssessments = append([]codeReviewDescriptionAssessment(nil), baseline.DescriptionAssessments...)
			expected.ReviewSummary = "Updated visual evidence was assessed against the completed code review."
			expected.DescriptionAssessments[0] = codeReviewDescriptionAssessment{Key: "screenshot", Status: codeReviewDescriptionAssessmentSatisfied, EvidenceBasis: models.CodeReviewDescriptionEvidenceBasisImage, EvidenceIDs: []string{"current-image"}, Reason: "Image shows the new interaction"}
			if tt.missing {
				expected.DescriptionAssessments[0].Status = codeReviewDescriptionAssessmentMissing
				expected.DescriptionAssessments[0].EvidenceBasis = models.CodeReviewDescriptionEvidenceBasisMissing
				expected.DescriptionAssessments[0].EvidenceIDs = []string{}
			}
			require.Equal(t, expected, updated, "only the explicitly permitted visual assessment may change")
			require.Equal(t, codeReviewDescriptionAssessmentMissing, baseline.DescriptionAssessments[0].Status, "merge must not mutate the captured baseline")
		})
	}
}

func TestRecheckResponseRejectsTrailingOrUnavailableEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		complete  bool
	}{
		{"two objects", "{} {}", true}, {"prose", "The screenshot looks fine", true}, {"incomplete capture", "{}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := validateCodeReviewRecheckResponse(tt.raw, uuid.New(), "digest", []string{"screenshot"}, codeReviewOrchestratorSynthesis{}, models.CodeReviewVisualEvidenceSnapshot{Complete: tt.complete})
			require.Error(t, err, "unverifiable output must fail closed")
		})
	}
}
