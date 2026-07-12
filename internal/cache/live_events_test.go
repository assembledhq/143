package cache

import (
	"context"
	"encoding/json"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestPublishAndReplayLiveEvent(t *testing.T) {
	t.Parallel()
	client, _ := testRedisClient(t)
	orgID, resourceID := uuid.New(), uuid.New()
	version := int64(4)
	event := models.LiveEvent{SchemaVersion: 1, EventID: uuid.New(), Type: models.LiveEventSessionUpdated, Scope: models.LiveEventScopeResource, OrgID: orgID, ResourceType: models.LiveResourceSession, ResourceID: &resourceID, Audience: models.LiveAudienceOrg, Version: &version, ChangedAt: time.Now().UTC(), Payload: json.RawMessage(`{"list_affected":true}`)}
	id, err := client.PublishLiveEvent(context.Background(), event, 4)
	require.NoError(t, err, "valid live event should publish to replay and bus")
	require.NotEmpty(t, id, "publication should return the Redis stream checkpoint")
	bounds, err := client.LiveReplayBounds(context.Background(), orgID)
	require.NoError(t, err, "replay bounds should load")
	require.Equal(t, id, bounds.First, "single entry should be the first replay checkpoint")
	require.Equal(t, id, bounds.Last, "single entry should be the last replay checkpoint")
	replay, err := client.ReplayLiveEvents(context.Background(), orgID, "0-0", id, LiveReplayLimit)
	require.NoError(t, err, "retained replay should succeed")
	require.Len(t, replay, 1, "replay should return the published event")
	require.Equal(t, event.EventID, replay[0].Event.EventID, "replay should preserve stable event identity")
}

func TestLiveReplayBudgetShedsRetentionWithoutStoppingLivePublication(t *testing.T) {
	t.Parallel()
	client, _ := testRedisClient(t)
	client.liveReplayBudgetBytes.Store(1)
	orgID, resourceID := uuid.New(), uuid.New()
	for version := int64(1); version <= 3; version++ {
		event := models.LiveEvent{SchemaVersion: 1, EventID: uuid.New(), Type: models.LiveEventSessionUpdated, Scope: models.LiveEventScopeResource, OrgID: orgID, ResourceType: models.LiveResourceSession, ResourceID: &resourceID, Audience: models.LiveAudienceOrg, Version: &version, ChangedAt: time.Now().UTC(), Payload: json.RawMessage(`{"status_projection":{"status":"running"},"list_affected":true}`)}
		streamID, err := client.PublishLiveEvent(context.Background(), event, 4)
		require.NoError(t, err, "hard replay pressure should not interrupt live publication")
		require.NotEmpty(t, streamID, "live publication should continue returning transport checkpoints")
	}
	bounds, err := client.LiveReplayBounds(context.Background(), orgID)
	require.NoError(t, err, "replay bounds should remain readable after retention shedding")
	require.LessOrEqual(t, bounds.Count, int64(1), "hard replay budget should retain only a minimal resync tail")
}
