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
	return &SlackbotMetrics{
		InboundEventsTotal:      inbound,
		SessionStartsTotal:      starts,
		OutboundMessagesTotal:   outbound,
		SlackAPIFailuresTotal:   failures,
		InteractionActionsTotal: actions,
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
