package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type reviewSetupHold struct {
	stop    context.CancelFunc
	done    chan struct{}
	release func()
	once    sync.Once
}

func (h *reviewSetupHold) Release() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		h.stop()
		timer := time.NewTimer(threadRuntimeStateUpdateTimeout)
		defer timer.Stop()
		select {
		case <-h.done:
		case <-timer.C:
		}
		h.release()
	})
}

func (o *Orchestrator) acquireReviewSetupHold(ctx context.Context, session *models.Session, sandbox *Sandbox, cancelRun context.CancelCauseFunc) (*reviewSetupHold, error) {
	if session.Origin != models.SessionOriginCodeReview || o.sandboxHolders == nil {
		return nil, nil
	}
	holderID, leaseToken := uuid.New(), uuid.New()
	kind := models.SessionSandboxHolderKindOperator
	releaseHolder := func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), threadRuntimeStateUpdateTimeout)
		defer cancel()
		if _, err := o.sandboxHolders.ReleaseWithLease(releaseCtx, session.OrgID, session.ID, kind, holderID, leaseToken); err != nil {
			o.logger.Warn().Err(err).Msg("failed to release review setup holder")
		}
	}
	writeCtx, writeCancel := context.WithTimeout(ctx, threadRuntimeStateUpdateTimeout)
	defer writeCancel()
	if _, err := o.sandboxHolders.CreateActive(writeCtx, session.OrgID, db.CreateSessionSandboxHolderParams{
		SessionID: session.ID, ContainerID: sandbox.ID, HolderKind: kind,
		HolderID: holderID, OwnerNodeID: o.nodeID, LeaseToken: leaseToken,
		LeaseDuration: threadRuntimeLeaseDuration,
	}); err != nil {
		releaseHolder()
		return nil, fmt.Errorf("%w: acquire review setup holder: %w", ErrSandboxWorkspaceNotReady, err)
	}
	deadline := withCodeReviewPreparationBudget(ctx).Value(codeReviewPreparationDeadlineKey{}).(time.Time)
	if deadline.Before(time.Now()) {
		deadline = time.Now()
	}
	heartbeatCtx, stop := context.WithDeadline(ctx, deadline.Add(CodeReviewWorkspaceVerificationTimeout+5*time.Second))
	hold := &reviewSetupHold{stop: stop, done: make(chan struct{}), release: releaseHolder}
	go func() {
		defer close(hold.done)
		ticker := time.NewTicker(threadRuntimeLeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				if ctx.Err() == nil && errors.Is(context.Cause(heartbeatCtx), context.DeadlineExceeded) {
					cancelRun(context.Cause(heartbeatCtx))
				}
				return
			case <-ticker.C:
				writeCtx, cancel := context.WithTimeout(heartbeatCtx, threadRuntimeStateUpdateTimeout)
				owned, err := o.sandboxHolders.HeartbeatWithLease(writeCtx, session.OrgID, session.ID, kind, holderID, leaseToken, threadRuntimeLeaseDuration)
				cancel()
				if heartbeatCtx.Err() != nil {
					if ctx.Err() == nil && errors.Is(context.Cause(heartbeatCtx), context.DeadlineExceeded) {
						cancelRun(context.Cause(heartbeatCtx))
					}
					return
				}
				if err != nil || !owned {
					o.logger.Warn().Err(err).Msg("review setup holder lease was lost")
					cancelRun(ErrWorkerDrainCause)
					return
				}
			}
		}
	}()
	acquired, err := o.sessions.AcquireExistingTurnHold(writeCtx, session.OrgID, session.ID, sandbox.ID)
	if err != nil || !acquired {
		hold.Release()
		if err != nil {
			return nil, fmt.Errorf("%w: fence review setup container: %w", ErrSandboxWorkspaceNotReady, err)
		}
		resetCtx, resetCancel := context.WithTimeout(ctx, threadRuntimeStateUpdateTimeout)
		defer resetCancel()
		if _, err := o.sessions.ResetAfterLostReuse(resetCtx, session.OrgID, session.ID); err != nil {
			return nil, fmt.Errorf("%w: reopen fenced review setup: %w", ErrSandboxWorkspaceNotReady, err)
		}
		return nil, ErrStaleSandboxIDCleared
	}
	return hold, nil
}
