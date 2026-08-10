package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ErrSandboxTurnConcurrency is returned when a workload-specific org limit is
// full. It is distinct from worker-local ErrSandboxCapacity so callers do not
// pointlessly move an affinity-bound turn to another node.
var ErrSandboxTurnConcurrency = errors.New("sandbox turn concurrency limit reached")

type activeSandboxTurnCounter interface {
	CountActiveSandboxTurnsByOrgAndClass(ctx context.Context, orgID uuid.UUID, workloadClass models.SandboxWorkloadClass) (int, error)
}

type sandboxSlotReservationReleaser interface {
	ReleaseSandboxSlotReservationWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error)
}

// admitSandboxTurn is the shared admission boundary for RunAgent and
// ContinueSession. It applies workload-specific org policy first, then reserves
// local capacity only when the turn needs a fresh sandbox. Existing-sandbox
// continuations still pass org admission but do not consume another local slot.
func (o *Orchestrator) admitSandboxTurn(
	ctx context.Context,
	session *models.Session,
	purpose string,
	needsFreshSandbox bool,
	excludeCurrentInteractiveSession bool,
) (*SandboxCapacityReservation, error) {
	if session == nil {
		return nil, fmt.Errorf("admit sandbox turn: session is nil")
	}
	workloadClass := models.SandboxWorkloadClassForSession(session)
	switch workloadClass {
	case models.SandboxWorkloadClassInteractive:
		if err := o.checkConcurrency(ctx, session.OrgID, excludeCurrentInteractiveSession); err != nil {
			return nil, err
		}
	case models.SandboxWorkloadClassCodeReview:
		if err := o.checkCodeReviewTurnConcurrency(ctx, session.OrgID); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("admit sandbox turn: %w", workloadClass.Validate())
	}

	if !needsFreshSandbox || o.sandboxCapacity == nil {
		return nil, nil
	}
	reservation, err := o.sandboxCapacity.Acquire(ctx, SandboxCapacityRequest{
		Purpose:       purpose,
		SessionID:     session.ID.String(),
		OrgID:         session.OrgID.String(),
		WorkloadClass: workloadClass,
	})
	if err != nil {
		return nil, err
	}
	return reservation, nil
}

func (o *Orchestrator) checkCodeReviewTurnConcurrency(ctx context.Context, orgID uuid.UUID) error {
	if o.orgs == nil {
		o.logger.Warn().Str("org_id", orgID.String()).Msg("code-review turn admission skipped because organization store is unavailable")
		return nil
	}
	counter, ok := o.jobs.(activeSandboxTurnCounter)
	if !ok {
		o.logger.Warn().Str("org_id", orgID.String()).Msg("code-review turn admission skipped because active-turn counter is unavailable")
		return nil
	}
	org, err := o.orgs.GetByID(ctx, orgID)
	if err != nil {
		return fmt.Errorf("load org settings for code-review turn admission: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return fmt.Errorf("parse org settings for code-review turn admission: %w", err)
	}
	active, err := counter.CountActiveSandboxTurnsByOrgAndClass(ctx, orgID, models.SandboxWorkloadClassCodeReview)
	if err != nil {
		return err
	}
	// The worker has already claimed the current job, so it is included in the
	// count. Equality is allowed; only the first turn beyond the limit waits.
	if active > settings.CodeReviewMaxConcurrentTurns {
		return fmt.Errorf("%w: %d/%d code-review turns active", ErrSandboxTurnConcurrency, active, settings.CodeReviewMaxConcurrentTurns)
	}
	return nil
}

func (o *Orchestrator) releaseSandboxRoutingReservation(ctx context.Context, log zerolog.Logger) {
	releaser, ok := o.jobs.(sandboxSlotReservationReleaser)
	if !ok {
		return
	}
	jobID, hasJobID := jobctx.JobIDFromContext(ctx)
	lockToken, hasLockToken := jobctx.LockTokenFromContext(ctx)
	if !hasJobID || !hasLockToken {
		return
	}
	released, err := releaser.ReleaseSandboxSlotReservationWithLease(ctx, jobID, lockToken)
	if err != nil {
		log.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to release durable sandbox routing reservation")
		return
	}
	if released {
		log.Debug().Str("job_id", jobID.String()).Msg("released durable sandbox routing reservation")
	}
}
