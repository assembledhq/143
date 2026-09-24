package db

import (
	"context"
	"errors"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// LockAssessmentPublicationJob must use the publication transaction's store.
// Locking the current lease row prevents a reclaim during external publication.
func (s *CodeReviewStore) LockAssessmentPublicationJob(ctx context.Context, orgID, jobID, token uuid.UUID) error {
	if jobID == uuid.Nil || token == uuid.Nil {
		return errors.New("assessment publication requires an active job lease")
	}
	var leased uuid.UUID
	return s.db.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND id=$2 AND lock_token=$3 AND status='running' FOR UPDATE`, orgID, jobID, token).Scan(&leased)
}

// PublishAssessmentUpdated is called after the assessment transaction commits.
// Event delivery is best effort; API readers remain authoritative.
func (s *CodeReviewStore) PublishAssessmentUpdated(ctx context.Context, assessment models.CodeReviewAssessment) {
	if s.streams == nil {
		return
	}
	if err := s.streams.PublishUpdated(ctx, assessment.OrgID, models.CodeReviewUpdatedEvent{OrgID: assessment.OrgID, PullRequestID: &assessment.PullRequestID, SessionID: &assessment.SessionID, AssessmentID: &assessment.ID, Generation: &assessment.Generation, Decision: assessment.Decision, UpdatedAt: time.Now().UTC()}); err != nil {
		s.logger.Warn().Err(err).Str("assessment_id", assessment.ID.String()).Msg("publish assessment update failed")
	}
}
