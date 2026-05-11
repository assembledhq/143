package agent_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

type fakeLiveSandboxCounter struct {
	count int
	err   error
	calls atomic.Int64
}

type contextWaitingLiveSandboxCounter struct {
	calls atomic.Int64
}

func (f *contextWaitingLiveSandboxCounter) CountLiveSandboxes(ctx context.Context) (int, error) {
	f.calls.Add(1)
	<-ctx.Done()
	return 0, ctx.Err()
}

type switchableBlockingLiveSandboxCounter struct {
	count   int
	block   atomic.Bool
	calls   atomic.Int64
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func newSwitchableBlockingLiveSandboxCounter(count int) *switchableBlockingLiveSandboxCounter {
	return &switchableBlockingLiveSandboxCounter{
		count:   count,
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
}

func (f *switchableBlockingLiveSandboxCounter) CountLiveSandboxes(ctx context.Context) (int, error) {
	f.calls.Add(1)
	if !f.block.Load() {
		return f.count, nil
	}
	f.once.Do(func() { close(f.started) })
	select {
	case <-f.unblock:
		return f.count, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (f *fakeLiveSandboxCounter) CountLiveSandboxes(context.Context) (int, error) {
	f.calls.Add(1)
	if f.err != nil {
		return 0, f.err
	}
	return f.count, nil
}

func TestSandboxCapacityGate_AcquireAllowsBelowCapacity(t *testing.T) {
	t.Parallel()

	counter := &fakeLiveSandboxCounter{count: 1}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   counter,
		MaxActive: 2,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})

	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{
		Purpose:   "agent_run",
		SessionID: "session-1",
		OrgID:     "org-1",
	})

	require.NoError(t, err, "Acquire should allow a sandbox below the configured live capacity")
	require.Equal(t, 1, gate.ReservedCount(), "Acquire should record one in-flight reservation")
	reservation.Release()
	require.Equal(t, 0, gate.ReservedCount(), "Release should drop the in-flight reservation")
	require.Equal(t, int64(1), counter.calls.Load(), "Acquire should count live local sandboxes once")
}

func TestSandboxCapacityGate_AcquireRejectsWhenFull(t *testing.T) {
	t.Parallel()

	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   &fakeLiveSandboxCounter{count: 2},
		MaxActive: 2,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})

	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})

	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "Acquire should reject when live sandboxes are already at capacity")
	require.Nil(t, reservation, "Acquire should not return a reservation when capacity is exhausted")
	require.Equal(t, 0, gate.ReservedCount(), "Rejected acquire should not leak a reservation")
}

func TestSandboxCapacityGate_AcquireRejectsOnCountFailure(t *testing.T) {
	t.Parallel()

	countErr := errors.New("docker unavailable")
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   &fakeLiveSandboxCounter{err: countErr},
		MaxActive: 2,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})

	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})

	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "Acquire should fail closed when live sandbox counting fails")
	require.ErrorIs(t, err, countErr, "Acquire should preserve the count failure for logs and debugging")
	require.Nil(t, reservation, "Acquire should not return a reservation when the live count is unknown")
}

func TestSandboxCapacityGate_AcquireUsesCountTimeout(t *testing.T) {
	t.Parallel()

	counter := &contextWaitingLiveSandboxCounter{}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:      counter,
		MaxActive:    2,
		CountTimeout: 20 * time.Millisecond,
		NodeID:       "worker-1",
		Logger:       zerolog.Nop(),
	})

	started := time.Now()
	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})

	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "Acquire should fail closed when live sandbox counting times out")
	require.ErrorIs(t, err, context.DeadlineExceeded, "Acquire should preserve the count timeout cause")
	require.Nil(t, reservation, "Acquire should not return a reservation when counting times out")
	require.Less(t, time.Since(started), 500*time.Millisecond, "Acquire should use the configured short count timeout instead of the caller's long-lived context")
	require.Equal(t, int64(1), counter.calls.Load(), "Acquire should invoke the live counter once")
}

func TestSandboxCapacityGate_ReleaseDoesNotWaitForBlockedCount(t *testing.T) {
	t.Parallel()

	counter := newSwitchableBlockingLiveSandboxCounter(0)
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:      counter,
		MaxActive:    2,
		CountTimeout: time.Second,
		NodeID:       "worker-1",
		Logger:       zerolog.Nop(),
	})
	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
	require.NoError(t, err, "first Acquire should reserve capacity")

	counter.block.Store(true)
	acquireDone := make(chan struct{})
	go func() {
		defer close(acquireDone)
		blockedReservation, acquireErr := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
		if acquireErr == nil && blockedReservation != nil {
			blockedReservation.Release()
		}
	}()
	<-counter.started

	releaseDone := make(chan struct{})
	go func() {
		defer close(releaseDone)
		reservation.Release()
	}()

	select {
	case <-releaseDone:
	case <-time.After(100 * time.Millisecond):
		close(counter.unblock)
		require.Fail(t, "Release should not block behind a live sandbox count")
	}

	close(counter.unblock)
	<-acquireDone
	require.Equal(t, 0, gate.ReservedCount(), "all reservations should be released after the blocked acquire completes")
}

func TestSandboxCapacityGate_ConcurrentAcquiresDoNotExceedCapacity(t *testing.T) {
	t.Parallel()

	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   &fakeLiveSandboxCounter{count: 0},
		MaxActive: 3,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})

	var wg sync.WaitGroup
	var successes atomic.Int64
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
			if err == nil {
				successes.Add(1)
				_ = reservation
			}
		}()
	}
	wg.Wait()

	require.Equal(t, int64(3), successes.Load(), "Concurrent Acquire calls should reserve at most the configured capacity")
	require.Equal(t, 3, gate.ReservedCount(), "Gate should retain the successful in-flight reservations")
}

func TestSandboxCapacityGate_ReleaseIsIdempotent(t *testing.T) {
	t.Parallel()

	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   &fakeLiveSandboxCounter{count: 0},
		MaxActive: 1,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})
	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
	require.NoError(t, err, "Acquire should reserve the only available slot")

	reservation.Release()
	reservation.Release()

	require.Equal(t, 0, gate.ReservedCount(), "Release should be safe to call more than once")
}
