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
	NodeID                   string             `json:"node_id"`
	Host                     string             `json:"host"`
	Status                   models.NodeStatus  `json:"status"`
	DrainIntent              models.DrainIntent `json:"drain_intent"`
	LastHeartbeatAt          *time.Time         `json:"last_heartbeat_at,omitempty"`
	FreshHeartbeat           bool               `json:"fresh_heartbeat"`
	DrainRequestedAt         *time.Time         `json:"drain_requested_at,omitempty"`
	DrainBudgetExpiresAt     *time.Time         `json:"drain_budget_expires_at,omitempty"`
	ActiveExecutorCount      int64              `json:"active_executor_count"`
	MaxExecutorDeadlineAt    *time.Time         `json:"max_executor_deadline_at,omitempty"`
	ActivePreviewCount       int64              `json:"active_preview_count"`
	MaxPreviewLeaseExpiresAt *time.Time         `json:"max_preview_lease_expires_at,omitempty"`
	OwnedRunningJobCount     int64              `json:"owned_running_job_count"`
	ActiveSessionHoldCount   int64              `json:"active_session_hold_count"`
	ActiveSandboxHolderCount int64              `json:"active_sandbox_holder_count"`
	EndpointBlockerCount     int64              `json:"endpoint_blocker_count"`
	RetireReady              bool               `json:"retire_ready"`
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
			endpoint_blockers.blocker_count
		FROM node_row n
		CROSS JOIN executors
		CROSS JOIN previews
		CROSS JOIN jobs_owned
		CROSS JOIN session_holds
		CROSS JOIN sandbox_holders
		CROSS JOIN endpoint_blockers`,
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
		status.EndpointBlockerCount == 0
	return status, nil
}
