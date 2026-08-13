package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// OpenPRDedupeKey scopes PR creation to the changeset PR slot. Every entry
// point must use this key so UI, agent, automation, and Slack requests collapse
// onto one active publish job for the same changeset.
func OpenPRDedupeKey(changesetID uuid.UUID) string {
	return "open_pr:" + changesetID.String()
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

// RunAgentEnqueueOpts is the canonical scheduling policy for a session's
// initial agent turn. Keeping workload classification next to payload and
// dedupe construction prevents retry and secondary-dispatch paths from
// silently falling back to the interactive default.
func RunAgentEnqueueOpts(run *models.Session, priority int, dedupeKey *string) EnqueueOpts {
	opts := EnqueueOpts{
		Queue:     "agent",
		JobType:   "run_agent",
		Payload:   RunAgentPayload(run),
		Priority:  priority,
		DedupeKey: dedupeKey,
	}
	// Interactive is the schema default, which keeps rolling-deploy inserts and
	// their query shape stable. Code review must be explicit so every initial or
	// retried review turn enters the review-specific queue fence.
	if models.SandboxWorkloadClassForSession(run) == models.SandboxWorkloadClassCodeReview {
		opts.WorkloadClass = models.SandboxWorkloadClassCodeReview
	}
	return opts
}

// ContinueSessionDedupeKey returns the dedupe key used for continue_session
// enqueues. Thread-level: each thread/tab gets its own dedupe scope so a
// concurrent send to thread B is not silently dropped while thread A is
// running. Rapid-fire sends to the same thread still collapse (the partial
// unique index turns the duplicate INSERT into a no-op, and the orchestrator's
// post-turn drain picks the queued messages up). Worker-side AcquireTurnHold
// serializes actual shared-sandbox execution when both threads run.
//
// Callers without a thread context (legacy session-level handlers, PR health
// repair) pass the session ID instead — that key occupies a different dedupe
// scope from any thread key (different UUID), which is intentional. See
// RunAgentDedupeKey for the partial-index rationale.
func ContinueSessionDedupeKey(scopeID uuid.UUID) string {
	return "continue_session:" + scopeID.String()
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

// WorkerLoadSample is an ops-oriented snapshot of worker-owned load. It spans
// orgs by design so the primary operations dashboard can show fleet capacity.
type WorkerLoadSample struct {
	WorkerNodeID          string
	NodeStatus            string
	RunningSessions       int64
	TurnHeldSessions      int64
	SandboxContainers     int64
	ActivePreviews        int64
	PreviewHeldContainers int64
	RunningJobs           int64
	RunningSessionJobs    int64
	ActiveUsageContainers int64
	ActiveMemoryAllocated int64
	ActiveCPUAllocated    float64
	ActiveDiskAllocated   int64
}

// RunningJobSample is an ops-oriented snapshot of currently running jobs,
// grouped by worker node and job type.
type RunningJobSample struct {
	WorkerNodeID string
	JobType      string
	Running      int64
}

type SandboxCapacitySummary struct {
	FreshWorkers      int
	WorkersWithSlots  int
	LiveSandboxes     int
	ReservedSandboxes int
	MaxSandboxes      int
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
	// WorkloadClass controls sandbox routing and admission. Empty defaults to
	// interactive for backward compatibility with non-sandbox jobs and older
	// enqueue call sites.
	WorkloadClass models.SandboxWorkloadClass
	// RunAt defers the job until the requested time. Nil keeps the jobs table
	// default of now(), preserving immediate execution for existing callers.
	RunAt *time.Time
	// MaxAttempts overrides the jobs table default when positive. Zero keeps
	// the schema default so existing enqueue call sites retain their current
	// retry budget.
	MaxAttempts int

	// TargetNodeID, when set, restricts the job to be claimed by this
	// specific worker node. Used for sandbox-bound jobs (continue_session,
	// open_pr, run_agent for resume) where the work must execute on the
	// same docker daemon as the session's recorded container_id. NULL means
	// any worker can claim. See ClaimNextRunnable for the unavailable-node
	// fallback that keeps a pinned job from starving.
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

// HasActiveByDedupeKey reports whether a pending or running job currently
// owns a dedupe key. Terminal jobs intentionally do not count, matching the
// partial unique index used by EnqueueWithOpts.
func (s *JobStore) HasActiveByDedupeKey(ctx context.Context, orgID uuid.UUID, queue, dedupeKey string) (bool, error) {
	var active bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM jobs
			WHERE org_id = @org_id
			  AND queue = @queue
			  AND dedupe_key = @dedupe_key
			  AND status IN ('pending', 'running')
		)`, pgx.NamedArgs{
		"org_id":     orgID,
		"queue":      queue,
		"dedupe_key": dedupeKey,
	}).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("query active job by dedupe key: %w", err)
	}
	return active, nil
}

type ActiveJobRef struct {
	ID     uuid.UUID
	Status models.JobStatus
}

// GetActiveByDedupeKey returns the pending or running job that currently owns
// a queue dedupe key. The partial unique index guarantees at most one row.
func (s *JobStore) GetActiveByDedupeKey(ctx context.Context, orgID uuid.UUID, queue, dedupeKey string) (ActiveJobRef, error) {
	var active ActiveJobRef
	err := s.db.QueryRow(ctx, `
		SELECT id, status
		FROM jobs
		WHERE org_id = @org_id
		  AND queue = @queue
		  AND dedupe_key = @dedupe_key
		  AND status IN ('pending', 'running')`, pgx.NamedArgs{
		"org_id":     orgID,
		"queue":      queue,
		"dedupe_key": dedupeKey,
	}).Scan(&active.ID, &active.Status)
	if err != nil {
		return ActiveJobRef{}, err
	}
	return active, nil
}

// QueueChangesetPRCreation atomically reserves a changeset's PR slot when
// needed and ensures it has an active open_pr job. A queued or pushing slot
// may outlive the job that started a pre-publication review, so those states
// are eligible for a deduplicated requeue. A false queued result means an
// active job already satisfies the request or PR creation has completed.
func (s *JobStore) QueueChangesetPRCreation(
	ctx context.Context,
	orgID, sessionID, changesetID uuid.UUID,
	queue string,
	payload any,
	priority int,
) (jobID uuid.UUID, queued bool, err error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return uuid.Nil, false, fmt.Errorf("job store does not support transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("begin changeset PR enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result, err := tx.Exec(ctx, `UPDATE session_changesets
		SET pr_creation_state = 'queued', pr_creation_error = NULL, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND pr_creation_state NOT IN ('queued', 'pushing', 'succeeded')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("reserve changeset PR creation: %w", err)
	}
	if result.RowsAffected() == 0 {
		var state models.PRCreationState
		err = tx.QueryRow(ctx, `SELECT pr_creation_state
			FROM session_changesets
			WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
			FOR UPDATE`, pgx.NamedArgs{
			"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
		}).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, nil
		}
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("load reserved changeset PR creation: %w", err)
		}
		if state != models.PRCreationStateQueued && state != models.PRCreationStatePushing {
			return uuid.Nil, false, nil
		}
	}

	dedupeKey := OpenPRDedupeKey(changesetID)
	jobID, err = enqueueOn(ctx, tx, orgID, EnqueueOpts{
		Queue: queue, JobType: "open_pr", Payload: payload, Priority: priority, DedupeKey: &dedupeKey,
	})
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("enqueue changeset PR creation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, false, fmt.Errorf("commit changeset PR enqueue: %w", err)
	}
	queued = jobID != uuid.Nil
	if queued {
		s.notify(ctx, jobID)
	}
	return jobID, queued, nil
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

// EnqueueWithTargetAndWorkload is the sandbox-aware affinity enqueue path.
// It keeps workload classification durable across orchestrator-generated
// continuation drains without changing the legacy positional API.
func (s *JobStore) EnqueueWithTargetAndWorkload(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string, targetNodeID *string, workloadClass models.SandboxWorkloadClass) (uuid.UUID, error) {
	return s.EnqueueWithOpts(ctx, orgID, EnqueueOpts{
		Queue:         queue,
		JobType:       jobType,
		Payload:       payload,
		Priority:      priority,
		DedupeKey:     dedupeKey,
		TargetNodeID:  targetNodeID,
		WorkloadClass: workloadClass,
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

// Wake publishes a best-effort queue wake-up after an existing job was made
// runnable by a direct state transition rather than by Enqueue.
// lint:allow-no-orgid reason="process-wide Redis wake-up for already-scoped runnable job rows"
func (s *JobStore) Wake(ctx context.Context) {
	if s.notifier == nil {
		return
	}
	if err := s.notifier.Publish(ctx); err != nil {
		s.logger.Warn().Err(err).Msg("failed to publish Redis job wake-up")
	}
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

// WorkerLoadSamples returns platform-wide worker load grouped by worker node.
// lint:allow-no-orgid reason="platform health sampler intentionally aggregates worker capacity across orgs"
func (s *JobStore) WorkerLoadSamples(ctx context.Context) ([]WorkerLoadSample, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		WITH worker_nodes AS (
			SELECT id AS worker_node_id, status AS node_status
			FROM nodes
			WHERE mode IN ('worker', 'all') AND status IN ('active', 'draining')
		),
		session_counts AS (
			SELECT
				COALESCE(NULLIF(worker_node_id, ''), 'unassigned') AS worker_node_id,
				COUNT(*) FILTER (WHERE status = 'running') AS running_sessions,
				COUNT(*) FILTER (WHERE turn_holding_container = TRUE) AS turn_held_sessions,
				COUNT(*) FILTER (WHERE container_id IS NOT NULL) AS sandbox_containers
			FROM sessions
			WHERE deleted_at IS NULL
			  AND (
				status = 'running'
				OR turn_holding_container = TRUE
				OR container_id IS NOT NULL
			  )
			GROUP BY COALESCE(NULLIF(worker_node_id, ''), 'unassigned')
		),
		preview_counts AS (
			SELECT
				COALESCE(NULLIF(worker_node_id, ''), 'unassigned') AS worker_node_id,
				COUNT(*) FILTER (WHERE status IN %s) AS active_previews,
				COUNT(*) FILTER (WHERE preview_holding_container = TRUE) AS preview_held_containers
			FROM preview_instances
			WHERE status IN %s OR preview_holding_container = TRUE
			GROUP BY COALESCE(NULLIF(worker_node_id, ''), 'unassigned')
		),
		job_counts AS (
			SELECT
				COALESCE(NULLIF(locked_by_node_id, ''), 'unassigned') AS worker_node_id,
				COUNT(*) AS running_jobs,
				COUNT(*) FILTER (WHERE job_type IN ('run_agent', 'continue_session', 'start_preview')) AS running_session_jobs
			FROM jobs
			WHERE status = 'running'
			GROUP BY COALESCE(NULLIF(locked_by_node_id, ''), 'unassigned')
		),
		active_usage_counts AS (
			SELECT
				COALESCE(NULLIF(s.worker_node_id, ''), 'unassigned') AS worker_node_id,
				COUNT(*) AS active_usage_containers,
				COALESCE(SUM(e.memory_limit_mb), 0) AS active_memory_allocated_mb,
				COALESCE(SUM(e.cpu_limit), 0)::double precision AS active_cpu_allocated,
				COALESCE(SUM(e.disk_limit_mb), 0) AS active_disk_allocated_mb
			FROM container_usage_events e
			JOIN sessions s ON s.org_id = e.org_id AND s.id = e.session_id
			WHERE e.stopped_at IS NULL
			GROUP BY COALESCE(NULLIF(s.worker_node_id, ''), 'unassigned')
		),
		worker_ids AS (
			SELECT worker_node_id FROM worker_nodes
			UNION
			SELECT worker_node_id FROM session_counts
			UNION
			SELECT worker_node_id FROM preview_counts
			UNION
			SELECT worker_node_id FROM job_counts
			UNION
			SELECT worker_node_id FROM active_usage_counts
		)
		SELECT
			worker_ids.worker_node_id,
			COALESCE(worker_nodes.node_status, '') AS node_status,
			COALESCE(session_counts.running_sessions, 0) AS running_sessions,
			COALESCE(session_counts.turn_held_sessions, 0) AS turn_held_sessions,
			COALESCE(session_counts.sandbox_containers, 0) AS sandbox_containers,
			COALESCE(preview_counts.active_previews, 0) AS active_previews,
			COALESCE(preview_counts.preview_held_containers, 0) AS preview_held_containers,
			COALESCE(job_counts.running_jobs, 0) AS running_jobs,
			COALESCE(job_counts.running_session_jobs, 0) AS running_session_jobs,
			COALESCE(active_usage_counts.active_usage_containers, 0) AS active_usage_containers,
			COALESCE(active_usage_counts.active_memory_allocated_mb, 0) AS active_memory_allocated_mb,
			COALESCE(active_usage_counts.active_cpu_allocated, 0) AS active_cpu_allocated,
			COALESCE(active_usage_counts.active_disk_allocated_mb, 0) AS active_disk_allocated_mb
		FROM worker_ids
		LEFT JOIN worker_nodes USING (worker_node_id)
		LEFT JOIN session_counts USING (worker_node_id)
		LEFT JOIN preview_counts USING (worker_node_id)
		LEFT JOIN job_counts USING (worker_node_id)
		LEFT JOIN active_usage_counts USING (worker_node_id)
		ORDER BY running_sessions DESC, active_previews DESC, running_jobs DESC, worker_ids.worker_node_id ASC`, activeStatusFilter, activeStatusFilter))
	if err != nil {
		return nil, fmt.Errorf("worker load samples: %w", err)
	}
	defer rows.Close()

	var samples []WorkerLoadSample
	for rows.Next() {
		var sample WorkerLoadSample
		if err := rows.Scan(
			&sample.WorkerNodeID,
			&sample.NodeStatus,
			&sample.RunningSessions,
			&sample.TurnHeldSessions,
			&sample.SandboxContainers,
			&sample.ActivePreviews,
			&sample.PreviewHeldContainers,
			&sample.RunningJobs,
			&sample.RunningSessionJobs,
			&sample.ActiveUsageContainers,
			&sample.ActiveMemoryAllocated,
			&sample.ActiveCPUAllocated,
			&sample.ActiveDiskAllocated,
		); err != nil {
			return nil, fmt.Errorf("scan worker load sample: %w", err)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("worker load samples rows: %w", err)
	}
	return samples, nil
}

// RunningJobSamples returns currently running jobs grouped by worker and type.
// lint:allow-no-orgid reason="platform health sampler intentionally aggregates running jobs across orgs"
func (s *JobStore) RunningJobSamples(ctx context.Context) ([]RunningJobSample, error) {
	rows, err := s.db.Query(ctx, `
		SELECT
			COALESCE(NULLIF(locked_by_node_id, ''), 'unassigned') AS worker_node_id,
			job_type,
			COUNT(*) AS running
		FROM jobs
		WHERE status = 'running'
		GROUP BY COALESCE(NULLIF(locked_by_node_id, ''), 'unassigned'), job_type
		ORDER BY worker_node_id ASC, running DESC, job_type ASC`)
	if err != nil {
		return nil, fmt.Errorf("running job samples: %w", err)
	}
	defer rows.Close()

	var samples []RunningJobSample
	for rows.Next() {
		var sample RunningJobSample
		if err := rows.Scan(&sample.WorkerNodeID, &sample.JobType, &sample.Running); err != nil {
			return nil, fmt.Errorf("scan running job sample: %w", err)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("running job samples rows: %w", err)
	}
	return samples, nil
}

type jobQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func enqueueOn(ctx context.Context, q jobQuerier, orgID uuid.UUID, opts EnqueueOpts) (uuid.UUID, error) {
	if opts.MaxAttempts < 0 {
		return uuid.Nil, fmt.Errorf("max attempts must not be negative")
	}
	payloadJSON, err := json.Marshal(opts.Payload)
	if err != nil {
		return uuid.Nil, err
	}
	if opts.WorkloadClass != "" {
		if err := opts.WorkloadClass.Validate(); err != nil {
			return uuid.Nil, err
		}
	}

	var id uuid.UUID
	args := pgx.NamedArgs{
		"org_id":     orgID,
		"queue":      opts.Queue,
		"job_type":   opts.JobType,
		"payload":    payloadJSON,
		"priority":   opts.Priority,
		"dedupe_key": opts.DedupeKey,
	}
	columns := []string{"queue", "job_type", "payload", "priority", "dedupe_key"}
	values := []string{"@queue", "@job_type", "@payload", "@priority", "@dedupe_key"}
	if opts.WorkloadClass != "" {
		columns = append(columns, "workload_class")
		values = append(values, "@workload_class")
		args["workload_class"] = opts.WorkloadClass
	}
	if opts.TargetNodeID != nil {
		columns = append(columns, "target_node_id")
		values = append(values, "@target_node_id")
		args["target_node_id"] = opts.TargetNodeID
	}
	if opts.RunAt != nil {
		columns = append(columns, "run_at")
		values = append(values, "@run_at")
		args["run_at"] = opts.RunAt.UTC()
	}
	if opts.MaxAttempts > 0 {
		columns = append(columns, "max_attempts")
		values = append(values, "@max_attempts")
		args["max_attempts"] = opts.MaxAttempts
	}
	query := fmt.Sprintf(`
		INSERT INTO jobs (org_id, %s)
		VALUES (@org_id, %s)
		ON CONFLICT DO NOTHING
		RETURNING id`, strings.Join(columns, ", "), strings.Join(values, ", "))

	err = q.QueryRow(ctx, query, args).Scan(&id)
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
	j.lease_expires_at, j.lock_token, j.run_owner_id, j.owner_kind, j.last_error,
	j.dedupe_key, j.workload_class, j.target_node_id, j.sandbox_slot_reserved_until,
	j.retry_window_started_at, j.created_at, j.updated_at, j.completed_at`

// nodeDeadHeartbeatThreshold is how long a node can go without heartbeating
// before its pinned jobs become claimable by any worker. Set generously
// above the node manager's heartbeat interval (currently 10s) so a transient
// network blip doesn't unpin jobs from a healthy worker.
const nodeDeadHeartbeatThreshold = 90 * time.Second

const (
	sandboxSlotReservationTTL = 30 * time.Second
	// SandboxFleetRetryDelay and SandboxOrgLimitRetryDelay are exported so the
	// routing transaction and worker handler cannot silently drift apart.
	SandboxFleetRetryDelay         = 10 * time.Second
	SandboxOrgLimitRetryDelay      = 5 * time.Second
	sandboxRoutingErrorRetryDelay  = 10 * time.Second
	sandboxRoutingTerminalProbeAge = 8 * time.Minute
	maxClaimAdmissionSkips         = 16
)

type SandboxRoutingReason string

const (
	SandboxRoutingReasonReserved         SandboxRoutingReason = "reserved"
	SandboxRoutingReasonFleetCapacity    SandboxRoutingReason = "fleet_capacity"
	SandboxRoutingReasonOrgLimit         SandboxRoutingReason = "org_limit"
	SandboxRoutingReasonLockContention   SandboxRoutingReason = "lock_contention"
	SandboxRoutingReasonTerminalProbe    SandboxRoutingReason = "terminal_probe"
	SandboxRoutingReasonMetadataFallback SandboxRoutingReason = "capacity_metadata_unavailable"
	SandboxRoutingReasonJobError         SandboxRoutingReason = "job_error"
)

// SandboxRoutingResult reports the durable placement decision made before a
// fresh sandbox job can be claimed. A nil TargetNodeID with Deferred=false is
// lock contention, not real fleet exhaustion; callers should leave the job due
// so another dispatcher can retry immediately.
type SandboxRoutingResult struct {
	JobID         uuid.UUID
	WorkloadClass models.SandboxWorkloadClass
	TargetNodeID  *string
	Deferred      bool
	Reason        SandboxRoutingReason
	RoutingError  string
}

type sandboxRoutingJob struct {
	ID                   uuid.UUID
	OrgID                uuid.UUID
	JobType              string
	SessionID            *uuid.UUID
	WorkloadClass        models.SandboxWorkloadClass
	Status               models.JobStatus
	LockToken            *uuid.UUID
	RetryWindowStartedAt *time.Time
	CreatedAt            time.Time
}

// RouteNextSandboxJob assigns one due, fresh-sandbox job to a worker before
// normal queue claim. The job row, org admission decision, worker selection,
// and expiring reservation are committed atomically. Existing-sandbox affinity
// jobs are excluded because they must stay on their recorded owner.
// lint:allow-no-orgid reason="system worker dispatcher routes sandbox jobs across organizations"
func (s *JobStore) RouteNextSandboxJob(ctx context.Context) (*SandboxRoutingResult, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return nil, fmt.Errorf("job store does not support sandbox routing transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin sandbox job routing: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var job sandboxRoutingJob
	var rawSessionID pgtype.Text
	var retryWindowStartedAt pgtype.Timestamptz
	err = tx.QueryRow(ctx, `
		SELECT j.id, j.org_id, j.job_type, j.payload->>'session_id',
			j.workload_class AS effective_workload_class,
			j.status, j.retry_window_started_at, j.created_at
		FROM jobs j
		LEFT JOIN nodes target ON target.id = j.target_node_id
		WHERE j.status = 'pending'
		  AND j.run_at <= now()
		  AND j.job_type IN ('run_agent', 'continue_session')
		  AND (
			j.target_node_id IS NULL
			OR (
				j.sandbox_slot_reserved_until IS NOT NULL
				AND (
					j.sandbox_slot_reserved_until <= now()
					OR target.id IS NULL
					OR target.status IN ('dead', 'draining')
					OR target.last_heartbeat_at < @dead_before
				)
			)
		  )
		ORDER BY
			j.priority DESC,
			CASE WHEN j.workload_class = 'interactive' THEN 0 ELSE 1 END,
			-- run_at is the durable fairness cursor: a capacity deferral moves
			-- the job behind still-due peers so a bounded routing pass cannot
			-- restart at the same saturated prefix on every worker poll.
			j.run_at ASC,
			j.created_at ASC
		FOR UPDATE OF j SKIP LOCKED
		LIMIT 1`, pgx.NamedArgs{
		"dead_before": time.Now().Add(-nodeDeadHeartbeatThreshold),
	}).Scan(&job.ID, &job.OrgID, &job.JobType, &rawSessionID, &job.WorkloadClass, &job.Status, &retryWindowStartedAt, &job.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select sandbox job for routing: %w", err)
	}
	job.SessionID = parseSandboxRoutingSessionID(rawSessionID)
	if retryWindowStartedAt.Valid {
		startedAt := retryWindowStartedAt.Time
		job.RetryWindowStartedAt = &startedAt
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT sandbox_route_candidate`); err != nil {
		return nil, fmt.Errorf("create sandbox routing candidate savepoint: %w", err)
	}
	if err := resolveSandboxRoutingWorkloadClass(ctx, tx, &job); err != nil {
		return s.deferSandboxRoutingJobError(ctx, tx, job, err)
	}

	result, err := reserveSandboxSlotForLockedJob(ctx, tx, job, "")
	if err != nil {
		return s.deferSandboxRoutingJobError(ctx, tx, job, err)
	}
	durableTerminalProbe := false
	if result.TargetNodeID == nil && (result.Reason == SandboxRoutingReasonOrgLimit || result.Reason == SandboxRoutingReasonFleetCapacity) {
		durableTerminalProbe, err = continueSessionNeedsTerminalProbe(ctx, tx, job)
		if err != nil {
			return s.deferSandboxRoutingJobError(ctx, tx, job, err)
		}
	}
	if result.TargetNodeID == nil && result.Reason != SandboxRoutingReasonLockContention {
		fallbackReason := SandboxRoutingReason("")
		terminalProbeEligible := durableTerminalProbe || (job.JobType == "run_agent" &&
			time.Since(job.CreatedAt) >= sandboxRoutingTerminalProbeAge &&
			(result.Reason == SandboxRoutingReasonFleetCapacity || result.Reason == SandboxRoutingReasonOrgLimit)) ||
			(job.JobType == "continue_session" &&
				result.Reason == SandboxRoutingReasonFleetCapacity &&
				job.RetryWindowStartedAt != nil &&
				time.Since(*job.RetryWindowStartedAt) >= sandboxRoutingTerminalProbeAge)
		if terminalProbeEligible {
			fallbackReason = SandboxRoutingReasonTerminalProbe
		} else if result.Reason == SandboxRoutingReasonFleetCapacity {
			metadataConfigured, metadataErr := sandboxCapacityMetadataConfigured(ctx, tx)
			if metadataErr != nil {
				return s.deferSandboxRoutingJobError(ctx, tx, job, metadataErr)
			}
			if !metadataConfigured {
				fallbackReason = SandboxRoutingReasonMetadataFallback
			}
		}
		if fallbackReason != "" {
			targetNodeID, fallbackErr := selectSandboxRoutingFallbackNode(ctx, tx)
			if fallbackErr != nil && !errors.Is(fallbackErr, pgx.ErrNoRows) {
				return s.deferSandboxRoutingJobError(ctx, tx, job, fallbackErr)
			}
			if fallbackErr == nil {
				var placementErr error
				if fallbackReason == SandboxRoutingReasonTerminalProbe {
					if durableTerminalProbe {
						if err := markSandboxCancellationCleanup(ctx, tx, job); err != nil {
							return s.deferSandboxRoutingJobError(ctx, tx, job, err)
						}
					}
					placementErr = updateSandboxTerminalProbePlacement(ctx, tx, job, targetNodeID)
				} else {
					placementErr = updateSandboxRoutingPlacement(ctx, tx, job, targetNodeID, nil)
				}
				if placementErr != nil {
					return s.deferSandboxRoutingJobError(ctx, tx, job, placementErr)
				}
				result.TargetNodeID = &targetNodeID
				result.Reason = fallbackReason
			}
		}
	}
	if result.TargetNodeID == nil && result.Reason != SandboxRoutingReasonLockContention {
		retryDelay := SandboxFleetRetryDelay
		if result.Reason == SandboxRoutingReasonOrgLimit {
			retryDelay = SandboxOrgLimitRetryDelay
		}
		deferredUntil := time.Now().Add(retryDelay)
		clearRetryWindow := job.JobType == "continue_session" && result.Reason == SandboxRoutingReasonOrgLimit
		resultTag, err := tx.Exec(ctx, `
			UPDATE jobs
			SET workload_class = @workload_class,
				payload = CASE
					WHEN job_type = 'continue_session' THEN jsonb_set(payload, '{capacity_waited}', 'true'::jsonb, true)
					ELSE payload
				END,
				target_node_id = NULL,
				sandbox_slot_reserved_until = NULL,
				retry_window_started_at = CASE
					WHEN @clear_retry_window THEN NULL
					ELSE COALESCE(retry_window_started_at, now())
				END,
				run_at = @run_at,
				updated_at = now()
			WHERE id = @job_id
			  AND status = 'pending'`, pgx.NamedArgs{
			"clear_retry_window": clearRetryWindow,
			"job_id":             job.ID,
			"run_at":             deferredUntil,
			"workload_class":     job.WorkloadClass,
		})
		if err != nil {
			return s.deferSandboxRoutingJobError(ctx, tx, job, fmt.Errorf("defer sandbox job without capacity: %w", err))
		}
		if resultTag.RowsAffected() != 1 {
			return s.deferSandboxRoutingJobError(ctx, tx, job, fmt.Errorf("defer sandbox job without capacity: pending job %s changed ownership", job.ID))
		}
		result.Deferred = true
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit sandbox job routing: %w", err)
	}
	if result.TargetNodeID != nil {
		s.notify(ctx, job.ID)
	}
	return result, nil
}

// deferSandboxRoutingJobError rolls back only the failed routing work before
// moving the still-locked candidate behind other due work. Keeping the job row
// lock across rollback-to-savepoint and deferral prevents another dispatcher
// from selecting the same malformed candidate in between those operations.
func (s *JobStore) deferSandboxRoutingJobError(ctx context.Context, tx pgx.Tx, job sandboxRoutingJob, routingErr error) (*SandboxRoutingResult, error) {
	if _, rollbackErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sandbox_route_candidate`); rollbackErr != nil {
		return nil, fmt.Errorf("rollback failed sandbox routing work for job %s after %v: %w", job.ID, routingErr, rollbackErr)
	}
	result, err := tx.Exec(ctx, `
		UPDATE jobs
		SET target_node_id = NULL,
			sandbox_slot_reserved_until = NULL,
			last_error = @last_error,
			run_at = @run_at,
			updated_at = now()
		WHERE id = @job_id
		  AND org_id = @org_id
		  AND status = 'pending'`, pgx.NamedArgs{
		"job_id":     job.ID,
		"last_error": fmt.Sprintf("sandbox routing deferred: %v", routingErr),
		"org_id":     job.OrgID,
		"run_at":     time.Now().Add(sandboxRoutingErrorRetryDelay),
	})
	if err != nil {
		return nil, fmt.Errorf("defer failed sandbox routing for job %s after %v: %w", job.ID, routingErr, err)
	}
	if result.RowsAffected() != 1 {
		return nil, fmt.Errorf("defer failed sandbox routing for job %s after %v: pending job changed ownership", job.ID, routingErr)
	}
	if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT sandbox_route_candidate`); err != nil {
		return nil, fmt.Errorf("release failed sandbox routing savepoint for job %s after %v: %w", job.ID, routingErr, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit failed sandbox routing deferral for job %s after %v: %w", job.ID, routingErr, err)
	}
	return &SandboxRoutingResult{
		JobID:         job.ID,
		WorkloadClass: job.WorkloadClass,
		Deferred:      true,
		Reason:        SandboxRoutingReasonJobError,
		RoutingError:  routingErr.Error(),
	}, nil
}

// lint:allow-no-orgid reason="fleet routing readiness intentionally inspects worker heartbeat metadata across organizations"
func sandboxCapacityMetadataConfigured(ctx context.Context, tx pgx.Tx) (bool, error) {
	var configured bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM nodes
			WHERE mode IN ('worker', 'all')
			  AND status = 'active'
			  AND last_heartbeat_at >= @dead_before
			  AND COALESCE(NULLIF(metadata->>'max_active_sandboxes', '')::int, 0) > 0
		)`, pgx.NamedArgs{
		"dead_before": time.Now().Add(-nodeDeadHeartbeatThreshold),
	}).Scan(&configured)
	if err != nil {
		return false, fmt.Errorf("inspect configured fleet sandbox capacity metadata: %w", err)
	}
	return configured, nil
}

// ReserveSandboxSlotForRetry atomically retargets a currently running sandbox
// job after the worker-local gate rejects it. The worker's fenced retry write
// preserves the target and reservation selected here.
// lint:allow-no-orgid reason="worker retry routes a globally identified job across worker nodes"
func (s *JobStore) ReserveSandboxSlotForRetry(ctx context.Context, jobID, lockToken uuid.UUID, excludeNodeID string) (*SandboxRoutingResult, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return nil, fmt.Errorf("job store does not support sandbox routing transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin sandbox retry routing: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var job sandboxRoutingJob
	var rawSessionID pgtype.Text
	var retryWindowStartedAt pgtype.Timestamptz
	err = tx.QueryRow(ctx, `
		SELECT j.id, j.org_id, j.job_type, j.payload->>'session_id',
			j.workload_class AS effective_workload_class,
			j.status, j.retry_window_started_at, j.created_at
		FROM jobs j
		WHERE j.id = @job_id
		  AND j.status = 'running'
		  AND j.lock_token = @lock_token
		  AND j.job_type IN ('run_agent', 'continue_session')
		FOR UPDATE OF j`, pgx.NamedArgs{
		"job_id":     jobID,
		"lock_token": lockToken,
	}).Scan(&job.ID, &job.OrgID, &job.JobType, &rawSessionID, &job.WorkloadClass, &job.Status, &retryWindowStartedAt, &job.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("lock sandbox retry job: %w", err)
	}
	job.LockToken = &lockToken
	job.SessionID = parseSandboxRoutingSessionID(rawSessionID)
	if retryWindowStartedAt.Valid {
		startedAt := retryWindowStartedAt.Time
		job.RetryWindowStartedAt = &startedAt
	}
	if err := resolveSandboxRoutingWorkloadClass(ctx, tx, &job); err != nil {
		return nil, err
	}

	result, err := reserveSandboxSlotForLockedJob(ctx, tx, job, excludeNodeID)
	if err != nil {
		return nil, err
	}
	if result.TargetNodeID == nil {
		resultTag, err := tx.Exec(ctx, `
			UPDATE jobs
			SET workload_class = @workload_class,
				target_node_id = NULL,
				sandbox_slot_reserved_until = NULL,
				updated_at = now()
			WHERE id = @job_id
			  AND status = 'running'
			  AND lock_token = @lock_token`, pgx.NamedArgs{
			"job_id":         job.ID,
			"lock_token":     lockToken,
			"workload_class": job.WorkloadClass,
		})
		if err != nil {
			return nil, fmt.Errorf("clear sandbox retry reservation: %w", err)
		}
		if resultTag.RowsAffected() != 1 {
			return nil, fmt.Errorf("clear sandbox retry reservation: job %s lease changed", job.ID)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit sandbox retry routing: %w", err)
	}
	return result, nil
}

func reserveSandboxSlotForLockedJob(ctx context.Context, tx pgx.Tx, job sandboxRoutingJob, excludeNodeID string) (*SandboxRoutingResult, error) {
	result := &SandboxRoutingResult{
		JobID:         job.ID,
		WorkloadClass: job.WorkloadClass,
		Reason:        SandboxRoutingReasonFleetCapacity,
	}
	if err := job.WorkloadClass.Validate(); err != nil {
		return nil, fmt.Errorf("route sandbox job workload class: %w", err)
	}

	admitted, err := admitLockedSandboxTurn(ctx, tx, job)
	if err != nil {
		return nil, err
	}
	if !admitted {
		result.Reason = SandboxRoutingReasonOrgLimit
		return result, nil
	}

	excludedNodeIDs := []string{}
	if excludeNodeID != "" {
		excludedNodeIDs = append(excludedNodeIDs, excludeNodeID)
	}
	lockContended := false
	for {
		candidate, err := selectSandboxRoutingCandidate(ctx, tx, job.WorkloadClass, excludedNodeIDs)
		if errors.Is(err, pgx.ErrNoRows) {
			if lockContended {
				result.Reason = SandboxRoutingReasonLockContention
			}
			return result, nil
		}
		if err != nil {
			return nil, err
		}

		var locked bool
		// Blue/green generations have distinct routing node IDs but share one
		// Docker daemon. Lock their host-stable capacity identity so overlapping
		// generations cannot admit against independent views of the same host.
		if err := tx.QueryRow(ctx, `
			SELECT pg_try_advisory_xact_lock(hashtextextended(
				COALESCE(
					NULLIF(metadata->>'sandbox_capacity_node_id', ''),
					regexp_replace(id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
				),
				143
			))
			FROM nodes
			WHERE id = @node_id`, pgx.NamedArgs{
			"node_id": candidate,
		}).Scan(&locked); err != nil {
			return nil, fmt.Errorf("lock sandbox routing candidate %s: %w", candidate, err)
		}
		if !locked {
			lockContended = true
			excludedNodeIDs = append(excludedNodeIDs, candidate)
			continue
		}

		hasCapacity, err := sandboxRoutingCandidateHasCapacity(ctx, tx, candidate, job.ID, job.WorkloadClass)
		if err != nil {
			return nil, err
		}
		if !hasCapacity {
			excludedNodeIDs = append(excludedNodeIDs, candidate)
			continue
		}

		expiresAt := time.Now().Add(sandboxSlotReservationTTL)
		if err := updateSandboxRoutingPlacement(ctx, tx, job, candidate, &expiresAt); err != nil {
			return nil, err
		}
		result.TargetNodeID = &candidate
		result.Reason = SandboxRoutingReasonReserved
		return result, nil
	}
}

func admitLockedSandboxTurn(ctx context.Context, tx pgx.Tx, job sandboxRoutingJob) (bool, error) {
	var rawSettings json.RawMessage
	if err := tx.QueryRow(ctx, `
		SELECT settings
		FROM organizations
		WHERE id = @org_id
		FOR NO KEY UPDATE`, pgx.NamedArgs{"org_id": job.OrgID}).Scan(&rawSettings); err != nil {
		return false, fmt.Errorf("lock organization for shared sandbox admission: %w", err)
	}
	settings, err := models.ParseOrgSettings(rawSettings)
	if err != nil {
		return false, fmt.Errorf("parse organization settings for shared sandbox admission: %w", err)
	}
	var active int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM jobs
		WHERE org_id = @org_id
		  AND id <> @job_id
		  AND job_type IN ('run_agent', 'continue_session')
		  AND (
			status = 'running'
			OR (status = 'pending' AND sandbox_slot_reserved_until > now())
		  )`, pgx.NamedArgs{
		"org_id": job.OrgID,
		"job_id": job.ID,
	}).Scan(&active); err != nil {
		return false, fmt.Errorf("count active sandbox turns: %w", err)
	}
	return active < settings.MaxConcurrentRuns, nil
}

// continueSessionNeedsTerminalProbe lets cancellation and non-resumable
// terminal state reach the lightweight handler cleanup path even when the
// organization has no sandbox turns available. The thread comparison stays
// text-based so malformed legacy payloads cannot fail the UUID cast.
func continueSessionNeedsTerminalProbe(ctx context.Context, tx pgx.Tx, job sandboxRoutingJob) (bool, error) {
	if job.JobType != "continue_session" || job.SessionID == nil {
		return false, nil
	}

	var sessionStatus models.SessionStatus
	var pendingSessionCancel bool
	var stoppedThread bool
	var cancellationCleanup bool
	err := tx.QueryRow(ctx, `
		SELECT s.status,
			EXISTS (
				SELECT 1
				FROM session_cancel_requests scr
				WHERE scr.org_id = @org_id
				  AND scr.session_id = @session_id
				  AND scr.delivered_at IS NULL
			),
			EXISTS (
				SELECT 1
				FROM session_threads st
				JOIN jobs payload_job
				  ON payload_job.id = @job_id
				 AND payload_job.org_id = @org_id
				WHERE st.org_id = @org_id
				  AND st.session_id = @session_id
				  AND st.id::text = payload_job.payload->>'thread_id'
				  AND st.archived_at IS NULL
				  AND (st.status = 'cancelled' OR st.cancel_requested_at IS NOT NULL)
			),
			EXISTS (
				SELECT 1
				FROM jobs cleanup_job
				WHERE cleanup_job.id = @job_id
				  AND cleanup_job.org_id = @org_id
				  AND cleanup_job.payload->>'capacity_cleanup' = 'true'
			)
		FROM sessions s
		WHERE s.org_id = @org_id
		  AND s.id = @session_id
		  AND s.deleted_at IS NULL`, pgx.NamedArgs{
		"job_id":     job.ID,
		"org_id":     job.OrgID,
		"session_id": *job.SessionID,
	}).Scan(&sessionStatus, &pendingSessionCancel, &stoppedThread, &cancellationCleanup)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect durable continue_session state before capacity deferral: %w", err)
	}
	return (sessionStatus.IsTerminal() && !sessionStatus.IsResumable()) || pendingSessionCancel || stoppedThread || cancellationCleanup, nil
}

func parseSandboxRoutingSessionID(raw pgtype.Text) *uuid.UUID {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	sessionID, err := uuid.Parse(raw.String)
	if err != nil {
		return nil
	}
	return &sessionID
}

func resolveSandboxRoutingWorkloadClass(ctx context.Context, tx pgx.Tx, job *sandboxRoutingJob) error {
	if job.WorkloadClass == models.SandboxWorkloadClassCodeReview || job.SessionID == nil {
		return nil
	}
	// Rolling-deploy compatibility for jobs inserted by binaries that predate
	// workload_class. Remove this repair lookup once every supported writer is
	// guaranteed to persist the class at enqueue time.
	var origin models.SessionOrigin
	err := tx.QueryRow(ctx, `
		SELECT origin
		FROM sessions
		WHERE org_id = @org_id
		  AND id = @session_id`, pgx.NamedArgs{
		"org_id":     job.OrgID,
		"session_id": *job.SessionID,
	}).Scan(&origin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve sandbox job workload class: %w", err)
	}
	if origin == models.SessionOriginCodeReview {
		job.WorkloadClass = models.SandboxWorkloadClassCodeReview
		result, updateErr := tx.Exec(ctx, `
			UPDATE jobs
			SET workload_class = @workload_class,
				updated_at = now()
			WHERE id = @job_id
			  AND status = @status`, pgx.NamedArgs{
			"job_id":         job.ID,
			"status":         job.Status,
			"workload_class": job.WorkloadClass,
		})
		if updateErr != nil {
			return fmt.Errorf("persist resolved sandbox workload class: %w", updateErr)
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("persist resolved sandbox workload class: job %s changed ownership", job.ID)
		}
	}
	return nil
}

func updateSandboxRoutingPlacement(ctx context.Context, tx pgx.Tx, job sandboxRoutingJob, targetNodeID string, reservedUntil *time.Time) error {
	query := `
		UPDATE jobs
		SET workload_class = @workload_class,
			target_node_id = @target_node_id,
			sandbox_slot_reserved_until = @reserved_until,
			updated_at = now()
		WHERE id = @job_id
		  AND status = @status`
	args := pgx.NamedArgs{
		"job_id":         job.ID,
		"status":         job.Status,
		"target_node_id": targetNodeID,
		"reserved_until": reservedUntil,
		"workload_class": job.WorkloadClass,
	}
	if job.LockToken != nil {
		query += ` AND lock_token = @lock_token`
		args["lock_token"] = *job.LockToken
	}
	result, err := tx.Exec(ctx, query, args)
	if err != nil {
		return fmt.Errorf("persist sandbox routing placement: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("persist sandbox routing placement: job %s ownership changed", job.ID)
	}
	return nil
}

// updateSandboxTerminalProbePlacement uses created_at as the durable marker for
// an initial run and preserves the fleet-wait marker for a continuation. Claim
// can therefore distinguish an initial-run terminal probe while continuations
// get a full bounded fleet window even after an unbounded organization wait.
func updateSandboxTerminalProbePlacement(ctx context.Context, tx pgx.Tx, job sandboxRoutingJob, targetNodeID string) error {
	retryWindowStartedAt := job.CreatedAt
	if job.JobType == "continue_session" && job.RetryWindowStartedAt != nil {
		retryWindowStartedAt = *job.RetryWindowStartedAt
	}
	result, err := tx.Exec(ctx, `
		UPDATE jobs
		SET workload_class = @workload_class,
			payload = CASE
				WHEN job_type = 'continue_session' THEN jsonb_set(payload, '{capacity_waited}', 'true'::jsonb, true)
				ELSE payload
			END,
			target_node_id = @target_node_id,
			sandbox_slot_reserved_until = NULL,
			retry_window_started_at = @retry_window_started_at,
			updated_at = now()
		WHERE id = @job_id
		  AND status = @status`, pgx.NamedArgs{
		"job_id":                  job.ID,
		"status":                  job.Status,
		"target_node_id":          targetNodeID,
		"workload_class":          job.WorkloadClass,
		"retry_window_started_at": retryWindowStartedAt,
	})
	if err != nil {
		return fmt.Errorf("persist sandbox terminal probe placement: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("persist sandbox terminal probe placement: job %s changed ownership", job.ID)
	}
	return nil
}

func markSandboxCancellationCleanup(ctx context.Context, tx pgx.Tx, job sandboxRoutingJob) error {
	result, err := tx.Exec(ctx, `
		UPDATE jobs
		SET payload = jsonb_set(
				jsonb_set(payload, '{capacity_waited}', 'true'::jsonb, true),
				'{capacity_cleanup}', 'true'::jsonb, true
			),
			updated_at = now()
		WHERE id = @job_id
		  AND org_id = @org_id
		  AND status = @status
		  AND job_type = 'continue_session'`, pgx.NamedArgs{
		"job_id": job.ID,
		"org_id": job.OrgID,
		"status": job.Status,
	})
	if err != nil {
		return fmt.Errorf("mark continuation for capacity cleanup: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("mark continuation for capacity cleanup: job %s changed ownership", job.ID)
	}
	return nil
}

// lint:allow-no-orgid reason="terminal capacity probe selects a fresh worker across organizations"
func selectSandboxRoutingFallbackNode(ctx context.Context, tx pgx.Tx) (string, error) {
	var nodeID string
	err := tx.QueryRow(ctx, `
		SELECT n.id
		FROM nodes n
		WHERE n.mode IN ('worker', 'all')
		  AND n.status = 'active'
		  AND n.last_heartbeat_at >= @dead_before
		ORDER BY
			COALESCE(NULLIF(n.metadata->>'active_job_count', '')::int, 0) ASC,
			n.last_heartbeat_at DESC,
			n.id ASC
		LIMIT 1`, pgx.NamedArgs{
		"dead_before": time.Now().Add(-nodeDeadHeartbeatThreshold),
	}).Scan(&nodeID)
	if err != nil {
		return "", fmt.Errorf("select worker for terminal sandbox capacity probe: %w", err)
	}
	return nodeID, nil
}

// lint:allow-no-orgid reason="worker capacity routing intentionally aggregates reservations across organizations"
func selectSandboxRoutingCandidate(ctx context.Context, tx pgx.Tx, workloadClass models.SandboxWorkloadClass, excludedNodeIDs []string) (string, error) {
	var nodeID string
	err := tx.QueryRow(ctx, `
		WITH candidate_nodes AS (
			SELECT n.*,
				COALESCE(
					NULLIF(n.metadata->>'sandbox_capacity_node_id', ''),
					regexp_replace(n.id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
				) AS capacity_node_id
			FROM nodes n
			WHERE n.mode IN ('worker', 'all')
			  AND n.status = 'active'
			  AND n.last_heartbeat_at >= @dead_before
			  AND COALESCE(n.metadata->>'live_sandbox_count_error', '') = ''
			  AND NOT (n.id = ANY(@excluded_node_ids::text[]))
		),
		raw_load AS (
			SELECT
				n.id,
				COALESCE(NULLIF(n.metadata->>'live_sandbox_count', '')::int, 0) AS live_sandboxes,
				COALESCE(NULLIF(n.metadata->>'reserved_sandbox_count', '')::int, 0) AS local_reservations,
				COALESCE(NULLIF(n.metadata->>'sandbox_turn_reserved_count', '')::int, 0) AS sandbox_turn_local_reservations,
				COALESCE(NULLIF(n.metadata->>'max_active_sandboxes', '')::int, 0) AS max_sandboxes,
				COALESCE(
					NULLIF(n.metadata->>'interactive_reserved_sandbox_slots', '')::int,
					CASE WHEN COALESCE(NULLIF(n.metadata->>'max_active_sandboxes', '')::int, 0) > 1 THEN 1 ELSE 0 END
				) AS interactive_reservations,
				COALESCE(NULLIF(n.metadata->>'active_job_count', '')::int, 0) AS active_jobs,
				(
					SELECT COUNT(*)
					FROM jobs reserved_job
					JOIN nodes reserved_node ON reserved_node.id = reserved_job.target_node_id
					WHERE COALESCE(
							NULLIF(reserved_node.metadata->>'sandbox_capacity_node_id', ''),
							regexp_replace(reserved_node.id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
						) = n.capacity_node_id
					  AND reserved_job.sandbox_slot_reserved_until > now()
					  AND reserved_job.status = 'pending'
				) AS pending_durable_reservations,
				(
					SELECT COUNT(*)
					FROM jobs reserved_job
					JOIN nodes reserved_node ON reserved_node.id = reserved_job.target_node_id
					WHERE COALESCE(
							NULLIF(reserved_node.metadata->>'sandbox_capacity_node_id', ''),
							regexp_replace(reserved_node.id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
						) = n.capacity_node_id
					  AND reserved_job.sandbox_slot_reserved_until > now()
					  AND reserved_job.status = 'running'
				) AS running_durable_reservations,
				(
					SELECT COUNT(*)
					FROM sandbox_capacity_reservations shared_reservation
					WHERE shared_reservation.node_id = n.capacity_node_id
					  AND shared_reservation.expires_at > now()
					  AND shared_reservation.job_id IS NOT NULL
					  AND NOT EXISTS (
						SELECT 1
						FROM jobs reserved_job
						JOIN nodes reserved_node ON reserved_node.id = reserved_job.target_node_id
						WHERE reserved_job.id = shared_reservation.job_id
						  AND reserved_job.lock_token = shared_reservation.job_lock_token
						  AND COALESCE(
								NULLIF(reserved_node.metadata->>'sandbox_capacity_node_id', ''),
								regexp_replace(reserved_node.id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
							) = n.capacity_node_id
						  AND reserved_job.sandbox_slot_reserved_until > now()
						  AND reserved_job.status IN ('pending', 'running')
					  )
				) AS shared_sandbox_turn_reservations,
				(
					SELECT COUNT(*)
					FROM sandbox_capacity_reservations shared_reservation
					WHERE shared_reservation.node_id = n.capacity_node_id
					  AND shared_reservation.expires_at > now()
					  AND shared_reservation.job_id IS NULL
				) AS shared_non_turn_reservations,
				n.last_heartbeat_at
			FROM candidate_nodes n
		),
		candidate_load AS (
			SELECT
				*,
				live_sandboxes
					+ GREATEST(local_reservations - sandbox_turn_local_reservations, shared_non_turn_reservations, 0)
					+ pending_durable_reservations
					+ GREATEST(sandbox_turn_local_reservations, running_durable_reservations + shared_sandbox_turn_reservations) AS capacity_load
			FROM raw_load
		)
		SELECT id
		FROM candidate_load
		WHERE max_sandboxes > 0
		  AND capacity_load <
			CASE
				WHEN @workload_class = 'code_review' THEN max_sandboxes - interactive_reservations
				ELSE max_sandboxes
			END
		ORDER BY
			capacity_load::double precision / max_sandboxes ASC,
			capacity_load ASC,
			active_jobs ASC,
			last_heartbeat_at DESC,
			id ASC
		LIMIT 1`, pgx.NamedArgs{
		"dead_before":       time.Now().Add(-nodeDeadHeartbeatThreshold),
		"excluded_node_ids": excludedNodeIDs,
		"workload_class":    workloadClass,
	}).Scan(&nodeID)
	if err != nil {
		return "", err
	}
	return nodeID, nil
}

// lint:allow-no-orgid reason="worker capacity routing intentionally aggregates reservations across organizations"
func sandboxRoutingCandidateHasCapacity(ctx context.Context, tx pgx.Tx, nodeID string, jobID uuid.UUID, workloadClass models.SandboxWorkloadClass) (bool, error) {
	var live, localReserved, sandboxTurnLocalReserved, maxActive, interactiveReserved, pendingDurableReserved, runningDurableReserved, sharedTurnReserved, sharedNonTurnReserved int
	err := tx.QueryRow(ctx, `
		WITH candidate_node AS (
			SELECT n.*,
				COALESCE(
					NULLIF(n.metadata->>'sandbox_capacity_node_id', ''),
					regexp_replace(n.id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
				) AS capacity_node_id
			FROM nodes n
			WHERE n.id = @node_id
			  AND n.mode IN ('worker', 'all')
			  AND n.status = 'active'
			  AND n.last_heartbeat_at >= @dead_before
			  AND COALESCE(n.metadata->>'live_sandbox_count_error', '') = ''
		)
		SELECT
			COALESCE(NULLIF(n.metadata->>'live_sandbox_count', '')::int, 0),
			COALESCE(NULLIF(n.metadata->>'reserved_sandbox_count', '')::int, 0),
			COALESCE(NULLIF(n.metadata->>'sandbox_turn_reserved_count', '')::int, 0),
			COALESCE(NULLIF(n.metadata->>'max_active_sandboxes', '')::int, 0),
			COALESCE(
				NULLIF(n.metadata->>'interactive_reserved_sandbox_slots', '')::int,
				CASE WHEN COALESCE(NULLIF(n.metadata->>'max_active_sandboxes', '')::int, 0) > 1 THEN 1 ELSE 0 END
			),
			(
				SELECT COUNT(*)
				FROM jobs reserved_job
				JOIN nodes reserved_node ON reserved_node.id = reserved_job.target_node_id
				WHERE COALESCE(
						NULLIF(reserved_node.metadata->>'sandbox_capacity_node_id', ''),
						regexp_replace(reserved_node.id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
					) = n.capacity_node_id
				  AND reserved_job.id <> @job_id
				  AND reserved_job.sandbox_slot_reserved_until > now()
				  AND reserved_job.status = 'pending'
			),
			(
				SELECT COUNT(*)
				FROM jobs reserved_job
				JOIN nodes reserved_node ON reserved_node.id = reserved_job.target_node_id
				WHERE COALESCE(
						NULLIF(reserved_node.metadata->>'sandbox_capacity_node_id', ''),
						regexp_replace(reserved_node.id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
					) = n.capacity_node_id
				  AND reserved_job.id <> @job_id
				  AND reserved_job.sandbox_slot_reserved_until > now()
				  AND reserved_job.status = 'running'
			),
			(
				SELECT COUNT(*)
				FROM sandbox_capacity_reservations shared_reservation
				WHERE shared_reservation.node_id = n.capacity_node_id
				  AND shared_reservation.expires_at > now()
				  AND shared_reservation.job_id IS NOT NULL
				  AND NOT EXISTS (
					SELECT 1
					FROM jobs reserved_job
					JOIN nodes reserved_node ON reserved_node.id = reserved_job.target_node_id
					WHERE reserved_job.id = shared_reservation.job_id
					  AND reserved_job.lock_token = shared_reservation.job_lock_token
					  AND COALESCE(
							NULLIF(reserved_node.metadata->>'sandbox_capacity_node_id', ''),
							regexp_replace(reserved_node.id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
						) = n.capacity_node_id
					  AND reserved_job.sandbox_slot_reserved_until > now()
					  AND reserved_job.status IN ('pending', 'running')
				  )
			),
			(
				SELECT COUNT(*)
				FROM sandbox_capacity_reservations shared_reservation
				WHERE shared_reservation.node_id = n.capacity_node_id
				  AND shared_reservation.expires_at > now()
				  AND shared_reservation.job_id IS NULL
			)
		FROM candidate_node n`, pgx.NamedArgs{
		"job_id":      jobID,
		"node_id":     nodeID,
		"dead_before": time.Now().Add(-nodeDeadHeartbeatThreshold),
	}).Scan(&live, &localReserved, &sandboxTurnLocalReserved, &maxActive, &interactiveReserved, &pendingDurableReserved, &runningDurableReserved, &sharedTurnReserved, &sharedNonTurnReserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("recheck sandbox capacity on worker %s: %w", nodeID, err)
	}
	effectiveMax := maxActive
	if workloadClass == models.SandboxWorkloadClassCodeReview {
		effectiveMax -= interactiveReserved
	}
	load := sandboxRoutingCapacityLoad(live, localReserved, sandboxTurnLocalReserved, pendingDurableReserved, runningDurableReserved, sharedTurnReserved, sharedNonTurnReserved)
	return effectiveMax > 0 && load < effectiveMax, nil
}

func sandboxRoutingCapacityLoad(live, localReserved, sandboxTurnLocalReserved, pendingDurableReserved, runningDurableReserved, sharedTurnReserved, sharedNonTurnReserved int) int {
	overlappingNonTurns := max(localReserved-sandboxTurnLocalReserved, sharedNonTurnReserved, 0)
	overlappingSandboxTurns := max(sandboxTurnLocalReserved, runningDurableReserved+sharedTurnReserved)
	return live + overlappingNonTurns + pendingDurableReserved + overlappingSandboxTurns
}

// CountActiveSandboxTurnsByOrg returns claimed interactive and code-review
// turns for the final organization admission fence. The current job is
// included, so callers reject only when the count is greater than the limit.
func (s *JobStore) CountActiveSandboxTurnsByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM jobs
		WHERE org_id = @org_id
		  AND job_type IN ('run_agent', 'continue_session')
		  AND status = 'running'`, pgx.NamedArgs{
		"org_id": orgID,
	}).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active sandbox turns: %w", err)
	}
	return count, nil
}

// CountAdmittedSandboxTurnsByOrg returns claimed turns plus pending turns with
// a live durable worker reservation. Runtime status uses this broader count so
// it reports the same capacity pressure as the atomic routing fence.
func (s *JobStore) CountAdmittedSandboxTurnsByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM jobs
		WHERE org_id = @org_id
		  AND job_type IN ('run_agent', 'continue_session')
		  AND (
			status = 'running'
			OR (status = 'pending' AND sandbox_slot_reserved_until > now())
		  )`, pgx.NamedArgs{
		"org_id": orgID,
	}).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count admitted sandbox turns by org: %w", err)
	}
	return count, nil
}

// IsSessionWaitingForSandboxCapacity reports whether a pending turn for the
// session is blocked by other admitted turns at the shared organization limit.
// The candidate's own live reservation is excluded so an admitted session is
// not mislabeled as waiting while its environment starts.
func (s *JobStore) IsSessionWaitingForSandboxCapacity(ctx context.Context, orgID, sessionID uuid.UUID, maxConcurrentRuns int) (bool, error) {
	var waiting bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM jobs waiting_job
			WHERE waiting_job.org_id = @org_id
			  AND waiting_job.job_type IN ('run_agent', 'continue_session')
			  AND waiting_job.status = 'pending'
			  AND waiting_job.payload->>'session_id' = @session_id
			  AND (
				waiting_job.sandbox_slot_reserved_until IS NULL
				OR waiting_job.sandbox_slot_reserved_until <= now()
			  )
			  AND waiting_job.payload->>'capacity_cleanup' IS DISTINCT FROM 'true'
			  AND NOT (
				waiting_job.job_type = 'run_agent'
				AND waiting_job.retry_window_started_at = waiting_job.created_at
				AND waiting_job.created_at <= now() - (@terminal_probe_seconds * interval '1 second')
			  )
			  AND (
				SELECT COUNT(*)
				FROM jobs admitted_job
				WHERE admitted_job.org_id = @org_id
				  AND admitted_job.id <> waiting_job.id
				  AND admitted_job.job_type IN ('run_agent', 'continue_session')
				  AND (
					admitted_job.status = 'running'
					OR (
						admitted_job.status = 'pending'
						AND admitted_job.sandbox_slot_reserved_until > now()
					)
				  )
			  ) >= @max_concurrent_runs
		)`, pgx.NamedArgs{
		"max_concurrent_runs":    maxConcurrentRuns,
		"org_id":                 orgID,
		"session_id":             sessionID.String(),
		"terminal_probe_seconds": int(sandboxRoutingTerminalProbeAge.Seconds()),
	}).Scan(&waiting)
	if err != nil {
		return false, fmt.Errorf("inspect session sandbox capacity wait: %w", err)
	}
	return waiting, nil
}

// ReleaseSandboxSlotReservationWithLease clears a routing reservation once
// the authoritative local gate has finished creating or hydrating the
// sandbox. The fencing token prevents a stale executor from clearing a newer
// attempt's placement.
// lint:allow-no-orgid reason="fenced worker cleanup addresses a globally unique job id"
func (s *JobStore) ReleaseSandboxSlotReservationWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
	result, err := s.db.Exec(ctx, `
		UPDATE jobs
		SET sandbox_slot_reserved_until = NULL,
			updated_at = now()
		WHERE id = $1
		  AND status = 'running'
		  AND lock_token = $2
		  AND sandbox_slot_reserved_until IS NOT NULL`, jobID, lockToken)
	if err != nil {
		return false, fmt.Errorf("release sandbox slot reservation with lease: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

// ReleaseSandboxRoutingPlacementWithLease clears a pre-claim placement after
// admission rejects the turn. Ordinary affinity pins have no durable slot
// reservation and are intentionally preserved by the final predicate.
// lint:allow-no-orgid reason="fenced worker cleanup addresses a globally unique job id"
func (s *JobStore) ReleaseSandboxRoutingPlacementWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
	result, err := s.db.Exec(ctx, `
		UPDATE jobs
		SET target_node_id = NULL,
			sandbox_slot_reserved_until = NULL,
			updated_at = now()
		WHERE id = $1
		  AND status = 'running'
		  AND lock_token = $2
		  AND sandbox_slot_reserved_until IS NOT NULL`, jobID, lockToken)
	if err != nil {
		return false, fmt.Errorf("release sandbox routing placement with lease: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

// ClearSandboxRoutingPlacementWithLease clears worker affinity when a fresh
// sandbox create or hydrate attempt fails. Unlike admission-rejection cleanup,
// this also clears compatibility and terminal-probe placements that have a
// target without sandbox_slot_reserved_until. Callers must use it only after a
// fresh-sandbox failure, never for an existing-sandbox affinity turn.
// lint:allow-no-orgid reason="fenced worker cleanup addresses a globally unique job id"
func (s *JobStore) ClearSandboxRoutingPlacementWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
	result, err := s.db.Exec(ctx, `
		UPDATE jobs
		SET target_node_id = NULL,
			sandbox_slot_reserved_until = NULL,
			updated_at = now()
		WHERE id = $1
		  AND status = 'running'
		  AND lock_token = $2
		  AND (target_node_id IS NOT NULL OR sandbox_slot_reserved_until IS NOT NULL)`, jobID, lockToken)
	if err != nil {
		return false, fmt.Errorf("clear sandbox routing placement with lease: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

type jobExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// ClaimNextRunnable atomically claims the next due pending job, marking it as
// running with a renewable lease and fencing token. Returns nil, nil when no
// runnable job exists.
//
// Node-affinity filter: a job with target_node_id set is claimable only by
// that node, or by any active worker once the target is unavailable — dead
// (status='dead' or stale heartbeat) or draining. A draining node keeps
// heartbeating to hold its previews, so without the draining case a pinned
// turn starves until the node dies; the claimer hydrates from the snapshot.
// lint:allow-no-orgid reason="worker queue consumer scans cross-org jobs by design"
func (s *JobStore) ClaimNextRunnable(ctx context.Context, nodeID, ownerID string, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, error) {
	for attempt := 0; attempt < maxClaimAdmissionSkips; attempt++ {
		job, deferred, err := s.claimNextRunnableAttempt(ctx, nodeID, ownerID, lockToken, leaseDuration)
		if err != nil {
			return nil, err
		}
		if job != nil || !deferred {
			return job, nil
		}
	}
	return nil, nil
}

// claimNextRunnableAttempt owns exactly one candidate transaction. In
// particular, an org-limit deferral commits before the next candidate is
// examined, so organization row locks can never accumulate in cross-org scan
// order and deadlock another dispatcher or a settings update.
func (s *JobStore) claimNextRunnableAttempt(ctx context.Context, nodeID, ownerID string, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return nil, false, fmt.Errorf("job store does not support claim transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin claim next runnable job: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var candidate sandboxRoutingJob
	var rawSessionID pgtype.Text
	var retryWindowStartedAt pgtype.Timestamptz
	err = tx.QueryRow(ctx, `
			WITH unavailable_target_nodes AS (
				SELECT id
				FROM nodes
				WHERE status IN ('dead', 'draining') OR last_heartbeat_at < @dead_before
			),
			claiming_node AS (
				SELECT id
				FROM nodes
				WHERE id = @node_id
				  AND status = 'active'
				  AND last_heartbeat_at >= @dead_before
			)
			SELECT j.id, j.org_id, j.job_type, j.payload->>'session_id',
				j.workload_class AS effective_workload_class,
				j.status, j.retry_window_started_at, j.created_at
			FROM jobs j
			LEFT JOIN unavailable_target_nodes d ON d.id = j.target_node_id
			JOIN claiming_node cn ON TRUE
			WHERE j.status = 'pending' AND j.run_at <= now()
			  AND (j.job_type NOT IN ('run_agent', 'continue_session') OR j.target_node_id IS NOT NULL)
			  AND (
			    j.target_node_id IS NULL
			    OR j.target_node_id = @node_id
			    OR (d.id IS NOT NULL AND j.sandbox_slot_reserved_until IS NULL)
			  )
			ORDER BY
				j.priority DESC,
				CASE
					WHEN j.job_type IN ('run_agent', 'continue_session')
					 AND j.workload_class = 'interactive' THEN 0
					WHEN j.job_type IN ('run_agent', 'continue_session') THEN 1
					ELSE 0
				END,
				-- Capacity deferrals advance run_at, so this durable order lets a
				-- bounded claim pass resume beyond the previously deferred prefix.
				j.run_at ASC,
				j.created_at ASC
			FOR UPDATE OF j SKIP LOCKED
			LIMIT 1`, pgx.NamedArgs{
		"dead_before": time.Now().Add(-nodeDeadHeartbeatThreshold),
		"node_id":     nodeID,
	}).Scan(
		&candidate.ID,
		&candidate.OrgID,
		&candidate.JobType,
		&rawSessionID,
		&candidate.WorkloadClass,
		&candidate.Status,
		&retryWindowStartedAt,
		&candidate.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("select next runnable job: %w", err)
	}
	candidate.SessionID = parseSandboxRoutingSessionID(rawSessionID)
	if retryWindowStartedAt.Valid {
		startedAt := retryWindowStartedAt.Time
		candidate.RetryWindowStartedAt = &startedAt
	}

	isSandboxJob := candidate.JobType == "run_agent" || candidate.JobType == "continue_session"
	if isSandboxJob {
		if _, err := tx.Exec(ctx, `SAVEPOINT sandbox_claim_candidate`); err != nil {
			return nil, false, fmt.Errorf("create sandbox claim candidate savepoint: %w", err)
		}
		if err := resolveSandboxRoutingWorkloadClass(ctx, tx, &candidate); err != nil {
			return s.deferSandboxClaimJobError(ctx, tx, candidate, err)
		}
	}
	// A terminal placement stamps the initial job creation time into the retry
	// window. Ordinary handler retries stamp their first failure time instead,
	// so they must still pass shared org admission on every subsequent claim.
	isTerminalInitialRun := candidate.JobType == "run_agent" &&
		candidate.RetryWindowStartedAt != nil &&
		candidate.RetryWindowStartedAt.Equal(candidate.CreatedAt) &&
		time.Since(*candidate.RetryWindowStartedAt) >= sandboxRoutingTerminalProbeAge
	if isSandboxJob && !isTerminalInitialRun {
		admitted, admissionErr := admitLockedSandboxTurn(ctx, tx, candidate)
		if admissionErr != nil {
			return s.deferSandboxClaimJobError(ctx, tx, candidate, admissionErr)
		}
		if !admitted {
			terminalProbe, probeErr := continueSessionNeedsTerminalProbe(ctx, tx, candidate)
			if probeErr != nil {
				return s.deferSandboxClaimJobError(ctx, tx, candidate, probeErr)
			}
			if terminalProbe {
				probeResult, markErr := tx.Exec(ctx, `
					UPDATE jobs
					SET payload = jsonb_set(
							jsonb_set(payload, '{capacity_waited}', 'true'::jsonb, true),
							'{capacity_cleanup}', 'true'::jsonb, true
						),
						sandbox_slot_reserved_until = NULL,
						updated_at = now()
					WHERE id = @job_id
					  AND status = 'pending'
					  AND job_type = 'continue_session'`, pgx.NamedArgs{"job_id": candidate.ID})
				if markErr != nil {
					return s.deferSandboxClaimJobError(ctx, tx, candidate, fmt.Errorf("mark org-limited continuation for terminal cleanup: %w", markErr))
				}
				if probeResult.RowsAffected() != 1 {
					return s.deferSandboxClaimJobError(ctx, tx, candidate, fmt.Errorf("mark org-limited continuation for terminal cleanup: pending job %s changed ownership", candidate.ID))
				}
			} else {
				deferResult, deferErr := tx.Exec(ctx, `
					UPDATE jobs
					SET workload_class = @workload_class,
						payload = CASE
							WHEN job_type = 'continue_session' THEN jsonb_set(payload, '{capacity_waited}', 'true'::jsonb, true)
							ELSE payload
						END,
						run_at = now() + (@retry_seconds * interval '1 second'),
						retry_window_started_at = CASE
							WHEN job_type = 'continue_session' THEN NULL
							WHEN job_type = 'run_agent' AND attempts = 0 THEN created_at
							ELSE COALESCE(retry_window_started_at, now())
						END,
						target_node_id = CASE
							WHEN sandbox_slot_reserved_until IS NULL THEN target_node_id
							ELSE NULL
						END,
						sandbox_slot_reserved_until = NULL,
						updated_at = now()
					WHERE id = @job_id
					  AND status = 'pending'`, pgx.NamedArgs{
					"job_id":         candidate.ID,
					"retry_seconds":  int(SandboxOrgLimitRetryDelay.Seconds()),
					"workload_class": candidate.WorkloadClass,
				})
				if deferErr != nil {
					return s.deferSandboxClaimJobError(ctx, tx, candidate, fmt.Errorf("defer affinity-bound sandbox job at org limit: %w", deferErr))
				}
				if deferResult.RowsAffected() != 1 {
					return s.deferSandboxClaimJobError(ctx, tx, candidate, fmt.Errorf("defer affinity-bound sandbox job at org limit: pending job %s changed ownership", candidate.ID))
				}
				if err := tx.Commit(ctx); err != nil {
					return nil, false, fmt.Errorf("commit sandbox org-limit deferral: %w", err)
				}
				return nil, true, nil
			}
		}
	}

	query := fmt.Sprintf(`
			UPDATE jobs j
			SET status = 'running',
				locked_by_node_id = @node_id,
				run_owner_id = @owner_id,
				owner_kind = 'worker',
				lock_token = @lock_token,
				locked_at = now(),
				lease_expires_at = now() + (@lease_seconds * interval '1 second'),
				attempts = attempts + 1,
				updated_at = now()
			WHERE j.id = @job_id
			  AND j.status = 'pending'
			RETURNING %s`, claimedJobColumns)
	job, err := scanJobRow(tx.QueryRow(ctx, query, pgx.NamedArgs{
		"job_id":        candidate.ID,
		"node_id":       nodeID,
		"owner_id":      ownerID,
		"lock_token":    lockToken,
		"lease_seconds": int(leaseDuration.Seconds()),
	}))
	if err != nil {
		if isSandboxJob {
			return s.deferSandboxClaimJobError(ctx, tx, candidate, fmt.Errorf("claim next runnable job: %w", err))
		}
		return nil, false, fmt.Errorf("claim next runnable job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit claim next runnable job: %w", err)
	}
	return job, false, nil
}

// deferSandboxClaimJobError recovers the candidate transaction after a
// deterministic sandbox admission error, then advances only that locked job.
// ClaimNextRunnable can continue its bounded scan instead of letting one
// malformed tenant configuration block every job type on the worker.
func (s *JobStore) deferSandboxClaimJobError(ctx context.Context, tx pgx.Tx, candidate sandboxRoutingJob, claimErr error) (*models.Job, bool, error) {
	if _, rollbackErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sandbox_claim_candidate`); rollbackErr != nil {
		return nil, false, fmt.Errorf("rollback failed sandbox claim work for job %s after %v: %w", candidate.ID, claimErr, rollbackErr)
	}
	result, err := tx.Exec(ctx, `
		UPDATE jobs
		SET last_error = @last_error,
			run_at = @run_at,
			target_node_id = CASE
				WHEN sandbox_slot_reserved_until IS NULL THEN target_node_id
				ELSE NULL
			END,
			sandbox_slot_reserved_until = NULL,
			updated_at = now()
		WHERE id = @job_id
		  AND org_id = @org_id
		  AND status = 'pending'`, pgx.NamedArgs{
		"job_id":     candidate.ID,
		"last_error": fmt.Sprintf("sandbox claim deferred: %v", claimErr),
		"org_id":     candidate.OrgID,
		"run_at":     time.Now().Add(sandboxRoutingErrorRetryDelay),
	})
	if err != nil {
		return nil, false, fmt.Errorf("defer failed sandbox claim for job %s after %v: %w", candidate.ID, claimErr, err)
	}
	if result.RowsAffected() != 1 {
		return nil, false, fmt.Errorf("defer failed sandbox claim for job %s after %v: pending job changed ownership", candidate.ID, claimErr)
	}
	if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT sandbox_claim_candidate`); err != nil {
		return nil, false, fmt.Errorf("release failed sandbox claim savepoint for job %s after %v: %w", candidate.ID, claimErr, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit failed sandbox claim deferral for job %s after %v: %w", candidate.ID, claimErr, err)
	}
	s.logger.Error().
		Err(claimErr).
		Str("job_id", candidate.ID.String()).
		Str("org_id", candidate.OrgID.String()).
		Str("job_type", candidate.JobType).
		Msg("sandbox claim failed; deferred only the affected job")
	return nil, true, nil
}

func scanJobRow(row pgx.Row) (*models.Job, error) {
	var job models.Job
	var lockedByNodeID pgtype.Text
	var lockedAt pgtype.Timestamptz
	var leaseExpiresAt pgtype.Timestamptz
	var persistedLockToken pgtype.UUID
	var runOwnerID pgtype.Text
	var ownerKind string
	var lastError pgtype.Text
	var dedupeKey pgtype.Text
	var targetNodeID pgtype.Text
	var sandboxSlotReservedUntil pgtype.Timestamptz
	var retryWindowStartedAt pgtype.Timestamptz
	var completedAt pgtype.Timestamptz
	err := row.Scan(
		&job.ID, &job.OrgID, &job.Queue, &job.JobType, &job.Payload, &job.Priority,
		&job.Status, &job.Attempts, &job.MaxAttempts, &job.RunAt, &lockedByNodeID,
		&lockedAt, &leaseExpiresAt, &persistedLockToken, &runOwnerID,
		&ownerKind, &lastError, &dedupeKey, &job.WorkloadClass, &targetNodeID,
		&sandboxSlotReservedUntil, &retryWindowStartedAt, &job.CreatedAt, &job.UpdatedAt, &completedAt,
	)
	if err != nil {
		return nil, err
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
	job.OwnerKind = models.JobOwnerKind(ownerKind)
	if lastError.Valid {
		job.LastError = &lastError.String
	}
	if dedupeKey.Valid {
		job.DedupeKey = &dedupeKey.String
	}
	if targetNodeID.Valid {
		job.TargetNodeID = &targetNodeID.String
	}
	if sandboxSlotReservedUntil.Valid {
		job.SandboxSlotReservedUntil = &sandboxSlotReservedUntil.Time
	}
	if retryWindowStartedAt.Valid {
		job.RetryWindowStartedAt = &retryWindowStartedAt.Time
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	return &job, nil
}

// EnsureRetryWindowStartedAtWithLease records the first bounded retry under
// the current fencing token and returns the durable start time. Repeated calls
// preserve the original timestamp so worker restarts cannot extend the window.
// lint:allow-no-orgid reason="worker queue consumer updates cross-org jobs by globally unique fenced job id"
func (s *JobStore) EnsureRetryWindowStartedAtWithLease(ctx context.Context, jobID, lockToken uuid.UUID, startedAt time.Time) (time.Time, bool, error) {
	var persisted time.Time
	err := s.db.QueryRow(ctx, `
		UPDATE jobs
		SET retry_window_started_at = COALESCE(retry_window_started_at, $1),
			updated_at = now()
		WHERE id = $2
		  AND status = 'running'
		  AND lock_token = $3
		RETURNING retry_window_started_at`, startedAt, jobID, lockToken).Scan(&persisted)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("ensure retry window started at with lease: %w", err)
	}
	return persisted, true, nil
}

// GetRunningForSessionExecutor returns the running job only when the executor
// still owns the job by owner id and fencing token.
// lint:allow-no-orgid reason="session executor boot validates cross-org job ownership by globally unique fenced job id"
func (s *JobStore) GetRunningForSessionExecutor(ctx context.Context, orgID, jobID, lockToken, executorID uuid.UUID) (*models.Job, bool, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM jobs j
		WHERE j.org_id = $1
		  AND j.id = $2
		  AND j.status = 'running'
		  AND j.lock_token = $3
		  AND j.owner_kind = 'session_executor'
		  AND j.run_owner_id = $4`, claimedJobColumns)

	job, err := scanJobRow(s.db.QueryRow(ctx, query, orgID, jobID, lockToken, executorID.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get running session executor job: %w", err)
	}
	return job, true, nil
}

// HandoffToSessionExecutorWithLease transfers a running job from the worker
// dispatcher to a durable session executor without changing the fencing token.
func (s *JobStore) HandoffToSessionExecutorWithLease(ctx context.Context, orgID, jobID, lockToken, executorID uuid.UUID, leaseDuration time.Duration) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE jobs
		SET owner_kind = 'session_executor',
			run_owner_id = $1,
			lease_expires_at = now() + ($5 * interval '1 second'),
			sandbox_slot_reserved_until = CASE
				WHEN sandbox_slot_reserved_until IS NULL THEN NULL
				ELSE now() + ($5 * interval '1 second')
			END,
			updated_at = now()
		WHERE org_id = $2
		  AND id = $3
		  AND status = 'running'
		  AND lock_token = $4`, executorID.String(), orgID, jobID, lockToken, int(leaseDuration.Seconds()))
	if err != nil {
		return false, fmt.Errorf("handoff job to session executor: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RenewLeaseForSessionExecutor extends a running executor-owned job lease only
// when both the fencing token and executor owner id still match.
func (s *JobStore) RenewLeaseForSessionExecutor(ctx context.Context, orgID, jobID, lockToken, executorID uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error) {
	query := `
		UPDATE jobs
		SET lease_expires_at = now() + (@lease_seconds * interval '1 second'),
			sandbox_slot_reserved_until = CASE
				WHEN sandbox_slot_reserved_until IS NULL THEN NULL
				WHEN payload->>'session_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
					AND EXISTS (
						SELECT 1
						FROM sessions sandbox_session
						WHERE sandbox_session.org_id = jobs.org_id
						  AND sandbox_session.id = CASE
							WHEN payload->>'session_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
							THEN (payload->>'session_id')::uuid
						  END
						  AND sandbox_session.container_id IS NOT NULL
					) THEN NULL
				ELSE now() + (@lease_seconds * interval '1 second')
			END,
			updated_at = now()
		WHERE id = @job_id
		  AND org_id = @org_id
		  AND status = 'running'
		  AND owner_kind = 'session_executor'
		  AND run_owner_id = @executor_id
		  AND lock_token = @lock_token
		  AND (
		    job_type NOT IN ('run_agent', 'continue_session')
		    OR NULLIF(payload->>'session_id', '') IS NULL
		    OR EXISTS (
		      SELECT 1
		      FROM sessions s
		      WHERE s.org_id = jobs.org_id
		        AND s.id = CASE
		          WHEN payload->>'session_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		          THEN (payload->>'session_id')::uuid
		        END
		        AND s.status NOT IN ('completed', 'failed', 'cancelled', 'skipped')
		    )
		  )
		RETURNING lease_expires_at`

	var leaseExpiresAt time.Time
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"lease_seconds": int(leaseDuration.Seconds()),
		"org_id":        orgID,
		"job_id":        jobID,
		"lock_token":    lockToken,
		"executor_id":   executorID.String(),
	}).Scan(&leaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if terminalizeErr := s.terminalizeIfReferencedSessionTerminal(ctx, jobID, lockToken); terminalizeErr != nil {
			return nil, false, terminalizeErr
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("renew session executor job lease: %w", err)
	}
	return &models.Job{ID: jobID, LockToken: &lockToken, LeaseExpiresAt: &leaseExpiresAt}, true, nil
}

// RenewLease extends the lease for a running job owned by the provided fencing
// token. ok=false means ownership was already lost.
// lint:allow-no-orgid reason="worker queue consumer renews cross-org job leases by design"
func (s *JobStore) RenewLease(ctx context.Context, jobID, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error) {
	query := `
		UPDATE jobs
		SET lease_expires_at = now() + (@lease_seconds * interval '1 second'),
			sandbox_slot_reserved_until = CASE
				WHEN sandbox_slot_reserved_until IS NULL THEN NULL
				WHEN payload->>'session_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
					AND EXISTS (
						SELECT 1
						FROM sessions sandbox_session
						WHERE sandbox_session.org_id = jobs.org_id
						  AND sandbox_session.id = CASE
							WHEN payload->>'session_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
							THEN (payload->>'session_id')::uuid
						  END
						  AND sandbox_session.container_id IS NOT NULL
					) THEN NULL
				ELSE now() + (@lease_seconds * interval '1 second')
			END,
			updated_at = now()
		WHERE id = @job_id
		  AND status = 'running'
		  AND lock_token = @lock_token
		  AND (
		    job_type NOT IN ('run_agent', 'continue_session')
		    OR NULLIF(payload->>'session_id', '') IS NULL
		    OR EXISTS (
		      SELECT 1
		      FROM sessions s
		      WHERE s.org_id = jobs.org_id
		        AND s.id = CASE
		          WHEN payload->>'session_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		          THEN (payload->>'session_id')::uuid
		        END
		        AND s.status NOT IN ('completed', 'failed', 'cancelled', 'skipped')
		    )
		  )
		RETURNING lease_expires_at`

	var leaseExpiresAt time.Time
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"lease_seconds": int(leaseDuration.Seconds()),
		"job_id":        jobID,
		"lock_token":    lockToken,
	}).Scan(&leaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if terminalizeErr := s.terminalizeIfReferencedSessionTerminal(ctx, jobID, lockToken); terminalizeErr != nil {
			return nil, false, terminalizeErr
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("renew job lease: %w", err)
	}
	return &models.Job{ID: jobID, LockToken: &lockToken, LeaseExpiresAt: &leaseExpiresAt}, true, nil
}

func (s *JobStore) terminalizeIfReferencedSessionTerminal(ctx context.Context, jobID, lockToken uuid.UUID) error {
	reason := "referenced session is already terminal; stopping session job lease renewal"
	var updated int64
	err := s.db.QueryRow(ctx, `
		WITH target AS (
			SELECT j.id, j.org_id, j.lock_token, j.owner_kind
			FROM jobs j
			WHERE j.id = $1
			  AND j.status = 'running'
			  AND j.lock_token = $2
			  AND j.job_type IN ('run_agent', 'continue_session')
			  AND NULLIF(j.payload->>'session_id', '') IS NOT NULL
			  AND EXISTS (
			    SELECT 1
			    FROM sessions s
			    WHERE s.org_id = j.org_id
			      AND s.id::text = j.payload->>'session_id'
			      AND s.status IN ('completed', 'failed', 'cancelled', 'skipped')
			  )
			FOR UPDATE
		),
		closed_executors AS (
			UPDATE session_executors se
			SET status = 'failed',
				completed_at = now(),
				exit_code = 1,
				last_error = $3,
				updated_at = now()
			FROM target
			WHERE target.owner_kind = 'session_executor'
			  AND se.org_id = target.org_id
			  AND se.job_id = target.id
			  AND se.lock_token = target.lock_token
			  AND se.status IN ('starting', 'running', 'draining')
			RETURNING se.id
		),
		updated_jobs AS (
			UPDATE jobs j
			SET status = 'failed',
				last_error = $3,
				completed_at = now(),
				locked_by_node_id = NULL,
				run_owner_id = NULL,
				owner_kind = 'worker',
				lock_token = NULL,
				locked_at = NULL,
				lease_expires_at = NULL,
				updated_at = now()
			FROM target
			WHERE j.org_id = target.org_id
			  AND j.id = target.id
			RETURNING j.id
		)
		SELECT COUNT(*) FROM updated_jobs`, jobID, lockToken, reason).Scan(&updated)
	if err != nil {
		return fmt.Errorf("terminalize session job after terminal session lease loss: %w", err)
	}
	return nil
}

// TerminalizeRunningSessionJobs stops in-flight session runner jobs for a
// session that has already reached terminal user-visible state.
func (s *JobStore) TerminalizeRunningSessionJobs(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (int64, error) {
	var updated int64
	err := s.db.QueryRow(ctx, `
		WITH target AS (
			SELECT id, org_id, lock_token, owner_kind
			FROM jobs
			WHERE org_id = $1
			  AND status = 'running'
			  AND job_type IN ('run_agent', 'continue_session')
			  AND payload->>'session_id' = $2::text
			FOR UPDATE
		),
		closed_executors AS (
			UPDATE session_executors se
			SET status = 'failed',
				completed_at = now(),
				exit_code = 1,
				last_error = $3,
				updated_at = now()
			FROM target
			WHERE target.owner_kind = 'session_executor'
			  AND se.org_id = target.org_id
			  AND se.job_id = target.id
			  AND se.lock_token = target.lock_token
			  AND se.status IN ('starting', 'running', 'draining')
			RETURNING se.id
		),
		updated_jobs AS (
			UPDATE jobs j
			SET status = 'failed',
				last_error = $3,
				completed_at = now(),
				locked_by_node_id = NULL,
				run_owner_id = NULL,
				owner_kind = 'worker',
				lock_token = NULL,
				locked_at = NULL,
				lease_expires_at = NULL,
				updated_at = now()
			FROM target
			WHERE j.org_id = target.org_id
			  AND j.id = target.id
			RETURNING j.id
		)
		SELECT COUNT(*) FROM updated_jobs`, orgID, sessionID, reason).Scan(&updated)
	if err != nil {
		return 0, fmt.Errorf("terminalize running session jobs: %w", err)
	}
	return updated, nil
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
			owner_kind = 'worker',
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
			owner_kind = 'worker',
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
			owner_kind = 'worker',
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

// RetryWithLeaseAndTarget requeues a running job and updates its target worker
// pin in the same fenced write. Used when a retry discovers durable state that
// makes the next attempt node-specific.
// lint:allow-no-orgid reason="worker queue consumer requeues cross-org jobs by design"
func (s *JobStore) RetryWithLeaseAndTarget(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time, targetNodeID *string) (bool, error) {
	tag, err := s.execLeaseTerminalUpdate(ctx, `
		UPDATE jobs
		SET status = 'pending',
			last_error = $1,
			run_at = $2,
			locked_by_node_id = NULL,
			run_owner_id = NULL,
			owner_kind = 'worker',
			lock_token = NULL,
			locked_at = NULL,
			lease_expires_at = NULL,
			target_node_id = $5,
			sandbox_slot_reserved_until = CASE
				WHEN $5 IS NULL THEN NULL
				ELSE sandbox_slot_reserved_until
			END,
			updated_at = now()
		WHERE id = $3
		  AND status = 'running'
		  AND lock_token = $4`, errMsg, runAt, jobID, lockToken, targetNodeID)
	if err != nil {
		return false, fmt.Errorf("retry job with lease and target: %w", err)
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
			owner_kind = 'worker',
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

// RetryWithoutConsumingAttemptWithLeaseAndTarget requeues a running job while
// undoing the claim-time attempt increment and updating its target worker pin.
// lint:allow-no-orgid reason="worker queue consumer requeues cross-org jobs by design"
func (s *JobStore) RetryWithoutConsumingAttemptWithLeaseAndTarget(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time, targetNodeID *string) (bool, error) {
	tag, err := s.execLeaseTerminalUpdate(ctx, `
		UPDATE jobs
		SET status = 'pending',
			last_error = $1,
			run_at = $2,
			attempts = GREATEST(attempts - 1, 0),
			locked_by_node_id = NULL,
			run_owner_id = NULL,
			owner_kind = 'worker',
			lock_token = NULL,
			locked_at = NULL,
			lease_expires_at = NULL,
			target_node_id = $5,
			sandbox_slot_reserved_until = CASE
				WHEN $5 IS NULL THEN NULL
				ELSE sandbox_slot_reserved_until
			END,
			updated_at = now()
		WHERE id = $3
		  AND status = 'running'
		  AND lock_token = $4`, errMsg, runAt, jobID, lockToken, targetNodeID)
	if err != nil {
		return false, fmt.Errorf("retry job without consuming attempt with lease and target: %w", err)
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
			owner_kind = 'worker',
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
// is considered dead. Draining is intentionally not treated as dead here: a
// running job on a draining node is reclaimed via lease expiry (or requeued by
// the executor's own drain handler), so it never needs the node-status path.
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
				OR (j.lease_expires_at IS NULL AND d.id IS NOT NULL)
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
				owner_kind = 'worker',
				lock_token = NULL,
				locked_at = NULL,
				lease_expires_at = NULL,
				run_at = now(),
				updated_at = now()
			FROM reclaimable r
			WHERE j.id = r.id
			RETURNING j.org_id, NULLIF(j.payload->>'session_id', '') AS session_id, j.job_type
		),
		updated_sessions AS (
			UPDATE sessions s
			SET recovery_state = 'queued',
			    recovery_queued_at = now(),
			    recovery_started_at = NULL,
			    runtime_stop_reason = 'worker_recovery'
			FROM updated_jobs uj
			WHERE uj.session_id IS NOT NULL
			  AND uj.job_type IN ('run_agent', 'continue_session')
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

// ReclaimLostRunningSessionJobsForSession is the targeted version of
// ReclaimLostRunningJobs used on user input paths to proactively recover a
// single leaderless session instead of waiting for the periodic sweep.
func (s *JobStore) ReclaimLostRunningSessionJobsForSession(ctx context.Context, orgID, sessionID uuid.UUID, staleBefore time.Time, limit int) (int64, error) {
	query := `
		WITH dead_nodes AS (
			SELECT id
			FROM nodes
			WHERE status = 'dead'
			   OR last_heartbeat_at < $3
		),
		reclaimable AS (
			SELECT j.id, j.org_id
			FROM jobs j
			LEFT JOIN dead_nodes d ON d.id = j.locked_by_node_id
			WHERE j.org_id = $1
			  AND j.status = 'running'
			  AND j.job_type IN ('run_agent', 'continue_session')
			  AND j.payload->>'session_id' = $2::text
			  AND (
				j.lease_expires_at < now()
				OR (j.lease_expires_at IS NULL AND d.id IS NOT NULL)
				OR d.id IS NOT NULL
			  )
			ORDER BY j.locked_at ASC
			FOR UPDATE OF j SKIP LOCKED
			LIMIT $4
		),
		updated_jobs AS (
			UPDATE jobs j
			SET status = 'pending',
				last_error = 'job ownership lost; queued for bounded recovery',
				locked_by_node_id = NULL,
				run_owner_id = NULL,
				owner_kind = 'worker',
				lock_token = NULL,
				locked_at = NULL,
				lease_expires_at = NULL,
				run_at = now(),
				updated_at = now()
			FROM reclaimable r
			WHERE j.org_id = r.org_id
			  AND j.id = r.id
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
	err := s.db.QueryRow(ctx, query, orgID, sessionID, staleBefore, limit).Scan(&reclaimed)
	if err != nil {
		return 0, fmt.Errorf("reclaim lost running session jobs for session: %w", err)
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

// SelectWorkerWithSandboxCapacity picks an active worker that currently
// advertises free local sandbox slots in its heartbeat metadata. The result is
// best-effort and still fenced by the worker-local admission gate when the job
// runs, because metadata can be stale between heartbeats.
// lint:allow-no-orgid reason="cross-org worker capacity routing for sandbox admission retries"
func (s *JobStore) SelectWorkerWithSandboxCapacity(ctx context.Context, excludeNodeID string) (*string, error) {
	var nodeID string
	err := s.db.QueryRow(ctx, `
		WITH candidates AS (
			SELECT
				id,
				COALESCE(NULLIF(metadata->>'live_sandbox_count', '')::int, 0) AS live_sandboxes,
				COALESCE(NULLIF(metadata->>'reserved_sandbox_count', '')::int, 0) AS reserved_sandboxes,
				COALESCE(NULLIF(metadata->>'max_active_sandboxes', '')::int, 0) AS max_active_sandboxes,
				COALESCE(NULLIF(metadata->>'active_job_count', '')::int, 0) AS active_job_count,
				last_heartbeat_at
			FROM nodes
			WHERE mode IN ('worker', 'all')
			  AND status = 'active'
			  AND last_heartbeat_at >= @dead_before
			  AND COALESCE(metadata->>'live_sandbox_count_error', '') = ''
			  AND (@exclude_node_id = '' OR id <> @exclude_node_id)
		)
		SELECT id
		FROM candidates
		WHERE max_active_sandboxes > 0
		  AND live_sandboxes + reserved_sandboxes < max_active_sandboxes
		ORDER BY
			live_sandboxes + reserved_sandboxes ASC,
			active_job_count ASC,
			last_heartbeat_at DESC,
			id ASC
		LIMIT 1`,
		pgx.NamedArgs{
			"exclude_node_id": excludeNodeID,
			"dead_before":     time.Now().Add(-nodeDeadHeartbeatThreshold),
		},
	).Scan(&nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select worker with sandbox capacity: %w", err)
	}
	return &nodeID, nil
}

// SandboxCapacitySummary returns best-effort aggregate sandbox capacity from
// fresh worker heartbeat metadata.
// lint:allow-no-orgid reason="cross-org worker capacity summary for speculative prewarm classification"
func (s *JobStore) SandboxCapacitySummary(ctx context.Context) (SandboxCapacitySummary, error) {
	var summary SandboxCapacitySummary
	err := s.db.QueryRow(ctx, `
		WITH fresh_workers AS (
			SELECT
				COALESCE(NULLIF(metadata->>'live_sandbox_count', '')::int, 0) AS live_sandboxes,
				COALESCE(NULLIF(metadata->>'reserved_sandbox_count', '')::int, 0) AS reserved_sandboxes,
				COALESCE(NULLIF(metadata->>'max_active_sandboxes', '')::int, 0) AS max_active_sandboxes
			FROM nodes
			WHERE mode IN ('worker', 'all')
			  AND status = 'active'
			  AND last_heartbeat_at >= @dead_before
			  AND COALESCE(metadata->>'live_sandbox_count_error', '') = ''
		)
		SELECT
			COUNT(*)::int AS fresh_workers,
			COUNT(*) FILTER (
				WHERE max_active_sandboxes > 0
				  AND live_sandboxes + reserved_sandboxes + 2 <= max_active_sandboxes
			)::int AS workers_with_slots,
			COALESCE(SUM(live_sandboxes), 0)::int AS live_sandboxes,
			COALESCE(SUM(reserved_sandboxes), 0)::int AS reserved_sandboxes,
			COALESCE(SUM(max_active_sandboxes), 0)::int AS max_sandboxes
		FROM fresh_workers`,
		pgx.NamedArgs{"dead_before": time.Now().Add(-nodeDeadHeartbeatThreshold)},
	).Scan(&summary.FreshWorkers, &summary.WorkersWithSlots, &summary.LiveSandboxes, &summary.ReservedSandboxes, &summary.MaxSandboxes)
	if err != nil {
		return SandboxCapacitySummary{}, fmt.Errorf("sandbox capacity summary: %w", err)
	}
	return summary, nil
}

func (s *JobStore) execLeaseTerminalUpdate(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	execer, ok := s.db.(jobExecer)
	if !ok {
		return pgconn.CommandTag{}, fmt.Errorf("job store db does not support Exec")
	}
	return execer.Exec(ctx, query, args...)
}
