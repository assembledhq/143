package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
)

// ListHostSandboxReferences includes all worker generations and lifecycle
// states. The host-local observer records physical absence without changing
// any tenant's execution, recovery, retention, or publication ownership.
// lint:allow-no-orgid reason="host-local physical presence observer inspects sandbox references across all tenants and worker generations"
func (s *SessionStore) ListHostSandboxReferences(ctx context.Context, physicalHostID string) ([]models.SandboxContainerReference, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT s.container_id, s.worker_node_id AS owner_node_id
 FROM sessions s JOIN nodes n ON n.id=s.worker_node_id
 WHERE s.container_id IS NOT NULL AND n.metadata->>'sandbox_host_id'=$1
 ORDER BY s.container_id,s.worker_node_id`, physicalHostID)
	if err != nil {
		return nil, fmt.Errorf("list host sandbox references: %w", err)
	}
	references, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.SandboxContainerReference])
	if err != nil {
		return nil, fmt.Errorf("collect host sandbox references: %w", err)
	}
	return references, nil
}
