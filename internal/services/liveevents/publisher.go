package liveevents

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

const (
	defaultClaimLease     = 30 * time.Second
	defaultPublishBatch   = 100
	defaultCoalesceWindow = 400 * time.Millisecond
)

type Publisher struct {
	store               *db.LiveEventStore
	redis               *cache.Client
	owner               string
	logger              zerolog.Logger
	wake                chan struct{}
	recordPublishResult func(bool)
	shards              int
}

func (p *Publisher) SetPublishResultRecorder(recorder func(bool)) { p.recordPublishResult = recorder }

func NewPublisher(store *db.LiveEventStore, redisClient *cache.Client, owner string, shards int, logger zerolog.Logger) *Publisher {
	if shards <= 0 {
		shards = cache.DefaultLiveBusShards
	}
	return &Publisher{store: store, redis: redisClient, owner: owner, shards: shards, logger: logger, wake: make(chan struct{}, 1)}
}

func (p *Publisher) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Publisher) Start(ctx context.Context) {
	// Database triggers make every producer atomic without forcing all service
	// call sites to know about this publisher. The short sweep is the fallback
	// wake path for those commits; Wake still gives in-process producers a
	// zero-wait fast path.
	ticker := time.NewTicker(250 * time.Millisecond)
	cleanup := time.NewTicker(10 * time.Minute)
	replayMaintenance := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer cleanup.Stop()
	defer replayMaintenance.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			p.publishBatch(ctx)
		case <-ticker.C:
			p.publishBatch(ctx)
		case <-cleanup.C:
			if count, err := p.store.Cleanup(ctx, time.Hour, 500); err != nil {
				p.logger.Warn().Err(err).Msg("failed to clean live event outbox")
			} else {
				metrics.cleaned(ctx, count)
			}
		case <-replayMaintenance.C:
			if err := p.redis.ReconcileLiveReplayStreams(ctx, 100); err != nil {
				p.logger.Warn().Err(err).Msg("failed to reconcile live replay streams")
			}
		}
	}
}

// StartPostgresListener provides the low-latency after-commit wake path. The
// periodic sweep in Start remains authoritative recovery if LISTEN disconnects.
func (p *Publisher) StartPostgresListener(ctx context.Context, pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	for ctx.Err() == nil {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			p.logger.Warn().Err(err).Msg("failed to acquire live outbox listener")
			if !waitListenerRetry(ctx) {
				return
			}
			continue
		}
		if _, err = conn.Exec(ctx, "LISTEN live_event_outbox"); err != nil {
			conn.Release()
			p.logger.Warn().Err(err).Msg("failed to listen for live outbox commits")
			if !waitListenerRetry(ctx) {
				return
			}
			continue
		}
		for ctx.Err() == nil {
			if _, err = conn.Conn().WaitForNotification(ctx); err != nil {
				break
			}
			metrics.inserted(ctx)
			p.Wake()
		}
		conn.Release()
		if ctx.Err() == nil {
			p.logger.Warn().Err(err).Msg("live outbox listener disconnected")
		}
	}
}

func waitListenerRetry(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *Publisher) publishBatch(ctx context.Context) {
	rows, err := p.store.ClaimPending(ctx, p.owner, defaultPublishBatch, defaultClaimLease)
	if err != nil {
		p.logger.Error().Err(err).Msg("failed to claim live event outbox rows")
		return
	}
	metrics.claimed(ctx, len(rows))
	materializedKeys := make(map[string]struct{})
	for _, row := range rows {
		if row.CoalesceKey != nil {
			if _, folded := materializedKeys[*row.CoalesceKey]; folded {
				continue
			}
		}
		if row.CoalesceKey != nil && !row.Aggregate {
			acquired, leaseErr := p.redis.AcquireLiveCoalesceLease(ctx, *row.CoalesceKey, defaultCoalesceWindow)
			if leaseErr == nil && !acquired {
				if err := p.store.DeferClaim(ctx, row.OrgID, row.ID, p.owner, defaultCoalesceWindow); err != nil {
					p.logger.Error().Err(err).Msg("failed to defer coalesced live event")
				}
				continue
			}
			if row.Attempts > 1 {
				aggregate, aggregateErr := p.store.MaterializeAggregate(ctx, row.OrgID, *row.CoalesceKey)
				if aggregateErr != nil {
					p.fail(ctx, row, aggregateErr)
					continue
				}
				if aggregate != nil {
					metrics.folded(ctx)
					materializedKeys[*row.CoalesceKey] = struct{}{}
					p.Wake()
					continue
				}
			}
		}
		var event models.LiveEvent
		if err := json.Unmarshal(row.Event, &event); err != nil {
			p.fail(ctx, row, err)
			continue
		}
		if _, err := p.redis.PublishLiveEvent(ctx, event, p.shards); err != nil {
			if p.recordPublishResult != nil {
				p.recordPublishResult(false)
			}
			p.fail(ctx, row, err)
			continue
		}
		if p.recordPublishResult != nil {
			p.recordPublishResult(true)
		}
		ok, err := p.store.MarkPublished(ctx, row.OrgID, row.ID, p.owner)
		if err != nil {
			p.logger.Error().Err(err).Str("event_type", string(row.EventType)).Msg("failed to acknowledge live event publication")
		} else if !ok {
			p.logger.Warn().Str("event_type", string(row.EventType)).Msg("live event claim expired before acknowledgement")
		} else {
			metrics.published(ctx, string(row.EventType), row.OriginatedAt)
		}
	}
	if len(rows) == defaultPublishBatch {
		p.Wake()
	}
}

func (p *Publisher) fail(ctx context.Context, row db.LiveEventOutboxRow, publishErr error) {
	metrics.retried(ctx, string(row.EventType))
	metrics.failed(ctx, string(row.EventType))
	exponent := math.Min(float64(row.Attempts), 6)
	delay := time.Duration(math.Pow(2, exponent)) * time.Second
	if err := p.store.MarkFailed(ctx, row.OrgID, row.ID, p.owner, publishErr, delay); err != nil {
		p.logger.Error().Err(err).Msg("failed to release live event claim")
	}
	p.logger.Warn().Err(publishErr).Str("event_type", string(row.EventType)).Int("attempt", row.Attempts).Msg("live event publication failed")
}
