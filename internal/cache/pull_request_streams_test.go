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

func TestPullRequestStreams_AvailabilityAndHelpers(t *testing.T) {
	t.Parallel()

	require.Nil(t, NewPullRequestStreams(nil, zerolog.Nop()), "constructor should return nil when Redis client is missing")

	client, _ := testRedisClient(t)
	streams := NewPullRequestStreams(client, zerolog.Nop())
	require.True(t, streams.Available(), "streams should report available when Redis is healthy")

	sub, err := streams.Subscribe(uuid.New())
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	require.Equal(t, "subscription closed", sub.CloseReason(), "fresh subscriptions should report the default close reason")
	sub.Close()
	require.NotEmpty(t, sub.CloseReason(), "closing a subscription should leave a non-empty close reason")

	require.Equal(t, "143:stream:{org:00000000-0000-0000-0000-000000000000}:pull_requests", pullRequestStreamChannel(uuid.Nil), "channel helper should scope pull request streams by org")
}

func TestPullRequestStreams_PublishAndSubscribe(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewPullRequestStreams(client, zerolog.Nop())
	orgID := uuid.New()

	sub, err := streams.Subscribe(orgID)
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()

	event := models.PullRequestUpdatedEvent{
		PullRequestID: uuid.New(),
		Version:       9,
		HeadSHA:       "head",
		BaseSHA:       "base",
		SyncedAt:      time.Now().UTC(),
	}

	require.NoError(t, streams.PublishUpdated(context.Background(), orgID, event), "publish should succeed for valid events")
	require.Eventually(t, func() bool {
		select {
		case got := <-sub.C:
			return got.PullRequestID == event.PullRequestID && got.Version == event.Version
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "published pull request events should be delivered to subscribers")
}

func TestPullRequestStreams_HandlesInvalidPayloadAndUnavailableRedis(t *testing.T) {
	t.Parallel()

	require.NoError(t, (*PullRequestStreams)(nil).PublishUpdated(context.Background(), uuid.New(), models.PullRequestUpdatedEvent{}), "publishing with nil streams should be a no-op")

	client, _ := testRedisClient(t)
	streams := NewPullRequestStreams(client, zerolog.Nop())
	orgID := uuid.New()

	sub, err := streams.Subscribe(orgID)
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()

	require.NoError(t, client.raw().Publish(context.Background(), pullRequestStreamChannel(orgID), "{not-json").Err(), "test should inject an invalid payload into the channel")
	require.Never(t, func() bool {
		select {
		case <-sub.C:
			return true
		default:
			return false
		}
	}, 150*time.Millisecond, 20*time.Millisecond, "invalid JSON payloads should be skipped rather than delivered")

	sub.Close()
	require.NotEmpty(t, sub.CloseReason(), "closing the subscription should record a close reason")

	unavailable := &Client{logger: zerolog.Nop()}
	unavailable.breaker = NewCircuitBreaker(zerolog.Nop())
	streams = NewPullRequestStreams(unavailable, zerolog.Nop())
	require.False(t, streams.Available(), "streams should report unavailable when the underlying Redis client is not ready")
	_, err = streams.Subscribe(uuid.New())
	require.Error(t, err, "subscribe should fail cleanly when Redis is unavailable")
	require.Contains(t, err.Error(), "redis unavailable", "subscribe should explain that Redis is unavailable")

	unavailable.rdb = client.raw()
	unavailable.breaker.ForceOpen()
	err = streams.PublishUpdated(context.Background(), uuid.New(), models.PullRequestUpdatedEvent{})
	require.Error(t, err, "publish should fail when the breaker is open")
	require.Contains(t, err.Error(), "publish pull request update event", "publish should wrap Redis publish failures with operation context")

	_, err = streams.Subscribe(uuid.New())
	require.Error(t, err, "subscribe should fail when availability probes fail")
	require.True(t, strings.Contains(err.Error(), "redis unavailable") || strings.Contains(err.Error(), "subscribe pull request stream"), "subscribe should surface Redis availability errors")
}
