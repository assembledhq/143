package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/requestctx"
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

// QueueChangesetPRCreation atomically reserves a changeset's PR slot and
// inserts its open_pr job. A false queued result means another caller already
// owns the slot or PR creation has completed; it is not an error.
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
		return uuid.Nil, false, nil
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
	s.notify(ctx, jobID)
	return jobID, true, nil
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
	payloadJSON, err := json.Marshal(opts.Payload)
	if err != nil {
		return uuid.Nil, err
	}
	if mutationID := requestctx.MutationID(ctx); mutationID != uuid.Nil {
		var object map[string]any
		if json.Unmarshal(payloadJSON, &object) == nil && object != nil {
			object["client_mutation_id"] = mutationID.String()
			payloadJSON, err = json.Marshal(object)
			if err != nil {
				return uuid.Nil, fmt.Errorf("marshal job causation: %w", err)
			}
		}
	}

	var id uuid.UUID
	args := pgx.NamedArgs{
		"org_id":         orgID,
		"queue":          opts.Queue,
		"job_type":       opts.JobType,
		"payload":        payloadJSON,
		"priority":       opts.Priority,
		"dedupe_key":     opts.DedupeKey,
		"target_node_id": opts.TargetNodeID,
	}
	query := `
		INSERT INTO jobs (org_id, queue, job_type, payload, priority, dedupe_key, target_node_id)
		VALUES (@org_id, @queue, @job_type, @payload, @priority, @dedupe_key, @target_node_id)
		ON CONFLICT DO NOTHING
		RETURNING id`
	if opts.TargetNodeID == nil {
		query = `
			INSERT INTO jobs (org_id, queue, job_type, payload, priority, dedupe_key)
			VALUES (@org_id, @queue, @job_type, @payload, @priority, @dedupe_key)
			ON CONFLICT DO NOTHING
			RETURNING id`
		delete(args, "target_node_id")
	}

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
// Node-affinity filter: a job with target_node_id set is claimable only by
// that node, or by any active worker once the target is unavailable — dead
// (status='dead' or stale heartbeat) or draining. A draining node keeps
// heartbeating to hold its previews, so without the draining case a pinned
// turn starves until the node dies; the claimer hydrates from the snapshot.
// lint:allow-no-orgid reason="worker queue consumer scans cross-org jobs by design"
func (s *JobStore) ClaimNextRunnable(ctx context.Context, nodeID, ownerID string, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, error) {
	query := fmt.Sprintf(`
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
		),
		next_job AS (
			SELECT j.id
			FROM jobs j
			LEFT JOIN unavailable_target_nodes d ON d.id = j.target_node_id
			JOIN claiming_node cn ON TRUE
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
			owner_kind = 'worker',
			lock_token = @lock_token,
			locked_at = now(),
			lease_expires_at = now() + (@lease_seconds * interval '1 second'),
			attempts = attempts + 1,
			updated_at = now()
		FROM next_job
		WHERE j.id = next_job.id
		RETURNING %s`, claimedJobColumns)

	job, err := scanJobRow(s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"node_id":       nodeID,
		"owner_id":      ownerID,
		"lock_token":    lockToken,
		"lease_seconds": int(leaseDuration.Seconds()),
		"dead_before":   time.Now().Add(-nodeDeadHeartbeatThreshold),
	}))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim next runnable job: %w", err)
	}
	return job, nil
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
	var completedAt pgtype.Timestamptz
	err := row.Scan(
		&job.ID, &job.OrgID, &job.Queue, &job.JobType, &job.Payload, &job.Priority,
		&job.Status, &job.Attempts, &job.MaxAttempts, &job.RunAt, &lockedByNodeID,
		&lockedAt, &leaseExpiresAt, &persistedLockToken, &runOwnerID,
		&ownerKind, &lastError, &dedupeKey, &targetNodeID, &job.CreatedAt, &job.UpdatedAt, &completedAt,
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
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	return &job, nil
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
func (s *JobStore) HandoffToSessionExecutorWithLease(ctx context.Context, orgID, jobID, lockToken, executorID uuid.UUID) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE jobs
		SET owner_kind = 'session_executor',
			run_owner_id = $1,
			updated_at = now()
		WHERE org_id = $2
		  AND id = $3
		  AND status = 'running'
		  AND lock_token = $4`, executorID.String(), orgID, jobID, lockToken)
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
		        AND s.id::text = payload->>'session_id'
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
		        AND s.id::text = payload->>'session_id'
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
