package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
)

type codeReviewRecheckResponse struct {
	BaselineAssessmentID uuid.UUID                                   `json:"baseline_assessment_id"`
	InputDigest          string                                      `json:"input_digest"`
	EscalateFullReview   *bool                                       `json:"escalate_full_review"`
	EscalationReason     string                                      `json:"escalation_reason"`
	Requirements         *[]models.CodeReviewRequirementReassessment `json:"requirement_reassessments"`
	Findings             *[]models.CodeReviewFindingReassessment     `json:"finding_reassessments"`
}

const maxCodeReviewRecheckEscalationReasonBytes = 1900

type codeReviewRecheckValidationInput struct {
	Raw               string
	BaselineID        uuid.UUID
	InputDigest       string
	Requirements      []models.CodeReviewDescriptionRequirement
	BaselineSynthesis codeReviewOrchestratorSynthesis
	BaselineFindings  []models.CodeReviewFinding
	BaselineManifest  codereviewsvc.ReviewInputManifest
	CurrentManifest   codereviewsvc.ReviewInputManifest
	VisualEvidence    models.CodeReviewVisualEvidenceSnapshot
}

type codeReviewRecheckValidation struct {
	Synthesis                codeReviewOrchestratorSynthesis
	EffectiveFindings        []models.CodeReviewFinding
	RequirementReassessments []models.CodeReviewRequirementReassessment
	FindingReassessments     []models.CodeReviewFindingReassessment
	EscalationReason         string
}

// A narrow response can change only evidence dispositions. The caller uses
// EffectiveFindings for the backend risk calculation and retains the original
// rows plus every disposition in the immutable assessment outcome.
func validateCodeReviewRecheckResponse(in codeReviewRecheckValidationInput) (codeReviewRecheckValidation, error) {
	invalid := codeReviewRecheckValidation{Synthesis: in.BaselineSynthesis}
	if len(in.Raw) > 128*1024 || in.BaselineID == uuid.Nil || in.InputDigest == "" || !in.VisualEvidence.Complete {
		return invalid, errors.New("incomplete recheck validation context")
	}
	content := strings.TrimSpace(in.Raw)
	if strings.HasPrefix(content, "```json\n") && strings.HasSuffix(content, "```") {
		content = strings.TrimSuffix(strings.TrimPrefix(content, "```json\n"), "```")
	}
	var output codeReviewRecheckResponse
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return invalid, fmt.Errorf("invalid recheck response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid, errors.New("recheck must return exactly one object")
	}
	if output.BaselineAssessmentID != in.BaselineID || output.InputDigest != in.InputDigest || output.EscalateFullReview == nil {
		return invalid, errors.New("recheck response identity or escalation flag is missing or stale")
	}
	if *output.EscalateFullReview {
		if strings.TrimSpace(output.EscalationReason) == "" || len(output.EscalationReason) > maxCodeReviewRecheckEscalationReasonBytes {
			return invalid, errors.New("escalation requires a bounded concrete reason")
		}
		invalid.EscalationReason = strings.TrimSpace(output.EscalationReason)
		return invalid, nil
	}
	if output.EscalationReason != "" || output.Requirements == nil || output.Findings == nil {
		return invalid, errors.New("recheck must include exact requirement and finding reassessments")
	}
	verify := newCodeReviewCitationVerifier(in)
	merged := in.BaselineSynthesis
	merged.DescriptionAssessments = append([]codeReviewDescriptionAssessment(nil), in.BaselineSynthesis.DescriptionAssessments...)
	byKey := make(map[string]int, len(merged.DescriptionAssessments))
	for i, assessment := range merged.DescriptionAssessments {
		if assessment.Key == "" || byKey[assessment.Key] != 0 {
			return invalid, errors.New("baseline description requirements are ambiguous")
		}
		byKey[assessment.Key] = i + 1
	}
	selected := make(map[string]models.CodeReviewDescriptionRequirement, len(in.Requirements))
	for _, requirement := range in.Requirements {
		if requirement.Key == "" || selected[requirement.Key].Key != "" || byKey[requirement.Key] == 0 || merged.DescriptionAssessments[byKey[requirement.Key]-1].Status == codeReviewDescriptionAssessmentNotApplicable {
			return invalid, errors.New("invalid requirement reassessment set")
		}
		selected[requirement.Key] = requirement
	}
	if len(*output.Requirements) != len(selected) {
		return invalid, errors.New("recheck omitted a requirement")
	}
	seenRequirements := make(map[string]bool, len(selected))
	for _, reassessment := range *output.Requirements {
		if err := reassessment.Validate(); err != nil {
			return invalid, err
		}
		requirement, selectedKey := selected[reassessment.Key]
		if !selectedKey || seenRequirements[reassessment.Key] || len(reassessment.Reason) > 2000 {
			return invalid, fmt.Errorf("invalid requirement reassessment %q", reassessment.Key)
		}
		seenRequirements[reassessment.Key] = true
		index := byKey[reassessment.Key] - 1
		baseline := merged.DescriptionAssessments[index]
		if reassessment.Status == models.CodeReviewRequirementMissing {
			if len(reassessment.EvidenceCitations) != 0 {
				return invalid, errors.New("missing requirement cannot cite satisfaction evidence")
			}
			merged.DescriptionAssessments[index] = codeReviewDescriptionAssessment{Key: reassessment.Key, Status: codeReviewDescriptionAssessmentMissing, EvidenceBasis: models.CodeReviewDescriptionEvidenceBasisMissing, Reason: reassessment.Reason}
			continue
		}
		if len(reassessment.EvidenceCitations) == 0 {
			if baseline.Status != codeReviewDescriptionAssessmentSatisfied || reassessment.Reason != baseline.Reason || baseline.EvidenceBasis != models.CodeReviewDescriptionEvidenceBasisRepository && baseline.EvidenceBasis != models.CodeReviewDescriptionEvidenceBasisDiff {
				return invalid, errors.New("satisfied requirement needs current evidence citations")
			}
			continue // Exact repository/diff evidence carries forward while code is identical.
		}
		citations, err := verify.check(reassessment.EvidenceCitations)
		if err != nil {
			return invalid, err
		}
		if baseline.Status == codeReviewDescriptionAssessmentMissing && !citations.changed {
			return invalid, errors.New("newly satisfied requirement needs changed evidence")
		}
		if requirement.EvidenceKind == models.CodeReviewDescriptionEvidenceKindVisual && len(citations.visualIDs) == 0 {
			return invalid, errors.New("visual requirement needs a current image citation")
		}
		if requirement.EvidenceKind == models.CodeReviewDescriptionEvidenceKindVisual && baseline.Status == codeReviewDescriptionAssessmentMissing && !citations.visualChanged {
			return invalid, errors.New("newly satisfied visual requirement needs a changed image")
		}
		basis := models.CodeReviewDescriptionEvidenceBasisCapturedText
		if len(citations.visualIDs) > 0 {
			basis = models.CodeReviewDescriptionEvidenceBasisImage
		}
		assessment := codeReviewDescriptionAssessment{Key: reassessment.Key, Status: codeReviewDescriptionAssessmentSatisfied, EvidenceBasis: basis, EvidenceIDs: citations.visualIDs, Reason: reassessment.Reason, currentTextCitationValidated: citations.textUsed}
		if err := validateCodeReviewDescriptionAssessmentEvidence(assessment, in.VisualEvidence); err != nil {
			return invalid, err
		}
		merged.DescriptionAssessments[index] = assessment
	}
	if len(*output.Findings) != len(in.BaselineFindings) {
		return invalid, errors.New("recheck omitted an original finding")
	}
	baselineFindings := make(map[uuid.UUID]models.CodeReviewFinding, len(in.BaselineFindings))
	for _, finding := range in.BaselineFindings {
		if finding.ID == uuid.Nil || baselineFindings[finding.ID].ID != uuid.Nil {
			return invalid, errors.New("baseline finding identities are ambiguous")
		}
		baselineFindings[finding.ID] = finding
	}
	seenFindings := make(map[uuid.UUID]bool, len(in.BaselineFindings))
	resolved := make(map[uuid.UUID]bool, len(in.BaselineFindings))
	for _, reassessment := range *output.Findings {
		if err := reassessment.Validate(); err != nil {
			return invalid, err
		}
		if _, exists := baselineFindings[reassessment.FindingID]; !exists || seenFindings[reassessment.FindingID] || len(reassessment.Reason) > 2000 {
			return invalid, errors.New("unknown, duplicate, or oversized finding reassessment")
		}
		seenFindings[reassessment.FindingID] = true
		if len(reassessment.EvidenceCitations) > 0 {
			citations, err := verify.check(reassessment.EvidenceCitations)
			if err != nil {
				return invalid, err
			}
			if reassessment.Status == models.CodeReviewFindingResolved && !citations.changed {
				return invalid, errors.New("resolved finding needs changed evidence")
			}
		} else if reassessment.Status == models.CodeReviewFindingResolved {
			return invalid, errors.New("resolved finding needs current evidence citations")
		}
		resolved[reassessment.FindingID] = reassessment.Status == models.CodeReviewFindingResolved
	}
	effective := make([]models.CodeReviewFinding, 0, len(in.BaselineFindings))
	for _, finding := range in.BaselineFindings {
		if !resolved[finding.ID] {
			effective = append(effective, finding)
		}
	}
	merged.ApprovalRecommended = false
	merged.ReviewSummary = "Updated evidence was assessed against the completed code review."
	return codeReviewRecheckValidation{Synthesis: merged, EffectiveFindings: effective, RequirementReassessments: *output.Requirements, FindingReassessments: *output.Findings}, nil
}

type codeReviewVerifiedCitations struct {
	changed       bool
	visualChanged bool
	textUsed      bool
	visualIDs     []string
}

type codeReviewCitationVerifier struct {
	visual         map[string]models.CodeReviewVisualEvidence
	currentVisual  map[string]string
	text           map[string]codereviewsvc.ReviewTextEvidence
	oldVisual      map[string]string
	oldTextContent []string
}

func newCodeReviewCitationVerifier(in codeReviewRecheckValidationInput) codeReviewCitationVerifier {
	v := codeReviewCitationVerifier{visual: make(map[string]models.CodeReviewVisualEvidence), currentVisual: make(map[string]string), text: make(map[string]codereviewsvc.ReviewTextEvidence), oldVisual: make(map[string]string)}
	for _, image := range in.VisualEvidence.Evidence {
		v.visual[image.EvidenceID] = image
	}
	for _, image := range in.CurrentManifest.Visual.Images {
		v.currentVisual[image.SourceID] = image.ContentDigest
	}
	for _, item := range in.CurrentManifest.TextEvidence.Items {
		v.text[item.EvidenceID] = item
	}
	for _, image := range in.BaselineManifest.Visual.Images {
		v.oldVisual[image.SourceID] = image.ContentDigest
	}
	for _, item := range in.BaselineManifest.TextEvidence.Items {
		v.oldTextContent = append(v.oldTextContent, item.Content)
	}
	return v
}

func (v codeReviewCitationVerifier) check(citations []models.CodeReviewEvidenceCitation) (codeReviewVerifiedCitations, error) {
	var checked codeReviewVerifiedCitations
	if len(citations) == 0 || len(citations) > 8 {
		return checked, errors.New("evidence citations must contain one to eight sources")
	}
	seen := make(map[string]bool, len(citations))
	for _, citation := range citations {
		id := citation.EvidenceID
		if id == "" || id != strings.TrimSpace(id) || seen[id] {
			return checked, errors.New("duplicate or empty evidence citation")
		}
		seen[id] = true
		image, isImage := v.visual[id]
		item, isText := v.text[id]
		if isImage == isText {
			return checked, fmt.Errorf("evidence %q is unknown or ambiguous", id)
		}
		if isImage {
			if citation.Quote != "" || image.Status != models.CodeReviewVisualEvidenceFetchStatusAvailable || image.StoredURL == "" || image.ContentSHA256 == "" {
				return checked, fmt.Errorf("image evidence %q is not available", id)
			}
			if currentDigest, exists := v.currentVisual[image.Source.SourceID]; !exists || currentDigest != image.ContentSHA256 {
				return checked, fmt.Errorf("image evidence %q does not match captured input", id)
			}
			checked.visualIDs = append(checked.visualIDs, id)
			oldBytesPresent := false
			for _, digest := range v.oldVisual {
				if digest == image.ContentSHA256 {
					oldBytesPresent = true
					break
				}
			}
			if !oldBytesPresent {
				checked.changed = true
				checked.visualChanged = true
			}
			continue
		}
		if strings.TrimSpace(citation.Quote) == "" || len(citation.Quote) > 1000 || !strings.Contains(item.Content, citation.Quote) {
			return checked, fmt.Errorf("text evidence %q does not contain its exact bounded quote", id)
		}
		checked.textUsed = true
		quotePreviouslyPresent := false
		for _, prior := range v.oldTextContent {
			if strings.Contains(prior, citation.Quote) {
				quotePreviouslyPresent = true
				break
			}
		}
		if !quotePreviouslyPresent {
			checked.changed = true
		}
	}
	return checked, nil
}
