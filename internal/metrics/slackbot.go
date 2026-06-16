package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

type SlackbotMetrics struct {
	InboundEventsTotal      otelmetric.Int64Counter
	SessionStartsTotal      otelmetric.Int64Counter
	OutboundMessagesTotal   otelmetric.Int64Counter
	SlackAPIFailuresTotal   otelmetric.Int64Counter
	InteractionActionsTotal otelmetric.Int64Counter
	RateLimitsTotal         otelmetric.Int64Counter
	DroppedUpdatesTotal     otelmetric.Int64Counter
	DedupeHitsTotal         otelmetric.Int64Counter
	InstallHealthTotal      otelmetric.Int64Counter
	MissingScopesTotal      otelmetric.Int64Counter
	SignatureFailuresTotal  otelmetric.Int64Counter
	CallbackLatency         otelmetric.Float64Histogram
	MessageUpdateLatency    otelmetric.Float64Histogram
}

func NewSlackbotMetrics() (*SlackbotMetrics, error) {
	meter := otel.Meter("github.com/assembledhq/143/slackbot")
	inbound, err := meter.Int64Counter("slackbot.inbound_events")
	if err != nil {
		return nil, err
	}
	starts, err := meter.Int64Counter("slackbot.session_starts")
	if err != nil {
		return nil, err
	}
	outbound, err := meter.Int64Counter("slackbot.outbound_messages")
	if err != nil {
		return nil, err
	}
	failures, err := meter.Int64Counter("slackbot.api_failures")
	if err != nil {
		return nil, err
	}
	actions, err := meter.Int64Counter("slackbot.interaction_actions")
	if err != nil {
		return nil, err
	}
	rateLimits, err := meter.Int64Counter("slackbot.rate_limits",
		otelmetric.WithDescription("Slack rate-limit signals observed by source"),
		otelmetric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	droppedUpdates, err := meter.Int64Counter("slackbot.dropped_updates")
	if err != nil {
		return nil, err
	}
	dedupeHits, err := meter.Int64Counter("slackbot.dedupe_hits")
	if err != nil {
		return nil, err
	}
	installHealth, err := meter.Int64Counter("slackbot.install_health")
	if err != nil {
		return nil, err
	}
	missingScopes, err := meter.Int64Counter("slackbot.missing_scopes")
	if err != nil {
		return nil, err
	}
	signatureFailures, err := meter.Int64Counter("slackbot.signature_failures")
	if err != nil {
		return nil, err
	}
	callbackLatency, err := meter.Float64Histogram("slackbot.callback_latency_ms",
		otelmetric.WithDescription("Latency for inbound Slack callbacks"),
		otelmetric.WithUnit("ms"),
		otelmetric.WithExplicitBucketBoundaries(25, 50, 100, 250, 500, 1000, 2500, 5000, 10000),
	)
	if err != nil {
		return nil, err
	}
	updateLatency, err := meter.Float64Histogram("slackbot.message_update_latency_ms",
		otelmetric.WithDescription("Latency for Slack message update API calls"),
		otelmetric.WithUnit("ms"),
		otelmetric.WithExplicitBucketBoundaries(25, 50, 100, 250, 500, 1000, 2500, 5000, 10000),
	)
	if err != nil {
		return nil, err
	}
	return &SlackbotMetrics{
		InboundEventsTotal:      inbound,
		SessionStartsTotal:      starts,
		OutboundMessagesTotal:   outbound,
		SlackAPIFailuresTotal:   failures,
		InteractionActionsTotal: actions,
		RateLimitsTotal:         rateLimits,
		DroppedUpdatesTotal:     droppedUpdates,
		DedupeHitsTotal:         dedupeHits,
		InstallHealthTotal:      installHealth,
		MissingScopesTotal:      missingScopes,
		SignatureFailuresTotal:  signatureFailures,
		CallbackLatency:         callbackLatency,
		MessageUpdateLatency:    updateLatency,
	}, nil
}

func (m *SlackbotMetrics) RecordInboundEvent(ctx context.Context, eventType, outcome string) {
	if m == nil {
		return
	}
	m.InboundEventsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("event_type", eventType), attrString("outcome", outcome)))
}

func (m *SlackbotMetrics) RecordSessionStart(ctx context.Context, source, outcome string) {
	if m == nil {
		return
	}
	m.SessionStartsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("source", source), attrString("outcome", outcome)))
}

func (m *SlackbotMetrics) RecordOutboundMessage(ctx context.Context, kind, outcome string) {
	if m == nil {
		return
	}
	m.OutboundMessagesTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("kind", kind), attrString("outcome", outcome)))
}

func (m *SlackbotMetrics) RecordAPIFailure(ctx context.Context, method string) {
	if m == nil {
		return
	}
	m.SlackAPIFailuresTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("method", method)))
}

func (m *SlackbotMetrics) RecordInteractionAction(ctx context.Context, actionID, outcome string) {
	if m == nil {
		return
	}
	m.InteractionActionsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("action_id", actionID), attrString("outcome", outcome)))
}

func (m *SlackbotMetrics) RecordRateLimit(ctx context.Context, source string) {
	if m == nil {
		return
	}
	m.RateLimitsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("source", source)))
}

func (m *SlackbotMetrics) RecordDroppedUpdate(ctx context.Context, updateKind, reason string) {
	if m == nil {
		return
	}
	m.DroppedUpdatesTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("update_kind", updateKind), attrString("reason", reason)))
}

func (m *SlackbotMetrics) RecordDedupeHit(ctx context.Context, source string) {
	if m == nil {
		return
	}
	m.DedupeHitsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("source", source)))
}

func (m *SlackbotMetrics) RecordInstallHealth(ctx context.Context, status string) {
	if m == nil {
		return
	}
	m.InstallHealthTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("status", status)))
}

func (m *SlackbotMetrics) RecordMissingScope(ctx context.Context, scope string) {
	if m == nil {
		return
	}
	m.MissingScopesTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("scope", scope)))
}

func (m *SlackbotMetrics) RecordSignatureFailure(ctx context.Context, reason string) {
	if m == nil {
		return
	}
	m.SignatureFailuresTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("reason", reason)))
}

func (m *SlackbotMetrics) RecordCallbackLatency(ctx context.Context, callbackType, outcome string, latencyMS float64) {
	if m == nil {
		return
	}
	m.CallbackLatency.Record(ctx, latencyMS, otelmetric.WithAttributes(attrString("callback_type", callbackType), attrString("outcome", outcome)))
}

func (m *SlackbotMetrics) RecordMessageUpdateLatency(ctx context.Context, method, outcome string, latencyMS float64) {
	if m == nil {
		return
	}
	m.MessageUpdateLatency.Record(ctx, latencyMS, otelmetric.WithAttributes(attrString("method", method), attrString("outcome", outcome)))
}
