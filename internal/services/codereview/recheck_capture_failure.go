package codereview

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrRecheckUnavailable is a recorded terminal admission failure, not a
// transient provider error. A new request can retry; redelivery stays failed.
var ErrRecheckUnavailable = errors.New("evidence could not be captured; try again or request a full review")

func (s *Service) failRecheckCapture(ctx context.Context, req ScheduleRequestInput, repoID uuid.UUID, hash, kind string, cause error, expected models.CodeReviewPRState) (ScheduleRequestResult, error) {
	var record db.CodeReviewRequestRecord
	err := s.scheduling.store.WithLockedPR(ctx, req.OrgID, repoID, req.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		if !captureFailureTargetMatches(state, expected) {
			var queued ScheduleRequestResult
			_, err := s.queuePendingAssessmentCaptureInTx(ctx, tx, state, req, assessmentRequestContext(req), hash, kind, &queued, recheckCaptureFailureAttempt{expected: expected})
			return err
		}
		id, _, err := db.RecordCodeReviewRequest(ctx, tx, req.OrgID, repoID, req.PullRequestID, kind, req.RequestID.String(), req.Mode, hash, state.Generation+1, req.RequesterID)
		if err != nil {
			return err
		}
		record, err = db.NewCodeReviewScheduleStore(tx).GetRequestByIdentity(ctx, req.OrgID, kind, req.RequestID.String())
		if err != nil || record.Status != "pending" {
			return err
		}
		return failRecheckCaptureInTx(ctx, tx, state, req.OrgID, id)
	})
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	record, err = s.scheduling.store.GetRequestByIdentity(ctx, req.OrgID, kind, req.RequestID.String())
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	if record.Status == "failed" {
		s.logger.Warn().Err(cause).Str("org_id", req.OrgID.String()).Str("pull_request_id", req.PullRequestID.String()).Str("request_id", req.RequestID.String()).Msg("evidence recheck capture failed")
	}
	return s.existingAssessmentRequestResult(ctx, req, record)
}

// The caller holds the PR lock. Terminal capture failures settle only their
// request. Callers must fence the captured target before calling it;
// matching request IDs alone do not authorize clearing retargeted intent.
func failRecheckCaptureInTx(ctx context.Context, tx pgx.Tx, state *models.CodeReviewPRState, orgID, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE code_review_requests SET status='failed' WHERE org_id=$1 AND id=$2 AND status='pending'`, orgID, id); err != nil {
		return err
	}
	ownsPending := state.PendingRequestID != nil && *state.PendingRequestID == id
	if !ownsPending && (state.PendingInput != nil || state.PendingRequestID != nil) {
		return nil
	}
	state.PendingRequestID, state.PendingInput = nil, nil
	state.FirstPendingAt, state.RetryAt, state.EligibleAt = nil, nil, nil
	if state.State == models.CodeReviewScheduleClosed {
		return nil
	}
	active := state.ActiveAssessmentID != nil
	if !active && state.ActiveSessionID != nil {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND status IN ('queued','running'))`, orgID, state.ActiveSessionID).Scan(&active); err != nil {
			return err
		}
	}
	if active {
		state.State, state.WaitReason = models.CodeReviewScheduleRunning, models.CodeReviewWaitActive
	} else {
		state.State, state.WaitReason = models.CodeReviewSchedulePaused, models.CodeReviewWaitContext
	}
	return nil
}
