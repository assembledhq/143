package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
)

// NodeStore manages reads from the cluster nodes table.
type NodeStore struct {
	db DBTX
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
