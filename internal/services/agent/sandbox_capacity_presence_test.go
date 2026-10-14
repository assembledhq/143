package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type atomicPresenceCounter struct {
	ids                        []string
	err                        error
	waitForDeadline            bool
	inventoryCalls, countCalls atomic.Int64
	started, release           chan struct{}
}

func (c *atomicPresenceCounter) CountLiveSandboxes(context.Context) (int, error) {
	c.countCalls.Add(1)
	return len(c.ids), c.err
}
func (c *atomicPresenceCounter) LiveSandboxContainerIDs(ctx context.Context) ([]string, error) {
	call := c.inventoryCalls.Add(1)
	if c.waitForDeadline {
		<-ctx.Done()
	}
	if call == 1 && c.started != nil {
		close(c.started)
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []string{}, nil
	}
	return c.ids, c.err
}

type snapshotPresenceObserver struct {
	refs            []models.SandboxContainerReference
	err             error
	live            []string
	calls           int
	waitForDeadline bool
}

func (o *snapshotPresenceObserver) ObserveMissingSandboxReferences(ctx context.Context, ids []string) ([]models.SandboxContainerReference, error) {
	if o.waitForDeadline {
		<-ctx.Done()
	}
	o.calls++
	o.live = append([]string{}, ids...)
	return o.refs, o.err
}

func TestSandboxCapacitySnapshotDiscardsLatePresenceResults(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("a", 64)
	tests := []struct {
		name                        string
		lateInventory, lateObserver bool
		observerCalls               int
	}{
		{name: "inventory success after deadline carries no proof", lateInventory: true, observerCalls: 0},
		{name: "inspection success after deadline carries no proof", lateObserver: true, observerCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			counter := &atomicPresenceCounter{ids: []string{}, waitForDeadline: tt.lateInventory}
			observer := &snapshotPresenceObserver{refs: []models.SandboxContainerReference{{ContainerID: id, OwnerNodeID: "old-worker"}}, waitForDeadline: tt.lateObserver}
			gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{Counter: counter, MaxActive: 2, CountTimeout: 10 * time.Millisecond, Logger: zerolog.Nop()})
			gate.SetReferenceObserver(observer)
			require.Equal(t, agent.SandboxCapacitySnapshot{MaxActive: 2, CountError: context.DeadlineExceeded.Error()}, gate.Snapshot(context.Background()), "a dependency returning late success must not renew expired count or absence evidence")
			require.Equal(t, tt.observerCalls, observer.calls, "expired inventory must stop presence inspection before collecting any proof")
		})
	}
}

func TestSandboxCapacitySnapshotPhysicalPresenceCoherence(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	tests := []struct {
		name                  string
		countErr, observerErr error
		want                  agent.SandboxCapacitySnapshot
	}{
		{name: "atomic count and exact missing proof", want: agent.SandboxCapacitySnapshot{Live: 1, MaxActive: 3, MissingReferences: []models.SandboxContainerReference{{ContainerID: b, OwnerNodeID: "old-worker"}}}},
		{name: "observer failure clears proof while retaining physical count", observerErr: errors.New("inspection unavailable"), want: agent.SandboxCapacitySnapshot{Live: 1, MaxActive: 3, MissingReferenceError: "inspection unavailable"}},
		{name: "count failure admits no presence proof", countErr: errors.New("inventory unavailable"), want: agent.SandboxCapacitySnapshot{MaxActive: 3, CountError: "inventory unavailable"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			counter := &atomicPresenceCounter{ids: []string{a}, err: tt.countErr}
			observer := &snapshotPresenceObserver{refs: []models.SandboxContainerReference{{ContainerID: b, OwnerNodeID: "old-worker"}}, err: tt.observerErr}
			gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{Counter: counter, MaxActive: 3, Logger: zerolog.Nop()})
			gate.SetReferenceObserver(observer)
			got := gate.Snapshot(context.Background())
			require.Equal(t, tt.want, got, "snapshot must retain the physical count and discard every failed presence observation")
			require.Equal(t, int64(1), counter.inventoryCalls.Load(), "count and proof must use one atomic daemon inventory")
			require.Equal(t, int64(0), counter.countCalls.Load(), "snapshot must not take an independent count while observing presence")
			if tt.countErr == nil {
				require.Equal(t, []string{a}, observer.live, "observer must consume the exact same physical ID sample")
			}
		})
	}
}

func TestSandboxCapacitySnapshotRetriesReservationReleaseRace(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("a", 64)
	counter := &atomicPresenceCounter{ids: []string{id}, started: make(chan struct{}), release: make(chan struct{})}
	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{Counter: counter, MaxActive: 2, CountTimeout: time.Second, Logger: zerolog.Nop()})
	reservation, err := gate.Acquire(context.Background(), agent.SandboxCapacityRequest{})
	require.NoError(t, err, "reserve one slot before the physical sample starts")
	observer := &snapshotPresenceObserver{refs: []models.SandboxContainerReference{}}
	gate.SetReferenceObserver(observer)
	result := make(chan agent.SandboxCapacitySnapshot, 1)
	go func() { result <- gate.Snapshot(context.Background()) }()
	select {
	case <-counter.started:
	case <-time.After(time.Second):
		require.FailNow(t, "snapshot must begin the blocked inventory")
	}
	reservation.Release()
	close(counter.release)
	var got agent.SandboxCapacitySnapshot
	select {
	case got = <-result:
	case <-time.After(time.Second):
		require.FailNow(t, "snapshot must complete after release invalidates its first sample")
	}
	require.Equal(t, agent.SandboxCapacitySnapshot{Live: 1, MaxActive: 2, MissingReferences: []models.SandboxContainerReference{}}, got, "recount must see the created container instead of stale pre-release headroom")
	require.Equal(t, int64(2), counter.inventoryCalls.Load(), "a release racing physical inventory must force a fresh coherent sample")
}
