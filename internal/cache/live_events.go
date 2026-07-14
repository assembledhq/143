package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	liveReplayMeter         = otel.Meter("github.com/assembledhq/143/live_replay")
	liveReplayActive, _     = liveReplayMeter.Int64UpDownCounter("live_events.replay_active_streams")
	liveReplayBytes, _      = liveReplayMeter.Int64Histogram("live_events.replay_estimated_bytes", otelmetric.WithUnit("By"))
	liveReplayTrims, _      = liveReplayMeter.Int64Counter("live_events.replay_trims")
	liveReplayShedding, _   = liveReplayMeter.Int64Counter("live_events.replay_retention_shedding")
	liveReplayEventBytes, _ = liveReplayMeter.Int64Histogram("live_events.replay_event_bytes", otelmetric.WithUnit("By"))
	liveReplayResults, _    = liveReplayMeter.Int64Counter("live_events.replay_results")
)

const (
	DefaultLiveBusShards = 32
	LiveReplayMaxLen     = 1024
	LiveReplayLimit      = 1000
	LiveReplayRetention  = 5 * time.Minute
	LiveReplayExpiry     = time.Hour
)

type LiveBusMessage struct {
	StreamID string           `json:"stream_id"`
	Event    models.LiveEvent `json:"event"`
	Probe    bool             `json:"probe,omitempty"`
}

type LiveReplayBounds struct {
	First string
	Last  string
	Count int64
}

func LiveBusShard(orgID uuid.UUID, shards int) int {
	if shards <= 0 {
		shards = DefaultLiveBusShards
	}
	h := fnv.New32a()
	_, _ = h.Write(orgID[:])
	// #nosec G115 -- the modulo result is bounded by the positive configured shard count.
	return int(uint64(h.Sum32()) % uint64(shards))
}

func liveReplayKey(orgID uuid.UUID) string {
	return "143:stream:{org:" + orgID.String() + "}:live_events"
}
func liveBusChannel(shard int) string { return "143:live_bus:" + strconv.Itoa(shard) }

const liveReplayRegistryKey = "143:{live_replay}:active"

func (c *Client) ValidateLiveReplayMemory(ctx context.Context) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis unavailable")
	}
	values, err := c.rdb.ConfigGet(ctx, "maxmemory").Result()
	if err != nil {
		return fmt.Errorf("read Redis maxmemory: %w", err)
	}
	raw := values["maxmemory"]
	maximum, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || maximum <= 0 {
		return fmt.Errorf("redis maxmemory must be a finite positive byte limit for live replay")
	}
	c.liveReplayBudgetBytes.Store(maximum / 4)
	return nil
}

var updateLiveReplayUsage = redis.NewScript(`
local old = tonumber(redis.call('HGET', KEYS[1], ARGV[1]) or '0')
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
return redis.call('INCRBY', KEYS[2], tonumber(ARGV[2]) - old)
`)

func (c *Client) enforceLiveReplayBudget(ctx context.Context, key string) {
	budget := c.liveReplayBudgetBytes.Load()
	if budget <= 0 {
		return
	}
	usage, err := c.rdb.MemoryUsage(ctx, key).Result()
	if err != nil {
		return
	}
	total, err := updateLiveReplayUsage.Run(ctx, c.rdb,
		[]string{"143:{live_replay}:sizes", "143:{live_replay}:estimated_bytes"}, key, usage).Int64()
	if err != nil {
		return
	}
	if total >= budget {
		if _, err := c.rdb.XTrimMaxLen(ctx, key, 1).Result(); err != nil {
			c.logger.Warn().Err(err).Msg("failed to shed live replay retention")
			return
		}
		liveReplayTrims.Add(ctx, 1)
		liveReplayShedding.Add(ctx, 1)
		if trimmedUsage, err := c.rdb.MemoryUsage(ctx, key).Result(); err == nil {
			updatedTotal, updateErr := updateLiveReplayUsage.Run(ctx, c.rdb,
				[]string{"143:{live_replay}:sizes", "143:{live_replay}:estimated_bytes"}, key, trimmedUsage).Int64()
			if updateErr != nil {
				c.logger.Warn().Err(updateErr).Msg("failed to update live replay usage after shedding")
			} else {
				total = updatedTotal
			}
		}
		c.logger.Warn().Int64("estimated_bytes", total).Int64("budget_bytes", budget).Msg("live replay budget reached; retention shed to minimal tail")
	} else if total >= budget*4/5 {
		if _, err := c.rdb.XTrimMaxLen(ctx, key, LiveReplayMaxLen/2).Result(); err != nil {
			c.logger.Warn().Err(err).Msg("failed to tighten live replay retention")
			return
		}
		if trimmedUsage, err := c.rdb.MemoryUsage(ctx, key).Result(); err == nil {
			updatedTotal, updateErr := updateLiveReplayUsage.Run(ctx, c.rdb,
				[]string{"143:{live_replay}:sizes", "143:{live_replay}:estimated_bytes"}, key, trimmedUsage).Int64()
			if updateErr != nil {
				c.logger.Warn().Err(updateErr).Msg("failed to update live replay usage after tightening retention")
			} else {
				total = updatedTotal
			}
		}
		liveReplayTrims.Add(ctx, 1)
		c.logger.Warn().Int64("estimated_bytes", total).Int64("budget_bytes", budget).Msg("live replay budget above warning threshold; retention tightened")
	}
	liveReplayBytes.Record(ctx, total)
}

func (c *Client) AcquireLiveCoalesceLease(ctx context.Context, key string, window time.Duration) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("redis unavailable")
	}
	return c.rdb.SetNX(ctx, "143:live_coalesce:"+key, "1", window).Result()
}

// ProbeLivePublish verifies the fixed-shard publication command path without
// creating replay data or delivering a product event. Subscription health is
// tracked independently by each manager shard's acknowledgement epoch.
func (c *Client) ProbeLivePublish(ctx context.Context) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis unavailable")
	}
	payload, err := json.Marshal(LiveBusMessage{Probe: true})
	if err != nil {
		return fmt.Errorf("marshal live publish probe: %w", err)
	}
	return c.doCommand(ctx, "live_publish_probe", func() error {
		channel := liveBusChannel(0)
		if cluster, ok := c.rdb.(*redis.ClusterClient); ok {
			return cluster.Do(ctx, "SPUBLISH", channel, payload).Err()
		}
		return c.rdb.Publish(ctx, channel, payload).Err()
	})
}

func (c *Client) PublishLiveEvent(ctx context.Context, event models.LiveEvent, shards int) (string, error) {
	if err := event.Validate(); err != nil {
		return "", fmt.Errorf("validate live event: %w", err)
	}
	if c == nil || c.rdb == nil {
		return "", errors.New("redis unavailable")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal live event: %w", err)
	}
	liveReplayEventBytes.Record(ctx, int64(len(raw)))
	key := liveReplayKey(event.OrgID)
	var streamID string
	err = c.doCommand(ctx, "live_publish", func() error {
		id, xerr := c.rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, MaxLen: LiveReplayMaxLen, Approx: true, Values: map[string]any{"event": raw}}).Result()
		if xerr != nil {
			return xerr
		}
		streamID = id
		// Age trimming is write-driven; expiry removes inactive organization streams.
		minID := strconv.FormatInt(time.Now().Add(-LiveReplayRetention).UnixMilli(), 10) + "-0"
		pipe := c.rdb.Pipeline()
		pipe.XTrimMinIDApprox(ctx, key, minID, 0)
		pipe.Expire(ctx, key, LiveReplayExpiry)
		registryAdd := pipe.ZAdd(ctx, liveReplayRegistryKey, redis.Z{Score: float64(time.Now().Unix()), Member: key})
		message, merr := json.Marshal(LiveBusMessage{StreamID: id, Event: event})
		if merr != nil {
			return merr
		}
		channel := liveBusChannel(LiveBusShard(event.OrgID, shards))
		if _, ok := c.rdb.(*redis.ClusterClient); ok {
			pipe.Do(ctx, "SPUBLISH", channel, message)
		} else {
			pipe.Publish(ctx, channel, message)
		}
		_, xerr = pipe.Exec(ctx)
		if xerr == nil && registryAdd.Val() > 0 {
			liveReplayActive.Add(ctx, 1)
		}
		return xerr
	})
	if err != nil {
		return "", fmt.Errorf("publish live event: %w", err)
	}
	c.enforceLiveReplayBudget(ctx, key)
	return streamID, nil
}

var removeLiveReplayUsage = redis.NewScript(`
local old = tonumber(redis.call('HGET', KEYS[1], ARGV[1]) or '0')
redis.call('HDEL', KEYS[1], ARGV[1])
local total = tonumber(redis.call('GET', KEYS[2]) or '0')
local next = math.max(0, total - old)
redis.call('SET', KEYS[2], next)
return next
`)

// ReconcileLiveReplayStreams performs bounded maintenance over the active-key
// registry; it never scans the Redis keyspace.
func (c *Client) ReconcileLiveReplayStreams(ctx context.Context, limit int64) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis unavailable")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	keys, err := c.rdb.ZRange(ctx, liveReplayRegistryKey, 0, limit-1).Result()
	if err != nil {
		return fmt.Errorf("read live replay registry: %w", err)
	}
	minID := strconv.FormatInt(time.Now().Add(-LiveReplayRetention).UnixMilli(), 10) + "-0"
	for _, key := range keys {
		exists, existsErr := c.rdb.Exists(ctx, key).Result()
		if existsErr != nil {
			return fmt.Errorf("check live replay stream: %w", existsErr)
		}
		if exists == 0 {
			pipe := c.rdb.Pipeline()
			pipe.ZRem(ctx, liveReplayRegistryKey, key)
			removeLiveReplayUsage.Run(ctx, pipe, []string{"143:{live_replay}:sizes", "143:{live_replay}:estimated_bytes"}, key)
			if _, pipeErr := pipe.Exec(ctx); pipeErr != nil {
				return fmt.Errorf("remove expired live replay stream: %w", pipeErr)
			}
			liveReplayActive.Add(ctx, -1)
			continue
		}
		if err := c.rdb.XTrimMinIDApprox(ctx, key, minID, 0).Err(); err != nil {
			return fmt.Errorf("reconcile live replay retention: %w", err)
		}
		liveReplayTrims.Add(ctx, 1)
		if err := c.rdb.ZAdd(ctx, liveReplayRegistryKey, redis.Z{Score: float64(time.Now().Unix()), Member: key}).Err(); err != nil {
			return fmt.Errorf("rotate live replay registry: %w", err)
		}
	}
	return nil
}

func (c *Client) LiveReplayBounds(ctx context.Context, orgID uuid.UUID) (LiveReplayBounds, error) {
	if c == nil || c.rdb == nil {
		return LiveReplayBounds{}, errors.New("redis unavailable")
	}
	key := liveReplayKey(orgID)
	pipe := c.rdb.Pipeline()
	firstCmd := pipe.XRangeN(ctx, key, "-", "+", 1)
	lastCmd := pipe.XRevRangeN(ctx, key, "+", "-", 1)
	countCmd := pipe.XLen(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return LiveReplayBounds{}, err
	}
	bounds := LiveReplayBounds{Count: countCmd.Val(), First: "0-0", Last: "0-0"}
	if entries := firstCmd.Val(); len(entries) > 0 {
		bounds.First = entries[0].ID
	}
	if entries := lastCmd.Val(); len(entries) > 0 {
		bounds.Last = entries[0].ID
	}
	return bounds, nil
}

func (c *Client) ReplayLiveEvents(ctx context.Context, orgID uuid.UUID, after, through string, limit int64) ([]LiveBusMessage, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis unavailable")
	}
	if limit <= 0 || limit > LiveReplayLimit {
		limit = LiveReplayLimit
	}
	entries, err := c.rdb.XRangeN(ctx, liveReplayKey(orgID), "("+after, through, limit+1).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("range live replay: %w", err)
	}
	if len(entries) > int(limit) {
		liveReplayResults.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("result", "too_large")))
		return nil, fmt.Errorf("live replay limit exceeded")
	}
	result := make([]LiveBusMessage, 0, len(entries))
	for _, entry := range entries {
		value, ok := entry.Values["event"]
		if !ok {
			continue
		}
		var raw []byte
		switch v := value.(type) {
		case string:
			raw = []byte(v)
		case []byte:
			raw = v
		default:
			continue
		}
		var event models.LiveEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("decode replay event: %w", err)
		}
		event.StreamID = entry.ID
		result = append(result, LiveBusMessage{StreamID: entry.ID, Event: event})
	}
	liveReplayResults.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("result", "hit")))
	return result, nil
}

func (c *Client) SubscribeLiveShard(ctx context.Context, shard int) *redis.PubSub {
	if c == nil || c.rdb == nil {
		return nil
	}
	channel := liveBusChannel(shard)
	if cluster, ok := c.rdb.(*redis.ClusterClient); ok {
		return cluster.SSubscribe(ctx, channel)
	}
	return c.rdb.Subscribe(ctx, channel)
}
