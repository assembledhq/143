package liveevents

import (
	"fmt"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestSubscriberCollapsesVersionsAndOverflowsToResync(t *testing.T) {
	t.Parallel()
	orgID, resourceID := uuid.New(), uuid.New()
	subscriber := newSubscriber(orgID, nil)
	for _, version := range []int64{2, 1, 3} {
		v := version
		subscriber.enqueue(cache.LiveBusMessage{StreamID: "1-0", Event: models.LiveEvent{Type: models.LiveEventSessionUpdated, Scope: models.LiveEventScopeResource, OrgID: orgID, ResourceID: &resourceID, Version: &v}})
	}
	messages, resync := subscriber.Drain()
	require.False(t, resync, "a single collapsible resource should not overflow")
	require.Len(t, messages, 1, "versions for one resource should collapse into one mailbox entry")
	require.Equal(t, int64(3), *messages[0].Event.Version, "mailbox should retain the newest monotonic version")

	for i := 0; i <= DefaultMailboxSize; i++ {
		id := uuid.New()
		subscriber.enqueue(cache.LiveBusMessage{Event: models.LiveEvent{Type: models.LiveEventSessionUpdated, Scope: models.LiveEventScopeResource, OrgID: orgID, ResourceID: &id}})
	}
	messages, resync = subscriber.Drain()
	require.True(t, resync, "too many distinct mailbox keys should require resynchronization")
	require.Empty(t, messages, "overflow should discard the incomplete mailbox")
}

func TestManagerWriteHeavyOrganizationKeepsPerClientMailboxesBounded(t *testing.T) {
	t.Parallel()

	const clientCount = 1_000
	const eventCount = 6_000 // 100 writes/second for the design's 60-second window.
	manager := NewManager(nil, 32, zerolog.Nop())
	orgID, resourceID := uuid.New(), uuid.New()
	manager.SetShardHealthyForTest(manager.ShardForOrg(orgID), true)
	subscribers := make([]*Subscriber, 0, clientCount)
	cancel := make([]func(), 0, clientCount)
	for range clientCount {
		subscriber, unsubscribe, err := manager.Subscribe(orgID, nil)
		require.NoError(t, err, "write-heavy capacity fixture should admit every logical client")
		subscribers = append(subscribers, subscriber)
		cancel = append(cancel, unsubscribe)
	}

	startedAt := time.Now()
	for index := range eventCount {
		version := int64(index + 1)
		manager.deliver(cache.LiveBusMessage{StreamID: fmt.Sprintf("%d-0", index+1), Event: models.LiveEvent{
			OrgID: orgID, Type: models.LiveEventSessionUpdated, Scope: models.LiveEventScopeResource,
			ResourceID: &resourceID, Version: &version,
		}})
	}
	require.Less(t, time.Since(startedAt), 10*time.Second, "six million logical fanout deliveries should stay within the local latency budget")
	for _, subscriber := range subscribers {
		messages, resync := subscriber.Drain()
		require.False(t, resync, "repeated writes for one resource should collapse rather than overflow")
		require.Len(t, messages, 1, "each client should retain only the newest resource version")
		require.Equal(t, int64(eventCount), *messages[0].Event.Version, "collapsed mailbox should retain the final version")
	}
	for _, unsubscribe := range cancel {
		unsubscribe()
	}
}

func TestManagerFanoutIsOrgScoped(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 2, zerolog.Nop())
	orgA, orgB := uuid.New(), uuid.New()
	m.SetShardHealthyForTest(cache.LiveBusShard(orgA, 2), true)
	a, cancelA, err := m.Subscribe(orgA, nil)
	require.NoError(t, err, "healthy shard should admit subscriber")
	defer cancelA()
	m.deliver(cache.LiveBusMessage{Event: models.LiveEvent{OrgID: orgB, Type: models.LiveEventSessionUpdated, Scope: models.LiveEventScopeCollection}})
	messages, _ := a.Drain()
	require.Empty(t, messages, "events must not cross organization fanout groups")
}

func TestManagerShardFailureClosesOnlyAffectedSubscribers(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 2, zerolog.Nop())
	orgA := uuid.New()
	orgB := uuid.New()
	for cache.LiveBusShard(orgA, 2) == cache.LiveBusShard(orgB, 2) {
		orgB = uuid.New()
	}
	shardA := cache.LiveBusShard(orgA, 2)
	shardB := cache.LiveBusShard(orgB, 2)
	m.SetShardHealthyForTest(shardA, true)
	m.SetShardHealthyForTest(shardB, true)
	a, cancelA, err := m.Subscribe(orgA, nil)
	require.NoError(t, err, "first healthy shard should admit a subscriber")
	defer cancelA()
	b, cancelB, err := m.Subscribe(orgB, nil)
	require.NoError(t, err, "second healthy shard should admit a subscriber")
	defer cancelB()

	m.SetShardHealthyForTest(shardA, false)
	require.True(t, a.Closed(), "failed shard should close its local subscribers")
	require.Equal(t, "degraded", a.CloseReason(), "failed shard should identify transport degradation")
	require.False(t, b.Closed(), "unrelated shard subscribers should remain connected")
	_, _, err = m.Subscribe(orgA, nil)
	require.Error(t, err, "failed shard should reject premature handshakes")

	m.SetShardHealthyForTest(shardA, true)
	recovered, cancelRecovered, err := m.Subscribe(orgA, nil)
	require.NoError(t, err, "acknowledged shard recovery should admit a replaying reconnect")
	require.False(t, recovered.Closed(), "recovered subscriber should start healthy")
	cancelRecovered()
}

func TestManagerPublishFailuresDegradeAndRequireSuccessfulProbe(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, 2, zerolog.Nop())
	orgID := uuid.New()
	manager.SetShardHealthyForTest(manager.ShardForOrg(orgID), true)
	subscriber, cancel, err := manager.Subscribe(orgID, nil)
	require.NoError(t, err, "healthy publisher should admit a subscriber")
	defer cancel()
	manager.RecordPublishResult(false)
	manager.RecordPublishResult(false)
	require.False(t, subscriber.Closed(), "transient command failures below the threshold should not flap clients")
	manager.RecordPublishResult(false)
	require.True(t, subscriber.Closed(), "consecutive command failures should close affected live clients")
	_, _, err = manager.Subscribe(orgID, nil)
	require.Error(t, err, "handshakes should remain blocked before a successful publish probe")
	manager.RecordPublishResult(true)
	require.True(t, manager.publishProbeHealthy.Load(), "successful publication should satisfy the recovery probe prerequisite")
}

func TestManagerDrainRejectsHandshakesAndSignalsExistingConnections(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, 2, zerolog.Nop())
	orgID := uuid.New()
	manager.SetShardHealthyForTest(manager.ShardForOrg(orgID), true)
	subscriber, cancel, err := manager.Subscribe(orgID, nil)
	require.NoError(t, err, "healthy manager should admit a pre-drain connection")
	defer cancel()
	manager.Drain()
	require.True(t, subscriber.Closed(), "drain should close existing connections")
	require.Equal(t, "draining", subscriber.CloseReason(), "drain should produce randomized-reconnect control semantics")
	_, _, err = manager.Subscribe(orgID, nil)
	require.Error(t, err, "draining manager should reject new handshakes")
}

func TestManagerAdmitsTenThousandDistinctOrganizationsWithFixedShards(t *testing.T) {
	// Process-wide heap and file-descriptor measurements require isolation from
	// the other capacity tests, so this test intentionally does not use t.Parallel.

	const shardCount = 32
	const organizationCount = 10_000
	manager := NewManager(nil, shardCount, zerolog.Nop())
	for shard := range shardCount {
		manager.SetShardHealthyForTest(shard, true)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fdBefore, _ := os.ReadDir("/proc/self/fd")
	startedAt := time.Now()
	cancel := make([]func(), 0, organizationCount)
	for range organizationCount {
		organizationID := uuid.New()
		_, unsubscribe, err := manager.Subscribe(organizationID, nil)
		require.NoError(t, err, "fixed-shard manager should admit the distinct-organization capacity target")
		cancel = append(cancel, unsubscribe)
	}
	require.Equal(t, organizationCount, manager.ActiveSubscribers(), "all logical organization subscribers should share the fixed shard set")
	require.Len(t, manager.health, shardCount, "subscriber cardinality must not increase the configured live-bus shard count")
	runtime.ReadMemStats(&after)
	fdAfter, _ := os.ReadDir("/proc/self/fd")
	require.Less(t, time.Since(startedAt), 5*time.Second, "ten-thousand-organization admission should stay within the local latency budget")
	require.Less(t, after.Alloc-before.Alloc, uint64(128<<20), "ten-thousand-organization fanout state should stay within the API heap budget")
	require.LessOrEqual(t, len(fdAfter)-len(fdBefore), shardCount, "logical organizations must not create per-org file descriptors")

	for _, unsubscribe := range cancel {
		unsubscribe()
	}
	require.Zero(t, manager.ActiveSubscribers(), "capacity fixtures should release all logical subscribers")
}

func TestManagerEnforcesTwentyThousandPhysicalConnectionBudget(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, 32, zerolog.Nop())
	organizations := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	for _, orgID := range organizations {
		manager.SetShardHealthyForTest(manager.ShardForOrg(orgID), true)
	}
	cancel := make([]func(), 0, MaxConnectionsPerNode)
	for index := 0; index < MaxConnectionsPerNode; index++ {
		// Distinct users avoid the narrower per-user budget while exercising the
		// physical per-node cap used by per-tab fallback browsers.
		_, unsubscribe, err := manager.SubscribeForUser(organizations[index/MaxConnectionsPerOrg], uuid.New(), nil)
		require.NoError(t, err, "manager should admit physical connections through the documented node budget")
		cancel = append(cancel, unsubscribe)
	}
	_, _, err := manager.SubscribeForUser(organizations[0], uuid.New(), nil)
	require.Error(t, err, "manager should reject connections above the documented node budget")
	for _, unsubscribe := range cancel {
		unsubscribe()
	}
}
