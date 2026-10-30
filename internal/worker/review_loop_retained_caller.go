package worker

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type retainedReviewLoopRecoverer interface {
	RecoverStart(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.SessionReviewLoop, error)
}

func recoverRetainedCallerReview(ctx context.Context, services *Services, orgID, sessionID, startID uuid.UUID) (*models.SessionReviewLoop, error) {
	if services == nil || services.ReviewLoops == nil {
		return nil, errors.New("retained review recovery service is unavailable")
	}
	recoverer, ok := services.ReviewLoops.(retainedReviewLoopRecoverer)
	if !ok {
		return nil, errors.New("retained review recovery capability is unavailable")
	}
	loop, err := recoverer.RecoverStart(ctx, orgID, sessionID, startID)
	if err != nil {
		return nil, err
	}
	if loop == nil || loop.ID != startID || loop.OrgID != orgID || loop.SessionID != sessionID {
		return nil, errors.New("retained review recovery identity conflict")
	}
	return loop, nil
}

// Recover before branch/head/policy work. The retained command is already the
// owner of this generation, even when no linked loop was visible to the caller.
func recoverPublicationReviewStart(ctx context.Context, stores *Stores, services *Services, run models.Session, publication *models.SessionPublication) (bool, error) {
	if publication.ReviewStartID == nil {
		return false, nil
	}
	loop, err := recoverRetainedCallerReview(ctx, services, run.OrgID, run.ID, *publication.ReviewStartID)
	// Explicit bypass keeps its existing publication behavior. Its own atomic
	// cancellation preserves the retained id for independent observation repair.
	if publication.ReviewGateState == models.SessionPublicationReviewGateNotRequired {
		if err != nil && !errors.Is(err, reviewloopsvc.ErrReviewLoopStartRefused) {
			return true, err
		}
		return false, nil
	}
	if err != nil && !errors.Is(err, reviewloopsvc.ErrReviewLoopStartRefused) {
		return true, err
	}
	if stores == nil || stores.SessionPublications == nil {
		return true, errors.New("retained publication review store is unavailable")
	}
	if errors.Is(err, reviewloopsvc.ErrReviewLoopStartRefused) {
		// The original creation is durably refused. Reuse the existing failed
		// review outcome rather than retrying that immutable refusal forever.
		return failRetainedPublicationReview(ctx, stores, run, publication)
	}
	if loop.Source != models.ReviewLoopSourcePublication || loop.ChangesetID == nil || *loop.ChangesetID != publication.ChangesetID {
		return true, errors.New("retained publication review caller mismatch")
	}
	// Only the existing durable stranded-restart marker with its reset caller
	// permits a fresh generation. A first-link-never-installed terminal owner
	// still uses its finite outcome even though its link is also NULL.
	if reviewloopsvc.IsStrandedPublicationRestart(*loop, *publication) {
		return false, nil
	}
	switch loop.Status {
	case models.ReviewLoopStatusRunning:
		if publication.ReviewGateState != models.SessionPublicationReviewGatePending {
			return true, nil
		}
		if loop.WorkspaceRevision == nil || loop.DesiredHeadSHA == nil {
			return true, errors.New("retained publication review evidence is unavailable")
		}
		if err := stores.SessionPublications.LinkReviewLoop(ctx, run.OrgID, run.ID, publication.ChangesetID, loop.ID, loop.MaxPasses, *loop.WorkspaceRevision, *loop.DesiredHeadSHA); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return true, nil
			}
			return true, err
		}
		publication.ReviewLoopID = &loop.ID
		publication.ReviewWorkspaceRevision = loop.WorkspaceRevision
		publication.ReviewDesiredHeadSHA = loop.DesiredHeadSHA
		return true, nil
	case models.ReviewLoopStatusNeedsHumanDecision, models.ReviewLoopStatusCancelled:
		if publication.ReviewGateState == models.SessionPublicationReviewGateNeedsHuman {
			return true, nil
		}
		return settleRetainedPublicationReviewGate(ctx, stores, run, publication, models.SessionPublicationReviewGateNeedsHuman)
	case models.ReviewLoopStatusFailed:
		return failRetainedPublicationReview(ctx, stores, run, publication)
	}
	// Clean originals still pass through the existing current-head comparison;
	// unresolved startup ownership must never create another generation.
	if loop.AdmissionState != nil {
		switch *loop.AdmissionState {
		case models.ReviewLoopAdmissionPending, models.ReviewLoopAdmissionAttempting, models.ReviewLoopAdmissionUnknown, models.ReviewLoopAdmissionAttention:
			return true, nil
		}
	}
	return false, nil
}

func failRetainedPublicationReview(ctx context.Context, stores *Stores, run models.Session, publication *models.SessionPublication) (bool, error) {
	if publication.ReviewGateState == models.SessionPublicationReviewGateFailed {
		return true, nil
	}
	return settleRetainedPublicationReviewGate(ctx, stores, run, publication, models.SessionPublicationReviewGateFailed)
}

func settleRetainedPublicationReviewGate(ctx context.Context, stores *Stores, run models.Session, publication *models.SessionPublication, gate models.SessionPublicationReviewGateState) (bool, error) {
	matched, err := stores.SessionPublications.SetRetainedReviewGate(ctx, run.OrgID, db.RetainedReviewGateScope{
		SessionID: run.ID, ChangesetID: publication.ChangesetID, StartID: *publication.ReviewStartID,
		Source: models.ReviewLoopSourcePublication,
	}, gate)
	if err != nil {
		return true, err
	}
	if matched {
		publication.ReviewGateState = gate
	}
	// A stale candidate is handled without granting publication or fresh-start
	// authority. A later owner rereads its current durable caller normally.
	return true, nil
}

// failAutomationPrePRReview keeps the existing finite terminal-failure mapping
// for both a failed loop and an original start known to have been refused.
func failAutomationPrePRReview(ctx context.Context, stores *Stores, logger zerolog.Logger, run models.Session, changesetID *uuid.UUID) (bool, error) {
	if err := setPublicationReviewGate(ctx, stores, run, changesetID, models.SessionPublicationReviewGateFailed); err != nil {
		return false, err
	}
	if stateErr := updateChangesetPRCreationState(ctx, stores, run.OrgID, run.ID, nil, models.PRCreationStateFailed, "Pre-PR review did not complete cleanly."); stateErr != nil {
		logger.Error().Err(stateErr).Str("session_id", run.ID.String()).Msg("failed to mark PR creation blocked by review loop")
	}
	return false, nil
}

// finishRetainedAutomationReview applies the existing status mapping only to
// the current pending publication generation. A lost CAS cannot advance any
// publication or overwrite a newer explicit action.
func finishRetainedAutomationReview(ctx context.Context, stores *Stores, logger zerolog.Logger, run models.Session, changesetID *uuid.UUID, startID uuid.UUID, status models.ReviewLoopStatus) (bool, error) {
	gate := models.SessionPublicationReviewGateFailed
	switch status {
	case models.ReviewLoopStatusClean:
		gate = models.SessionPublicationReviewGatePassed
	case models.ReviewLoopStatusRunning:
		gate = models.SessionPublicationReviewGatePending
	case models.ReviewLoopStatusNeedsHumanDecision:
		gate = models.SessionPublicationReviewGateNeedsHuman
	}
	if stores != nil && stores.SessionPublications != nil && changesetID != nil {
		matched, err := stores.SessionPublications.SetRetainedReviewGate(ctx, run.OrgID, db.RetainedReviewGateScope{
			SessionID: run.ID, ChangesetID: *changesetID, StartID: startID,
			Source: models.ReviewLoopSourceAutomation, AutomationRunID: run.AutomationRunID,
		}, gate)
		if err != nil {
			return false, err
		}
		if !matched {
			return false, nil
		}
	}
	switch status {
	case models.ReviewLoopStatusClean:
		return true, nil
	case models.ReviewLoopStatusRunning:
		logger.Info().Str("session_id", run.ID.String()).Str("review_loop_id", startID.String()).Msg("open_pr waiting for pre-pr review loop")
		retryAfter := prePRReviewRetryDelay
		return false, &RetryableError{Err: errors.New("pre-pr review loop is still running"), RetryAfter: &retryAfter}
	}
	message := "Pre-PR review did not complete cleanly."
	if status == models.ReviewLoopStatusNeedsHumanDecision {
		message = "Pre-PR review needs human decision."
	}
	if stateErr := updateChangesetPRCreationState(ctx, stores, run.OrgID, run.ID, nil, models.PRCreationStateFailed, message); stateErr != nil {
		logger.Error().Err(stateErr).Str("session_id", run.ID.String()).Msg("failed to mark PR creation blocked by review loop")
	}
	return false, nil
}
