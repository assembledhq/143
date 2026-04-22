package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/rs/zerolog"
)

type NodeManager struct {
	pool   db.DBTX
	logger zerolog.Logger
	nodeID string
	mode   string

	mu               sync.RWMutex
	draining         bool
	metadataProvider func() map[string]any
}

func NewNodeManager(pool db.DBTX, logger zerolog.Logger, nodeID, mode string) *NodeManager {
	return &NodeManager{pool: pool, logger: logger, nodeID: nodeID, mode: mode}
}

func (n *NodeManager) Register(ctx context.Context, host string) error {
	metadata, err := n.buildMetadata(nil)
	if err != nil {
		return err
	}

	_, err = n.pool.Exec(ctx, `
		INSERT INTO nodes (id, mode, host, started_at, last_heartbeat_at, status, metadata)
		VALUES ($1, $2, $3, now(), now(), 'active', $4)
		ON CONFLICT (id) DO UPDATE SET
			mode = EXCLUDED.mode,
			host = EXCLUDED.host,
			started_at = now(),
			last_heartbeat_at = now(),
			status = 'active',
			metadata = EXCLUDED.metadata
	`, n.nodeID, n.mode, host, metadata)
	return err
}

func (n *NodeManager) StartHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := n.HeartbeatOnce(ctx); err != nil {
				n.logger.Error().Err(err).Msg("heartbeat failed")
			}
		}
	}
}

func (n *NodeManager) HeartbeatOnce(ctx context.Context) error {
	status := "active"
	if n.IsDraining() {
		status = "draining"
	}
	metadata, err := n.buildMetadata(nil)
	if err != nil {
		return err
	}
	_, err = n.pool.Exec(ctx, `
		UPDATE nodes
		SET last_heartbeat_at = now(),
			status = $2,
			metadata = $3
		WHERE id = $1
	`, n.nodeID, status, metadata)
	return err
}

func (n *NodeManager) SetMetadataProvider(fn func() map[string]any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.metadataProvider = fn
}

func (n *NodeManager) RequestDrain(ctx context.Context, requestedAt time.Time) error {
	n.mu.Lock()
	n.draining = true
	n.mu.Unlock()

	metadata, err := n.buildMetadata(map[string]any{
		"drain_requested_at": requestedAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}

	_, err = n.pool.Exec(ctx, `
		UPDATE nodes SET status = 'draining', metadata = $2 WHERE id = $1
	`, n.nodeID, metadata)
	if err != nil {
		return fmt.Errorf("request node drain: %w", err)
	}
	return nil
}

func (n *NodeManager) IsDraining() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.draining
}

// MarkStaleNodesDead updates nodes that have stopped heartbeating so recovery
// loops and operators have a concrete state to inspect.
func (n *NodeManager) MarkStaleNodesDead(ctx context.Context, staleBefore time.Time) (int64, error) {
	tag, err := n.pool.Exec(ctx, `
		UPDATE nodes SET status = 'dead'
		WHERE status <> 'dead' AND last_heartbeat_at < $1
	`, staleBefore)
	if err != nil {
		return 0, fmt.Errorf("mark stale nodes dead: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (n *NodeManager) buildMetadata(extra map[string]any) ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	metadata := map[string]any{}
	if n.metadataProvider != nil {
		for key, value := range n.metadataProvider() {
			metadata[key] = value
		}
	}
	for key, value := range extra {
		metadata[key] = value
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal node metadata: %w", err)
	}
	return raw, nil
}
