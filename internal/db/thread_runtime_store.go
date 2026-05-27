package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ThreadRuntimeStore struct {
	db DBTX
}

func NewThreadRuntimeStore(db DBTX) *ThreadRuntimeStore {
	return &ThreadRuntimeStore{db: db}
}

type CreateThreadRuntimeParams struct {
	SessionID                  uuid.UUID
	ThreadID                   uuid.UUID
	SandboxID                  uuid.UUID
	ContainerID                string
	RuntimeHandleID            string
	AgentType                  models.AgentType
	Model                      string
	OwnerNodeID                string
	LeaseToken                 uuid.UUID
	LastDeliveredSequence      int64
	LastAckedSequence          int64
	BaseWorkspaceGeneration    int64
	CurrentWorkspaceGeneration int64
	LeaseDuration              time.Duration
}

type ThreadRuntimeReclaimResult struct {
	LostRuntimes           int64
	ExpiredHolders         int64
	ResetInboxEntries      int64
	UnknownDeliveryEntries int64
}

const threadRuntimeColumns = `id, org_id, session_id, thread_id, sandbox_id, container_id,
	runtime_handle_id, agent_type, model, status, owner_node_id, lease_token,
	last_delivered_sequence, last_acked_sequence, base_workspace_generation,
	current_workspace_generation, started_at, heartbeat_at, lease_expires_at,
	closed_at, stop_reason, last_error, created_at, updated_at`

func (s *ThreadRuntimeStore) CreateStarting(ctx context.Context, orgID uuid.UUID, params CreateThreadRuntimeParams) (models.ThreadRuntime, error) {
	if err := params.AgentType.Validate(); err != nil {
		return models.ThreadRuntime{}, err
	}
	leaseSeconds := int(params.LeaseDuration.Seconds())
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}

	rows, err := s.db.Query(ctx, `
		WITH expired AS (
			UPDATE thread_runtimes
			SET status = 'lost',
				closed_at = COALESCE(closed_at, now()),
				stop_reason = COALESCE(stop_reason, 'lease_expired_before_restart'),
				updated_at = now()
			WHERE org_id = @org_id
			  AND thread_id = @thread_id
			  AND status IN ('starting', 'live', 'paused', 'draining')
			  AND (lease_expires_at IS NULL OR lease_expires_at <= now())
			RETURNING id, org_id, session_id
		), expired_holders AS (
			UPDATE session_sandbox_holders h
			SET status = 'expired',
				released_at = COALESCE(released_at, now()),
				updated_at = now()
			FROM expired e
			WHERE h.org_id = e.org_id
			  AND h.session_id = e.session_id
			  AND h.holder_kind = 'thread_runtime'
			  AND h.holder_id = e.id
			  AND h.status IN ('active', 'draining')
			RETURNING h.id
		), reset_inbox AS (
			UPDATE thread_inbox_entries inbox
			SET delivery_state = 'pending',
				runtime_id = NULL,
				owner_node_id = NULL,
				last_error = COALESCE(last_error, 'runtime lease expired before replacement runtime started'),
				updated_at = now()
			FROM expired e
			WHERE inbox.org_id = e.org_id
			  AND inbox.runtime_id = e.id
			  AND inbox.delivery_state = 'delivering'
			RETURNING inbox.id
		), unknown_inbox AS (
			UPDATE thread_inbox_entries inbox
			SET delivery_state = 'unknown_delivery',
				last_error = COALESCE(last_error, 'runtime lease expired after live delivery before replacement runtime started'),
				updated_at = now()
			FROM expired e
			WHERE inbox.org_id = e.org_id
			  AND inbox.runtime_id = e.id
			  AND inbox.delivery_state = 'delivered'
			RETURNING inbox.id
		)
		INSERT INTO thread_runtimes (
			org_id, session_id, thread_id, sandbox_id, container_id,
			runtime_handle_id, agent_type, model, status, owner_node_id,
			lease_token, last_delivered_sequence, last_acked_sequence,
			base_workspace_generation, current_workspace_generation,
			heartbeat_at, lease_expires_at
		)
		VALUES (
			@org_id, @session_id, @thread_id, @sandbox_id, @container_id,
			@runtime_handle_id, @agent_type, NULLIF(@model, ''), 'starting',
			@owner_node_id, @lease_token, @last_delivered_sequence,
			@last_acked_sequence, @base_workspace_generation,
			@current_workspace_generation, now(), now() + (@lease_seconds * interval '1 second')
		)
		RETURNING `+threadRuntimeColumns, pgx.NamedArgs{
		"org_id":                       orgID,
		"session_id":                   params.SessionID,
		"thread_id":                    params.ThreadID,
		"sandbox_id":                   params.SandboxID,
		"container_id":                 params.ContainerID,
		"runtime_handle_id":            params.RuntimeHandleID,
		"agent_type":                   params.AgentType,
		"model":                        params.Model,
		"owner_node_id":                params.OwnerNodeID,
		"lease_token":                  params.LeaseToken,
		"last_delivered_sequence":      params.LastDeliveredSequence,
		"last_acked_sequence":          params.LastAckedSequence,
		"base_workspace_generation":    params.BaseWorkspaceGeneration,
		"current_workspace_generation": params.CurrentWorkspaceGeneration,
		"lease_seconds":                leaseSeconds,
	})
	if err != nil {
		return models.ThreadRuntime{}, fmt.Errorf("create thread runtime: %w", err)
	}
	runtime, err := pgx.CollectOneRow(rows, scanThreadRuntimeRow)
	if err != nil {
		return models.ThreadRuntime{}, fmt.Errorf("create thread runtime: %w", err)
	}
	return runtime, nil
}

func scanThreadRuntimeRow(row pgx.CollectableRow) (models.ThreadRuntime, error) {
	var runtime models.ThreadRuntime
	var agentType string
	var status string
	var model sql.NullString
	var heartbeatAt pgtype.Timestamptz
	var leaseExpiresAt pgtype.Timestamptz
	var closedAt pgtype.Timestamptz
	var stopReason sql.NullString
	var lastError sql.NullString
	if err := row.Scan(
		&runtime.ID,
		&runtime.OrgID,
		&runtime.SessionID,
		&runtime.ThreadID,
		&runtime.SandboxID,
		&runtime.ContainerID,
		&runtime.RuntimeHandleID,
		&agentType,
		&model,
		&status,
		&runtime.OwnerNodeID,
		&runtime.LeaseToken,
		&runtime.LastDeliveredSequence,
		&runtime.LastAckedSequence,
		&runtime.BaseWorkspaceGeneration,
		&runtime.CurrentWorkspaceGeneration,
		&runtime.StartedAt,
		&heartbeatAt,
		&leaseExpiresAt,
		&closedAt,
		&stopReason,
		&lastError,
		&runtime.CreatedAt,
		&runtime.UpdatedAt,
	); err != nil {
		return models.ThreadRuntime{}, err
	}
	runtime.AgentType = models.AgentType(agentType)
	runtime.Status = models.ThreadRuntimeStatus(status)
	if model.Valid {
		runtime.Model = &model.String
	}
	if heartbeatAt.Valid {
		t := heartbeatAt.Time.UTC()
		runtime.HeartbeatAt = &t
	}
	if leaseExpiresAt.Valid {
		t := leaseExpiresAt.Time.UTC()
		runtime.LeaseExpiresAt = &t
	}
	if closedAt.Valid {
		t := closedAt.Time.UTC()
		runtime.ClosedAt = &t
	}
	if stopReason.Valid {
		runtime.StopReason = &stopReason.String
	}
	if lastError.Valid {
		runtime.LastError = &lastError.String
	}
	runtime.StartedAt = runtime.StartedAt.In(time.UTC)
	runtime.CreatedAt = runtime.CreatedAt.In(time.UTC)
	runtime.UpdatedAt = runtime.UpdatedAt.In(time.UTC)
	return runtime, nil
}

func (s *ThreadRuntimeStore) GetActiveByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadRuntime, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+threadRuntimeColumns+`
		FROM thread_runtimes
		WHERE org_id = $1
		  AND thread_id = $2
		  AND status IN ('starting', 'live', 'paused', 'draining')
		  AND lease_expires_at > now()
		ORDER BY started_at DESC
		LIMIT 1`, orgID, threadID)
	if err != nil {
		return models.ThreadRuntime{}, fmt.Errorf("get active thread runtime: %w", err)
	}
	runtime, err := pgx.CollectOneRow(rows, scanThreadRuntimeRow)
	if err != nil {
		return models.ThreadRuntime{}, fmt.Errorf("get active thread runtime: %w", err)
	}
	return runtime, nil
}

func (s *ThreadRuntimeStore) MarkLiveWithLease(ctx context.Context, orgID, runtimeID, leaseToken uuid.UUID, runtimeHandleID string, leaseDuration time.Duration) (bool, error) {
	leaseSeconds := int(leaseDuration.Seconds())
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE thread_runtimes
		SET status = 'live',
			runtime_handle_id = NULLIF($4, ''),
			heartbeat_at = now(),
			lease_expires_at = now() + ($5 * interval '1 second'),
			updated_at = now()
		WHERE org_id = $1
		  AND id = $2
		  AND lease_token = $3
		  AND status IN ('starting', 'live', 'paused')`, orgID, runtimeID, leaseToken, runtimeHandleID, leaseSeconds)
	if err != nil {
		return false, fmt.Errorf("mark thread runtime live: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *ThreadRuntimeStore) HeartbeatWithLease(ctx context.Context, orgID, runtimeID, leaseToken uuid.UUID, leaseDuration time.Duration) (bool, error) {
	leaseSeconds := int(leaseDuration.Seconds())
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE thread_runtimes
		SET heartbeat_at = now(),
			lease_expires_at = now() + ($4 * interval '1 second'),
			updated_at = now()
		WHERE org_id = $1
		  AND id = $2
		  AND lease_token = $3
		  AND status IN ('starting', 'live', 'paused', 'draining')`, orgID, runtimeID, leaseToken, leaseSeconds)
	if err != nil {
		return false, fmt.Errorf("heartbeat thread runtime: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *ThreadRuntimeStore) AdvanceDeliveryWithLease(ctx context.Context, orgID, runtimeID, leaseToken uuid.UUID, deliveredSequence, ackedSequence int64) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE thread_runtimes
		SET last_delivered_sequence = GREATEST(last_delivered_sequence, $4),
			last_acked_sequence = GREATEST(last_acked_sequence, $5),
			heartbeat_at = now(),
			updated_at = now()
		WHERE org_id = $1
		  AND id = $2
		  AND lease_token = $3
		  AND status IN ('starting', 'live', 'paused', 'draining')`, orgID, runtimeID, leaseToken, deliveredSequence, ackedSequence)
	if err != nil {
		return false, fmt.Errorf("advance thread runtime delivery: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *ThreadRuntimeStore) CommitInboxDeliveryWithLease(ctx context.Context, orgID, runtimeID, leaseToken, threadID uuid.UUID, ownerNodeID string, deliveredSequence, ackedSequence int64) (bool, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return false, fmt.Errorf("thread runtime store does not support transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin thread inbox delivery transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()

	tag, err := tx.Exec(ctx, `
		UPDATE thread_runtimes
		SET last_delivered_sequence = GREATEST(last_delivered_sequence, $4),
			last_acked_sequence = GREATEST(last_acked_sequence, $5),
			heartbeat_at = now(),
			updated_at = now()
		WHERE org_id = $1
		  AND id = $2
		  AND lease_token = $3
		  AND status IN ('starting', 'live', 'paused', 'draining')`, orgID, runtimeID, leaseToken, deliveredSequence, ackedSequence)
	if err != nil {
		return false, fmt.Errorf("advance thread runtime delivery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'acked',
			runtime_id = $3,
			owner_node_id = NULLIF($4, ''),
			delivered_at = COALESCE(delivered_at, now()),
			acked_at = COALESCE(acked_at, now()),
			updated_at = now()
		WHERE org_id = $1
		  AND thread_id = $2
		  AND sequence_no <= $5
		  AND runtime_id = $3
		  AND delivery_state = 'delivered'`, orgID, threadID, runtimeID, ownerNodeID, ackedSequence); err != nil {
		return false, fmt.Errorf("mark thread inbox delivery committed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit thread inbox delivery transaction: %w", err)
	}
	committed = true
	return true, nil
}

func (s *ThreadRuntimeStore) MarkTerminalWithLease(ctx context.Context, orgID, runtimeID, leaseToken uuid.UUID, status models.ThreadRuntimeStatus, stopReason, lastError string) (bool, error) {
	if status != models.ThreadRuntimeStatusClosed && status != models.ThreadRuntimeStatusFailed && status != models.ThreadRuntimeStatusLost {
		return false, fmt.Errorf("terminal thread runtime status required: %s", status)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE thread_runtimes
		SET status = $4,
			closed_at = now(),
			stop_reason = NULLIF($5, ''),
			last_error = NULLIF($6, ''),
			updated_at = now()
		WHERE org_id = $1
		  AND id = $2
		  AND lease_token = $3
		  AND status IN ('starting', 'live', 'paused', 'draining')`, orgID, runtimeID, leaseToken, status, stopReason, lastError)
	if err != nil {
		return false, fmt.Errorf("mark thread runtime terminal: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// lint:allow-no-orgid reason="system-wide runtime lease recovery scans expired runtime leases across orgs"
func (s *ThreadRuntimeStore) ReclaimExpiredLeases(ctx context.Context, expiredBefore time.Time, limit int) (ThreadRuntimeReclaimResult, error) {
	if limit <= 0 {
		limit = 100
	}
	var result ThreadRuntimeReclaimResult
	err := s.db.QueryRow(ctx, `
		WITH candidates AS (
			SELECT id, org_id, session_id
			FROM thread_runtimes
			WHERE status IN ('starting', 'live', 'paused', 'draining')
			  AND (lease_expires_at IS NULL OR lease_expires_at <= $1)
			ORDER BY lease_expires_at NULLS FIRST, started_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		), lost_runtimes AS (
			UPDATE thread_runtimes tr
			SET status = 'lost',
				closed_at = COALESCE(closed_at, now()),
				stop_reason = COALESCE(stop_reason, 'lease_expired'),
				last_error = COALESCE(last_error, 'runtime lease expired before terminal close'),
				updated_at = now()
			FROM candidates c
			WHERE tr.id = c.id
			  AND tr.org_id = c.org_id
			RETURNING tr.id, tr.org_id, tr.session_id
		), expired_holders AS (
			UPDATE session_sandbox_holders h
			SET status = 'expired',
				released_at = COALESCE(released_at, now()),
				updated_at = now()
			FROM lost_runtimes lr
			WHERE h.org_id = lr.org_id
			  AND h.session_id = lr.session_id
			  AND h.holder_kind = 'thread_runtime'
			  AND h.holder_id = lr.id
			  AND h.status IN ('active', 'draining')
			RETURNING h.id
		), reset_inbox AS (
			UPDATE thread_inbox_entries e
			SET delivery_state = 'pending',
				runtime_id = NULL,
				owner_node_id = NULL,
				last_error = COALESCE(last_error, 'runtime lease expired before ack'),
				updated_at = now()
			FROM lost_runtimes lr
			WHERE e.org_id = lr.org_id
			  AND e.runtime_id = lr.id
			  AND e.delivery_state = 'delivering'
			RETURNING e.id
		), unknown_inbox AS (
			UPDATE thread_inbox_entries e
			SET delivery_state = 'unknown_delivery',
				last_error = COALESCE(last_error, 'runtime lease expired after live delivery before ack'),
				updated_at = now()
			FROM lost_runtimes lr
			WHERE e.org_id = lr.org_id
			  AND e.runtime_id = lr.id
			  AND e.delivery_state = 'delivered'
			RETURNING e.id
		)
		SELECT
			(SELECT count(*) FROM lost_runtimes) AS lost_runtime_count,
			(SELECT count(*) FROM expired_holders) AS expired_holder_count,
			(SELECT count(*) FROM reset_inbox) AS reset_inbox_count,
			(SELECT count(*) FROM unknown_inbox) AS unknown_inbox_count`, expiredBefore, limit).Scan(
		&result.LostRuntimes,
		&result.ExpiredHolders,
		&result.ResetInboxEntries,
		&result.UnknownDeliveryEntries,
	)
	if err != nil {
		return ThreadRuntimeReclaimResult{}, fmt.Errorf("reclaim expired thread runtime leases: %w", err)
	}
	return result, nil
}
