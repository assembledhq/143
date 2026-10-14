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
		var sessionID uuid.UUID
		var scope models.CodeReviewScope
		err := tx.QueryRow(ctx, `SELECT status,head_sha,base_sha,base_ref,session_id,review_scope FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2 AND repository_id=$3 AND pull_request_id=$4`, orgID, assessmentID, repositoryID, pullRequestID).Scan(&status, &headSHA, &baseSHA, &baseRef, &sessionID, &scope)
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
		if _, err := tx.Exec(ctx, `UPDATE code_review_requests SET status=$3
 WHERE org_id=$1 AND status IN ('pending','joined')
 AND (assessment_id=$2 OR ($3='satisfied' AND $6='full' AND assessment_id IS NULL AND status='joined' AND session_id=$4 AND pull_request_id=$5))`, orgID, assessmentID, requestStatus, sessionID, pullRequestID, scope); err != nil {
			return err
		}
		ownsAssessment := state.ActiveAssessmentID != nil && *state.ActiveAssessmentID == assessmentID
		ownsLegacySession := state.ActiveAssessmentID == nil && scope == models.CodeReviewScopeFull && state.ActiveSessionID != nil && *state.ActiveSessionID == sessionID
		if !ownsAssessment && !ownsLegacySession {
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

// SettleCompletedFullReviewJoins preserves the original session of requests
// joined before a full assessment was captured, including pre-capture sessions.
// Call under the PR scheduling lock before replaying or advancing requests.
func SettleCompletedFullReviewJoins(ctx context.Context, tx pgx.Tx, orgID, prID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE code_review_requests r SET status='satisfied'
 FROM code_review_session_metadata m
 WHERE r.org_id=$1 AND r.pull_request_id=$2 AND r.status='joined' AND r.assessment_id IS NULL
 AND m.org_id=r.org_id AND m.pull_request_id=r.pull_request_id AND m.session_id=r.session_id AND m.status='completed'`, orgID, prID)
	return err
}
