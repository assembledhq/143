package codereview

import (
	"encoding/json"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// EffectiveAssessmentFindings projects immutable source findings through one
// completed assessment. Incomplete or inconsistent reassessments fail closed.
func EffectiveAssessmentFindings(current, source models.CodeReviewAssessment, findings []models.CodeReviewFinding) ([]models.CodeReviewFinding, []models.CodeReviewFindingReassessment, error) {
	if current.Status != models.CodeReviewAssessmentCompleted || source.Status != models.CodeReviewAssessmentCompleted || source.ReviewScope != models.CodeReviewScopeFull ||
		current.OrgID != source.OrgID || current.SessionID != source.SessionID || current.PullRequestID != source.PullRequestID {
		return nil, nil, fmt.Errorf("assessment finding source does not match completed assessment")
	}
	if current.ReviewScope == models.CodeReviewScopeFull {
		if current.ID != source.ID || current.SourceAssessmentID != nil {
			return nil, nil, fmt.Errorf("full assessment finding source is inconsistent")
		}
	} else if current.ReviewScope != models.CodeReviewScopeEvidenceOnly || current.SourceAssessmentID == nil || *current.SourceAssessmentID != source.ID {
		return nil, nil, fmt.Errorf("evidence assessment finding source is inconsistent")
	}
	byID := make(map[uuid.UUID]struct{}, len(findings))
	for _, finding := range findings {
		if finding.ID == uuid.Nil || finding.OrgID != source.OrgID || finding.SessionID != source.SessionID {
			return nil, nil, fmt.Errorf("source finding identity does not match assessment")
		}
		if _, duplicate := byID[finding.ID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate source finding")
		}
		byID[finding.ID] = struct{}{}
	}
	if current.ReviewScope == models.CodeReviewScopeFull {
		return findings, nil, nil
	}
	var outcome struct {
		FindingReassessments []models.CodeReviewFindingReassessment `json:"finding_reassessments"`
	}
	if err := json.Unmarshal(current.StructuredOutcome, &outcome); err != nil {
		return nil, nil, fmt.Errorf("decode finding reassessments: %w", err)
	}
	if len(outcome.FindingReassessments) != len(findings) {
		return nil, nil, fmt.Errorf("finding reassessments do not cover source findings")
	}
	statusByID := make(map[uuid.UUID]models.CodeReviewFindingReassessmentStatus, len(findings))
	for _, item := range outcome.FindingReassessments {
		if err := item.Validate(); err != nil {
			return nil, nil, fmt.Errorf("invalid finding reassessment: %w", err)
		}
		if _, found := byID[item.FindingID]; !found {
			return nil, nil, fmt.Errorf("finding reassessment references unrelated finding")
		}
		if _, duplicate := statusByID[item.FindingID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate finding reassessment")
		}
		statusByID[item.FindingID] = item.Status
	}
	effective := make([]models.CodeReviewFinding, 0, len(findings))
	for _, finding := range findings {
		if statusByID[finding.ID] == models.CodeReviewFindingRetained {
			effective = append(effective, finding)
		}
	}
	return effective, outcome.FindingReassessments, nil
}
