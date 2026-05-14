package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
)

type JobHandler func(ctx context.Context, jobType string, payload json.RawMessage) error

// RetryableError wraps an error to indicate that the job should be retried
// without consuming an attempt. This is useful for transient conditions like
// concurrency limits where the job will succeed once a slot opens.
//
// RetryAfter, when non-nil, replaces the exponential backoff schedule for
// this retry only. Use it for transient gates where the wait time is known —
// e.g. waiting on the Linear pre-start preparation worker — so we don't
// thrash the queue with `1<<attempts`-second backoffs. A pointer (rather
// than a bare time.Duration) is used so callers can request an explicit
// zero-delay retry without colliding with the "unset, use backoff" sentinel.
type RetryableError struct {
	Err        error
	RetryAfter *time.Duration
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// FatalError wraps an error to indicate that the job should be dead-lettered
// immediately without retrying. Use this for persistent failures where retrying
// would produce the same result (e.g. Docker daemon unreachable, missing config).
type FatalError struct {
	Err error
}

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }

type jobContextKey string

const (
	jobOrgIDContextKey   jobContextKey = "job_org_id"
	defaultLeaseDuration               = 60 * time.Second
	defaultRenewInterval               = 20 * time.Second
)

func withJobOrgID(ctx context.Context, orgID uuid.UUID) context.Context {
	return context.WithValue(ctx, jobOrgIDContextKey, orgID)
}

func jobOrgIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	orgID, ok := ctx.Value(jobOrgIDContextKey).(uuid.UUID)
	return orgID, ok
}

type jobLeaseStore interface {
	ClaimNextRunnable(ctx context.Context, nodeID, ownerID string, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, error)
	RenewLease(ctx context.Context, jobID, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error)
	MarkSucceededWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error)
	MarkFailedWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error)
	RetryWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error)
	RetryWithoutConsumingAttemptWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error)
	DeadLetterWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error)
}

// maxRetryableDuration is the maximum wall-clock time a retryable job is
// allowed to keep retrying before being dead-lettered. This prevents jobs
// from retrying indefinitely (e.g. when stuck behind a concurrency limit).
const maxRetryableDuration = 8 * time.Minute

var defaultMaxLongRunningJobDuration = time.Duration(models.MaxAbsoluteRuntimeCeilingSeconds)*time.Second + 2*time.Minute

type Worker struct {
	jobs          jobLeaseStore
	logger        zerolog.Logger
	nodeID        string
	handlers      map[string]JobHandler
	pollInterval  time.Duration
	leaseDuration time.Duration
	renewInterval time.Duration
	// maxLongRunningJobDuration is a worker-level lease-renewal watchdog for
	// session jobs. Handler-owned contexts should normally stop first; this
	// outer guard prevents a wedged handler from renewing a job forever.
	maxLongRunningJobDuration time.Duration
	wakeCh                    chan struct{}

	draining           atomic.Bool
	activeJobs         atomic.Int32
	activeRunAgentJobs atomic.Int32
}

func New(pool db.DBTX, logger zerolog.Logger, nodeID string) *Worker {
	return &Worker{
		jobs:                      db.NewJobStore(pool),
		logger:                    logger,
		nodeID:                    nodeID,
		handlers:                  make(map[string]JobHandler),
		pollInterval:              5 * time.Second,
		leaseDuration:             defaultLeaseDuration,
		renewInterval:             defaultRenewInterval,
		maxLongRunningJobDuration: defaultMaxLongRunningJobDuration,
		wakeCh:                    make(chan struct{}, 1),
	}
}

func (w *Worker) Wake() {
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *Worker) Register(jobType string, handler JobHandler) {
	w.handlers[jobType] = handler
}

func (w *Worker) Start(ctx context.Context) {
	w.logger.Info().Str("node_id", w.nodeID).Msg("worker started")
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info().Msg("worker stopping")
			return
		case <-w.wakeCh:
			w.poll(ctx)
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

func (w *Worker) poll(ctx context.Context) {
	if w.draining.Load() {
		return
	}

	lockToken := uuid.New()
	job, err := w.jobs.ClaimNextRunnable(ctx, w.nodeID, w.nodeID, lockToken, w.leaseDuration)
	if err != nil {
		w.logger.Error().Err(err).Msg("failed to claim job")
		return
	}
	if job == nil {
		return
	}
	if job.LockToken == nil {
		w.logger.Error().Str("job_id", job.ID.String()).Msg("claimed job missing fencing token")
		return
	}

	w.activeJobs.Add(1)
	if isLongRunningSessionJob(job.JobType) {
		w.activeRunAgentJobs.Add(1)
	}
	defer func() {
		w.activeJobs.Add(-1)
		if isLongRunningSessionJob(job.JobType) {
			w.activeRunAgentJobs.Add(-1)
		}
	}()

	handler, ok := w.handlers[job.JobType]
	if !ok {
		w.logger.Warn().Str("job_type", job.JobType).Msg("no handler registered")
		w.failJob(ctx, job.ID, *job.LockToken, fmt.Sprintf("no handler for job type: %s", job.JobType))
		return
	}

	handlerCtx := withJobOrgID(ctx, job.OrgID)
	handlerCtx = jobctx.WithDeadLetterHooks(handlerCtx)
	handlerCtx = jobctx.WithLockToken(handlerCtx, *job.LockToken)
	if job.TargetNodeID != nil && *job.TargetNodeID != "" && *job.TargetNodeID != w.nodeID {
		handlerCtx = jobctx.WithDeadTargetNode(handlerCtx, *job.TargetNodeID)
	}
	handlerCtx, cancelHandler := context.WithCancel(handlerCtx)
	defer cancelHandler()
	if isLongRunningSessionJob(job.JobType) && w.maxLongRunningJobDuration > 0 {
		var cancelWatchdog context.CancelFunc
		handlerCtx, cancelWatchdog = context.WithTimeout(handlerCtx, w.maxLongRunningJobDuration)
		defer cancelWatchdog()
	}

	var lostOwnership atomic.Bool
	renewDone := make(chan struct{})
	initialLeaseExpiry := time.Now().Add(w.leaseDuration)
	if job.LeaseExpiresAt != nil {
		initialLeaseExpiry = *job.LeaseExpiresAt
	}
	go w.renewLeaseLoop(handlerCtx, job.ID, *job.LockToken, initialLeaseExpiry, &lostOwnership, cancelHandler, renewDone)

	w.logger.Info().Str("job_id", job.ID.String()).Str("job_type", job.JobType).Msg("processing job")
	err = handler(handlerCtx, job.JobType, job.Payload)
	cancelHandler()
	<-renewDone

	if lostOwnership.Load() {
		w.logger.Warn().Str("job_id", job.ID.String()).Msg("job lease lost during execution; skipping terminal write")
		return
	}

	if err == nil {
		w.succeedJob(ctx, job.ID, *job.LockToken)
		return
	}

	var fatal *FatalError
	if errors.As(err, &fatal) {
		w.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("job failed (fatal, skipping retries)")
		w.deadLetterJob(ctx, job.ID, *job.LockToken, err.Error())
		w.runDeadLetterHooks(handlerCtx, err)
		return
	}

	var retryable *RetryableError
	if errors.As(err, &retryable) {
		if time.Since(job.CreatedAt) > maxRetryableDuration {
			w.logger.Error().Err(err).
				Str("job_id", job.ID.String()).
				Dur("age", time.Since(job.CreatedAt)).
				Msg("retryable job exceeded max duration, dead-lettering")
			timeoutErr := fmt.Errorf("retryable job timed out after %s: %w", maxRetryableDuration, err)
			w.deadLetterJob(ctx, job.ID, *job.LockToken, timeoutErr.Error())
			w.runDeadLetterHooks(handlerCtx, timeoutErr)
			return
		}
		w.logger.Info().Err(err).Str("job_id", job.ID.String()).Msg("job deferred (retryable)")
		w.retryJobWithDelay(ctx, job.ID, *job.LockToken, err.Error(), job.Attempts, true, retryable.RetryAfter)
		return
	}

	w.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("job failed")
	if job.Attempts >= job.MaxAttempts {
		w.deadLetterJob(ctx, job.ID, *job.LockToken, err.Error())
		w.runDeadLetterHooks(handlerCtx, err)
		return
	}
	w.retryJob(ctx, job.ID, *job.LockToken, err.Error(), job.Attempts, false)
}

func (w *Worker) RequestDrain() {
	w.draining.Store(true)
}

func (w *Worker) IsDraining() bool {
	return w.draining.Load()
}

func (w *Worker) ActiveJobCount() int {
	return int(w.activeJobs.Load())
}

func (w *Worker) ActiveRunAgentCount() int {
	return int(w.activeRunAgentJobs.Load())
}

func (w *Worker) renewLeaseLoop(
	ctx context.Context,
	jobID uuid.UUID,
	lockToken uuid.UUID,
	leaseExpiry time.Time,
	lostOwnership *atomic.Bool,
	cancelHandler context.CancelFunc,
	done chan<- struct{},
) {
	defer close(done)

	ticker := time.NewTicker(w.renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, ok, err := w.jobs.RenewLease(ctx, jobID, lockToken, w.leaseDuration)
			if err != nil {
				w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to renew job lease")
				if time.Now().After(leaseExpiry) {
					lostOwnership.Store(true)
					cancelHandler()
					return
				}
				continue
			}
			if job != nil && job.LeaseExpiresAt != nil {
				leaseExpiry = *job.LeaseExpiresAt
			}
			if !ok {
				lostOwnership.Store(true)
				cancelHandler()
				return
			}
		}
	}
}

func (w *Worker) succeedJob(ctx context.Context, jobID, lockToken uuid.UUID) {
	ok, err := w.jobs.MarkSucceededWithLease(ctx, jobID, lockToken)
	if err != nil {
		w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to mark job as succeeded")
		return
	}
	if !ok {
		w.logger.Warn().Str("job_id", jobID.String()).Msg("lost ownership before marking job as succeeded")
	}
}

func (w *Worker) failJob(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) {
	ok, err := w.jobs.MarkFailedWithLease(ctx, jobID, lockToken, errMsg)
	if err != nil {
		w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to mark job as failed")
		return
	}
	if !ok {
		w.logger.Warn().Str("job_id", jobID.String()).Msg("lost ownership before marking job as failed")
	}
}

func (w *Worker) retryJob(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, attempt int, preserveAttempts bool) {
	w.retryJobWithDelay(ctx, jobID, lockToken, errMsg, attempt, preserveAttempts, nil)
}

func (w *Worker) retryJobWithDelay(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, attempt int, preserveAttempts bool, override *time.Duration) {
	var backoff time.Duration
	if override != nil {
		backoff = *override
	} else {
		backoff = retryBackoff(attempt)
	}
	runAt := time.Now().Add(backoff)

	var (
		ok  bool
		err error
	)
	if preserveAttempts {
		ok, err = w.jobs.RetryWithoutConsumingAttemptWithLease(ctx, jobID, lockToken, errMsg, runAt)
	} else {
		ok, err = w.jobs.RetryWithLease(ctx, jobID, lockToken, errMsg, runAt)
	}
	if err != nil {
		w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to schedule job retry")
		return
	}
	if !ok {
		w.logger.Warn().Str("job_id", jobID.String()).Msg("lost ownership before scheduling job retry")
	}
}

func (w *Worker) deadLetterJob(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) {
	ok, err := w.jobs.DeadLetterWithLease(ctx, jobID, lockToken, errMsg)
	if err != nil {
		w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to dead-letter job")
		return
	}
	if !ok {
		w.logger.Warn().Str("job_id", jobID.String()).Msg("lost ownership before dead-lettering job")
	}
}

func (w *Worker) runDeadLetterHooks(handlerCtx context.Context, err error) {
	hookCtx, cancel := context.WithTimeout(context.WithoutCancel(handlerCtx), 30*time.Second)
	defer cancel()
	jobctx.RunDeadLetterHooks(hookCtx, err)
}

func retryBackoff(attempt int) time.Duration {
	exp := attempt
	if exp > 10 {
		exp = 10
	}
	return time.Duration(1<<exp) * time.Second
}

func isLongRunningSessionJob(jobType string) bool {
	return jobType == "run_agent" || jobType == "continue_session"
}
