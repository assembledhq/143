package cluster

import (
	"context"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/rs/zerolog"
)

type NodeManager struct {
	pool   db.DBTX
	logger zerolog.Logger
	nodeID string
	mode   string
}

func NewNodeManager(pool db.DBTX, logger zerolog.Logger, nodeID, mode string) *NodeManager {
	return &NodeManager{pool: pool, logger: logger, nodeID: nodeID, mode: mode}
}

func (n *NodeManager) Register(ctx context.Context, host string) error {
	_, err := n.pool.Exec(ctx, `
		INSERT INTO nodes (id, mode, host, started_at, last_heartbeat_at, status)
		VALUES ($1, $2, $3, now(), now(), 'active')
		ON CONFLICT (id) DO UPDATE SET
			mode = EXCLUDED.mode,
			host = EXCLUDED.host,
			started_at = now(),
			last_heartbeat_at = now(),
			status = 'active'
	`, n.nodeID, n.mode, host)
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
			_, err := n.pool.Exec(ctx, `
				UPDATE nodes SET last_heartbeat_at = now() WHERE id = $1
			`, n.nodeID)
			if err != nil {
				n.logger.Error().Err(err).Msg("heartbeat failed")
			}
		}
	}
}
