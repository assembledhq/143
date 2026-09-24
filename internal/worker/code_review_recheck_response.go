package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type codeReviewRecheckResponse struct {
	BaselineAssessmentID   uuid.UUID                         `json:"baseline_assessment_id"`
	InputDigest            string                            `json:"input_digest"`
	EscalateFullReview     *bool                             `json:"escalate_full_review"`
	EscalationReason       string                            `json:"escalation_reason"`
	DescriptionAssessments []codeReviewDescriptionAssessment `json:"description_assessments"`
}

// Keep room for the supervisor's fixed prefix in the lifecycle fallback
// reason, whose public request contract caps the complete string at 2000.
const maxCodeReviewRecheckEscalationReasonBytes = 1900

// Only the explicitly selected visual assessments may change. The caller must
// fall back to a full review on escalation; malformed output is a failed turn,
// never the legacy evaluator's best-effort description success.
func validateCodeReviewRecheckResponse(raw string, baselineID uuid.UUID, digest string, keys []string, baseline codeReviewOrchestratorSynthesis, visual models.CodeReviewVisualEvidenceSnapshot) (codeReviewOrchestratorSynthesis, string, error) {
	if len(raw) > 128*1024 || baselineID == uuid.Nil || digest == "" || !visual.Complete || len(keys) == 0 {
		return baseline, "", errors.New("incomplete recheck validation context")
	}
	content := strings.TrimSpace(raw)
	if strings.HasPrefix(content, "```json\n") && strings.HasSuffix(content, "```") {
		content = strings.TrimSuffix(strings.TrimPrefix(content, "```json\n"), "```")
	}
	var output codeReviewRecheckResponse
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return baseline, "", fmt.Errorf("invalid recheck response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return baseline, "", errors.New("recheck must return exactly one object")
	}
	if output.BaselineAssessmentID != baselineID || output.InputDigest != digest || output.EscalateFullReview == nil {
		return baseline, "", errors.New("recheck response identity or escalation flag is missing or stale")
	}
	if *output.EscalateFullReview {
		if strings.TrimSpace(output.EscalationReason) == "" || len(output.EscalationReason) > maxCodeReviewRecheckEscalationReasonBytes {
			return baseline, "", errors.New("escalation requires a bounded concrete reason")
		}
		return baseline, strings.TrimSpace(output.EscalationReason), nil
	}
	if output.EscalationReason != "" {
		return baseline, "", errors.New("unexpected escalation reason")
	}
	expected := make(map[string]bool, len(keys))
	for _, key := range keys {
		if key == "" || expected[key] {
			return baseline, "", errors.New("invalid visual requirement set")
		}
		expected[key] = true
	}
	updates := make(map[string]codeReviewDescriptionAssessment, len(keys))
	for _, assessment := range output.DescriptionAssessments {
		if !expected[assessment.Key] {
			return baseline, "", fmt.Errorf("unexpected visual requirement %q", assessment.Key)
		}
		if _, exists := updates[assessment.Key]; exists {
			return baseline, "", fmt.Errorf("duplicate visual requirement %q", assessment.Key)
		}
		if strings.TrimSpace(assessment.Reason) == "" || len(assessment.Reason) > 2000 {
			return baseline, "", errors.New("visual requirement needs a bounded evidence reason")
		}
		if assessment.Status != codeReviewDescriptionAssessmentMissing && assessment.Status != codeReviewDescriptionAssessmentSatisfied {
			return baseline, "", errors.New("recheck cannot remove a visual requirement")
		}
		if assessment.Status == codeReviewDescriptionAssessmentSatisfied && assessment.EvidenceBasis != models.CodeReviewDescriptionEvidenceBasisImage {
			return baseline, "", errors.New("recheck satisfaction requires current image evidence")
		}
		if err := validateCodeReviewDescriptionAssessmentEvidence(assessment, visual); err != nil {
			return baseline, "", err
		}
		updates[assessment.Key] = assessment
	}
	if len(updates) != len(expected) {
		return baseline, "", errors.New("recheck omitted a visual requirement")
	}
	result := baseline
	result.DescriptionAssessments = append([]codeReviewDescriptionAssessment(nil), baseline.DescriptionAssessments...)
	remaining := len(updates)
	for i, assessment := range result.DescriptionAssessments {
		if updated, ok := updates[assessment.Key]; ok {
			result.DescriptionAssessments[i] = updated
			remaining--
		}
	}
	if remaining != 0 {
		return baseline, "", errors.New("visual requirement is absent from baseline")
	}
	// Only the backend can authorize the final decision. Preserve all baseline
	// flags/findings and suppress generated approval prose from the old turn.
	result.ApprovalRecommended = false
	result.ReviewSummary = "Updated visual evidence was assessed against the completed code review."
	return result, "", nil
}
