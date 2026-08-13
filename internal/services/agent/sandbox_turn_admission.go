package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ErrSandboxTurnConcurrency is returned when the shared organization turn
// limit is full. It is distinct from worker-local ErrSandboxCapacity so
// callers do not pointlessly move an affinity-bound turn to another node.
var ErrSandboxTurnConcurrency = errors.New("sandbox turn concurrency limit reached")

const sandboxRoutingCleanupTimeout = 5 * time.Second

type activeSandboxTurnCounter interface {
	CountActiveSandboxTurnsByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
}

type sandboxSlotReservationReleaser interface {
	ReleaseSandboxSlotReservationWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error)
}

type sandboxRoutingPlacementReleaser interface {
	ReleaseSandboxRoutingPlacementWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error)
}

// admitSandboxTurn is the shared admission boundary for RunAgent and
// ContinueSession. Every workload draws from the same organization turn limit;
// workload class only affects worker placement and reserved interactive
// capacity. Existing-sandbox continuations still pass org admission but do not
// consume another local slot.
func (o *Orchestrator) admitSandboxTurn(
	ctx context.Context,
	session *models.Session,
	purpose string,
	needsFreshSandbox bool,
	excludeCurrentRunningSession bool,
) (*SandboxCapacityReservation, error) {
	if session == nil {
		return nil, fmt.Errorf("admit sandbox turn: session is nil")
	}
	workloadClass := models.SandboxWorkloadClassForSession(session)
	if err := workloadClass.Validate(); err != nil {
		return nil, fmt.Errorf("admit sandbox turn: %w", err)
	}
	if err := o.checkSharedTurnConcurrency(ctx, session.OrgID, excludeCurrentRunningSession); err != nil {
		o.releaseRejectedSandboxRoutingPlacement(ctx, o.logger)
		return nil, err
	}

	if !needsFreshSandbox || o.sandboxCapacity == nil {
		return nil, nil
	}
	var jobID *uuid.UUID
	if currentJobID, ok := jobctx.JobIDFromContext(ctx); ok && currentJobID != uuid.Nil {
		jobID = &currentJobID
	}
	reservation, err := o.sandboxCapacity.Acquire(ctx, SandboxCapacityRequest{
		Purpose:       purpose,
		SessionID:     session.ID.String(),
		OrgID:         session.OrgID.String(),
		JobID:         jobID,
		WorkloadClass: workloadClass,
	})
	if err != nil {
		o.releaseRejectedSandboxRoutingPlacement(ctx, o.logger)
		return nil, err
	}
	return reservation, nil
}

func (o *Orchestrator) checkSharedTurnConcurrency(ctx context.Context, orgID uuid.UUID, excludeCurrentRunning bool) error {
	if o.orgs == nil {
		return o.checkConcurrency(ctx, orgID, excludeCurrentRunning)
	}
	counter, ok := o.jobs.(activeSandboxTurnCounter)
	if !ok {
		return o.checkConcurrency(ctx, orgID, excludeCurrentRunning)
	}
	org, err := o.orgs.GetByID(ctx, orgID)
	if err != nil {
		return fmt.Errorf("load org settings for shared turn admission: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return fmt.Errorf("parse org settings for shared turn admission: %w", err)
	}
	active, err := counter.CountActiveSandboxTurnsByOrg(ctx, orgID)
	if err != nil {
		return err
	}
	_, currentJobIncluded := jobctx.JobIDFromContext(ctx)
	// The shared job counter identifies the current claimed turn directly, so
	// its greater-than comparison supersedes the legacy session-row
	// excludeCurrentRunning adjustment used by the fallbacks above.
	if active > settings.MaxConcurrentRuns || (!currentJobIncluded && active >= settings.MaxConcurrentRuns) {
		return fmt.Errorf("%w: %d/%d agent turns active", ErrSandboxTurnConcurrency, active, settings.MaxConcurrentRuns)
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
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxRoutingCleanupTimeout)
	defer cancel()
	released, err := releaser.ReleaseSandboxSlotReservationWithLease(releaseCtx, jobID, lockToken)
	if err != nil {
		log.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to release durable sandbox routing reservation")
		return
	}
	if released {
		log.Debug().Str("job_id", jobID.String()).Msg("released durable sandbox routing reservation")
	}
}

func (o *Orchestrator) releaseRejectedSandboxRoutingPlacement(ctx context.Context, log zerolog.Logger) {
	releaser, ok := o.jobs.(sandboxRoutingPlacementReleaser)
	if !ok {
		return
	}
	jobID, hasJobID := jobctx.JobIDFromContext(ctx)
	lockToken, hasLockToken := jobctx.LockTokenFromContext(ctx)
	if !hasJobID || !hasLockToken {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxRoutingCleanupTimeout)
	defer cancel()
	released, err := releaser.ReleaseSandboxRoutingPlacementWithLease(releaseCtx, jobID, lockToken)
	if err != nil {
		log.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to release rejected sandbox routing placement")
		return
	}
	if released {
		log.Debug().Str("job_id", jobID.String()).Msg("released rejected sandbox routing placement")
	}
}
