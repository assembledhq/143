package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// NodeStore manages reads from the cluster nodes table.
type NodeStore struct {
	db DBTX
}

type WorkerHeartbeatHealth struct {
	ActiveWorkers             int64
	FreshWorkers              int64
	StaleWorkers              int64
	NewestHeartbeatAgeSeconds float64
}

type WorkerDeployStatus struct {
	NodeID                     string             `json:"node_id"`
	Host                       string             `json:"host"`
	Status                     models.NodeStatus  `json:"status"`
	DrainIntent                models.DrainIntent `json:"drain_intent"`
	LastHeartbeatAt            *time.Time         `json:"last_heartbeat_at,omitempty"`
	FreshHeartbeat             bool               `json:"fresh_heartbeat"`
	DrainRequestedAt           *time.Time         `json:"drain_requested_at,omitempty"`
	DrainBudgetExpiresAt       *time.Time         `json:"drain_budget_expires_at,omitempty"`
	ActiveExecutorCount        int64              `json:"active_executor_count"`
	MaxExecutorDeadlineAt      *time.Time         `json:"max_executor_deadline_at,omitempty"`
	ActivePreviewCount         int64              `json:"active_preview_count"`
	MaxPreviewLeaseExpiresAt   *time.Time         `json:"max_preview_lease_expires_at,omitempty"`
	OwnedRunningJobCount       int64              `json:"owned_running_job_count"`
	ActiveSessionHoldCount     int64              `json:"active_session_hold_count"`
	ActiveSandboxHolderCount   int64              `json:"active_sandbox_holder_count"`
	EndpointBlockerCount       int64              `json:"endpoint_blocker_count"`
	PendingSnapshotUploadCount int64              `json:"pending_snapshot_upload_count"`
	DetachedCleanupJobCount    int64              `json:"detached_cleanup_job_count"`
	RetireReady                bool               `json:"retire_ready"`
}

type WorkerDeployImpact struct {
	NodeID string                   `json:"node_id"`
	Items  []WorkerDeployImpactItem `json:"items"`
}

type WorkerDeployImpactItem struct {
	Kind        string     `json:"kind"`
	OrgID       uuid.UUID  `json:"org_id"`
	SessionID   *uuid.UUID `json:"session_id,omitempty"`
	ThreadID    *uuid.UUID `json:"thread_id,omitempty"`
	RuntimeID   uuid.UUID  `json:"runtime_id"`
	JobID       *uuid.UUID `json:"job_id,omitempty"`
	Status      string     `json:"status"`
	DeadlineAt  *time.Time `json:"deadline_at,omitempty"`
	EndpointURL string     `json:"endpoint_url,omitempty"`
	Reason      string     `json:"reason,omitempty"`
}

type MarkNodeDrainingParams struct {
	NodeID          string
	Intent          models.DrainIntent
	DeployID        string
	Reason          string
	RequestedBy     string
	BudgetExpiresAt *time.Time
	BuildSHA        string
	Metadata        map[string]any
}

type RetainWorkerImagesParams struct {
	NodeID    string
	DeployID  string
	Reason    string
	ExpiresAt time.Time
}

func NewNodeStore(db DBTX) *NodeStore {
	return &NodeStore{db: db}
}

const nodeColumns = `id, mode, host, status, drain_intent, metadata, started_at, last_heartbeat_at,
	drain_requested_at, drain_budget_expires_at, drain_requested_by, drain_reason`

// GetByID returns a node by ID.
// lint:allow-no-orgid reason="nodes is a cluster-scoped table with no org_id"
func (s *NodeStore) GetByID(ctx context.Context, id string) (*models.Node, error) {
	rows, err := s.db.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM nodes WHERE id = @id`, nodeColumns),
		pgx.NamedArgs{"id": id},
	)
	if err != nil {
		return nil, fmt.Errorf("query node: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.Node])
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	return &row, nil
}

// GetLatestByHost returns the newest worker/all node registered for a host.
// lint:allow-no-orgid reason="nodes is a cluster-scoped table with no org_id"
func (s *NodeStore) GetLatestByHost(ctx context.Context, host string) (*models.Node, error) {
	rows, err := s.db.Query(ctx,
		fmt.Sprintf(`
			SELECT %s
			FROM nodes
			WHERE host = @host AND mode IN ('worker', 'all')
			ORDER BY started_at DESC, id DESC
			LIMIT 1`, nodeColumns),
		pgx.NamedArgs{"host": host},
	)
	if err != nil {
		return nil, fmt.Errorf("query node by host: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.Node])
	if err != nil {
		return nil, fmt.Errorf("get node by host: %w", err)
	}
	return &row, nil
}

// ListActive returns all active cluster nodes.
// lint:allow-no-orgid reason="nodes is a cluster-scoped table with no org_id"
func (s *NodeStore) ListActive(ctx context.Context) ([]models.Node, error) {
	rows, err := s.db.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM nodes WHERE status = 'active' ORDER BY id ASC`, nodeColumns),
	)
	if err != nil {
		return nil, fmt.Errorf("list active nodes: %w", err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.Node])
	if err != nil {
		return nil, fmt.Errorf("scan active nodes: %w", err)
	}
	return result, nil
}

// WorkerHeartbeatHealth returns aggregate worker heartbeat freshness for
// control-plane alerts.
// lint:allow-no-orgid reason="nodes is a cluster-scoped table with no org_id"
func (s *NodeStore) WorkerHeartbeatHealth(ctx context.Context, staleBefore time.Time) (WorkerHeartbeatHealth, error) {
	var health WorkerHeartbeatHealth
	err := s.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE mode IN ('worker', 'all') AND status = 'active') AS active_workers,
			COUNT(*) FILTER (WHERE mode IN ('worker', 'all') AND status = 'active' AND last_heartbeat_at >= @stale_before) AS fresh_workers,
			COUNT(*) FILTER (WHERE mode IN ('worker', 'all') AND status = 'active' AND last_heartbeat_at < @stale_before) AS stale_workers,
			COALESCE(EXTRACT(EPOCH FROM now() - MAX(last_heartbeat_at) FILTER (WHERE mode IN ('worker', 'all') AND status = 'active'))::double precision, 0) AS newest_heartbeat_age_seconds
		FROM nodes`,
		pgx.NamedArgs{"stale_before": staleBefore},
	).Scan(
		&health.ActiveWorkers,
		&health.FreshWorkers,
		&health.StaleWorkers,
		&health.NewestHeartbeatAgeSeconds,
	)
	if err != nil {
		return WorkerHeartbeatHealth{}, fmt.Errorf("worker heartbeat health: %w", err)
	}
	return health, nil
}

// MarkDraining records an admission drain for a node without requiring the
// worker process to receive SIGTERM.
// lint:allow-no-orgid reason="nodes is a cluster-scoped table with no org_id"
func (s *NodeStore) MarkDraining(ctx context.Context, params MarkNodeDrainingParams) error {
	if params.NodeID == "" {
		return fmt.Errorf("node id is required")
	}
	if params.Intent == "" {
		params.Intent = models.DrainIntentPlannedRollout
	}
	if err := params.Intent.Validate(); err != nil {
		return err
	}
	metadata := params.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	rawMetadata, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal worker deploy event metadata: %w", err)
	}

	tag, err := s.db.Exec(ctx, `
		UPDATE nodes
		SET status = 'draining',
			drain_intent = @intent,
			drain_requested_at = now(),
			drain_budget_expires_at = @budget_expires_at,
			drain_requested_by = @requested_by,
			drain_reason = @reason
		WHERE id = @node_id`,
		pgx.NamedArgs{
			"node_id":           params.NodeID,
			"intent":            params.Intent,
			"budget_expires_at": params.BudgetExpiresAt,
			"requested_by":      params.RequestedBy,
			"reason":            params.Reason,
		})
	if err != nil {
		return fmt.Errorf("mark node draining: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("mark node draining: node %q not found", params.NodeID)
	}

	if params.DeployID == "" {
		params.DeployID = uuid.NewString()
	}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO worker_deploy_events (
			deploy_id, node_id, host, build_sha, event_type, drain_intent,
			requested_by, reason, metadata
		)
		SELECT @deploy_id, id, COALESCE(host, ''), @build_sha, 'node_marked_draining',
			@intent, @requested_by, @reason, @metadata
		FROM nodes
		WHERE id = @node_id`,
		pgx.NamedArgs{
			"deploy_id":    params.DeployID,
			"node_id":      params.NodeID,
			"build_sha":    params.BuildSHA,
			"intent":       params.Intent,
			"requested_by": params.RequestedBy,
			"reason":       params.Reason,
			"metadata":     rawMetadata,
		}); err != nil {
		return fmt.Errorf("record worker deploy event: %w", err)
	}
	return nil
}

// WorkerDeployStatus returns a retire-safety summary for a worker node.
// lint:allow-no-orgid reason="worker deploy status is node-scoped across all orgs"
func (s *NodeStore) WorkerDeployStatus(ctx context.Context, nodeID string) (WorkerDeployStatus, error) {
	var status WorkerDeployStatus
	var nodeStatus string
	var drainIntent string
	var lastHeartbeatAt pgtype.Timestamptz
	var drainRequestedAt pgtype.Timestamptz
	var drainBudgetExpiresAt pgtype.Timestamptz
	var maxExecutorDeadlineAt pgtype.Timestamptz
	var maxPreviewLeaseExpiresAt pgtype.Timestamptz
	err := s.db.QueryRow(ctx, `
		WITH node_row AS (
			SELECT id, COALESCE(host, '') AS host, status, drain_intent,
				last_heartbeat_at, drain_requested_at, drain_budget_expires_at
			FROM nodes
			WHERE id = @node_id
		),
		executors AS (
			SELECT COUNT(*) AS active_count, MAX(runtime_deadline_at) AS max_deadline_at
			FROM session_executors
			WHERE host_node_id = @node_id
			  AND status IN ('starting', 'running', 'draining')
		),
		previews AS (
			SELECT COUNT(*) AS active_count, MAX(lease_expires_at) AS max_lease_expires_at
			FROM preview_runtimes
			WHERE worker_node_id = @node_id
			  AND status IN ('starting', 'ready', 'draining')
			  AND lease_expires_at > now()
		),
		jobs_owned AS (
			SELECT COUNT(*) AS running_count
			FROM jobs
			WHERE locked_by_node_id = @node_id
			  AND status = 'running'
		),
		session_holds AS (
			SELECT COUNT(*) AS active_count
			FROM sessions
			WHERE worker_node_id = @node_id
			  AND deleted_at IS NULL
			  AND (
				turn_holding_container = TRUE
				OR (container_id IS NOT NULL AND status IN ('pending', 'running', 'awaiting_input'))
			  )
		),
		sandbox_holders AS (
			SELECT COUNT(*) AS active_count
			FROM session_sandbox_holders
			WHERE owner_node_id = @node_id
			  AND status IN ('active', 'draining')
			  AND expires_at > now()
		),
		endpoint_blockers AS (
			SELECT COUNT(*) AS blocker_count
			FROM preview_runtimes pr
			JOIN node_row n ON pr.worker_node_id = n.id
			WHERE pr.status IN ('starting', 'ready', 'draining')
			  AND pr.lease_expires_at > now()
		),
		pending_snapshot_uploads AS (
			SELECT COUNT(*) AS active_count
			FROM sessions
			WHERE worker_node_id = @node_id
			  AND pending_snapshot_key IS NOT NULL
			  AND pending_snapshot_set_at IS NOT NULL
		),
		detached_cleanup_jobs AS (
			SELECT COUNT(*) AS active_count
			FROM jobs
			WHERE locked_by_node_id = @node_id
			  AND status = 'running'
			  AND job_type IN ('open_pr', 'push_pr_changes', 'create_branch', 'start_preview', 'stop_preview', 'cleanup_stale_sandbox')
		)
		SELECT
			n.id,
			n.host,
			n.status,
			n.drain_intent,
			n.last_heartbeat_at,
			n.drain_requested_at,
			n.drain_budget_expires_at,
			executors.active_count,
			executors.max_deadline_at,
			previews.active_count,
			previews.max_lease_expires_at,
			jobs_owned.running_count,
			session_holds.active_count,
			sandbox_holders.active_count,
			endpoint_blockers.blocker_count,
			pending_snapshot_uploads.active_count,
			detached_cleanup_jobs.active_count
		FROM node_row n
		CROSS JOIN executors
		CROSS JOIN previews
		CROSS JOIN jobs_owned
		CROSS JOIN session_holds
		CROSS JOIN sandbox_holders
		CROSS JOIN endpoint_blockers
		CROSS JOIN pending_snapshot_uploads
		CROSS JOIN detached_cleanup_jobs`,
		pgx.NamedArgs{"node_id": nodeID},
	).Scan(
		&status.NodeID,
		&status.Host,
		&nodeStatus,
		&drainIntent,
		&lastHeartbeatAt,
		&drainRequestedAt,
		&drainBudgetExpiresAt,
		&status.ActiveExecutorCount,
		&maxExecutorDeadlineAt,
		&status.ActivePreviewCount,
		&maxPreviewLeaseExpiresAt,
		&status.OwnedRunningJobCount,
		&status.ActiveSessionHoldCount,
		&status.ActiveSandboxHolderCount,
		&status.EndpointBlockerCount,
		&status.PendingSnapshotUploadCount,
		&status.DetachedCleanupJobCount,
	)
	if err != nil {
		return WorkerDeployStatus{}, fmt.Errorf("worker deploy status: %w", err)
	}
	status.Status = models.NodeStatus(nodeStatus)
	status.DrainIntent = models.DrainIntent(drainIntent)
	if lastHeartbeatAt.Valid {
		status.LastHeartbeatAt = &lastHeartbeatAt.Time
		status.FreshHeartbeat = time.Since(lastHeartbeatAt.Time) <= 60*time.Second
	}
	if drainRequestedAt.Valid {
		status.DrainRequestedAt = &drainRequestedAt.Time
	}
	if drainBudgetExpiresAt.Valid {
		status.DrainBudgetExpiresAt = &drainBudgetExpiresAt.Time
	}
	if maxExecutorDeadlineAt.Valid {
		status.MaxExecutorDeadlineAt = &maxExecutorDeadlineAt.Time
	}
	if maxPreviewLeaseExpiresAt.Valid {
		status.MaxPreviewLeaseExpiresAt = &maxPreviewLeaseExpiresAt.Time
	}
	status.RetireReady = status.ActiveExecutorCount == 0 &&
		status.ActivePreviewCount == 0 &&
		status.OwnedRunningJobCount == 0 &&
		status.ActiveSessionHoldCount == 0 &&
		status.ActiveSandboxHolderCount == 0 &&
		status.EndpointBlockerCount == 0 &&
		status.PendingSnapshotUploadCount == 0 &&
		status.DetachedCleanupJobCount == 0
	return status, nil
}

// WorkerDeployImpact returns runtime identities that a maintenance dry run or
// deploy status surface can show to operators.
// lint:allow-no-orgid reason="worker deploy impact is node-scoped across all orgs"
func (s *NodeStore) WorkerDeployImpact(ctx context.Context, nodeID string) (WorkerDeployImpact, error) {
	rows, err := s.db.Query(ctx, `
		WITH active_executors AS (
			SELECT
				'executor'::text AS kind,
				se.org_id,
				se.session_id,
				se.thread_id,
				se.id AS runtime_id,
				se.job_id,
				se.status::text AS status,
				COALESCE(ext.extend_until, se.runtime_deadline_at) AS deadline_at,
				''::text AS endpoint_url,
				se.drain_intent::text AS reason
			FROM session_executors se
			LEFT JOIN deploy_drain_extensions ext
			  ON ext.org_id = se.org_id
			 AND ext.session_id = se.session_id
			 AND (ext.thread_id IS NULL OR ext.thread_id = se.thread_id)
			 AND ext.active = true
			 AND ext.extend_until > now()
			WHERE se.host_node_id = @node_id
			  AND se.status IN ('starting', 'running', 'draining')
		),
		active_previews AS (
			SELECT
				'preview'::text AS kind,
				pr.org_id,
				pi.session_id,
				NULL::uuid AS thread_id,
				pr.id AS runtime_id,
				NULL::uuid AS job_id,
				pr.status::text AS status,
				pr.lease_expires_at AS deadline_at,
				pr.endpoint_url,
				pi.name AS reason
			FROM preview_runtimes pr
			JOIN preview_instances pi
			  ON pi.org_id = pr.org_id
			 AND pi.id = pr.preview_instance_id
			WHERE pr.worker_node_id = @node_id
			  AND pr.status IN ('starting', 'ready', 'draining')
			  AND pr.lease_expires_at > now()
		)
		SELECT kind, org_id, session_id, thread_id, runtime_id, job_id, status, deadline_at, endpoint_url, reason
		FROM active_executors
		UNION ALL
		SELECT kind, org_id, session_id, thread_id, runtime_id, job_id, status, deadline_at, endpoint_url, reason
		FROM active_previews
		ORDER BY kind, deadline_at NULLS LAST`,
		pgx.NamedArgs{"node_id": nodeID},
	)
	if err != nil {
		return WorkerDeployImpact{}, fmt.Errorf("query worker deploy impact: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (WorkerDeployImpactItem, error) {
		var item WorkerDeployImpactItem
		var sessionID pgtype.UUID
		var threadID pgtype.UUID
		var jobID pgtype.UUID
		var deadlineAt pgtype.Timestamptz
		if err := row.Scan(
			&item.Kind,
			&item.OrgID,
			&sessionID,
			&threadID,
			&item.RuntimeID,
			&jobID,
			&item.Status,
			&deadlineAt,
			&item.EndpointURL,
			&item.Reason,
		); err != nil {
			return WorkerDeployImpactItem{}, err
		}
		if sessionID.Valid {
			id := uuid.UUID(sessionID.Bytes)
			item.SessionID = &id
		}
		if threadID.Valid {
			id := uuid.UUID(threadID.Bytes)
			item.ThreadID = &id
		}
		if jobID.Valid {
			id := uuid.UUID(jobID.Bytes)
			item.JobID = &id
		}
		if deadlineAt.Valid {
			item.DeadlineAt = &deadlineAt.Time
		}
		return item, nil
	})
	if err != nil {
		return WorkerDeployImpact{}, fmt.Errorf("scan worker deploy impact: %w", err)
	}
	return WorkerDeployImpact{NodeID: nodeID, Items: items}, nil
}

// MigrationVersion returns the current golang-migrate schema version.
// lint:allow-no-orgid reason="schema_migrations is process-global deploy state"
func (s *NodeStore) MigrationVersion(ctx context.Context) (int, bool, error) {
	var version int
	var dirty bool
	if err := s.db.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &dirty); err != nil {
		return 0, false, fmt.Errorf("load schema migration version: %w", err)
	}
	return version, dirty, nil
}

// RetainActiveExecutorImages records active executor images that must remain
// available for running turns and rollback decisions before deploy pruning.
// lint:allow-no-orgid reason="worker image retention is cluster-scoped infrastructure state"
func (s *NodeStore) RetainActiveExecutorImages(ctx context.Context, params RetainWorkerImagesParams) (int64, error) {
	if params.NodeID == "" {
		return 0, fmt.Errorf("node id is required")
	}
	if params.ExpiresAt.IsZero() {
		params.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO worker_image_retention (
			image, build_sha, node_id, executor_id, deploy_id, reason, expires_at
		)
		SELECT DISTINCT image, build_sha, host_node_id, id, @deploy_id, @reason, CAST(@expires_at AS timestamptz)
		FROM session_executors
		WHERE host_node_id = @node_id
		  AND status IN ('starting', 'running', 'draining')
		  AND image <> ''`,
		pgx.NamedArgs{
			"node_id":    params.NodeID,
			"deploy_id":  params.DeployID,
			"reason":     params.Reason,
			"expires_at": pgtype.Timestamptz{Time: params.ExpiresAt.UTC(), Valid: true},
		})
	if err != nil {
		return 0, fmt.Errorf("retain active executor images: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ReleaseExpiredImageRetention marks expired image retention rows inactive.
// lint:allow-no-orgid reason="worker image retention is cluster-scoped infrastructure state"
func (s *NodeStore) ReleaseExpiredImageRetention(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE worker_image_retention
		SET active = false,
			released_at = COALESCE(released_at, @now)
		WHERE active = true
		  AND expires_at <= @now`,
		pgx.NamedArgs{"now": now.UTC()})
	if err != nil {
		return 0, fmt.Errorf("release expired worker image retention: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ExtendSessionDrain records an operator-approved per-session drain extension.
func (s *NodeStore) ExtendSessionDrain(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID, nodeID, deployID, requestedBy, reason string, extendUntil time.Time) error {
	if requestedBy == "" || reason == "" {
		return fmt.Errorf("requested_by and reason are required")
	}
	if extendUntil.IsZero() {
		return fmt.Errorf("extend_until is required")
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO deploy_drain_extensions (
			org_id, session_id, thread_id, node_id, deploy_id, requested_by, reason, extend_until
		)
		VALUES (@org_id, @session_id, @thread_id, @node_id, @deploy_id, @requested_by, @reason, @extend_until)`,
		pgx.NamedArgs{
			"org_id":       orgID,
			"session_id":   sessionID,
			"thread_id":    threadID,
			"node_id":      nodeID,
			"deploy_id":    deployID,
			"requested_by": requestedBy,
			"reason":       reason,
			"extend_until": extendUntil.UTC(),
		})
	if err != nil {
		return fmt.Errorf("extend session drain: %w", err)
	}
	return nil
}
