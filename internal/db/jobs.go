package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"
)

// RunAgentDedupeKey returns the dedupe key used for run_agent enqueues. The
// partial unique index on (queue, dedupe_key) WHERE status IN
// ('pending','running') collapses concurrent run_agent enqueues for the same
// session into one — preventing the COALESCE race at AcquireTurnHold that
// surfaced as "sandbox race: another holder attached first" in production.
// Terminal-status rows (succeeded/failed/dead_letter) don't conflict, so a
// legitimate retry after the prior job finishes still goes through.
func RunAgentDedupeKey(sessionID uuid.UUID) string {
	return "run_agent:" + sessionID.String()
}

func RunAgentPayload(run *models.Session) map[string]string {
	payload := map[string]string{
		"session_id": run.ID.String(),
		"org_id":     run.OrgID.String(),
	}
	if run.PrimaryThreadID != nil && *run.PrimaryThreadID != uuid.Nil {
		payload["thread_id"] = run.PrimaryThreadID.String()
	}
	return payload
}

// ContinueSessionDedupeKey returns the dedupe key used for continue_session
// enqueues. Thread-level because concurrent tabs are allowed to queue follow-up
// turns independently; worker-side locking still serializes actual shared-
// sandbox execution when needed. See RunAgentDedupeKey for the partial-index
// rationale.
func ContinueSessionDedupeKey(threadID uuid.UUID) string {
	return "continue_session:" + threadID.String()
}

type JobStore struct {
	db       DBTX
	notifier *cache.JobNotifier
	logger   zerolog.Logger
}

// JobQueueHealthSample is an ops-oriented queue snapshot grouped by queue and
// job type. It intentionally spans orgs so dashboards can show platform-wide
// pressure rather than one tenant's view.
type JobQueueHealthSample struct {
	Queue                    string
	JobType                  string
	PendingRunnable          int64
	PendingDeferred          int64
	Running                  int64
	DeadLetter               int64
	OldestRunnableAgeSeconds float64
}

func NewJobStore(db DBTX) *JobStore {
	return &JobStore{db: db, logger: zerolog.Nop()}
}

// SetNotifier injects the Redis-backed job notifier used for wake-up publishes.
// lint:allow-no-orgid reason="process-wide dependency injection for Redis job notifications"
func (s *JobStore) SetNotifier(notifier *cache.JobNotifier) {
	s.notifier = notifier
}

// SetLogger injects the structured logger used for best-effort notifier failures.
// lint:allow-no-orgid reason="process-wide dependency injection for store logging"
func (s *JobStore) SetLogger(logger zerolog.Logger) {
	s.logger = logger
}

// GetLatestFailedByType returns the most recent failed or dead_letter job for the given org and job type.
// Returns nil, nil if no failed job exists.
func (s *JobStore) GetLatestFailedByType(ctx context.Context, orgID uuid.UUID, jobType string) (*models.LatestJobError, error) {
	query := `
		SELECT id, last_error, updated_at
		FROM jobs
		WHERE org_id = @org_id AND job_type = @job_type AND status IN ('failed', 'dead_letter')
		ORDER BY updated_at DESC
		LIMIT 1`

	var result models.LatestJobError
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"job_type": jobType,
	}).Scan(&result.JobID, &result.LastError, &result.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// EnqueueOpts captures all parameters for a single Enqueue call. Callers fill
// in the fields they need; zero values pass through.
//
// New enqueue-time scheduling features (target node, deferred run-at, custom
// max attempts) are added here rather than as new positional method
// parameters so the signature stays stable as the queue grows new affinity /
// scheduling capabilities. The plain Enqueue/EnqueueInTx methods remain as
// thin wrappers around the opts form for the common "no special scheduling"
// case so the bulk of existing call sites stay untouched.
type EnqueueOpts struct {
	Queue     string
	JobType   string
	Payload   any
	Priority  int
	DedupeKey *string

	// TargetNodeID, when set, restricts the job to be claimed by this
	// specific worker node. Used for sandbox-bound jobs (continue_session,
	// open_pr, run_agent for resume) where the work must execute on the
	// same docker daemon as the session's recorded container_id. NULL
	// means any worker can claim. The claim path falls back to "any worker"
	// if the target node is marked dead in the `nodes` table, so a pinned
	// job cannot starve when its node is permanently lost.
	TargetNodeID *string
}

// EnqueueWithOpts is the canonical enqueue path. Enqueue/EnqueueInTx wrap it
// for the common case.
func (s *JobStore) EnqueueWithOpts(ctx context.Context, orgID uuid.UUID, opts EnqueueOpts) (uuid.UUID, error) {
	id, err := enqueueOn(ctx, s.db, orgID, opts)
	if err != nil {
		return id, err
	}
	s.notify(ctx, id)
	return id, nil
}

// EnqueueInTxWithOpts is the in-transaction variant of EnqueueWithOpts. The
// caller is responsible for calling Notify after commit so the wake-up isn't
// fired before the row is durable.
func (s *JobStore) EnqueueInTxWithOpts(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, opts EnqueueOpts) (uuid.UUID, error) {
	return enqueueOn(ctx, tx, orgID, opts)
}

func (s *JobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	return s.EnqueueWithOpts(ctx, orgID, EnqueueOpts{
		Queue:     queue,
		JobType:   jobType,
		Payload:   payload,
		Priority:  priority,
		DedupeKey: dedupeKey,
	})
}

// EnqueueWithTarget is the positional-args wrapper for callers (typically
// service-package interfaces with a fixed JobStore method shape) that need
// to set TargetNodeID without depending on the EnqueueOpts type. Behaves
// identically to EnqueueWithOpts; pass nil targetNodeID for an unpinned job.
func (s *JobStore) EnqueueWithTarget(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string, targetNodeID *string) (uuid.UUID, error) {
	return s.EnqueueWithOpts(ctx, orgID, EnqueueOpts{
		Queue:        queue,
		JobType:      jobType,
		Payload:      payload,
		Priority:     priority,
		DedupeKey:    dedupeKey,
		TargetNodeID: targetNodeID,
	})
}

// EnqueueInTx inserts a job inside an existing transaction so callers that
// must create a row and a job atomically (e.g. automation RunNow) don't leave
// orphaned state when one side fails.
func (s *JobStore) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	return s.EnqueueInTxWithOpts(ctx, tx, orgID, EnqueueOpts{
		Queue:     queue,
		JobType:   jobType,
		Payload:   payload,
		Priority:  priority,
		DedupeKey: dedupeKey,
	})
}

// Notify publishes a best-effort wake-up for an already-created job row.
// lint:allow-no-orgid reason="process-wide post-commit Redis wake-up for already-scoped job rows"
func (s *JobStore) Notify(ctx context.Context, id uuid.UUID) {
	s.notify(ctx, id)
}

func (s *JobStore) notify(ctx context.Context, id uuid.UUID) {
	if s.notifier == nil || id == uuid.Nil {
		return
	}
	if err := s.notifier.Publish(ctx); err != nil {
		s.logger.Warn().Err(err).Str("job_id", id.String()).Msg("failed to publish Redis job wake-up")
	}
}

// OldestPendingSessionJobAge returns how long the oldest runnable pending
// session job has been waiting in the global queue.
// lint:allow-no-orgid reason="queue pressure read spans jobs across all orgs by design"
func (s *JobStore) OldestPendingSessionJobAge(ctx context.Context) (time.Duration, bool, error) {
	var runnableAt time.Time
	err := s.db.QueryRow(ctx, `
		SELECT run_at
		FROM jobs
		WHERE status = 'pending'
		  AND org_id IS NOT NULL
		  AND run_at <= now()
		  AND job_type IN ('run_agent', 'continue_session')
		ORDER BY run_at ASC
		LIMIT 1`,
	).Scan(&runnableAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("oldest pending session job age: %w", err)
	}
	return time.Since(runnableAt), true, nil
}

// QueueHealthSamples returns platform-wide queue health grouped by queue and
// job type for the worker ops sampler.
// lint:allow-no-orgid reason="platform health sampler intentionally aggregates queue pressure across orgs"
func (s *JobStore) QueueHealthSamples(ctx context.Context) ([]JobQueueHealthSample, error) {
	rows, err := s.db.Query(ctx, `
		SELECT
			queue,
			job_type,
			COUNT(*) FILTER (WHERE status = 'pending' AND run_at <= now()) AS pending_runnable,
			COUNT(*) FILTER (WHERE status = 'pending' AND run_at > now()) AS pending_deferred,
			COUNT(*) FILTER (WHERE status = 'running') AS running,
			COUNT(*) FILTER (WHERE status = 'dead_letter') AS dead_letter,
			EXTRACT(EPOCH FROM now() - MIN(run_at) FILTER (WHERE status = 'pending' AND run_at <= now()))::double precision AS oldest_runnable_age_seconds
		FROM jobs
		WHERE status IN ('pending', 'running', 'dead_letter')
		GROUP BY queue, job_type
		ORDER BY pending_runnable DESC, running DESC, queue ASC, job_type ASC`)
	if err != nil {
		return nil, fmt.Errorf("queue health samples: %w", err)
	}
	defer rows.Close()

	var samples []JobQueueHealthSample
	for rows.Next() {
		var sample JobQueueHealthSample
		var oldest any
		if err := rows.Scan(
			&sample.Queue,
			&sample.JobType,
			&sample.PendingRunnable,
			&sample.PendingDeferred,
			&sample.Running,
			&sample.DeadLetter,
			&oldest,
		); err != nil {
			return nil, fmt.Errorf("scan queue health sample: %w", err)
		}
		switch v := oldest.(type) {
		case nil:
		case float64:
			sample.OldestRunnableAgeSeconds = v
		case float32:
			sample.OldestRunnableAgeSeconds = float64(v)
		case int64:
			sample.OldestRunnableAgeSeconds = float64(v)
		case int:
			sample.OldestRunnableAgeSeconds = float64(v)
		case pgtype.Float8:
			if v.Valid {
				sample.OldestRunnableAgeSeconds = v.Float64
			}
		case pgtype.Numeric:
			oldestFloat, err := v.Float64Value()
			if err != nil {
				return nil, fmt.Errorf("scan queue health sample oldest runnable age: %w", err)
			}
			if oldestFloat.Valid {
				sample.OldestRunnableAgeSeconds = oldestFloat.Float64
			}
		default:
			return nil, fmt.Errorf("scan queue health sample: unsupported oldest runnable age type %T", oldest)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue health samples rows: %w", err)
	}
	return samples, nil
}

type jobQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func enqueueOn(ctx context.Context, q jobQuerier, orgID uuid.UUID, opts EnqueueOpts) (uuid.UUID, error) {
	payloadJSON, err := json.Marshal(opts.Payload)
	if err != nil {
		return uuid.Nil, err
	}

	var id uuid.UUID
	query := `
		INSERT INTO jobs (org_id, queue, job_type, payload, priority, dedupe_key, target_node_id)
		VALUES (@org_id, @queue, @job_type, @payload, @priority, @dedupe_key, @target_node_id)
		ON CONFLICT DO NOTHING
		RETURNING id`

	err = q.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":         orgID,
		"queue":          opts.Queue,
		"job_type":       opts.JobType,
		"payload":        payloadJSON,
		"priority":       opts.Priority,
		"dedupe_key":     opts.DedupeKey,
		"target_node_id": opts.TargetNodeID,
	}).Scan(&id)
	// ON CONFLICT DO NOTHING returns no row when a pending/running job with the
	// same (queue, dedupe_key) already exists. Treat that as a successful no-op:
	// the existing job will satisfy the caller's intent.
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	return id, err
}

// DeleteExpiredCompleted removes completed/failed jobs older than the given number of days.
// lint:allow-no-orgid reason="system-wide retention cleanup across all orgs"
func (s *JobStore) DeleteExpiredCompleted(ctx context.Context, retentionDays int) (int64, error) {
	var deleted int64
	err := s.db.QueryRow(ctx,
		"SELECT delete_expired_completed_jobs($1)", retentionDays,
	).Scan(&deleted)
	return deleted, err
}

const claimedJobColumns = `j.id, j.org_id, j.queue, j.job_type, j.payload, j.priority, j.status,
	j.attempts, j.max_attempts, j.run_at, j.locked_by_node_id, j.locked_at,
	j.lease_expires_at, j.lock_token, j.run_owner_id, j.last_error,
	j.dedupe_key, j.target_node_id, j.created_at, j.updated_at, j.completed_at`

// nodeDeadHeartbeatThreshold is how long a node can go without heartbeating
// before its pinned jobs become claimable by any worker. Set generously
// above the node manager's heartbeat interval (currently 10s) so a transient
// network blip doesn't unpin jobs from a healthy worker.
const nodeDeadHeartbeatThreshold = 90 * time.Second

type jobExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// ClaimNextRunnable atomically claims the next due pending job, marking it as
// running with a renewable lease and fencing token. Returns nil, nil when no
// runnable job exists.
//
// Node-affinity filter: jobs with target_node_id NULL can be claimed by any
// worker (the common case). Jobs with target_node_id set can only be claimed
// by the matching node OR if the target node is dead (status='dead' or stale
// heartbeat). The dead-node fallback prevents starvation when a pinned worker
// is permanently lost — the job becomes claimable by anyone instead of
// sitting forever. The freshness threshold matches ReclaimLostRunningJobs's
// dead-node detection so the two paths agree on what "dead" means.
// lint:allow-no-orgid reason="worker queue consumer scans cross-org jobs by design"
func (s *JobStore) ClaimNextRunnable(ctx context.Context, nodeID, ownerID string, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, error) {
	query := fmt.Sprintf(`
		WITH dead_target_nodes AS (
			SELECT id
			FROM nodes
			WHERE status = 'dead' OR last_heartbeat_at < @dead_before
		),
		next_job AS (
			SELECT j.id
			FROM jobs j
			LEFT JOIN dead_target_nodes d ON d.id = j.target_node_id
			WHERE j.status = 'pending' AND j.run_at <= now()
			  AND (
			    j.target_node_id IS NULL
			    OR j.target_node_id = @node_id
			    OR d.id IS NOT NULL
			  )
			ORDER BY j.priority DESC, j.created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE jobs j
		SET status = 'running',
			locked_by_node_id = @node_id,
			run_owner_id = @owner_id,
			lock_token = @lock_token,
			locked_at = now(),
			lease_expires_at = now() + (@lease_seconds * interval '1 second'),
			attempts = attempts + 1,
			updated_at = now()
		FROM next_job
		WHERE j.id = next_job.id
		RETURNING %s`, claimedJobColumns)

	var job models.Job
	var lockedByNodeID pgtype.Text
	var lockedAt pgtype.Timestamptz
	var leaseExpiresAt pgtype.Timestamptz
	var persistedLockToken pgtype.UUID
	var runOwnerID pgtype.Text
	var lastError pgtype.Text
	var dedupeKey pgtype.Text
	var targetNodeID pgtype.Text
	var completedAt pgtype.Timestamptz
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"node_id":       nodeID,
		"owner_id":      ownerID,
		"lock_token":    lockToken,
		"lease_seconds": int(leaseDuration.Seconds()),
		"dead_before":   time.Now().Add(-nodeDeadHeartbeatThreshold),
	}).Scan(
		&job.ID, &job.OrgID, &job.Queue, &job.JobType, &job.Payload, &job.Priority,
		&job.Status, &job.Attempts, &job.MaxAttempts, &job.RunAt, &lockedByNodeID,
		&lockedAt, &leaseExpiresAt, &persistedLockToken, &runOwnerID,
		&lastError, &dedupeKey, &targetNodeID, &job.CreatedAt, &job.UpdatedAt, &completedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim next runnable job: %w", err)
	}
	if lockedByNodeID.Valid {
		job.LockedByNodeID = &lockedByNodeID.String
	}
	if lockedAt.Valid {
		job.LockedAt = &lockedAt.Time
	}
	if leaseExpiresAt.Valid {
		job.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	if persistedLockToken.Valid {
		token := uuid.UUID(persistedLockToken.Bytes)
		job.LockToken = &token
	}
	if runOwnerID.Valid {
		job.RunOwnerID = &runOwnerID.String
	}
	if lastError.Valid {
		job.LastError = &lastError.String
	}
	if dedupeKey.Valid {
		job.DedupeKey = &dedupeKey.String
	}
	if targetNodeID.Valid {
		job.TargetNodeID = &targetNodeID.String
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	return &job, nil
}

// RenewLease extends the lease for a running job owned by the provided fencing
// token. ok=false means ownership was already lost.
// lint:allow-no-orgid reason="worker queue consumer renews cross-org job leases by design"
func (s *JobStore) RenewLease(ctx context.Context, jobID, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error) {
	query := `
		UPDATE jobs
		SET lease_expires_at = now() + (@lease_seconds * interval '1 second'),
			updated_at = now()
		WHERE id = @job_id
		  AND status = 'running'
		  AND lock_token = @lock_token
		RETURNING lease_expires_at`

	var leaseExpiresAt time.Time
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"lease_seconds": int(leaseDuration.Seconds()),
		"job_id":        jobID,
		"lock_token":    lockToken,
	}).Scan(&leaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("renew job lease: %w", err)
	}
	return &models.Job{ID: jobID, LockToken: &lockToken, LeaseExpiresAt: &leaseExpiresAt}, true, nil
}

// MarkSucceededWithLease transitions a running job to succeeded only if the
// caller still owns the current fencing token.
// lint:allow-no-orgid reason="worker queue consumer completes cross-org jobs by design"
func (s *JobStore) MarkSucceededWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
	tag, err := s.execLeaseTerminalUpdate(ctx, `
		UPDATE jobs
		SET status = 'succeeded',
			completed_at = now(),
			locked_by_node_id = NULL,
			run_owner_id = NULL,
			lock_token = NULL,
			locked_at = NULL,
			lease_expires_at = NULL,
			updated_at = now()
		WHERE id = $1
		  AND status = 'running'
		  AND lock_token = $2`, jobID, lockToken)
	if err != nil {
		return false, fmt.Errorf("mark job succeeded with lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// MarkFailedWithLease transitions a running job to failed only if the caller
// still owns the current fencing token.
// lint:allow-no-orgid reason="worker queue consumer completes cross-org jobs by design"
func (s *JobStore) MarkFailedWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error) {
	tag, err := s.execLeaseTerminalUpdate(ctx, `
		UPDATE jobs
		SET status = 'failed',
			last_error = $1,
			locked_by_node_id = NULL,
			run_owner_id = NULL,
			lock_token = NULL,
			locked_at = NULL,
			lease_expires_at = NULL,
			updated_at = now()
		WHERE id = $2
		  AND status = 'running'
		  AND lock_token = $3`, errMsg, jobID, lockToken)
	if err != nil {
		return false, fmt.Errorf("mark job failed with lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RetryWithLease requeues a running job for a future retry only if the caller
// still owns the current fencing token.
// lint:allow-no-orgid reason="worker queue consumer requeues cross-org jobs by design"
func (s *JobStore) RetryWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error) {
	tag, err := s.execLeaseTerminalUpdate(ctx, `
		UPDATE jobs
		SET status = 'pending',
			last_error = $1,
			run_at = $2,
			locked_by_node_id = NULL,
			run_owner_id = NULL,
			lock_token = NULL,
			locked_at = NULL,
			lease_expires_at = NULL,
			updated_at = now()
		WHERE id = $3
		  AND status = 'running'
		  AND lock_token = $4`, errMsg, runAt, jobID, lockToken)
	if err != nil {
		return false, fmt.Errorf("retry job with lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RetryWithoutConsumingAttemptWithLease requeues a running job while undoing the
// claim-time attempt increment. This preserves the existing semantics for
// retryable capacity/dependency conditions.
// lint:allow-no-orgid reason="worker queue consumer requeues cross-org jobs by design"
func (s *JobStore) RetryWithoutConsumingAttemptWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error) {
	tag, err := s.execLeaseTerminalUpdate(ctx, `
		UPDATE jobs
		SET status = 'pending',
			last_error = $1,
			run_at = $2,
			attempts = GREATEST(attempts - 1, 0),
			locked_by_node_id = NULL,
			run_owner_id = NULL,
			lock_token = NULL,
			locked_at = NULL,
			lease_expires_at = NULL,
			updated_at = now()
		WHERE id = $3
		  AND status = 'running'
		  AND lock_token = $4`, errMsg, runAt, jobID, lockToken)
	if err != nil {
		return false, fmt.Errorf("retry job without consuming attempt with lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DeadLetterWithLease transitions a running job to dead_letter only if the
// caller still owns the current fencing token.
// lint:allow-no-orgid reason="worker queue consumer completes cross-org jobs by design"
func (s *JobStore) DeadLetterWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error) {
	tag, err := s.execLeaseTerminalUpdate(ctx, `
		UPDATE jobs
		SET status = 'dead_letter',
			last_error = $1,
			completed_at = now(),
			locked_by_node_id = NULL,
			run_owner_id = NULL,
			lock_token = NULL,
			locked_at = NULL,
			lease_expires_at = NULL,
			updated_at = now()
		WHERE id = $2
		  AND status = 'running'
		  AND lock_token = $3`, errMsg, jobID, lockToken)
	if err != nil {
		return false, fmt.Errorf("dead-letter job with lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReclaimLostRunningJobs requeues jobs whose lease expired or whose owner node
// is considered dead.
// lint:allow-no-orgid reason="recovery loop scans cross-org jobs by design"
func (s *JobStore) ReclaimLostRunningJobs(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
	query := `
		WITH dead_nodes AS (
			SELECT id
			FROM nodes
			WHERE status = 'dead'
			   OR last_heartbeat_at < $1
		),
		candidates AS (
			SELECT
				j.id,
				j.org_id,
				j.job_type,
				j.locked_at,
				COALESCE(sess.snapshot_key, '') AS snapshot_key,
				CASE
					WHEN j.job_type IN ('run_agent', 'continue_session') THEN
						ROW_NUMBER() OVER (
							PARTITION BY j.org_id
							ORDER BY
								CASE WHEN COALESCE(sess.snapshot_key, '') <> '' THEN 0 ELSE 1 END,
								j.locked_at ASC
						)
					ELSE 1
				END AS org_recovery_rank
			FROM jobs j
			LEFT JOIN dead_nodes d ON d.id = j.locked_by_node_id
			LEFT JOIN sessions sess
				ON sess.org_id = j.org_id
			   AND NULLIF(j.payload->>'session_id', '') IS NOT NULL
			   AND sess.id = NULLIF(j.payload->>'session_id', '')::uuid
			WHERE j.status = 'running'
			  AND (
				j.lease_expires_at < now()
				OR d.id IS NOT NULL
			  )
		),
		reclaimable AS (
			SELECT id, org_id
			FROM candidates
			WHERE job_type NOT IN ('run_agent', 'continue_session')
			   OR org_recovery_rank <= 3
			ORDER BY
				CASE WHEN job_type IN ('run_agent', 'continue_session') THEN 0 ELSE 1 END,
				CASE WHEN snapshot_key <> '' THEN 0 ELSE 1 END,
				locked_at ASC
			LIMIT $2
		),
		updated_jobs AS (
			UPDATE jobs j
			SET status = 'pending',
				last_error = 'job ownership lost; queued for bounded recovery',
				locked_by_node_id = NULL,
				run_owner_id = NULL,
				lock_token = NULL,
				locked_at = NULL,
				lease_expires_at = NULL,
				run_at = now(),
				updated_at = now()
			FROM reclaimable r
			WHERE j.id = r.id
			RETURNING j.org_id, NULLIF(j.payload->>'session_id', '') AS session_id
		),
		updated_sessions AS (
			UPDATE sessions s
			SET recovery_state = 'queued',
			    recovery_queued_at = now(),
			    recovery_started_at = NULL,
			    runtime_stop_reason = 'worker_recovery'
			FROM updated_jobs uj
			WHERE uj.session_id IS NOT NULL
			  AND s.org_id = uj.org_id
			  AND s.id = uj.session_id::uuid
		)
		SELECT COUNT(*) FROM updated_jobs`

	var reclaimed int64
	err := s.db.QueryRow(ctx, query, staleBefore, limit).Scan(&reclaimed)
	if err != nil {
		return 0, fmt.Errorf("reclaim lost running jobs: %w", err)
	}
	return reclaimed, nil
}

// CountRunningOwnedByNode returns the number of running jobs currently owned by
// the given node.
// lint:allow-no-orgid reason="worker drain status is node-scoped across all orgs"
func (s *JobStore) CountRunningOwnedByNode(ctx context.Context, nodeID string) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM jobs
		WHERE status = 'running' AND locked_by_node_id = $1
	`, nodeID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count running jobs by node: %w", err)
	}
	return count, nil
}

func (s *JobStore) execLeaseTerminalUpdate(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	execer, ok := s.db.(jobExecer)
	if !ok {
		return pgconn.CommandTag{}, fmt.Errorf("job store db does not support Exec")
	}
	return execer.Exec(ctx, query, args...)
}
