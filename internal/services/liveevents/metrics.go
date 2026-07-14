package liveevents

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

type liveMetrics struct {
	connections       otelmetric.Int64UpDownCounter
	busReconnects     otelmetric.Int64Counter
	fanout            otelmetric.Int64Counter
	mailboxResync     otelmetric.Int64Counter
	outboxPublished   otelmetric.Int64Counter
	outboxRetried     otelmetric.Int64Counter
	publishLatency    otelmetric.Float64Histogram
	busBytes          otelmetric.Int64Counter
	shardHealth       otelmetric.Int64UpDownCounter
	activeGroups      otelmetric.Int64UpDownCounter
	outboxFailed      otelmetric.Int64Counter
	outboxCleaned     otelmetric.Int64Counter
	outboxInserted    otelmetric.Int64Counter
	outboxClaimed     otelmetric.Int64Counter
	outboxFolded      otelmetric.Int64Counter
	pendingAge        otelmetric.Float64Histogram
	revocationLatency otelmetric.Float64Histogram
}

func newLiveMetrics() *liveMetrics {
	meter := otel.Meter("github.com/assembledhq/143/live_events")
	connections, _ := meter.Int64UpDownCounter("live_events.sse_connections")
	busReconnects, _ := meter.Int64Counter("live_events.bus_reconnects")
	fanout, _ := meter.Int64Counter("live_events.fanout_deliveries")
	mailboxResync, _ := meter.Int64Counter("live_events.mailbox_resyncs")
	outboxPublished, _ := meter.Int64Counter("live_events.outbox_published")
	outboxRetried, _ := meter.Int64Counter("live_events.outbox_retried")
	publishLatency, _ := meter.Float64Histogram("live_events.commit_to_publish_ms",
		otelmetric.WithUnit("ms"),
		otelmetric.WithExplicitBucketBoundaries(10, 25, 50, 100, 150, 250, 500, 750, 1000, 2000, 5000),
	)
	busBytes, _ := meter.Int64Counter("live_events.bus_bytes")
	shardHealth, _ := meter.Int64UpDownCounter("live_events.bus_shard_healthy")
	activeGroups, _ := meter.Int64UpDownCounter("live_events.active_org_groups")
	outboxFailed, _ := meter.Int64Counter("live_events.outbox_failed")
	outboxCleaned, _ := meter.Int64Counter("live_events.outbox_cleaned")
	outboxInserted, _ := meter.Int64Counter("live_events.outbox_inserted")
	outboxClaimed, _ := meter.Int64Counter("live_events.outbox_claimed")
	outboxFolded, _ := meter.Int64Counter("live_events.outbox_folded")
	pendingAge, _ := meter.Float64Histogram("live_events.outbox_oldest_pending_seconds", otelmetric.WithUnit("s"))
	revocationLatency, _ := meter.Float64Histogram("live_events.authorization_revocation_ms", otelmetric.WithUnit("ms"))
	return &liveMetrics{connections: connections, busReconnects: busReconnects, fanout: fanout, mailboxResync: mailboxResync, outboxPublished: outboxPublished, outboxRetried: outboxRetried, publishLatency: publishLatency, busBytes: busBytes, shardHealth: shardHealth, activeGroups: activeGroups, outboxFailed: outboxFailed, outboxCleaned: outboxCleaned, outboxInserted: outboxInserted, outboxClaimed: outboxClaimed, outboxFolded: outboxFolded, pendingAge: pendingAge, revocationLatency: revocationLatency}
}
func (m *liveMetrics) received(ctx context.Context, shard, bytes int) {
	m.busBytes.Add(ctx, int64(bytes), otelmetric.WithAttributes(attribute.Int("bus_shard", shard)))
}
func (m *liveMetrics) shardState(ctx context.Context, shard int, delta int64) {
	m.shardHealth.Add(ctx, delta, otelmetric.WithAttributes(attribute.Int("bus_shard", shard)))
}
func (m *liveMetrics) group(ctx context.Context, delta int64) { m.activeGroups.Add(ctx, delta) }
func (m *liveMetrics) failed(ctx context.Context, eventType string) {
	m.outboxFailed.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("event_type", eventType)))
}
func (m *liveMetrics) cleaned(ctx context.Context, count int64) { m.outboxCleaned.Add(ctx, count) }
func (m *liveMetrics) inserted(ctx context.Context)             { m.outboxInserted.Add(ctx, 1) }
func (m *liveMetrics) claimed(ctx context.Context, count int)   { m.outboxClaimed.Add(ctx, int64(count)) }
func (m *liveMetrics) folded(ctx context.Context)               { m.outboxFolded.Add(ctx, 1) }
func (m *liveMetrics) pending(ctx context.Context, age time.Duration) {
	m.pendingAge.Record(ctx, age.Seconds())
}
func (m *liveMetrics) revoked(ctx context.Context, changedAt time.Time) {
	m.revocationLatency.Record(ctx, float64(time.Since(changedAt).Microseconds())/1000)
}

var metrics = newLiveMetrics()

func (m *liveMetrics) connection(ctx context.Context, delta int64) { m.connections.Add(ctx, delta) }
func (m *liveMetrics) reconnect(ctx context.Context, shard int) {
	m.busReconnects.Add(ctx, 1, otelmetric.WithAttributes(attribute.Int("bus_shard", shard)))
}
func (m *liveMetrics) delivered(ctx context.Context, eventType string, count int) {
	m.fanout.Add(ctx, int64(count), otelmetric.WithAttributes(attribute.String("event_type", eventType)))
}
func (m *liveMetrics) resync(ctx context.Context, cause string) {
	m.mailboxResync.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("cause", cause)))
}
func (m *liveMetrics) published(ctx context.Context, eventType string, originatedAt time.Time) {
	attrs := otelmetric.WithAttributes(attribute.String("event_type", eventType))
	m.outboxPublished.Add(ctx, 1, attrs)
	m.publishLatency.Record(ctx, float64(time.Since(originatedAt).Microseconds())/1000, attrs)
}
func (m *liveMetrics) retried(ctx context.Context, eventType string) {
	m.outboxRetried.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("event_type", eventType)))
}
