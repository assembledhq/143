package agent_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type fakeLiveSandboxCounter struct {
	count int
	err   error
	calls atomic.Int64
}

type fakeSharedSandboxCapacityStore struct {
	reservationID uuid.UUID
	total         int
	acquired      bool
	err           error
	jobID         *uuid.UUID
	workloadClass models.SandboxWorkloadClass
	live          int
	effectiveMax  int
	reserveCalls  atomic.Int64
	releaseCalls  atomic.Int64
}

type contextWaitingSharedSandboxCapacityStore struct{}

func (contextWaitingSharedSandboxCapacityStore) ReserveSandboxCapacity(
	ctx context.Context,
	_ string,
	_ *uuid.UUID,
	_ models.SandboxWorkloadClass,
	_ func(context.Context) (int, error),
	_ int,
	_ time.Time,
) (uuid.UUID, int, int, bool, error) {
	<-ctx.Done()
	return uuid.Nil, 0, 0, false, ctx.Err()
}

func (contextWaitingSharedSandboxCapacityStore) ReleaseSandboxCapacity(context.Context, uuid.UUID) error {
	return nil
}

func (f *fakeSharedSandboxCapacityStore) ReserveSandboxCapacity(ctx context.Context, _ string, jobID *uuid.UUID, workloadClass models.SandboxWorkloadClass, countLiveSandboxes func(context.Context) (int, error), effectiveMax int, _ time.Time) (uuid.UUID, int, int, bool, error) {
	f.reserveCalls.Add(1)
	f.jobID = jobID
	f.workloadClass = workloadClass
	f.effectiveMax = effectiveMax
	if f.err != nil {
		return uuid.Nil, 0, f.total, false, f.err
	}
	liveSandboxes, err := countLiveSandboxes(ctx)
	if err != nil {
		return uuid.Nil, 0, f.total, false, err
	}
	f.live = liveSandboxes
	return f.reservationID, liveSandboxes, f.total, f.acquired, nil
}

func (f *fakeSharedSandboxCapacityStore) ReleaseSandboxCapacity(context.Context, uuid.UUID) error {
	f.releaseCalls.Add(1)
	return f.err
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

type staleDuringReleaseLiveSandboxCounter struct {
	calls         atomic.Int64
	secondStarted chan struct{}
	unblockSecond chan struct{}
}

func newStaleDuringReleaseLiveSandboxCounter() *staleDuringReleaseLiveSandboxCounter {
	return &staleDuringReleaseLiveSandboxCounter{
		secondStarted: make(chan struct{}),
		unblockSecond: make(chan struct{}),
	}
}

func (f *staleDuringReleaseLiveSandboxCounter) CountLiveSandboxes(ctx context.Context) (int, error) {
	call := f.calls.Add(1)
	switch call {
	case 1:
		return 0, nil
	case 2:
		close(f.secondStarted)
		select {
		case <-f.unblockSecond:
			return 0, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	default:
		return 1, nil
	}
}

func (f *fakeLiveSandboxCounter) CountLiveSandboxes(context.Context) (int, error) {
	f.calls.Add(1)
	if f.err != nil {
		return 0, f.err
	}
	return f.count, nil
}

type sequenceLiveSandboxCounter struct {
	counts []int
	calls  atomic.Int64
}

func (s *sequenceLiveSandboxCounter) CountLiveSandboxes(context.Context) (int, error) {
	call := int(s.calls.Add(1))
	if call <= len(s.counts) {
		return s.counts[call-1], nil
	}
	return s.counts[len(s.counts)-1], nil
}

type fakeSandboxPressureCleaner struct {
	err   error
	calls atomic.Int64
}

func (f *fakeSandboxPressureCleaner) ReapForCapacity(context.Context, time.Time) error {
	f.calls.Add(1)
	return f.err
}

type deadlineObservingSandboxPressureCleaner struct {
	calls       atomic.Int64
	sawDeadline atomic.Bool
}

func (f *deadlineObservingSandboxPressureCleaner) ReapForCapacity(ctx context.Context, _ time.Time) error {
	f.calls.Add(1)
	if _, ok := ctx.Deadline(); ok {
		f.sawDeadline.Store(true)
	}
	<-ctx.Done()
	return ctx.Err()
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

func TestSandboxCapacityGate_AcquireIncludesCrossProcessReservations(t *testing.T) {
	t.Parallel()

	jobID := uuid.New()
	shared := &fakeSharedSandboxCapacityStore{total: 3}
	cleaner := &fakeSandboxPressureCleaner{}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:            &fakeLiveSandboxCounter{count: 1},
		SharedReservations: shared,
		PressureCleaner:    cleaner,
		MaxActive:          3,
		NodeID:             "worker-1",
		Logger:             zerolog.Nop(),
	})

	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "continue_session", JobID: &jobID})

	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "other processes' reservations should participate in final local admission")
	require.Nil(t, reservation, "admission at shared capacity should not return a local reservation")
	require.Equal(t, &jobID, shared.jobID, "shared admission should identify the current job so its routing reservation is not double counted")
	require.Equal(t, int64(1), shared.reserveCalls.Load(), "final admission should use the shared reservation store once")
	require.Equal(t, int64(0), cleaner.calls.Load(), "shared reservations should not trigger physical pressure cleanup while Docker still has capacity")
}

func TestSandboxCapacityGate_AcquireReleasesSharedReservation(t *testing.T) {
	t.Parallel()

	shared := &fakeSharedSandboxCapacityStore{reservationID: uuid.New(), total: 1, acquired: true}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:            &fakeLiveSandboxCounter{},
		SharedReservations: shared,
		MaxActive:          2,
		NodeID:             "worker-1",
		Logger:             zerolog.Nop(),
	})

	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "branch_preview"})
	require.NoError(t, err, "capacity admission should persist a shared cross-process reservation")
	require.Equal(t, 1, gate.ReservedCount(), "shared admission should remain visible in local heartbeat metadata")
	require.Equal(t, int64(0), shared.releaseCalls.Load(), "shared reservation should remain active during sandbox creation")

	reservation.Release()
	require.Equal(t, 0, gate.ReservedCount(), "reservation release should clear local heartbeat state")
	require.Equal(t, int64(1), shared.releaseCalls.Load(), "reservation release should remove the shared capacity lease")
}

func TestSandboxCapacityGate_AcquireFailsClosedWhenSharedReservationFails(t *testing.T) {
	t.Parallel()

	shared := &fakeSharedSandboxCapacityStore{err: errors.New("database unavailable")}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:            &fakeLiveSandboxCounter{},
		SharedReservations: shared,
		MaxActive:          2,
		NodeID:             "worker-1",
		Logger:             zerolog.Nop(),
	})

	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "branch_preview"})

	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "shared reservation failures should fail closed")
	require.ErrorIs(t, err, agent.ErrSandboxCapacityCoordination, "shared reservation failures should be distinguishable from genuine saturation")
	require.ErrorContains(t, err, "database unavailable", "capacity error should retain the shared reservation failure")
	require.Nil(t, reservation, "failed shared reservations should not reserve local capacity")
}

func TestSandboxCapacityGate_SnapshotSeparatesSandboxTurnReservations(t *testing.T) {
	t.Parallel()

	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   &fakeLiveSandboxCounter{},
		MaxActive: 4,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})

	turnReservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "continue_session"})
	require.NoError(t, err, "sandbox turn should reserve local capacity")
	previewReservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "branch_preview"})
	require.NoError(t, err, "preview should reserve local capacity")

	snapshot := gate.Snapshot(context.Background())
	require.Equal(t, 2, snapshot.Reserved, "snapshot should include every local reservation")
	require.Equal(t, 1, snapshot.SandboxTurnReserved, "snapshot should identify only reservations that overlap durable sandbox-turn routing")

	turnReservation.Release()
	previewReservation.Release()
	released := gate.Snapshot(context.Background())
	require.Equal(t, 0, released.Reserved, "releasing reservations should clear the total count")
	require.Equal(t, 0, released.SandboxTurnReserved, "releasing a sandbox turn should clear the overlapping count")
}

func TestSandboxCapacityGate_AcquireReservesInteractiveCapacity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		maxActive           int
		interactiveReserved int
		live                int
		workloadClass       models.SandboxWorkloadClass
		expectAllowed       bool
		expectedReserve     int
	}{
		{name: "code review can use non-reserved capacity", maxActive: 4, interactiveReserved: 1, live: 2, workloadClass: models.SandboxWorkloadClassCodeReview, expectAllowed: true, expectedReserve: 1},
		{name: "code review cannot consume interactive reserve", maxActive: 4, interactiveReserved: 1, live: 3, workloadClass: models.SandboxWorkloadClassCodeReview},
		{name: "interactive work can consume reserved slot", maxActive: 4, interactiveReserved: 1, live: 3, workloadClass: models.SandboxWorkloadClassInteractive, expectAllowed: true, expectedReserve: 1},
		{name: "single-slot workers remain usable for code review", maxActive: 1, interactiveReserved: 1, live: 0, workloadClass: models.SandboxWorkloadClassCodeReview, expectAllowed: true, expectedReserve: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
				Counter:             &fakeLiveSandboxCounter{count: tt.live},
				MaxActive:           tt.maxActive,
				InteractiveReserved: tt.interactiveReserved,
				NodeID:              "worker-1",
				Logger:              zerolog.Nop(),
			})
			reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{
				Purpose:       "agent_run",
				WorkloadClass: tt.workloadClass,
			})

			if !tt.expectAllowed {
				require.ErrorIs(t, err, agent.ErrSandboxCapacity, "code-review admission should preserve capacity reserved for interactive work")
				require.Nil(t, reservation, "rejected code-review admission should not return a reservation")
				return
			}
			require.NoError(t, err, "workload should be admitted within its effective local capacity")
			require.NotNil(t, reservation, "successful admission should return a local slot reservation")
			require.Equal(t, tt.expectedReserve, gate.InteractiveReserved(), "interactive reserve should be clamped safely for the worker size")
			reservation.Release()
		})
	}
}

func TestSandboxCapacityGate_AcquireRunsPressureCleanupBeforeRejectingFullHost(t *testing.T) {
	t.Parallel()

	counter := &sequenceLiveSandboxCounter{counts: []int{2, 1}}
	cleaner := &fakeSandboxPressureCleaner{}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:         counter,
		PressureCleaner: cleaner,
		MaxActive:       2,
		NodeID:          "worker-1",
		Logger:          zerolog.Nop(),
	})

	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
	require.NoError(t, err, "Acquire should retry admission after pressure cleanup frees a sandbox slot")
	require.NotNil(t, reservation, "Acquire should return a reservation after pressure cleanup creates capacity")
	require.Equal(t, int64(1), cleaner.calls.Load(), "Acquire should run pressure cleanup once when the host initially appears full")
	require.Equal(t, int64(2), counter.calls.Load(), "Acquire should recount live sandboxes after pressure cleanup")
	reservation.Release()
}

func TestSandboxCapacityGate_AcquireSkipsPressureCleanupForInteractiveReserve(t *testing.T) {
	t.Parallel()

	counter := &fakeLiveSandboxCounter{count: 3}
	cleaner := &fakeSandboxPressureCleaner{}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:             counter,
		PressureCleaner:     cleaner,
		MaxActive:           4,
		InteractiveReserved: 1,
		NodeID:              "worker-1",
		Logger:              zerolog.Nop(),
	})

	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{
		Purpose:       "agent_run",
		WorkloadClass: models.SandboxWorkloadClassCodeReview,
	})
	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "code-review work should stop at the interactive reserve boundary")
	require.Nil(t, reservation, "reserve-only rejection should not return a local slot")
	require.Equal(t, int64(0), cleaner.calls.Load(), "interactive reserve pressure should not reap healthy sandboxes while the host still has physical capacity")
	require.Equal(t, int64(1), counter.calls.Load(), "reserve-only rejection should not recount after unnecessary cleanup")
}

func TestSandboxCapacityGate_AcquireRunsPressureCleanupAtMostOnce(t *testing.T) {
	t.Parallel()

	counter := &sequenceLiveSandboxCounter{counts: []int{2, 2}}
	cleaner := &fakeSandboxPressureCleaner{}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:         counter,
		PressureCleaner: cleaner,
		MaxActive:       2,
		NodeID:          "worker-1",
		Logger:          zerolog.Nop(),
	})

	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "Acquire should reject when pressure cleanup does not free capacity")
	require.Nil(t, reservation, "Acquire should not return a reservation when the host remains full after cleanup")
	require.Equal(t, int64(1), cleaner.calls.Load(), "Acquire should not loop pressure cleanup indefinitely")
	require.Equal(t, int64(2), counter.calls.Load(), "Acquire should recount exactly once after pressure cleanup")
}

func TestSandboxCapacityGate_AcquireBoundsPressureCleanupWithTimeout(t *testing.T) {
	t.Parallel()

	counter := &sequenceLiveSandboxCounter{counts: []int{2, 2}}
	cleaner := &deadlineObservingSandboxPressureCleaner{}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:                counter,
		PressureCleaner:        cleaner,
		PressureCleanupTimeout: 20 * time.Millisecond,
		MaxActive:              2,
		NodeID:                 "worker-1",
		Logger:                 zerolog.Nop(),
	})

	started := time.Now()
	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "Acquire should reject when deadline-bounded pressure cleanup does not free capacity")
	require.Nil(t, reservation, "Acquire should not return a reservation after timed-out pressure cleanup")
	require.Less(t, time.Since(started), 500*time.Millisecond, "Acquire should bound pressure cleanup with the configured timeout")
	require.Equal(t, int64(1), cleaner.calls.Load(), "Acquire should invoke pressure cleanup once")
	require.True(t, cleaner.sawDeadline.Load(), "Acquire should pass a deadline-limited context to pressure cleanup")
	require.Equal(t, int64(2), counter.calls.Load(), "Acquire should recount after deadline-bounded pressure cleanup")
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

func TestSandboxCapacityGate_AcquireUsesSeparateSharedAdmissionTimeout(t *testing.T) {
	t.Parallel()

	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:                &fakeLiveSandboxCounter{},
		SharedReservations:     contextWaitingSharedSandboxCapacityStore{},
		MaxActive:              2,
		CountTimeout:           20 * time.Millisecond,
		SharedAdmissionTimeout: 80 * time.Millisecond,
		NodeID:                 "worker-1",
		Logger:                 zerolog.Nop(),
	})

	started := time.Now()
	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
	elapsed := time.Since(started)

	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "shared admission timeout should remain retryable as worker capacity")
	require.ErrorIs(t, err, agent.ErrSandboxCapacityCoordination, "shared admission timeout should be distinguishable from genuine saturation")
	require.ErrorIs(t, err, context.DeadlineExceeded, "shared admission should retain its timeout cause")
	require.Nil(t, reservation, "timed-out shared admission should not return a reservation")
	require.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "shared lock contention should not consume only the shorter Docker count budget")
	require.Less(t, elapsed, 500*time.Millisecond, "shared admission should remain bounded by its dedicated timeout")
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

func TestSandboxCapacityGate_AcquireRejectsWhenCountStalesDuringRelease(t *testing.T) {
	t.Parallel()

	counter := newStaleDuringReleaseLiveSandboxCounter()
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:      counter,
		MaxActive:    1,
		CountTimeout: time.Second,
		NodeID:       "worker-1",
		Logger:       zerolog.Nop(),
	})
	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
	require.NoError(t, err, "first Acquire should reserve the only available slot")

	type acquireResult struct {
		reservation *agent.SandboxCapacityReservation
		err         error
	}
	resultCh := make(chan acquireResult, 1)
	go func() {
		staleReservation, staleErr := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{Purpose: "agent_run"})
		resultCh <- acquireResult{reservation: staleReservation, err: staleErr}
	}()

	select {
	case <-counter.secondStarted:
	case <-time.After(100 * time.Millisecond):
		require.Fail(t, "second Acquire should begin counting while the first reservation is still in-flight")
	}

	reservation.Release()
	close(counter.unblockSecond)

	var result acquireResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		require.Fail(t, "second Acquire should finish after the stale count is released")
	}
	if result.reservation != nil {
		result.reservation.Release()
	}

	require.ErrorIs(t, result.err, agent.ErrSandboxCapacity, "Acquire should reject after retrying a count that went stale during a reservation release")
	require.Nil(t, result.reservation, "Acquire should not return a reservation when the refreshed live count is full")
	require.Equal(t, 0, gate.ReservedCount(), "rejected stale acquire should not leak a reservation")
	require.Equal(t, int64(3), counter.calls.Load(), "Acquire should recount after a reservation release invalidates the in-flight count")
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
