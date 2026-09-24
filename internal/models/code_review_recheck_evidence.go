package models

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// CodeReviewEvidenceCitation identifies a captured source in this assessment.
// Quote is the excerpt the assessor relied on when the source is text.
type CodeReviewEvidenceCitation struct {
	EvidenceID string `json:"evidence_id"`
	Quote      string `json:"quote,omitempty"`
}

type CodeReviewFindingReassessmentStatus string

const (
	CodeReviewFindingRetained CodeReviewFindingReassessmentStatus = "retained"
	CodeReviewFindingResolved CodeReviewFindingReassessmentStatus = "resolved"
)

func (s CodeReviewFindingReassessmentStatus) Validate() error {
	if s == CodeReviewFindingRetained || s == CodeReviewFindingResolved {
		return nil
	}
	return fmt.Errorf("invalid code review finding reassessment status %q", s)
}

// CodeReviewFindingReassessment records a current assessment's treatment of
// one immutable source finding. It never modifies the original finding row.
type CodeReviewFindingReassessment struct {
	FindingID         uuid.UUID                           `json:"finding_id"`
	Status            CodeReviewFindingReassessmentStatus `json:"status"`
	Reason            string                              `json:"reason"`
	EvidenceCitations []CodeReviewEvidenceCitation        `json:"evidence_citations"`
}

type CodeReviewRequirementReassessmentStatus string

const (
	CodeReviewRequirementSatisfied CodeReviewRequirementReassessmentStatus = "satisfied"
	CodeReviewRequirementMissing   CodeReviewRequirementReassessmentStatus = "missing"
)

func (s CodeReviewRequirementReassessmentStatus) Validate() error {
	if s == CodeReviewRequirementSatisfied || s == CodeReviewRequirementMissing {
		return nil
	}
	return fmt.Errorf("invalid code review requirement reassessment status %q", s)
}

// CodeReviewRequirementReassessment records how current captured evidence
// applies to one description requirement in this assessment.
type CodeReviewRequirementReassessment struct {
	Key               string                                  `json:"key"`
	Status            CodeReviewRequirementReassessmentStatus `json:"status"`
	Reason            string                                  `json:"reason"`
	EvidenceCitations []CodeReviewEvidenceCitation            `json:"evidence_citations"`
}

func (r CodeReviewRequirementReassessment) Validate() error {
	if strings.TrimSpace(r.Key) == "" || strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("requirement reassessment requires key and reason")
	}
	return r.Status.Validate()
}

func (r CodeReviewFindingReassessment) Validate() error {
	if r.FindingID == uuid.Nil {
		return fmt.Errorf("finding reassessment requires finding_id")
	}
	if err := r.Status.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("finding reassessment requires reason")
	}
	for _, citation := range r.EvidenceCitations {
		id := strings.TrimSpace(citation.EvidenceID)
		if id == "" {
			return fmt.Errorf("finding reassessment citation requires evidence_id")
		}
	}
	return nil
}
