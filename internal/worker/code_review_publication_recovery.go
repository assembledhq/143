package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/jackc/pgx/v5"
)

var errCodeReviewPublicationPaused = errors.New("publication requires operator reconciliation")

func pauseExpiredCodeReviewPublication(ctx context.Context, stores *Stores, assessment models.CodeReviewAssessment) (bool, error) {
	if assessment.Status != models.CodeReviewAssessmentPublishing || assessment.PublicationState != models.CodeReviewPublicationUncertain {
		return false, nil
	}
	if assessment.FailureDetail != nil && strings.HasPrefix(*assessment.FailureDetail, "operator_reconciliation_required:") {
		return true, nil
	}
	if time.Since(assessment.CreatedAt) < db.CodeReviewPublicationReconciliationWindow {
		return false, nil
	}
	return stores.CodeReviewAssessments.PauseExpiredPublication(ctx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest)
}

// Recover before terminal metadata and changed-head exits. A confirmed review
// is a historical fact even when the PR moved or the controller exhausted its
// retries while waiting for runtime drain. Reuse its exact staged outcome.
func recoverStagedFullAssessment(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload) (bool, error) {
	if stores.CodeReviewAssessments == nil {
		return false, nil
	}
	a, err := stores.CodeReviewAssessments.GetBySessionID(ctx, job.OrgID, job.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.ResultOrigin == nil || (a.Status != models.CodeReviewAssessmentRunning && a.Status != models.CodeReviewAssessmentPublishing) {
		return false, nil
	}
	if a.RepositoryID != job.RepositoryID || a.PullRequestID != job.PullRequestID || a.PolicyID != job.PolicyID || a.HeadSHA != job.HeadSHA || a.PublicationKey != job.OutputKey {
		return true, fmt.Errorf("staged assessment differs from original controller identity: %w", db.ErrCodeReviewAssessmentState)
	}
	if paused, err := pauseExpiredCodeReviewPublication(ctx, stores, a); paused || err != nil {
		if paused && err == nil {
			return true, errCodeReviewPublicationPaused
		}
		return true, err
	}
	metadata, err := stores.CodeReviews.GetBySessionID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return true, err
	}
	return true, resumeStagedFullAssessment(ctx, stores, services, job, metadata, a, nil)
}

// Called while holding the publication lock and job lease. Persist the known
// rejection outside the lock transaction, which rolls back when we return the
// original error. Terminal reconciliation can then retire this failed review.
func reconcileRejectedCodeReviewPublication(ctx context.Context, stores *Stores, assessment models.CodeReviewAssessment, attemptStartedReserved bool, submitErr error) error {
	if !attemptStartedReserved || !errors.Is(submitErr, codereviewsvc.ErrReviewPublicationRejected) {
		return submitErr
	}
	// Once GitHub has rejected the send, preserve that fact even if shutdown or
	// the publication deadline cancels the caller. Keep the independent write
	// bounded while the publication callback still owns its lock and job lease.
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := stores.CodeReviewAssessments.RestoreRejectedPublication(recoveryCtx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest, submitErr.Error()); err != nil {
		return errors.Join(submitErr, fmt.Errorf("record rejected review publication: %w", err))
	}
	return submitErr
}
