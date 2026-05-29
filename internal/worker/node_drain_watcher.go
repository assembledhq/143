package worker

import (
	"context"
	"errors"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type drainNodeGetter interface {
	GetByID(ctx context.Context, id string) (*models.Node, error)
}

// RunNodeDrainWatcher turns DB node drain state into local worker admission
// drain without shutting down owned runtime serving.
func RunNodeDrainWatcher(ctx context.Context, nodes drainNodeGetter, workers []*Worker, nodeID string, logger zerolog.Logger, interval time.Duration) {
	if nodes == nil || nodeID == "" || len(workers) == 0 {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if markWorkersDrainingFromDB(ctx, nodes, workers, nodeID, logger) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func markWorkersDrainingFromDB(ctx context.Context, nodes drainNodeGetter, workers []*Worker, nodeID string, logger zerolog.Logger) bool {
	node, err := nodes.GetByID(ctx, nodeID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Str("node_id", nodeID).Msg("failed to watch node drain state")
		}
		return false
	}
	if node.Status != models.NodeStatusDraining {
		return false
	}
	for _, w := range workers {
		if w != nil {
			w.RequestDrain()
		}
	}
	logger.Info().
		Str("node_id", nodeID).
		Str("drain_intent", string(node.DrainIntent)).
		Msg("worker admission drained from DB node state")
	return true
}
