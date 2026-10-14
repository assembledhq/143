package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// This value carries immutable admission provenance to either terminal writer;
// no after-error database read is allowed to reconstruct the failed generation.
type codeReviewPreparationIntentError struct {
	Intent models.CodeReviewPreparationIntent
	Err    error
}

func (e *codeReviewPreparationIntentError) Error() string { return e.Err.Error() }
func (e *codeReviewPreparationIntentError) Unwrap() error { return e.Err }

type preparationIntentLeaseStore interface {
	DeadLetterContinueSessionWithPreparationIntentWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.CodeReviewPreparationIntent) (bool, error)
}

var _ preparationIntentLeaseStore = (*db.JobStore)(nil)

func captureCodeReviewPreparationIntent(ctx context.Context, stores *Stores, services *Services, session models.Session, thread *models.SessionThread, recheck bool) (*models.CodeReviewPreparationIntent, error) {
	if recheck || session.Origin != models.SessionOriginCodeReview {
		return nil, nil
	}
	active, err := codeReviewPreparationActiveForSession(ctx, stores, services, &session)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	jobID, hasJob := jobctx.JobIDFromContext(ctx)
	token, hasToken := jobctx.LockTokenFromContext(ctx)
	if !hasJob || !hasToken || jobID == uuid.Nil || token == uuid.Nil {
		return nil, &FatalError{Err: errors.New("activated review continuation requires durable job and lease provenance")}
	}
	if stores == nil || stores.CodeReviews == nil {
		return nil, fmt.Errorf("review preparation intent store unavailable")
	}
	intent, err := stores.CodeReviews.CaptureCodeReviewPreparationIntent(ctx, session.OrgID, jobID, token, session, thread)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &FatalError{Err: fmt.Errorf("review continuation admission superseded: %w", err)}
	}
	if err != nil {
		return nil, err
	}
	return &intent, nil
}

func terminalCodeReviewPreparationIntent(captured *models.CodeReviewPreparationIntent, err error) error {
	if captured == nil {
		return nil
	}
	var typed *agent.CodeReviewWorkspacePreparationError
	cleanup := errors.Is(err, agent.ErrCodeReviewPreparationCleanup)
	integrity := errors.Is(err, agent.ErrCodeReviewSourceIntegrity)
	if !errors.As(err, &typed) && !cleanup && !integrity {
		return nil
	}
	if !cleanup && !integrity {
		if errors.Is(err, agent.ErrStalePullRequestHead) {
			return nil
		}
		if !errors.Is(err, context.DeadlineExceeded) && (errors.Is(err, context.Canceled) || errors.Is(err, agent.ErrSessionCancelled) || errors.Is(err, agent.ErrThreadCancelledBeforeWorkspaceReady)) {
			return nil
		}
		if typed != nil && typed.Stage != agent.CodeReviewPreparationStageDependencySetup && !errors.Is(err, context.DeadlineExceeded) && (errors.Is(err, agent.ErrSandboxWorkspaceNotReady) || errors.Is(err, agent.ErrSandboxCapacity) || errors.Is(err, agent.ErrSnapshotPending) || errors.Is(err, agent.ErrSessionInterrupted) || errors.Is(err, agent.ErrStaleSandboxIDCleared) || errors.Is(err, agent.ErrSandboxOnDifferentNode) || errors.Is(err, agent.ErrThreadRuntimeAlreadyActive)) {
			return nil
		}
	}
	intent := *captured
	if typed != nil {
		intent.Stage = models.CodeReviewPreparationStage(typed.Stage)
	}
	switch {
	case cleanup:
		intent.FailureClass = models.CodeReviewPreparationFailureCleanup
	case integrity:
		intent.FailureClass = models.CodeReviewPreparationFailureIntegrity
	case errors.Is(err, context.DeadlineExceeded):
		intent.FailureClass = models.CodeReviewPreparationFailureDeadline
	default:
		intent.FailureClass = models.CodeReviewPreparationFailureSetup
	}
	if validateErr := intent.Validate(); validateErr != nil {
		return &FatalError{Err: fmt.Errorf("invalid captured review preparation intent: %w", validateErr)}
	}
	// Reconstruct the finite cause so joined result-persistence diagnostics cannot
	// enter the terminal job record or author-visible operational reason.
	safe := codeReviewPreparationIntentCause(intent)
	return &FatalError{Err: &codeReviewPreparationIntentError{Intent: intent, Err: safe}}
}

func codeReviewPreparationIntentCause(intent models.CodeReviewPreparationIntent) error {
	var cause error
	switch intent.FailureClass {
	case models.CodeReviewPreparationFailureCleanup:
		cause = agent.ErrCodeReviewPreparationCleanup
	case models.CodeReviewPreparationFailureIntegrity:
		cause = agent.ErrCodeReviewSourceIntegrity
	case models.CodeReviewPreparationFailureDeadline:
		cause = context.DeadlineExceeded
	default:
		cause = errors.New("preparation failed")
	}
	return agent.NewCodeReviewWorkspacePreparationError(string(intent.Stage), cause)
}

func reconcileTerminalCodeReviewPreparationIntents(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload) (bool, error) {
	if stores == nil || stores.Jobs == nil || stores.CodeReviews == nil {
		return false, nil
	}
	batch, err := stores.Jobs.PendingCodeReviewPreparationIntents(ctx, job.OrgID, job.MetadataID, job.SessionID)
	if err != nil {
		return true, err
	}
	for _, intent := range batch.Intents {
		reason := codeReviewWorkspacePreparationFailureMessage(codeReviewPreparationIntentCause(intent))
		outcome, err := stores.CodeReviews.ProjectCodeReviewPreparationIntent(ctx, job.OrgID, intent, reason)
		if err != nil {
			return true, err
		}
		if outcome.Pending {
			return true, &RetryableError{Err: errors.New("terminal review preparation projection waiting for runtime ownership to drain"), RetryAfter: durationPtr(codeReviewWorkspaceWait)}
		}
		if outcome.Projected {
			return true, reconcileCodeReviewWorkspacePreparationFailure(ctx, stores, services, logger, job, reason)
		}
	}
	if batch.More {
		return true, &RetryableError{Err: errors.New("terminal review preparation journal has another bounded batch"), RetryAfter: durationPtr(codeReviewWorkspaceWait)}
	}
	return false, nil
}
