package db

import (
	"context"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// GetCompletedAssessmentByID keeps dispute triage on the exact assessment
// captured when the objection was filed, even if a newer review completes.
func (s *CodeReviewStore) GetCompletedAssessmentByID(ctx context.Context, orgID, assessmentID uuid.UUID) (models.CodeReviewAssessment, error) {
	assessment, err := NewCodeReviewAssessmentStore(s.db).GetByID(ctx, orgID, assessmentID)
	if err != nil {
		return models.CodeReviewAssessment{}, err
	}
	if assessment.Status != models.CodeReviewAssessmentCompleted {
		return models.CodeReviewAssessment{}, ErrCodeReviewAssessmentState
	}
	return assessment, nil
}

// ListAssessmentFindings reads only rows linked to the immutable source.
func (s *CodeReviewStore) ListAssessmentFindings(ctx context.Context, orgID, assessmentID uuid.UUID) ([]models.CodeReviewFinding, error) {
	return NewCodeReviewAssessmentStore(s.db).ListFindings(ctx, orgID, assessmentID)
}
