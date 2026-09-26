package worker

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/cluster"
	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type drainNodeStub struct{ node models.Node }

func (s drainNodeStub) GetByID(context.Context, string) (*models.Node, error) {
	return &s.node, nil
}

func TestNodeDrainWatcherHonorsDurableIntent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   models.NodeStatus
		intent   models.DrainIntent
		expected bool
	}{
		{"healthy worker", models.NodeStatusActive, models.DrainIntentNone, false},
		{"legacy worker without intent", models.NodeStatusActive, "", false},
		{"draining status", models.NodeStatusDraining, models.DrainIntentNone, true},
		{"active status with rollout intent", models.NodeStatusActive, models.DrainIntentPlannedRollout, true},
		{"dead status with maintenance intent", models.NodeStatusDead, models.DrainIntentHostMaintenance, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, second := &Worker{}, &Worker{}
			nodeManager := cluster.NewNodeManager(nil, zerolog.Nop(), "worker", "worker")
			nodes := drainNodeStub{models.Node{Status: tt.status, DrainIntent: tt.intent}}
			actual := markWorkersDrainingFromDB(context.Background(), nodes, nodeManager, []*Worker{first, nil, second}, "worker", zerolog.Nop())
			require.Equal(t, tt.expected, actual, "watcher should honor persisted drain intent even when heartbeat status changed")
			require.Equal(t, []bool{tt.expected, tt.expected}, []bool{first.IsDraining(), second.IsDraining()}, "all local queues should stop admitting jobs together")
			require.Equal(t, tt.expected, nodeManager.IsDraining(), "heartbeat state must latch with the queues without a database write")
		})
	}
}
