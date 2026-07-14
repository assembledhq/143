package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/requestctx"
	"github.com/assembledhq/143/internal/services/agent"
)

var (
	ErrExecutorInvalidHandoff = errors.New("invalid session executor handoff")
	ErrExecutorLostLease      = errors.New("session executor lost fenced ownership")
)

type executorRuntimeExecutorStore interface {
	GetByID(ctx context.Context, executorID uuid.UUID) (models.SessionExecutor, error)
	MarkRunningWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID, leaseDuration time.Duration) (bool, error)
	HeartbeatWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID, leaseDuration time.Duration) (bool, models.DrainIntent, error)
	MarkDrainingWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID) (bool, error)
	MarkHumanInputCheckpointByJob(ctx context.Context, orgID, jobID, lockToken uuid.UUID) (bool, error)
	MarkTerminalWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID, status models.SessionExecutorStatus, exitCode *int, lastError string) (bool, error)
}

type executorRuntimeJobStore interface {
	GetRunningForSessionExecutor(ctx context.Context, orgID, jobID, lockToken, executorID uuid.UUID) (*models.Job, bool, error)
	RenewLeaseForSessionExecutor(ctx context.Context, orgID, jobID, lockToken, executorID uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error)
	RenewLease(ctx context.Context, jobID, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error)
	MarkSucceededWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error)
	MarkFailedWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error)
	RetryWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error)
	RetryWithoutConsumingAttemptWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error)
	DeadLetterWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error)
}

type SessionExecutorRuntime struct {
	Executors executorRuntimeExecutorStore
	Jobs      executorRuntimeJobStore
	Stores    *Stores
	Services  *Services
	Handlers  map[string]JobHandler
	Logger    zerolog.Logger

	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	RenewInterval     time.Duration

	BootValidationTimeout  time.Duration
	BootValidationInterval time.Duration
}

func (r *SessionExecutorRuntime) Run(ctx context.Context, executorID uuid.UUID) error {
	if r.Executors == nil {
		return fmt.Errorf("session executor store is required")
	}
	if r.Jobs == nil {
		return fmt.Errorf("job store is required")
	}
	leaseDuration := r.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = defaultLeaseDuration
	}

	executor, err := r.Executors.GetByID(ctx, executorID)
	if err != nil {
		r.loggerPtr().Error().Err(err).Str("executor_id", executorID.String()).Msg("session executor failed to load executor row")
		return err
	}
	logger := r.logger().With().
		Str("org_id", executor.OrgID.String()).
		Str("session_id", executor.SessionID.String()).
		Str("job_id", executor.JobID.String()).
		Str("job_type", executor.JobType).
		Str("executor_id", executor.ID.String()).
		Str("host_node_id", executor.HostNodeID).
		Str("build_sha", executor.BuildSHA).
		Logger()
	logger.Info().Str("executor_status", string(executor.Status)).Msg("session executor boot loaded executor row")
	if executor.Status == models.SessionExecutorStatusCompleted ||
		executor.Status == models.SessionExecutorStatusFailed ||
		executor.Status == models.SessionExecutorStatusLost {
		logger.Warn().Str("executor_status", string(executor.Status)).Msg("session executor boot found terminal executor row")
		return fmt.Errorf("%w: executor is already terminal: %s", ErrExecutorInvalidHandoff, executor.Status)
	}

	bootStartedAt := time.Now()
	logger.Info().Msg("session executor boot waiting for job handoff")
	job, ok, err := r.waitForRunningJob(ctx, executor)
	if err != nil {
		logger.Error().Err(err).Dur("wait_duration", time.Since(bootStartedAt)).Msg("session executor boot failed while waiting for job handoff")
		return err
	}
	if !ok || job == nil {
		logger.Error().Dur("wait_duration", time.Since(bootStartedAt)).Msg("session executor boot validation timed out waiting for job handoff")
		r.markExecutorTerminal(context.WithoutCancel(ctx), executor, models.SessionExecutorStatusFailed, 1, "executor boot validation timed out waiting for job handoff")
		return fmt.Errorf("%w: running job ownership does not match executor", ErrExecutorInvalidHandoff)
	}
	logger.Info().
		Dur("wait_duration", time.Since(bootStartedAt)).
		Str("job_status", string(job.Status)).
		Int("attempts", job.Attempts).
		Int("max_attempts", job.MaxAttempts).
		Msg("session executor boot observed job handoff")
	if job.LockToken == nil || *job.LockToken != executor.LockToken {
		logger.Error().Msg("session executor boot found job lock token mismatch")
		return fmt.Errorf("%w: job lock token mismatch", ErrExecutorInvalidHandoff)
	}

	ok, err = r.Executors.MarkRunningWithLease(ctx, executor.OrgID, executor.ID, executor.LockToken, leaseDuration)
	if err != nil {
		logger.Error().Err(err).Msg("session executor failed to mark executor running")
		return err
	}
	if !ok {
		logger.Warn().Msg("session executor lost lease before marking executor running")
		return fmt.Errorf("%w: executor row was not claimable", ErrExecutorLostLease)
	}
	logger.Info().Dur("lease_duration", leaseDuration).Msg("session executor marked executor running")

	handler, ok := r.handlerFor(job.JobType)
	if !ok {
		err := fmt.Errorf("no handler for job type: %s", job.JobType)
		logger.Error().Err(err).Msg("session executor has no handler for job type")
		r.markJobFailed(ctx, executor, job, err.Error())
		r.markExecutorTerminal(ctx, executor, models.SessionExecutorStatusFailed, 1, err.Error())
		return err
	}

	handlerCtx := withJobOrgID(ctx, job.OrgID)
	handlerCtx = jobctx.WithDeadLetterHooks(handlerCtx)
	handlerCtx = jobctx.WithJobID(handlerCtx, job.ID)
	handlerCtx = requestctx.WithMutationIDFromPayload(handlerCtx, job.Payload)
	handlerCtx = jobctx.WithLockToken(handlerCtx, executor.LockToken)
	handlerCtx = jobctx.WithOwnerKind(handlerCtx, string(models.JobOwnerKindSessionExecutor))
	handlerCtx = jobctx.WithJobCreatedAt(handlerCtx, job.CreatedAt)
	handlerCtx = jobctx.WithWorkerNodeID(handlerCtx, executor.HostNodeID)
	if job.TargetNodeID != nil && *job.TargetNodeID != "" && *job.TargetNodeID != executor.HostNodeID {
		handlerCtx = jobctx.WithDeadTargetNode(handlerCtx, *job.TargetNodeID)
	}
	handlerCtx, cancelHandler := context.WithCancel(handlerCtx)

	var lostOwnership atomic.Bool
	var drainHandled atomic.Bool
	var wg sync.WaitGroup
	r.startOwnershipLoops(handlerCtx, &wg, executor, job, leaseDuration, &lostOwnership, &drainHandled, cancelHandler)
	drainDone := r.startDrainWatcher(ctx, executor, &drainHandled)

	logger.Info().
		Str("executor_id", executor.ID.String()).
		Str("job_id", job.ID.String()).
		Str("job_type", job.JobType).
		Str("deploy_generation", executor.BuildSHA).
		Str("host_node_id", executor.HostNodeID).
		Str("drain_intent", string(executor.DrainIntent)).
		Msg("session executor processing job")

	runErr := handler(handlerCtx, job.JobType, job.Payload)
	cancelHandler()
	wg.Wait()
	if ctx.Err() != nil {
		<-drainDone
	}

	if lostOwnership.Load() {
		logger.Warn().Msg("session executor lost ownership while handler was running")
		r.markExecutorTerminal(context.WithoutCancel(ctx), executor, models.SessionExecutorStatusLost, 1, ErrExecutorLostLease.Error())
		return ErrExecutorLostLease
	}
	if drainHandled.Load() && errors.Is(runErr, context.Canceled) {
		var retryable *RetryableError
		if errors.As(runErr, &retryable) {
			r.loggerPtr().Info().
				Str("executor_id", executor.ID.String()).
				Str("job_id", job.ID.String()).
				Msg("session executor drain interrupted handler; preserving retryable decision")
		} else {
			r.loggerPtr().Info().
				Str("executor_id", executor.ID.String()).
				Str("job_id", job.ID.String()).
				Msg("session executor drain interrupted handler; retrying job")
			runErr = fmt.Errorf("%w: %w", agent.ErrSessionInterrupted, runErr)
		}
	}

	if runErr != nil {
		logger.Warn().Err(runErr).Msg("session executor handler finished with error")
	} else {
		logger.Info().Msg("session executor handler finished successfully")
	}
	return r.finishAttempt(ctx, handlerCtx, executor, job, runErr)
}

func (r *SessionExecutorRuntime) waitForRunningJob(ctx context.Context, executor models.SessionExecutor) (*models.Job, bool, error) {
	timeout := r.BootValidationTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	interval := r.BootValidationInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		job, ok, err := r.Jobs.GetRunningForSessionExecutor(ctx, executor.OrgID, executor.JobID, executor.LockToken, executor.ID)
		if err != nil || ok {
			return job, ok, err
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return nil, false, nil
		case <-timer.C:
		}
	}
}

func (r *SessionExecutorRuntime) handlerFor(jobType string) (JobHandler, bool) {
	if r.Handlers != nil {
		h, ok := r.Handlers[jobType]
		return h, ok
	}
	if r.Services == nil || r.Stores == nil {
		return nil, false
	}
	services := *r.Services
	services.SessionExecutorDispatcher = nil
	services.RequireSessionExecutorDispatcher = false
	switch jobType {
	case "run_agent":
		return newRunAgentHandler(r.Stores, &services, r.logger()), true
	case "continue_session":
		return newContinueSessionHandler(r.Stores, &services, r.logger()), true
	default:
		return nil, false
	}
}

func (r *SessionExecutorRuntime) startOwnershipLoops(ctx context.Context, wg *sync.WaitGroup, executor models.SessionExecutor, job *models.Job, leaseDuration time.Duration, lostOwnership *atomic.Bool, drainHandled *atomic.Bool, cancel context.CancelFunc) {
	if r.HeartbeatInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.heartbeatLoop(ctx, executor, leaseDuration, lostOwnership, drainHandled, cancel)
		}()
	}
	if r.RenewInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.renewLoop(ctx, executor, job, leaseDuration, lostOwnership, cancel)
		}()
	}
}

func (r *SessionExecutorRuntime) heartbeatLoop(ctx context.Context, executor models.SessionExecutor, leaseDuration time.Duration, lostOwnership *atomic.Bool, drainHandled *atomic.Bool, cancel context.CancelFunc) {
	ticker := time.NewTicker(r.HeartbeatInterval)
	defer ticker.Stop()
	budgetStopRequested := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, drainIntent, err := r.Executors.HeartbeatWithLease(ctx, executor.OrgID, executor.ID, executor.LockToken, leaseDuration)
			if err != nil {
				r.loggerPtr().Warn().Err(err).Str("executor_id", executor.ID.String()).Msg("failed to heartbeat session executor")
				continue
			}
			if !ok {
				lostOwnership.Store(true)
				cancel()
				return
			}
			if budgetStopRequested {
				continue
			}
			if drainIntent == models.DrainIntentDeployBudgetExpired {
				budgetStopRequested = true
				drainHandled.Store(true)
				if r.Services == nil || r.Services.Orchestrator == nil {
					r.loggerPtr().Warn().
						Str("executor_id", executor.ID.String()).
						Str("session_id", executor.SessionID.String()).
						Msg("deploy budget expired but orchestrator service is unavailable")
					cancel()
					return
				}
				if !r.Services.Orchestrator.RequestSessionStopByID(executor.SessionID, agent.StopReasonDeployBudgetExpired) {
					r.loggerPtr().Warn().
						Str("executor_id", executor.ID.String()).
						Str("session_id", executor.SessionID.String()).
						Msg("deploy budget expired but session stop request was not accepted")
					cancel()
					return
				}
				r.loggerPtr().Info().
					Str("executor_id", executor.ID.String()).
					Str("session_id", executor.SessionID.String()).
					Msg("deploy budget expired; requested typed session stop")
			}
		}
	}
}

func (r *SessionExecutorRuntime) renewLoop(ctx context.Context, executor models.SessionExecutor, job *models.Job, leaseDuration time.Duration, lostOwnership *atomic.Bool, cancel context.CancelFunc) {
	ticker := time.NewTicker(r.RenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, ok, err := r.Jobs.RenewLeaseForSessionExecutor(ctx, executor.OrgID, job.ID, executor.LockToken, executor.ID, leaseDuration)
			if err != nil {
				r.loggerPtr().Warn().Err(err).Str("job_id", job.ID.String()).Msg("failed to renew session executor job lease")
				continue
			}
			if !ok {
				lostOwnership.Store(true)
				cancel()
				return
			}
		}
	}
}

func (r *SessionExecutorRuntime) startDrainWatcher(ctx context.Context, executor models.SessionExecutor, drainHandled *atomic.Bool) <-chan struct{} {
	done := make(chan struct{})
	if ctx.Done() == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		<-ctx.Done()
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if ok, err := r.Executors.MarkDrainingWithLease(drainCtx, executor.OrgID, executor.ID, executor.LockToken); err != nil {
			r.loggerPtr().Warn().Err(err).Str("executor_id", executor.ID.String()).Msg("failed to mark session executor draining")
		} else if ok {
			r.loggerPtr().Info().Str("executor_id", executor.ID.String()).Msg("session executor marked draining")
		}
	}()
	return done
}

func (r *SessionExecutorRuntime) finishAttempt(ctx context.Context, handlerCtx context.Context, executor models.SessionExecutor, job *models.Job, err error) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err == nil {
		if marked, markErr := r.Executors.MarkHumanInputCheckpointByJob(writeCtx, executor.OrgID, job.ID, executor.LockToken); markErr != nil {
			r.loggerPtr().Warn().
				Err(markErr).
				Str("executor_id", executor.ID.String()).
				Str("job_id", job.ID.String()).
				Msg("failed to record human-input checkpoint drain intent")
		} else if marked {
			r.loggerPtr().Info().
				Str("executor_id", executor.ID.String()).
				Str("job_id", job.ID.String()).
				Str("drain_intent", string(models.DrainIntentHumanInputCheckpoint)).
				Msg("session executor released ownership at human-input checkpoint")
		}
		if ok := r.markJobSucceeded(writeCtx, executor, job); ok {
			r.markExecutorTerminal(writeCtx, executor, models.SessionExecutorStatusCompleted, 0, "")
		}
		return nil
	}

	var handoff *HandoffError
	if errors.As(err, &handoff) {
		return fmt.Errorf("session executor handler attempted nested handoff: %w", err)
	}

	var fatal *FatalError
	if errors.As(err, &fatal) {
		r.markJobDeadLetter(writeCtx, handlerCtx, executor, job, err)
		r.markExecutorTerminal(writeCtx, executor, models.SessionExecutorStatusFailed, 1, err.Error())
		return nil
	}

	var retryable *RetryableError
	if errors.As(err, &retryable) {
		if retryable.ConsumeAttempt && job.Attempts >= job.MaxAttempts {
			r.markJobDeadLetter(writeCtx, handlerCtx, executor, job, err)
			r.markExecutorTerminal(writeCtx, executor, models.SessionExecutorStatusFailed, 1, err.Error())
			return nil
		}
		if !retryable.BypassMaxRetryDuration && time.Since(job.CreatedAt) > maxRetryableDuration {
			timeoutErr := fmt.Errorf("retryable job timed out after %s: %w", maxRetryableDuration, err)
			r.markJobDeadLetter(writeCtx, handlerCtx, executor, job, timeoutErr)
			r.markExecutorTerminal(writeCtx, executor, models.SessionExecutorStatusFailed, 1, timeoutErr.Error())
			return nil
		}
		r.retryJob(writeCtx, executor, job, err.Error(), !retryable.ConsumeAttempt, retryable.RetryAfter, retryable.TargetNodeID, retryable.ClearTargetNodeID)
		r.markExecutorTerminal(writeCtx, executor, models.SessionExecutorStatusRequeued, 0, err.Error())
		return nil
	}

	if job.Attempts >= job.MaxAttempts {
		r.markJobDeadLetter(writeCtx, handlerCtx, executor, job, err)
		r.markExecutorTerminal(writeCtx, executor, models.SessionExecutorStatusFailed, 1, err.Error())
		return nil
	}
	r.retryJob(writeCtx, executor, job, err.Error(), false, nil, nil, false)
	r.markExecutorTerminal(writeCtx, executor, models.SessionExecutorStatusRequeued, 0, err.Error())
	return nil
}

func (r *SessionExecutorRuntime) retryJob(ctx context.Context, executor models.SessionExecutor, job *models.Job, errMsg string, preserveAttempts bool, override *time.Duration, targetNodeID *string, clearTargetNodeID bool) {
	backoff := retryBackoff(job.Attempts)
	if override != nil {
		backoff = *override
	}
	runAt := time.Now().Add(backoff)
	var (
		ok  bool
		err error
	)
	updateTarget := targetNodeID != nil || clearTargetNodeID
	if updateTarget {
		if targetStore, supportsTargetRetry := r.Jobs.(targetRetryLeaseStore); supportsTargetRetry {
			if preserveAttempts {
				ok, err = targetStore.RetryWithoutConsumingAttemptWithLeaseAndTarget(ctx, job.ID, executor.LockToken, errMsg, runAt, targetNodeID)
			} else {
				ok, err = targetStore.RetryWithLeaseAndTarget(ctx, job.ID, executor.LockToken, errMsg, runAt, targetNodeID)
			}
		} else if preserveAttempts {
			ok, err = r.Jobs.RetryWithoutConsumingAttemptWithLease(ctx, job.ID, executor.LockToken, errMsg, runAt)
		} else {
			ok, err = r.Jobs.RetryWithLease(ctx, job.ID, executor.LockToken, errMsg, runAt)
		}
	} else if preserveAttempts {
		ok, err = r.Jobs.RetryWithoutConsumingAttemptWithLease(ctx, job.ID, executor.LockToken, errMsg, runAt)
	} else {
		ok, err = r.Jobs.RetryWithLease(ctx, job.ID, executor.LockToken, errMsg, runAt)
	}
	if err != nil {
		r.loggerPtr().Warn().Err(err).Str("job_id", job.ID.String()).Msg("failed to schedule session executor job retry")
		return
	}
	if !ok {
		r.loggerPtr().Warn().Str("job_id", job.ID.String()).Msg("lost ownership before scheduling session executor job retry")
	}
}

func (r *SessionExecutorRuntime) markJobSucceeded(ctx context.Context, executor models.SessionExecutor, job *models.Job) bool {
	ok, err := r.Jobs.MarkSucceededWithLease(ctx, job.ID, executor.LockToken)
	if err != nil {
		r.loggerPtr().Warn().Err(err).Str("job_id", job.ID.String()).Msg("failed to mark session executor job succeeded")
		return false
	}
	return ok
}

func (r *SessionExecutorRuntime) markJobFailed(ctx context.Context, executor models.SessionExecutor, job *models.Job, errMsg string) bool {
	ok, err := r.Jobs.MarkFailedWithLease(ctx, job.ID, executor.LockToken, errMsg)
	if err != nil {
		r.loggerPtr().Warn().Err(err).Str("job_id", job.ID.String()).Msg("failed to mark session executor job failed")
		return false
	}
	return ok
}

func (r *SessionExecutorRuntime) markJobDeadLetter(ctx context.Context, handlerCtx context.Context, executor models.SessionExecutor, job *models.Job, err error) {
	ok, writeErr := r.Jobs.DeadLetterWithLease(ctx, job.ID, executor.LockToken, err.Error())
	if writeErr != nil {
		r.loggerPtr().Warn().Err(writeErr).Str("job_id", job.ID.String()).Msg("failed to dead-letter session executor job")
		return
	}
	if ok {
		hookCtx, cancel := context.WithTimeout(context.WithoutCancel(handlerCtx), 30*time.Second)
		defer cancel()
		jobctx.RunDeadLetterHooks(hookCtx, err)
	}
}

func (r *SessionExecutorRuntime) markExecutorTerminal(ctx context.Context, executor models.SessionExecutor, status models.SessionExecutorStatus, exitCode int, lastError string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	ok, err := r.Executors.MarkTerminalWithLease(writeCtx, executor.OrgID, executor.ID, executor.LockToken, status, &exitCode, lastError)
	if err != nil {
		r.loggerPtr().Warn().Err(err).Str("executor_id", executor.ID.String()).Msg("failed to mark session executor terminal")
		return
	}
	if !ok {
		r.loggerPtr().Warn().Str("executor_id", executor.ID.String()).Msg("lost ownership before marking session executor terminal")
	}
}

func (r *SessionExecutorRuntime) logger() zerolog.Logger {
	if r.Logger.GetLevel() == zerolog.Disabled {
		return zerolog.Nop()
	}
	return r.Logger
}

func (r *SessionExecutorRuntime) loggerPtr() *zerolog.Logger {
	logger := r.logger()
	return &logger
}
