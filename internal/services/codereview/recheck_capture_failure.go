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

func (s *Service) failRecheckCapture(ctx context.Context, req ScheduleRequestInput, repoID uuid.UUID, hash, kind string, cause error) (ScheduleRequestResult, error) {
	var record db.CodeReviewRequestRecord
	err := s.scheduling.store.WithLockedPR(ctx, req.OrgID, repoID, req.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		id, _, err := db.RecordCodeReviewRequest(ctx, tx, req.OrgID, repoID, req.PullRequestID, kind, req.RequestID.String(), req.Mode, hash, state.Generation+1, req.RequesterID)
		if err != nil {
			return err
		}
		record, err = db.NewCodeReviewScheduleStore(tx).GetRequestByIdentity(ctx, req.OrgID, kind, req.RequestID.String())
		if err != nil || record.Status != "pending" {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE code_review_requests SET status='failed' WHERE org_id=$1 AND id=$2 AND status='pending'`, req.OrgID, id); err != nil {
			return err
		}
		// Capture runs outside the lock. Preserve any pending intent owned by
		// another request, including automatic reviews without a request ID.
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
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND status IN ('queued','running'))`, req.OrgID, state.ActiveSessionID).Scan(&active); err != nil {
				return err
			}
		}
		if active {
			state.State, state.WaitReason = models.CodeReviewScheduleRunning, models.CodeReviewWaitActive
		} else {
			state.State, state.WaitReason = models.CodeReviewSchedulePaused, models.CodeReviewWaitContext
		}
		return nil
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
