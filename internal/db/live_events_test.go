package db

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var liveOutboxTestColumns = []string{"id", "org_id", "event_type", "coalesce_key", "event", "attempts", "available_at", "claim_owner", "claim_expires_at", "aggregate", "published_at", "folded_into_event_id", "last_error", "originated_at", "created_at"}

func liveOutboxRow(id, orgID uuid.UUID, key string, raw []byte, originated time.Time) []any {
	owner := "worker"
	claimExpiry := originated.Add(30 * time.Second)
	return []any{id, orgID, string(models.LiveEventSessionUpdated), &key, raw, 1, originated, &owner, &claimExpiry, false, nil, nil, nil, originated, originated}
}

func TestLiveEventStoreMaterializeAggregate(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer mock.Close()
	orgID, resourceID := uuid.New(), uuid.New()
	key := orgID.String() + ":session:" + resourceID.String()
	now := time.Now().UTC()
	version1, version2 := int64(2), int64(3)
	event1 := models.LiveEvent{SchemaVersion: 1, EventID: uuid.New(), Type: models.LiveEventSessionUpdated, Scope: models.LiveEventScopeResource, OrgID: orgID, ResourceType: models.LiveResourceSession, ResourceID: &resourceID, Audience: models.LiveAudienceOrg, Version: &version1, ChangedAt: now, Payload: json.RawMessage(`{"status_projection":{"status":"running"},"list_affected":true,"counts_affected":true}`)}
	event2 := event1
	event2.EventID = uuid.New()
	event2.Version = &version2
	event2.ChangedAt = now.Add(100 * time.Millisecond)
	event2.Payload = json.RawMessage(`{"status_projection":{"status":"completed"},"list_affected":false,"counts_affected":false}`)
	raw1, _ := json.Marshal(event1)
	raw2, _ := json.Marshal(event2)
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+liveEventOutboxColumns)).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(liveOutboxTestColumns).AddRow(liveOutboxRow(event1.EventID, orgID, key, raw1, now)...).AddRow(liveOutboxRow(event2.EventID, orgID, key, raw2, now.Add(100*time.Millisecond))...))
	mock.ExpectExec("INSERT INTO live_event_outbox").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE live_event_outbox SET folded_into_event_id").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectCommit()
	aggregate, err := NewLiveEventStore(mock).MaterializeAggregate(context.Background(), orgID, key)
	require.NoError(t, err, "coalescer should durably materialize a trailing aggregate")
	require.NotNil(t, aggregate, "multiple pending sources should create an aggregate")
	require.True(t, aggregate.Aggregate, "materialized row should be marked aggregate")
	require.Equal(t, now, aggregate.OriginatedAt, "aggregate should preserve the earliest source transition time")
	var decoded models.LiveEvent
	require.NoError(t, json.Unmarshal(aggregate.Event, &decoded), "aggregate payload should remain a typed event")
	require.Equal(t, version2, *decoded.Version, "aggregate should retain the newest resource projection")
	var payload models.SessionUpdatedPayload
	require.NoError(t, json.Unmarshal(decoded.Payload, &payload), "aggregate payload should decode to the typed session effect")
	require.True(t, payload.ListAffected, "aggregate should preserve list effects from every folded source")
	require.True(t, payload.CountsAffected, "aggregate should preserve count effects from every folded source")
	require.NotEqual(t, event1.EventID, decoded.EventID, "aggregate should receive its own stable durable identity")
	require.NoError(t, mock.ExpectationsWereMet(), "all aggregate transaction expectations should be met")
}

func TestLiveEventStoreClaimAndOwnership(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer mock.Close()
	store := NewLiveEventStore(mock)
	orgID, eventID := uuid.New(), uuid.New()
	now := time.Now()
	key := "key"
	raw := []byte(`{}`)
	mock.ExpectQuery("FOR UPDATE SKIP LOCKED").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(liveOutboxTestColumns).AddRow(liveOutboxRow(eventID, orgID, key, raw, now)...))
	rows, err := store.ClaimPending(context.Background(), "worker", 10, 30*time.Second)
	require.NoError(t, err, "eligible and expired claims should be atomically reclaimable")
	require.Len(t, rows, 1, "claim should return the leased row")
	mock.ExpectExec("claim_owner = NULL").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	ok, err := store.MarkPublished(context.Background(), orgID, eventID, "stale-worker")
	require.NoError(t, err, "lost ownership should not be an SQL error")
	require.False(t, ok, "an expired or different owner must not acknowledge publication")
	require.NoError(t, mock.ExpectationsWereMet(), "all claim ownership expectations should be met")
}
