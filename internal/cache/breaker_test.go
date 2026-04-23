package cache

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreaker_OpensOnWindowErrorRate(t *testing.T) {
	t.Parallel()

	cb := NewCircuitBreaker(zerolog.Nop())
	for i := 0; i < 10; i++ {
		cb.RecordFailure()
		cb.RecordSuccess()
	}
	cb.RecordFailure()
	require.Equal(t, breakerStateOpen, cb.State(), "breaker should open when the failure rate crosses the threshold with enough samples")
}

func TestCircuitBreaker_HalfOpenProbeClosesOnSuccess(t *testing.T) {
	t.Parallel()

	cb := NewCircuitBreaker(zerolog.Nop())
	cb.cooldown = time.Millisecond
	cb.ForceOpen()
	time.Sleep(2 * time.Millisecond)

	require.True(t, cb.Allow(), "breaker should allow a single half-open probe after cooldown")
	cb.RecordSuccess()
	require.Equal(t, breakerStateClosed, cb.State(), "successful half-open probe should close the breaker")
}

func TestCircuitBreaker_ReadyDoesNotAdvanceOpenBreaker(t *testing.T) {
	t.Parallel()

	cb := NewCircuitBreaker(zerolog.Nop())
	cb.cooldown = time.Millisecond
	cb.ForceOpen()
	time.Sleep(2 * time.Millisecond)

	require.False(t, cb.Ready(), "ready checks should not consume the half-open recovery probe")
	require.Equal(t, breakerStateOpen, cb.State(), "ready checks should leave the breaker open until a command path probes it")
	require.True(t, cb.Allow(), "the next command probe should still be allowed after cooldown")
}

func TestClientAvailable_UsesPingProbeToRecoverBreaker(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	client.breaker.cooldown = time.Millisecond
	client.breaker.ForceOpen()
	time.Sleep(2 * time.Millisecond)

	require.True(t, client.Available(), "availability checks should use a bounded ping probe to recover after cooldown")
	require.Equal(t, breakerStateClosed, client.breaker.State(), "successful availability probes should close the breaker")
	require.True(t, client.Healthy(context.Background()), "client should still report Redis healthy after the breaker recovery probe")
}
