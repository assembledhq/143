package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const liveEventOutboxColumns = `id, org_id, event_type, coalesce_key, event, attempts, available_at,
	claim_owner, claim_expires_at, aggregate, published_at, folded_into_event_id, last_error,
	originated_at, created_at`
const liveEventOutboxReturningColumns = `o.id, o.org_id, o.event_type, o.coalesce_key, o.event, o.attempts, o.available_at,
	o.claim_owner, o.claim_expires_at, o.aggregate, o.published_at, o.folded_into_event_id, o.last_error,
	o.originated_at, o.created_at`

type LiveEventOutboxRow struct {
	ID                uuid.UUID            `db:"id"`
	OrgID             uuid.UUID            `db:"org_id"`
	EventType         models.LiveEventType `db:"event_type"`
	CoalesceKey       *string              `db:"coalesce_key"`
	Event             json.RawMessage      `db:"event"`
	Attempts          int                  `db:"attempts"`
	AvailableAt       time.Time            `db:"available_at"`
	ClaimOwner        *string              `db:"claim_owner"`
	ClaimExpiresAt    *time.Time           `db:"claim_expires_at"`
	Aggregate         bool                 `db:"aggregate"`
	PublishedAt       *time.Time           `db:"published_at"`
	FoldedIntoEventID *uuid.UUID           `db:"folded_into_event_id"`
	LastError         *string              `db:"last_error"`
	OriginatedAt      time.Time            `db:"originated_at"`
	CreatedAt         time.Time            `db:"created_at"`
}

type LiveEventStore struct{ db DBTX }

func NewLiveEventStore(db DBTX) *LiveEventStore { return &LiveEventStore{db: db} }

func (s *LiveEventStore) Insert(ctx context.Context, orgID uuid.UUID, event models.LiveEvent, coalesceKey *string, aggregate bool) error {
	if event.OrgID != orgID {
		return fmt.Errorf("live event org does not match store scope")
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate live event: %w", err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal live event: %w", err)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO live_event_outbox
		(id, org_id, event_type, coalesce_key, event, aggregate, originated_at)
		VALUES (@id, @org_id, @event_type, @coalesce_key, @event, @aggregate, @originated_at)`, pgx.NamedArgs{
		"id": event.EventID, "org_id": orgID, "event_type": event.Type, "coalesce_key": coalesceKey,
		"event": raw, "aggregate": aggregate, "originated_at": event.ChangedAt,
	})
	if err != nil {
		return fmt.Errorf("insert live event outbox row: %w", err)
	}
	return nil
}

// lint:allow-no-orgid reason="system outbox worker claims pending events across organizations"
func (s *LiveEventStore) ClaimPending(ctx context.Context, owner string, limit int, lease time.Duration) ([]LiveEventOutboxRow, error) {
	if limit <= 0 {
		return []LiveEventOutboxRow{}, nil
	}
	rows, err := s.db.Query(ctx, `WITH candidates AS (
		SELECT id FROM live_event_outbox
		WHERE published_at IS NULL AND folded_into_event_id IS NULL
		  AND available_at <= now() AND (claim_expires_at IS NULL OR claim_expires_at <= now())
		ORDER BY available_at, created_at FOR UPDATE SKIP LOCKED LIMIT @limit
	) UPDATE live_event_outbox o SET claim_owner = @owner,
		claim_expires_at = now() + make_interval(secs => @lease_seconds), attempts = attempts + 1
	FROM candidates c WHERE o.id = c.id RETURNING `+liveEventOutboxReturningColumns, pgx.NamedArgs{
		"owner": owner, "limit": limit, "lease_seconds": lease.Seconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("claim live event rows: %w", err)
	}
	claimed, err := pgx.CollectRows(rows, pgx.RowToStructByName[LiveEventOutboxRow])
	if err != nil {
		return nil, fmt.Errorf("scan claimed live event rows: %w", err)
	}
	return claimed, nil
}

func (s *LiveEventStore) MarkPublished(ctx context.Context, orgID uuid.UUID, eventID uuid.UUID, owner string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE live_event_outbox SET published_at = now(), claim_owner = NULL, claim_expires_at = NULL, last_error = NULL
		WHERE id = @id AND org_id = @org_id AND claim_owner = @owner AND claim_expires_at > now()
		  AND published_at IS NULL`, pgx.NamedArgs{"id": eventID, "org_id": orgID, "owner": owner})
	if err != nil {
		return false, fmt.Errorf("mark live event published: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *LiveEventStore) MarkFailed(ctx context.Context, orgID uuid.UUID, eventID uuid.UUID, owner string, publishErr error, delay time.Duration) error {
	_, err := s.db.Exec(ctx, `UPDATE live_event_outbox SET claim_owner = NULL, claim_expires_at = NULL,
		available_at = now() + make_interval(secs => @delay_seconds), last_error = @last_error
		WHERE id = @id AND org_id = @org_id AND claim_owner = @owner AND published_at IS NULL`, pgx.NamedArgs{
		"id": eventID, "org_id": orgID, "owner": owner, "delay_seconds": delay.Seconds(), "last_error": publishErr.Error(),
	})
	if err != nil {
		return fmt.Errorf("release failed live event: %w", err)
	}
	return nil
}

func (s *LiveEventStore) DeferClaim(ctx context.Context, orgID uuid.UUID, eventID uuid.UUID, owner string, delay time.Duration) error {
	_, err := s.db.Exec(ctx, `UPDATE live_event_outbox SET claim_owner = NULL, claim_expires_at = NULL,
		available_at = now() + make_interval(secs => @delay_seconds)
		WHERE id = @id AND org_id = @org_id AND claim_owner = @owner AND published_at IS NULL`, pgx.NamedArgs{
		"id": eventID, "org_id": orgID, "owner": owner, "delay_seconds": delay.Seconds(),
	})
	if err != nil {
		return fmt.Errorf("defer live event claim: %w", err)
	}
	return nil
}

func (s *LiveEventStore) MaterializeAggregate(ctx context.Context, orgID uuid.UUID, coalesceKey string) (*LiveEventOutboxRow, error) {
	starter, ok := s.db.(TxStarter)
	if !ok {
		return nil, fmt.Errorf("live event store does not support transactions")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin live event aggregate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(@org_id::text || ':' || @coalesce_key, 0))`, pgx.NamedArgs{"org_id": orgID, "coalesce_key": coalesceKey}); err != nil {
		return nil, fmt.Errorf("lock live event aggregate: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT `+liveEventOutboxColumns+` FROM live_event_outbox
		WHERE org_id=@org_id AND coalesce_key=@coalesce_key AND published_at IS NULL AND folded_into_event_id IS NULL
		ORDER BY originated_at, created_at FOR UPDATE`, pgx.NamedArgs{"org_id": orgID, "coalesce_key": coalesceKey})
	if err != nil {
		return nil, fmt.Errorf("load live event aggregate sources: %w", err)
	}
	sources, err := pgx.CollectRows(rows, pgx.RowToStructByName[LiveEventOutboxRow])
	if err != nil {
		return nil, fmt.Errorf("scan live event aggregate sources: %w", err)
	}
	if len(sources) < 2 {
		return nil, nil
	}
	var aggregateEvent models.LiveEvent
	if err := json.Unmarshal(sources[len(sources)-1].Event, &aggregateEvent); err != nil {
		return nil, fmt.Errorf("decode aggregate source: %w", err)
	}
	var mergedPayload map[string]any
	if err := json.Unmarshal(aggregateEvent.Payload, &mergedPayload); err != nil {
		return nil, fmt.Errorf("decode aggregate payload: %w", err)
	}
	for _, source := range sources {
		var sourceEvent models.LiveEvent
		if err := json.Unmarshal(source.Event, &sourceEvent); err != nil {
			return nil, fmt.Errorf("decode aggregate source payload: %w", err)
		}
		var sourcePayload map[string]any
		if err := json.Unmarshal(sourceEvent.Payload, &sourcePayload); err != nil {
			return nil, fmt.Errorf("decode aggregate source payload: %w", err)
		}
		for _, field := range []string{"list_affected", "counts_affected"} {
			if affected, ok := sourcePayload[field].(bool); ok && affected {
				mergedPayload[field] = true
			}
		}
	}
	mergedPayloadRaw, err := json.Marshal(mergedPayload)
	if err != nil {
		return nil, fmt.Errorf("encode aggregate payload: %w", err)
	}
	aggregateEvent.Payload = mergedPayloadRaw
	aggregateEvent.EventID = uuid.New()
	aggregateEvent.ChangedAt = sources[0].OriginatedAt
	if err := aggregateEvent.Validate(); err != nil {
		return nil, fmt.Errorf("validate aggregate event: %w", err)
	}
	raw, err := json.Marshal(aggregateEvent)
	if err != nil {
		return nil, fmt.Errorf("encode aggregate event: %w", err)
	}
	row := LiveEventOutboxRow{ID: aggregateEvent.EventID, OrgID: orgID, EventType: aggregateEvent.Type, CoalesceKey: &coalesceKey, Event: raw, Aggregate: true, OriginatedAt: sources[0].OriginatedAt}
	if _, err := tx.Exec(ctx, `INSERT INTO live_event_outbox (id,org_id,event_type,coalesce_key,event,aggregate,originated_at)
		VALUES (@id,@org_id,@event_type,@coalesce_key,@event,true,@originated_at)`, pgx.NamedArgs{"id": row.ID, "org_id": orgID, "event_type": row.EventType, "coalesce_key": coalesceKey, "event": raw, "originated_at": row.OriginatedAt}); err != nil {
		return nil, fmt.Errorf("insert aggregate live event: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	if _, err := tx.Exec(ctx, `UPDATE live_event_outbox SET folded_into_event_id=@aggregate_id,claim_owner=NULL,claim_expires_at=NULL
		WHERE org_id=@org_id AND id=ANY(@source_ids)`, pgx.NamedArgs{"aggregate_id": row.ID, "org_id": orgID, "source_ids": ids}); err != nil {
		return nil, fmt.Errorf("fold aggregate live event sources: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit live event aggregate: %w", err)
	}
	return &row, nil
}

// lint:allow-no-orgid reason="system cleanup removes only already delivered short-lived outbox rows"
func (s *LiveEventStore) Cleanup(ctx context.Context, olderThan time.Duration, limit int) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM live_event_outbox WHERE id IN (
		SELECT id FROM live_event_outbox WHERE (published_at IS NOT NULL OR folded_into_event_id IS NOT NULL)
		AND created_at < now() - make_interval(secs => @age_seconds) ORDER BY created_at LIMIT @limit
	)`, pgx.NamedArgs{"age_seconds": olderThan.Seconds(), "limit": limit})
	if err != nil {
		return 0, fmt.Errorf("clean live event outbox: %w", err)
	}
	return tag.RowsAffected(), nil
}

// lint:allow-no-orgid reason="node-wide health monitor samples one indexed aggregate across the outbox"
func (s *LiveEventStore) OldestPendingAge(ctx context.Context) (time.Duration, error) {
	var seconds *float64
	err := s.db.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM now() - MIN(originated_at))
		FROM live_event_outbox WHERE published_at IS NULL AND folded_into_event_id IS NULL`).Scan(&seconds)
	if err != nil {
		return 0, fmt.Errorf("query oldest pending live event: %w", err)
	}
	if seconds == nil {
		return 0, nil
	}
	return time.Duration(*seconds * float64(time.Second)), nil
}
