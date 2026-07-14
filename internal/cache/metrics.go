package cache

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	attrRedisCommand = attribute.Key("redis.command")
	attrFallback     = attribute.Key("redis.fallback.reason")
)

type Metrics struct {
	CommandsTotal    otelmetric.Int64Counter
	CommandDuration  otelmetric.Float64Histogram
	FallbackTotal    otelmetric.Int64Counter
	CleanupBatchSize otelmetric.Int64Histogram
	LogEntryBytes    otelmetric.Int64Histogram
	SessionReaders   otelmetric.Int64UpDownCounter
}

func NewMetrics() (*Metrics, error) {
	return newMetrics(otel.Meter("github.com/assembledhq/143/redis"))
}

func newMetrics(meter otelmetric.Meter) (*Metrics, error) {
	commands, err := meter.Int64Counter("redis.commands",
		otelmetric.WithDescription("Total Redis command calls"),
	)
	if err != nil {
		return nil, err
	}
	duration, err := meter.Float64Histogram("redis.command.duration",
		otelmetric.WithDescription("Redis command latency"),
		otelmetric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	fallbacks, err := meter.Int64Counter("redis.fallbacks",
		otelmetric.WithDescription("Redis fallback activations"),
	)
	if err != nil {
		return nil, err
	}
	cleanup, err := meter.Int64Histogram("redis.cleanup.batch_size",
		otelmetric.WithDescription("Redis cleanup batch sizes"),
	)
	if err != nil {
		return nil, err
	}
	entryBytes, err := meter.Int64Histogram("session.log.entry.bytes",
		otelmetric.WithDescription("Size of Redis-published session log entries after truncation"),
		otelmetric.WithUnit("By"),
	)
	if err != nil {
		return nil, err
	}
	readers, err := meter.Int64UpDownCounter("session.stream.active_readers", otelmetric.WithDescription("Active blocking Redis readers for session resource streams"))
	if err != nil {
		return nil, err
	}

	return &Metrics{
		CommandsTotal:    commands,
		CommandDuration:  duration,
		FallbackTotal:    fallbacks,
		CleanupBatchSize: cleanup,
		LogEntryBytes:    entryBytes,
		SessionReaders:   readers,
	}, nil
}

func (m *Metrics) RecordSessionReader(ctx context.Context, kind string, delta int64) {
	if m == nil {
		return
	}
	m.SessionReaders.Add(ctx, delta, otelmetric.WithAttributes(attribute.String("stream_kind", kind)))
}

func (m *Metrics) RecordCommand(ctx context.Context, command string, durationSeconds float64) {
	if m == nil {
		return
	}
	attrs := otelmetric.WithAttributes(attrRedisCommand.String(command))
	m.CommandsTotal.Add(ctx, 1, attrs)
	m.CommandDuration.Record(ctx, durationSeconds, attrs)
}

func (m *Metrics) RecordFallback(ctx context.Context, reason string) {
	if m == nil {
		return
	}
	m.FallbackTotal.Add(ctx, 1, otelmetric.WithAttributes(attrFallback.String(reason)))
}

func (m *Metrics) RecordCleanupBatch(ctx context.Context, size int) {
	if m == nil {
		return
	}
	m.CleanupBatchSize.Record(ctx, int64(size))
}

func (m *Metrics) RecordLogEntryBytes(ctx context.Context, size int) {
	if m == nil {
		return
	}
	m.LogEntryBytes.Record(ctx, int64(size))
}
