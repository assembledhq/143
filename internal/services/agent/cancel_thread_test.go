package agent

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const (
	testWaitTimeout  = 2 * time.Second
	testPollInterval = 10 * time.Millisecond
)

func TestThreadCancelRegistry_CancelThreadUsesContextCancelOnly(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	cancelled := false
	registry := NewThreadCancelRegistry(zerolog.Nop())

	registry.Register(threadID, func() {
		cancelled = true
	})

	accepted := registry.CancelThread(threadID)
	require.True(t, accepted, "CancelThread should accept registered threads")

	require.Eventually(t, func() bool {
		return cancelled
	}, testWaitTimeout, testPollInterval, "CancelThread should unwind the run context")
}

func TestThreadCancelRegistry_DeregisterRemovesEntry(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() {})

	registry.Deregister(threadID)
	require.False(t, registry.CancelThread(threadID), "deregistered threads should not be cancellable")
}

func TestThreadCancelRegistry_CancelThreadIsIdempotent(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	calls := 0
	registry := NewThreadCancelRegistry(zerolog.Nop())
	registry.Register(threadID, func() { calls++ })

	require.True(t, registry.CancelThread(threadID))
	require.True(t, registry.CancelThread(threadID), "re-cancel of a still-registered thread is accepted")

	require.Eventually(t, func() bool { return calls == 1 }, testWaitTimeout, testPollInterval,
		"sync.Once must guarantee the cancel func fires exactly once")
}
