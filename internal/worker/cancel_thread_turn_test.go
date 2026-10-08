package worker

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type turnCancellationOrchestrator struct {
	*orchestratorServiceStub
	registry *agent.ThreadCancelRegistry
}

func (o *turnCancellationOrchestrator) CancelThreadTurnByID(thread uuid.UUID, turn int) bool {
	return o.registry.CancelThreadTurn(thread, turn)
}

func TestCancelThreadHandlerExpectedTurn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		registeredTurn int
		cancelled      bool
	}{
		{"interrupt exact old turn", 4, true},
		{"delayed payload cannot interrupt later turn", 5, false},
		{"delayed payload cannot interrupt ordinary turn", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			thread := uuid.New()
			var cancelled atomic.Bool
			registry := agent.NewThreadCancelRegistry(zerolog.Nop())
			registry.RegisterTurnWithSpec(thread, tt.registeredTurn, func() { cancelled.Store(true) }, agent.DefaultCancellationSpec)
			orchestrator := &turnCancellationOrchestrator{orchestratorServiceStub: &orchestratorServiceStub{}, registry: registry}
			payload, err := json.Marshal(map[string]any{"org_id": uuid.New().String(), "session_id": uuid.New().String(), "thread_id": thread.String(), "expected_turn": 4})
			require.NoError(t, err, "encode the delayed exact-turn interrupt")
			handler := newCancelThreadHandler(nil, &Services{Orchestrator: orchestrator}, zerolog.Nop())
			require.NoError(t, handler(context.Background(), "cancel_thread_turn", payload), "matching or obsolete interrupt delivery should settle successfully")
			require.Equal(t, 0, orchestrator.cancelThreadCalls, "queued exact-turn interrupts must never use generic cancellation")
			if tt.cancelled {
				require.Eventually(t, cancelled.Load, time.Second, time.Millisecond, "matching queued interrupt must reach its original run")
			} else {
				require.False(t, cancelled.Load(), "old payload must leave a later registered run untouched")
			}
		})
	}
}
