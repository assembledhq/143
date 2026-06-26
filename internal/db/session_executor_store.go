package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type SessionExecutorStore struct {
	db DBTX
}

func NewSessionExecutorStore(db DBTX) *SessionExecutorStore {
	return &SessionExecutorStore{db: db}
}

const sessionExecutorColumns = `id, org_id, session_id, thread_id, job_id, job_type,
	host_node_id, owner_id, lock_token, status, image, build_sha, heartbeat_at,
	lease_expires_at, runtime_deadline_at, drain_intent, drain_requested_at,
	drain_deadline_at, started_at, completed_at, exit_code, last_error, created_at, updated_at`

func (s *SessionExecutorStore) ClearPreHandoffReservation(ctx context.Context, orgID, sessionID, jobID uuid.UUID) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE session_executors se
		SET status = 'failed',
			completed_at = now(),
			exit_code = 1,
			last_error = 'executor launch or handoff did not complete; cleared stale pre-handoff reservation before retry',
			updated_at = now()
		FROM jobs j
		WHERE se.org_id = $1
		  AND se.session_id = $2
		  AND se.job_id = $3
		  AND se.status = 'starting'
		  AND j.org_id = se.org_id
		  AND j.id = se.job_id
		  AND j.status = 'running'
		  AND j.owner_kind = 'worker'`, orgID, sessionID, jobID)
	if err != nil {
		return 0, fmt.Errorf("clear pre-handoff session executor reservation: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *SessionExecutorStore) CreateStarting(ctx context.Context, orgID uuid.UUID, params models.CreateSessionExecutorParams) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		INSERT INTO session_executors (
			org_id, session_id, thread_id, job_id, job_type, host_node_id,
			owner_id, lock_token, status, image, build_sha, heartbeat_at,
			lease_expires_at, runtime_deadline_at
		)
		VALUES (
			@org_id, @session_id, @thread_id, @job_id, @job_type, @host_node_id,
			@owner_id, @lock_token, 'starting', @image, @build_sha, now(),
			now() + interval '60 seconds', @runtime_deadline_at
		)
		RETURNING id`, pgx.NamedArgs{
		"org_id":              orgID,
		"session_id":          params.SessionID,
		"thread_id":           params.ThreadID,
		"job_id":              params.JobID,
		"job_type":            params.JobType,
		"host_node_id":        params.HostNodeID,
		"owner_id":            params.OwnerID,
		"lock_token":          params.LockToken,
		"image":               params.Image,
		"build_sha":           params.BuildSHA,
		"runtime_deadline_at": params.RuntimeDeadlineAt,
	}).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create session executor: %w", err)
	}
	return id, nil
}

func (s *SessionExecutorStore) HeartbeatWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID, leaseDuration time.Duration) (bool, models.DrainIntent, error) {
	var drainIntent string
	err := s.db.QueryRow(ctx, `
		UPDATE session_executors
		SET heartbeat_at = now(),
			lease_expires_at = now() + ($4 * interval '1 second'),
			updated_at = now()
		WHERE org_id = $1
		  AND id = $2
		  AND lock_token = $3
		  AND status IN ('starting', 'running', 'draining')
		RETURNING drain_intent`, orgID, executorID, lockToken, int(leaseDuration.Seconds())).Scan(&drainIntent)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, models.DrainIntentNone, nil
	}
	if err != nil {
		return false, models.DrainIntentNone, fmt.Errorf("heartbeat session executor: %w", err)
	}
	intent := models.DrainIntent(drainIntent)
	if intent == "" {
		intent = models.DrainIntentNone
	}
	return true, intent, nil
}

// GetByID loads an executor by its globally unique id during executor boot,
// before the process knows the org_id to use for subsequent fenced writes.
// lint:allow-no-orgid reason="executor boot lookup by globally unique executor id before org scope is known"
func (s *SessionExecutorStore) GetByID(ctx context.Context, executorID uuid.UUID) (models.SessionExecutor, error) {
	executor, err := scanSessionExecutorRow(s.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT %s
		FROM session_executors
		WHERE id = $1`, sessionExecutorColumns), executorID))
	if err != nil {
		return models.SessionExecutor{}, fmt.Errorf("get session executor: %w", err)
	}
	return executor, nil
}

func scanSessionExecutorRow(row pgx.Row) (models.SessionExecutor, error) {
	var executor models.SessionExecutor
	var threadID pgtype.UUID
	var status string
	var heartbeatAt pgtype.Timestamptz
	var leaseExpiresAt pgtype.Timestamptz
	var runtimeDeadlineAt pgtype.Timestamptz
	var drainIntent string
	var drainRequestedAt pgtype.Timestamptz
	var drainDeadlineAt pgtype.Timestamptz
	var completedAt pgtype.Timestamptz
	var exitCode pgtype.Int4
	var lastError pgtype.Text
	if err := row.Scan(
		&executor.ID,
		&executor.OrgID,
		&executor.SessionID,
		&threadID,
		&executor.JobID,
		&executor.JobType,
		&executor.HostNodeID,
		&executor.OwnerID,
		&executor.LockToken,
		&status,
		&executor.Image,
		&executor.BuildSHA,
		&heartbeatAt,
		&leaseExpiresAt,
		&runtimeDeadlineAt,
		&drainIntent,
		&drainRequestedAt,
		&drainDeadlineAt,
		&executor.StartedAt,
		&completedAt,
		&exitCode,
		&lastError,
		&executor.CreatedAt,
		&executor.UpdatedAt,
	); err != nil {
		return models.SessionExecutor{}, err
	}
	if threadID.Valid {
		id := uuid.UUID(threadID.Bytes)
		executor.ThreadID = &id
	}
	executor.Status = models.SessionExecutorStatus(status)
	if heartbeatAt.Valid {
		executor.HeartbeatAt = &heartbeatAt.Time
	}
	if leaseExpiresAt.Valid {
		executor.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	if runtimeDeadlineAt.Valid {
		executor.RuntimeDeadlineAt = &runtimeDeadlineAt.Time
	}
	executor.DrainIntent = models.DrainIntent(drainIntent)
	if executor.DrainIntent == "" {
		executor.DrainIntent = models.DrainIntentNone
	}
	if drainRequestedAt.Valid {
		executor.DrainRequestedAt = &drainRequestedAt.Time
	}
	if drainDeadlineAt.Valid {
		executor.DrainDeadlineAt = &drainDeadlineAt.Time
	}
	if completedAt.Valid {
		executor.CompletedAt = &completedAt.Time
	}
	if exitCode.Valid {
		code := int(exitCode.Int32)
		executor.ExitCode = &code
	}
	if lastError.Valid {
		executor.LastError = &lastError.String
	}
	return executor, nil
}

func (s *SessionExecutorStore) MarkRunningWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID, leaseDuration time.Duration) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE session_executors
		SET status = 'running',
			heartbeat_at = now(),
			lease_expires_at = now() + ($4 * interval '1 second'),
			updated_at = now()
		WHERE org_id = $1
		  AND id = $2
		  AND lock_token = $3
		  AND status IN ('starting', 'running')`, orgID, executorID, lockToken, int(leaseDuration.Seconds()))
	if err != nil {
		return false, fmt.Errorf("mark session executor running: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *SessionExecutorStore) MarkDrainingWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE session_executors
		SET status = 'draining',
			drain_intent = 'host_maintenance',
			drain_requested_at = COALESCE(drain_requested_at, now()),
			updated_at = now()
		WHERE org_id = $1
		  AND id = $2
		  AND lock_token = $3
		  AND status IN ('starting', 'running')`, orgID, executorID, lockToken)
	if err != nil {
		return false, fmt.Errorf("mark session executor draining: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// MarkDeployBudgetExpiredByNode asks active executors on a draining worker to
// enter the deploy-budget-expired graceful checkpoint path.
// lint:allow-no-orgid reason="worker deploy budget expiry is node-scoped across all orgs"
func (s *SessionExecutorStore) MarkDeployBudgetExpiredByNode(ctx context.Context, nodeID string, now time.Time, graceWindow time.Duration) (int64, error) {
	if nodeID == "" {
		return 0, fmt.Errorf("node id is required")
	}
	graceSeconds := int(graceWindow.Seconds())
	if graceSeconds <= 0 {
		graceSeconds = 30
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE session_executors
		SET status = 'draining',
			drain_intent = 'deploy_budget_expired',
			drain_requested_at = COALESCE(drain_requested_at, @now),
			drain_deadline_at = @now + (@grace_seconds * interval '1 second'),
			updated_at = now()
		WHERE host_node_id = @node_id
		  AND status IN ('starting', 'running', 'draining')
		  AND drain_intent <> 'deploy_budget_expired'
		  AND runtime_deadline_at IS NOT NULL
		  AND runtime_deadline_at <= @now`,
		pgx.NamedArgs{
			"node_id":       nodeID,
			"now":           now.UTC(),
			"grace_seconds": graceSeconds,
		})
	if err != nil {
		return 0, fmt.Errorf("mark deploy budget expired executors: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *SessionExecutorStore) MarkHumanInputCheckpointByJob(ctx context.Context, orgID, jobID, lockToken uuid.UUID) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE session_executors se
		SET drain_intent = 'human_input_checkpoint',
			drain_requested_at = COALESCE(drain_requested_at, now()),
			updated_at = now()
		FROM sessions sess
		WHERE se.org_id = $1
		  AND se.job_id = $2
		  AND se.lock_token = $3
		  AND se.session_id = sess.id
		  AND sess.org_id = se.org_id
		  AND se.status IN ('starting', 'running', 'draining')
		  AND se.drain_intent IN ('none', 'planned_rollout')
		  AND (
			sess.status = 'awaiting_input'
			OR EXISTS (
				SELECT 1
				FROM session_threads th
				WHERE th.org_id = se.org_id
				  AND th.id = se.thread_id
				  AND th.status = 'awaiting_input'
			)
		  )`, orgID, jobID, lockToken)
	if err != nil {
		return false, fmt.Errorf("mark human input checkpoint executor: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *SessionExecutorStore) MarkTerminalWithLease(ctx context.Context, orgID, executorID, lockToken uuid.UUID, status models.SessionExecutorStatus, exitCode *int, lastError string) (bool, error) {
	if status != models.SessionExecutorStatusCompleted && status != models.SessionExecutorStatusRequeued && status != models.SessionExecutorStatusFailed && status != models.SessionExecutorStatusLost {
		return false, fmt.Errorf("terminal session executor status required: %s", status)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE session_executors
		SET status = $4,
			completed_at = now(),
			exit_code = $5,
			last_error = NULLIF($6, ''),
			updated_at = now()
		WHERE org_id = $1
		  AND id = $2
		  AND lock_token = $3
		  AND status IN ('starting', 'running', 'draining')`, orgID, executorID, lockToken, status, exitCode, lastError)
	if err != nil {
		return false, fmt.Errorf("mark session executor terminal: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReclaimLost marks stale executors lost and requeues their running jobs.
// lint:allow-no-orgid reason="recovery loop scans stale executor leases across orgs by design"
func (s *SessionExecutorStore) ReclaimLost(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
	var reclaimed int64
	err := s.db.QueryRow(ctx, `
		WITH stale_active AS (
			SELECT se.id, se.org_id, se.job_id, se.lock_token, j.owner_kind, j.status AS job_status
			FROM session_executors se
			JOIN jobs j
			  ON j.org_id = se.org_id
			 AND j.id = se.job_id
			WHERE se.status IN ('starting', 'running', 'draining')
			  AND se.heartbeat_at < $1
			  AND NOT EXISTS (
				SELECT 1
				FROM thread_runtimes tr
				WHERE tr.org_id = se.org_id
				  AND tr.thread_id = se.thread_id
				  AND tr.status IN ('starting', 'live', 'paused', 'draining')
				  AND tr.lease_expires_at > now()
			  )
			  AND (
				(j.status = 'running' AND j.owner_kind = 'session_executor' AND j.lock_token = se.lock_token AND j.lease_expires_at < now())
				OR
				(se.status = 'starting' AND j.owner_kind = 'worker' AND (j.lock_token IS NULL OR j.lock_token = se.lock_token))
				OR
				(j.status IN ('succeeded', 'failed', 'cancelled', 'skipped', 'dead_letter') AND (j.lock_token IS NULL OR j.lock_token = se.lock_token))
			  )
			ORDER BY se.heartbeat_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		),
		pre_handoff_orphans AS (
			UPDATE session_executors se
			SET status = 'failed',
				completed_at = now(),
				exit_code = 1,
				last_error = 'executor launch or handoff did not complete; cleared stale pre-handoff reservation',
				updated_at = now()
			FROM stale_active stale
			WHERE se.id = stale.id
			  AND (
				stale.owner_kind = 'worker'
				OR stale.job_status IN ('succeeded', 'failed', 'cancelled', 'skipped', 'dead_letter')
			  )
			RETURNING se.id
		),
		lost_executors AS (
			UPDATE session_executors se
			SET status = 'lost',
				completed_at = now(),
				last_error = 'executor heartbeat lost; queued for bounded recovery',
				updated_at = now()
			FROM stale_active stale
			WHERE se.id = stale.id
			  AND stale.owner_kind = 'session_executor'
			  AND stale.job_status = 'running'
			RETURNING stale.org_id, se.session_id, se.thread_id, stale.job_id, stale.lock_token
		),
		lost_thread_runtimes AS (
			UPDATE thread_runtimes tr
			SET status = 'lost',
				closed_at = COALESCE(closed_at, now()),
				stop_reason = COALESCE(stop_reason, 'executor_lease_lost'),
				last_error = COALESCE(last_error, 'session executor lease was lost before runtime closed'),
				updated_at = now()
			FROM lost_executors lost
			WHERE lost.thread_id IS NOT NULL
			  AND tr.org_id = lost.org_id
			  AND tr.session_id = lost.session_id
			  AND tr.thread_id = lost.thread_id
			  AND tr.status IN ('starting', 'live', 'paused', 'draining')
			  AND (tr.lease_expires_at IS NULL OR tr.lease_expires_at <= now())
			RETURNING tr.id, tr.org_id, tr.session_id
		),
		lost_runtime_holders AS (
			UPDATE session_sandbox_holders h
			SET status = 'expired',
				released_at = COALESCE(released_at, now()),
				updated_at = now()
			FROM lost_thread_runtimes lost
			WHERE h.org_id = lost.org_id
			  AND h.session_id = lost.session_id
			  AND h.holder_kind = 'thread_runtime'
			  AND h.holder_id = lost.id
			  AND h.status IN ('active', 'draining')
			RETURNING h.id
		),
		reset_inbox AS (
			UPDATE thread_inbox_entries e
			SET delivery_state = 'pending',
				runtime_id = NULL,
				owner_node_id = NULL,
				last_error = COALESCE(last_error, 'session executor lease lost before ack'),
				updated_at = now()
			FROM lost_thread_runtimes lost
			WHERE e.org_id = lost.org_id
			  AND e.runtime_id = lost.id
			  AND e.delivery_state = 'delivering'
			RETURNING e.id
		),
		unknown_inbox AS (
			UPDATE thread_inbox_entries e
			SET delivery_state = 'unknown_delivery',
				last_error = COALESCE(last_error, 'session executor lease lost after live delivery before ack'),
				updated_at = now()
			FROM lost_thread_runtimes lost
			WHERE e.org_id = lost.org_id
			  AND e.runtime_id = lost.id
			  AND e.delivery_state = 'delivered'
			RETURNING e.id
		),
		updated_jobs AS (
			UPDATE jobs j
			SET status = 'pending',
				last_error = 'session executor ownership lost; queued for bounded recovery',
				locked_by_node_id = NULL,
				run_owner_id = NULL,
				owner_kind = 'worker',
				lock_token = NULL,
				locked_at = NULL,
				lease_expires_at = NULL,
				run_at = now(),
				updated_at = now()
			FROM lost_executors lost
			WHERE j.org_id = lost.org_id
			  AND j.id = lost.job_id
			  AND j.lock_token = lost.lock_token
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
		SELECT
			(SELECT COUNT(*) FROM lost_executors)
			+ (SELECT COUNT(*) FROM pre_handoff_orphans)`, staleBefore, limit).Scan(&reclaimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reclaim lost session executors: %w", err)
	}
	return reclaimed, nil
}

// ReclaimLostForSession is the targeted version of ReclaimLost used on user
// input paths to recover one session's stale executor ownership immediately.
func (s *SessionExecutorStore) ReclaimLostForSession(ctx context.Context, orgID, sessionID uuid.UUID, staleBefore time.Time, limit int) (int64, error) {
	var reclaimed int64
	err := s.db.QueryRow(ctx, `
		WITH stale_active AS (
			SELECT se.id, se.org_id, se.job_id, se.lock_token, j.owner_kind, j.status AS job_status
			FROM session_executors se
			JOIN jobs j
			  ON j.org_id = se.org_id
			 AND j.id = se.job_id
			WHERE se.org_id = $1
			  AND se.session_id = $2
			  AND se.status IN ('starting', 'running', 'draining')
			  AND se.heartbeat_at < $3
			  AND (
				(j.status = 'running' AND j.owner_kind = 'session_executor' AND j.lock_token = se.lock_token AND j.lease_expires_at < now())
				OR
				(se.status = 'starting' AND j.owner_kind = 'worker' AND (j.lock_token IS NULL OR j.lock_token = se.lock_token))
				OR
				(j.status IN ('succeeded', 'failed', 'cancelled', 'skipped', 'dead_letter') AND (j.lock_token IS NULL OR j.lock_token = se.lock_token))
			  )
			ORDER BY se.heartbeat_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $4
		),
		pre_handoff_orphans AS (
			UPDATE session_executors se
			SET status = 'failed',
				completed_at = now(),
				exit_code = 1,
				last_error = 'executor launch or handoff did not complete; cleared stale pre-handoff reservation',
				updated_at = now()
			FROM stale_active stale
			WHERE se.id = stale.id
			  AND (
				stale.owner_kind = 'worker'
				OR stale.job_status IN ('succeeded', 'failed', 'cancelled', 'skipped', 'dead_letter')
			  )
			RETURNING se.id
		),
		lost_executors AS (
			UPDATE session_executors se
			SET status = 'lost',
				completed_at = now(),
				last_error = 'executor heartbeat lost; queued for bounded recovery',
				updated_at = now()
			FROM stale_active stale
			WHERE se.id = stale.id
			  AND stale.owner_kind = 'session_executor'
			  AND stale.job_status = 'running'
			RETURNING stale.org_id, stale.job_id, stale.lock_token
		),
		updated_jobs AS (
			UPDATE jobs j
			SET status = 'pending',
				last_error = 'session executor ownership lost; queued for bounded recovery',
				locked_by_node_id = NULL,
				run_owner_id = NULL,
				owner_kind = 'worker',
				lock_token = NULL,
				locked_at = NULL,
				lease_expires_at = NULL,
				run_at = now(),
				updated_at = now()
			FROM lost_executors lost
			WHERE j.org_id = lost.org_id
			  AND j.id = lost.job_id
			  AND j.lock_token = lost.lock_token
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
		SELECT
			(SELECT COUNT(*) FROM lost_executors)
			+ (SELECT COUNT(*) FROM pre_handoff_orphans)`, orgID, sessionID, staleBefore, limit).Scan(&reclaimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reclaim lost session executors for session: %w", err)
	}
	return reclaimed, nil
}
