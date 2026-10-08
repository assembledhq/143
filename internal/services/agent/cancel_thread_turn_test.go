package agent

import (
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestThreadCancelRegistryExactTurn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		registered int
		requested  int
		accepted   bool
	}{
		{"selected entry survives replacement", 1, 1, true},
		{"delayed interrupt cannot cancel newer turn", 2, 1, false},
		{"generic entry cannot inherit old interrupt", 0, 1, false},
		{"invalid turn is rejected", 1, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registry := NewThreadCancelRegistry(zerolog.Nop())
			threadID := uuid.New()
			var originalCancelled, replacementCancelled atomic.Bool
			registry.RegisterTurnWithSpec(threadID, tt.registered, func() { originalCancelled.Store(true) }, DefaultCancellationSpec)
			value, ok := registry.mu.Load(threadID)
			require.True(t, ok, "fixture must register the original cancellable entry")
			entry := value.(*threadCancelEntry)
			entry.mu.Lock()
			accepted := registry.CancelThreadTurn(threadID, tt.requested)
			// Hold doCancel before it captures the handle, then replace the live
			// registry entry exactly as a later turn would do.
			registry.RegisterTurnWithSpec(threadID, tt.registered+1, func() { replacementCancelled.Store(true) }, DefaultCancellationSpec)
			entry.mu.Unlock()
			require.Equal(t, tt.accepted, accepted, "interrupt admission must match the registered immutable turn")
			if accepted {
				require.Eventually(t, originalCancelled.Load, testWaitTimeout, testPollInterval, "selected interrupt must still target only the original entry")
			} else {
				require.False(t, originalCancelled.Load(), "mismatched turn cannot cancel the registered run")
			}
			require.False(t, replacementCancelled.Load(), "registry replacement must never receive the old interrupt")
		})
	}
}
