package db

import (
	"context"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
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

func NewNodeStore(db DBTX) *NodeStore {
	return &NodeStore{db: db}
}

const nodeColumns = `id, mode, host, status, metadata, started_at, last_heartbeat_at`

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
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Node])
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
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
	result, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Node])
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
