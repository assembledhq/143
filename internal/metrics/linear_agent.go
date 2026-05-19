package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// attrString is a tiny helper to build a single string attribute. Pulled
// out because the metrics package's other files use the longer
// attribute.Key("foo").String("bar") form, but the attribute package's
// own attribute.String("foo", "bar") shape reads better at the call
// sites in this file.
func attrString(key, value string) attribute.KeyValue {
	return attribute.String(key, value)
}

// LinearAgentMetrics holds OTel instruments for the inbound Linear agent
// path. The dispatcher and worker hot paths emit on every webhook
// delivery, so cardinality is bounded to event_type, action, and outcome
// — explicitly *not* org/issue/agent_session id (would explode the
// cardinality envelope to no operator benefit).
type LinearAgentMetrics struct {
	// EventsTotal counts inbound webhook deliveries by event_type +
	// action + outcome ("dispatched", "ignored", "feature_off"). The
	// outcome dimension is what an operator pivots on for "is the
	// agent path healthy?" without scanning logs.
	EventsTotal otelmetric.Int64Counter
	// SessionCreatedTotal counts 143 sessions created off the agent
	// path. Pairs with EventsTotal{outcome=dispatched} to surface the
	// dispatcher→worker conversion rate.
	SessionCreatedTotal otelmetric.Int64Counter
	// ActivitiesEmittedTotal counts AgentActivity emits by type.
	// Skipped emits (Reserve UNIQUE collision) are tracked under the
	// dedicated SkippedDuplicateTotal so operators can distinguish
	// "we sent" from "we deduped".
	ActivitiesEmittedTotal otelmetric.Int64Counter
	// SkippedDuplicateTotal counts activity emits short-circuited by
	// the at-most-once log. A high rate here suggests the milestone
	// fan-out is running too aggressively or replays are too frequent.
	SkippedDuplicateTotal otelmetric.Int64Counter
	// BootstrapEmitLatency records dispatcher → bootstrap-thought
	// emit latency in milliseconds. The Linear 10s SLA ceiling lives
	// at the high end; we want the p99 well under that.
	BootstrapEmitLatency otelmetric.Float64Histogram
}

// NewLinearAgentMetrics constructs and registers the OTel instruments.
func NewLinearAgentMetrics() (*LinearAgentMetrics, error) {
	meter := otel.Meter("github.com/assembledhq/143/linear_agent")

	events, err := meter.Int64Counter("linear_agent.events",
		otelmetric.WithDescription("Inbound Linear AgentSessionEvent / AppUserNotification webhook deliveries by outcome"),
		otelmetric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	created, err := meter.Int64Counter("linear_agent.sessions_created",
		otelmetric.WithDescription("Total 143 sessions created off the inbound agent path"),
		otelmetric.WithUnit("{session}"),
	)
	if err != nil {
		return nil, err
	}
	emitted, err := meter.Int64Counter("linear_agent.activities_emitted",
		otelmetric.WithDescription("AgentActivities emitted to Linear by type"),
		otelmetric.WithUnit("{activity}"),
	)
	if err != nil {
		return nil, err
	}
	skipped, err := meter.Int64Counter("linear_agent.activities_skipped_duplicate",
		otelmetric.WithDescription("AgentActivity emits short-circuited by the at-most-once log"),
		otelmetric.WithUnit("{activity}"),
	)
	if err != nil {
		return nil, err
	}
	bootstrap, err := meter.Float64Histogram("linear_agent.bootstrap_emit_latency_ms",
		otelmetric.WithDescription("Dispatcher→bootstrap-thought emit latency in milliseconds (Linear 10s SLA)"),
		otelmetric.WithUnit("ms"),
		otelmetric.WithExplicitBucketBoundaries(50, 100, 200, 500, 1000, 2000, 5000, 10000),
	)
	if err != nil {
		return nil, err
	}
	return &LinearAgentMetrics{
		EventsTotal:            events,
		SessionCreatedTotal:    created,
		ActivitiesEmittedTotal: emitted,
		SkippedDuplicateTotal:  skipped,
		BootstrapEmitLatency:   bootstrap,
	}, nil
}

// RecordEvent records one inbound webhook delivery. nil-safe so callers
// don't have to guard the metrics field.
func (m *LinearAgentMetrics) RecordEvent(ctx context.Context, eventType, action, outcome string) {
	if m == nil {
		return
	}
	m.EventsTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("event_type", eventType),
		attrString("action", action),
		attrString("outcome", outcome),
	))
}

// RecordSessionCreated records one successful session create.
func (m *LinearAgentMetrics) RecordSessionCreated(ctx context.Context, repoSource string) {
	if m == nil {
		return
	}
	m.SessionCreatedTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("repo_source", repoSource),
	))
}

// RecordActivityEmitted records one outbound AgentActivity emit. The
// `skipped` parameter routes to the dedupe counter so callers don't
// have to remember which counter to hit.
func (m *LinearAgentMetrics) RecordActivityEmitted(ctx context.Context, activityType string, skipped bool) {
	if m == nil {
		return
	}
	if skipped {
		m.SkippedDuplicateTotal.Add(ctx, 1, otelmetric.WithAttributes(
			attrString("activity_type", activityType),
		))
		return
	}
	m.ActivitiesEmittedTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("activity_type", activityType),
	))
}

// RecordBootstrapLatency records dispatcher→first-emit latency.
func (m *LinearAgentMetrics) RecordBootstrapLatency(ctx context.Context, latencyMS float64) {
	if m == nil {
		return
	}
	m.BootstrapEmitLatency.Record(ctx, latencyMS)
}
