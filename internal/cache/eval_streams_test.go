package cache

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestEvalBatchStreams_AvailabilityAndHelpers(t *testing.T) {
	t.Parallel()

	require.Nil(t, NewEvalBatchStreams(nil, zerolog.Nop()), "constructor should return nil when Redis client is missing")

	client, _ := testRedisClient(t)
	streams := NewEvalBatchStreams(client, zerolog.Nop())
	require.True(t, streams.Available(), "streams should report available when Redis is healthy")

	sub, err := streams.Subscribe(uuid.New())
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	require.Equal(t, "subscription closed", sub.CloseReason(), "fresh subscriptions should report the default close reason")
	sub.Close()
	require.NotEmpty(t, sub.CloseReason(), "closing a subscription should leave a non-empty close reason")

	require.Equal(t, "subscription closed", (*EvalBatchSubscription)(nil).CloseReason(), "nil subscription receivers should report the default close reason")

	require.Equal(t, "143:stream:{batch:00000000-0000-0000-0000-000000000000}:eval_batches", evalBatchStreamChannel(uuid.Nil), "channel helper should scope eval batch streams by batch ID so unrelated batches in the same org don't fan out")
}

func TestEvalBatchStreams_PublishAndSubscribe(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewEvalBatchStreams(client, zerolog.Nop())
	batchID := uuid.New()

	sub, err := streams.Subscribe(batchID)
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()

	event := models.EvalBatchUpdatedEvent{
		BatchID:   batchID,
		OrgID:     uuid.New(),
		Status:    models.EvalBatchStatusRunning,
		UpdatedAt: time.Now().UTC(),
	}

	require.NoError(t, streams.PublishUpdated(context.Background(), event), "publish should succeed for valid events")
	require.Eventually(t, func() bool {
		select {
		case got := <-sub.C:
			return got.BatchID == event.BatchID && got.Status == event.Status
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "published eval batch events should be delivered to subscribers")
}

func TestEvalBatchStreams_PerBatchScoping(t *testing.T) {
	t.Parallel()

	// Per-batch channel keys (vs per-org) mean a subscriber to batch A must
	// not receive events for batch B even though they're in the same org.
	// Locks in the scoping change so a future regression to per-org keys
	// would surface here instead of as a quiet performance bug.
	client, _ := testRedisClient(t)
	streams := NewEvalBatchStreams(client, zerolog.Nop())
	orgID := uuid.New()
	batchA := uuid.New()
	batchB := uuid.New()

	subA, err := streams.Subscribe(batchA)
	require.NoError(t, err, "subscribe to batch A should succeed")
	defer subA.Close()

	require.NoError(t, streams.PublishUpdated(context.Background(), models.EvalBatchUpdatedEvent{
		BatchID:   batchB,
		OrgID:     orgID,
		Status:    models.EvalBatchStatusRunning,
		UpdatedAt: time.Now().UTC(),
	}), "publish for batch B should succeed")

	require.Never(t, func() bool {
		select {
		case <-subA.C:
			return true
		default:
			return false
		}
	}, 200*time.Millisecond, 20*time.Millisecond, "subscriber to batch A must not receive events scoped to batch B")
}

func TestEvalBatchStreams_HandlesInvalidPayloadAndUnavailableRedis(t *testing.T) {
	t.Parallel()

	require.NoError(t, (*EvalBatchStreams)(nil).PublishUpdated(context.Background(), models.EvalBatchUpdatedEvent{}), "publishing with nil streams should be a no-op")

	client, _ := testRedisClient(t)
	streams := NewEvalBatchStreams(client, zerolog.Nop())
	batchID := uuid.New()

	sub, err := streams.Subscribe(batchID)
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()

	require.NoError(t, client.raw().Publish(context.Background(), evalBatchStreamChannel(batchID), "{not-json").Err(), "test should inject an invalid payload into the channel")
	require.Never(t, func() bool {
		select {
		case <-sub.C:
			return true
		default:
			return false
		}
	}, 150*time.Millisecond, 20*time.Millisecond, "invalid JSON payloads should be skipped rather than delivered")

	unavailable := &Client{logger: zerolog.Nop()}
	unavailable.breaker = NewCircuitBreaker(zerolog.Nop())
	streams = NewEvalBatchStreams(unavailable, zerolog.Nop())
	require.False(t, streams.Available(), "streams should report unavailable when the underlying Redis client is not ready")
	_, err = streams.Subscribe(uuid.New())
	require.Error(t, err, "subscribe should fail cleanly when Redis is unavailable")
	require.Contains(t, err.Error(), "redis unavailable", "subscribe should explain that Redis is unavailable")

	unavailable.rdb = client.raw()
	unavailable.breaker.ForceOpen()
	err = streams.PublishUpdated(context.Background(), models.EvalBatchUpdatedEvent{BatchID: uuid.New()})
	require.Error(t, err, "publish should fail when the breaker is open")
	require.Contains(t, err.Error(), "publish eval batch update event", "publish should wrap Redis publish failures with operation context")

	_, err = streams.Subscribe(uuid.New())
	require.Error(t, err, "subscribe should fail when availability probes fail")
	require.True(t, strings.Contains(err.Error(), "redis unavailable") || strings.Contains(err.Error(), "subscribe eval batch stream"), "subscribe should surface Redis availability errors")
}

func TestEvalBootstrapStreams_PublishAndSubscribe(t *testing.T) {
	t.Parallel()

	require.Nil(t, NewEvalBootstrapStreams(nil, zerolog.Nop()), "constructor should return nil when Redis client is missing")
	require.NoError(t, (*EvalBootstrapStreams)(nil).PublishUpdated(context.Background(), models.EvalBootstrapUpdatedEvent{}), "publishing with nil streams should be a no-op")

	client, _ := testRedisClient(t)
	streams := NewEvalBootstrapStreams(client, zerolog.Nop())
	runID := uuid.New()
	require.True(t, streams.Available(), "streams should report available when Redis is healthy")
	require.Equal(t, "143:stream:{bootstrap:00000000-0000-0000-0000-000000000000}:eval_bootstraps", evalBootstrapStreamChannel(uuid.Nil), "channel helper should scope eval bootstrap streams by run ID")

	sub, err := streams.Subscribe(runID)
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()

	sessionID := uuid.New()
	event := models.EvalBootstrapUpdatedEvent{
		BootstrapRunID: runID,
		OrgID:          uuid.New(),
		Status:         models.EvalBootstrapStatusRunning,
		SessionID:      &sessionID,
		UpdatedAt:      time.Now().UTC(),
	}

	require.NoError(t, streams.PublishUpdated(context.Background(), event), "publish should succeed for valid events")
	require.Eventually(t, func() bool {
		select {
		case got := <-sub.C:
			return got.BootstrapRunID == event.BootstrapRunID && got.Status == event.Status && got.SessionID != nil && *got.SessionID == sessionID
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "published eval bootstrap events should be delivered to subscribers")

	require.Equal(t, "subscription closed", (*EvalBootstrapSubscription)(nil).CloseReason(), "nil subscription receivers should report the default close reason")
	require.Equal(t, "subscription closed", sub.CloseReason(), "fresh subscriptions should report the default close reason before close")
}
