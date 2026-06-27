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

func TestCodeReviewStreams_AvailabilityAndHelpers(t *testing.T) {
	t.Parallel()

	require.Nil(t, NewCodeReviewStreams(nil, zerolog.Nop()), "constructor should return nil when Redis client is missing")

	client, _ := testRedisClient(t)
	streams := NewCodeReviewStreams(client, zerolog.Nop())
	require.True(t, streams.Available(), "streams should report available when Redis is healthy")

	sub, err := streams.Subscribe(uuid.New())
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	require.Equal(t, "subscription closed", sub.CloseReason(), "fresh subscriptions should report the default close reason")
	sub.Close()
	require.NotEmpty(t, sub.CloseReason(), "closing a subscription should leave a non-empty close reason")

	require.Equal(t, "subscription closed", (*CodeReviewSubscription)(nil).CloseReason(), "nil subscription receivers should report the default close reason")

	naturalSub, err := streams.Subscribe(uuid.New())
	require.NoError(t, err, "subscribe should succeed for the natural-close case")
	require.NoError(t, naturalSub.pubsub.Close(), "closing pubsub directly should drive the goroutine to its default close path")
	select {
	case <-naturalSub.C:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription goroutine did not exit after pubsub close")
	}
	require.Equal(t, "subscription closed", naturalSub.CloseReason(), "natural pubsub closure should record the default close reason")

	require.Equal(t, "143:stream:{org:00000000-0000-0000-0000-000000000000}:code_reviews", codeReviewStreamChannel(uuid.Nil), "channel helper should scope code review streams by org")
}

func TestCodeReviewStreams_PublishAndSubscribe(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewCodeReviewStreams(client, zerolog.Nop())
	orgID := uuid.New()

	sub, err := streams.Subscribe(orgID)
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()

	sessionID := uuid.New()
	event := models.CodeReviewUpdatedEvent{
		OrgID:     orgID,
		SessionID: &sessionID,
		Status:    models.CodeReviewSessionStatusCompleted,
		UpdatedAt: time.Now().UTC(),
	}

	require.NoError(t, streams.PublishUpdated(context.Background(), orgID, event), "publish should succeed for valid events")
	require.Eventually(t, func() bool {
		select {
		case got := <-sub.C:
			return got.SessionID != nil && *got.SessionID == sessionID && got.Status == event.Status
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "published code review events should be delivered to subscribers")
}

func TestCodeReviewStreams_HandlesInvalidPayloadAndUnavailableRedis(t *testing.T) {
	t.Parallel()

	require.NoError(t, (*CodeReviewStreams)(nil).PublishUpdated(context.Background(), uuid.New(), models.CodeReviewUpdatedEvent{}), "publishing with nil streams should be a no-op")

	client, _ := testRedisClient(t)
	streams := NewCodeReviewStreams(client, zerolog.Nop())
	orgID := uuid.New()

	sub, err := streams.Subscribe(orgID)
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()

	require.NoError(t, client.raw().Publish(context.Background(), codeReviewStreamChannel(orgID), "{not-json").Err(), "test should inject an invalid payload into the channel")
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
	streams = NewCodeReviewStreams(unavailable, zerolog.Nop())
	require.False(t, streams.Available(), "streams should report unavailable when the underlying Redis client is not ready")
	_, err = streams.Subscribe(uuid.New())
	require.Error(t, err, "subscribe should fail cleanly when Redis is unavailable")
	require.Contains(t, err.Error(), "redis unavailable", "subscribe should explain that Redis is unavailable")

	unavailable.rdb = client.raw()
	unavailable.breaker.ForceOpen()
	err = streams.PublishUpdated(context.Background(), uuid.New(), models.CodeReviewUpdatedEvent{})
	require.Error(t, err, "publish should fail when the breaker is open")
	require.Contains(t, err.Error(), "publish code review update event", "publish should wrap Redis publish failures with operation context")

	_, err = streams.Subscribe(uuid.New())
	require.Error(t, err, "subscribe should fail when availability probes fail")
	require.True(t, strings.Contains(err.Error(), "redis unavailable") || strings.Contains(err.Error(), "subscribe code review stream"), "subscribe should surface Redis availability errors")
}
