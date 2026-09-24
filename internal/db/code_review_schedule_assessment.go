package db

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SettleAssessment reconciles the PR scheduler after an assessment reaches a
// terminal state. It also settles every request joined to that assessment.
// Repeating the call is safe; a newer pending request keeps its wake and state.
func (s *CodeReviewScheduleStore) SettleAssessment(ctx context.Context, orgID, assessmentID uuid.UUID) error {
	var repositoryID, pullRequestID uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT repository_id,pull_request_id FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, orgID, assessmentID).Scan(&repositoryID, &pullRequestID)
	if err != nil {
		return err
	}
	return s.WithLockedPR(ctx, orgID, repositoryID, pullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		var status models.CodeReviewAssessmentStatus
		var headSHA, baseSHA, baseRef string
		err := tx.QueryRow(ctx, `SELECT status,head_sha,base_sha,base_ref FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2 AND repository_id=$3 AND pull_request_id=$4`, orgID, assessmentID, repositoryID, pullRequestID).Scan(&status, &headSHA, &baseSHA, &baseRef)
		if err != nil {
			return err
		}
		requestStatus := ""
		switch status {
		case models.CodeReviewAssessmentCompleted:
			requestStatus = "satisfied"
		case models.CodeReviewAssessmentFailed, models.CodeReviewAssessmentCancelled, models.CodeReviewAssessmentSuperseded:
			requestStatus = "failed"
		default:
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE code_review_requests SET status=$3 WHERE org_id=$1 AND assessment_id=$2 AND status IN ('pending','joined')`, orgID, assessmentID, requestStatus); err != nil {
			return err
		}
		if state.ActiveAssessmentID == nil || *state.ActiveAssessmentID != assessmentID {
			return nil
		}
		state.ActiveAssessmentID = nil
		state.ActiveSessionID = nil
		if status == models.CodeReviewAssessmentCompleted {
			state.CurrentAssessmentID = &assessmentID
		} else if state.CurrentAssessmentID != nil && *state.CurrentAssessmentID == assessmentID {
			var completedID uuid.UUID
			err := tx.QueryRow(ctx, `SELECT id FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 AND status='completed' AND superseded_by_assessment_id IS NULL ORDER BY generation DESC LIMIT 1`, orgID, pullRequestID).Scan(&completedID)
			if errors.Is(err, pgx.ErrNoRows) {
				state.CurrentAssessmentID = nil
			} else if err != nil {
				return err
			} else {
				state.CurrentAssessmentID = &completedID
			}
		}
		if state.PendingInput != nil {
			// The queued intent and wake were already committed under the PR lock.
			return nil
		}
		state.State = models.CodeReviewScheduleIdle
		if status == models.CodeReviewAssessmentCompleted && headSHA == state.HeadSHA && baseSHA == state.BaseSHA && baseRef == state.BaseRef {
			state.State = models.CodeReviewScheduleCovered
		}
		state.WaitReason = models.CodeReviewWaitNone
		state.RetryAt = nil
		state.EligibleAt = nil
		return nil
	})
}
