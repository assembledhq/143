package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	sessionPublicationOnce    sync.Once
	sessionPublicationMetrics *SessionPublicationMetrics
)

// SessionPublicationMetrics exposes bounded-cardinality signals for the
// durable branch/PR publication state machine. Identifiers deliberately stay
// in structured logs; metrics only carry state, source, and outcome.
type SessionPublicationMetrics struct {
	TransitionsTotal          otelmetric.Int64Counter
	ReconciliationsTotal      otelmetric.Int64Counter
	AgentPRIntentsTotal       otelmetric.Int64Counter
	AgentPRIntentMissingTotal otelmetric.Int64Counter
}

func getSessionPublicationMetrics() *SessionPublicationMetrics {
	sessionPublicationOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/session_publication")
		transitions, err := meter.Int64Counter(
			"session_publication.transitions",
			otelmetric.WithDescription("Durable session publication checkpoints by state and source"),
			otelmetric.WithUnit("{transition}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		reconciliations, err := meter.Int64Counter(
			"session_publication.reconciliations",
			otelmetric.WithDescription("Session publication reconciliation attempts by outcome"),
			otelmetric.WithUnit("{attempt}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		agentPRIntents, err := meter.Int64Counter(
			"agent_pr_intents",
			otelmetric.WithDescription("Agent PR publication intents by source and outcome"),
			otelmetric.WithUnit("{intent}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		agentPRIntentMissing, err := meter.Int64Counter(
			"agent_pr_intent_missing",
			otelmetric.WithDescription("Eligible agent turns ending with a diff but no publication intent"),
			otelmetric.WithUnit("{turn}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		sessionPublicationMetrics = &SessionPublicationMetrics{
			TransitionsTotal:          transitions,
			ReconciliationsTotal:      reconciliations,
			AgentPRIntentsTotal:       agentPRIntents,
			AgentPRIntentMissingTotal: agentPRIntentMissing,
		}
	})
	return sessionPublicationMetrics
}

func RecordAgentPRIntentMissing(ctx context.Context, agentType, sessionOrigin string) {
	metrics := getSessionPublicationMetrics()
	if metrics == nil || metrics.AgentPRIntentMissingTotal == nil {
		return
	}
	metrics.AgentPRIntentMissingTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("agent_type", agentType),
		attrString("session_origin", sessionOrigin),
	))
}

func RecordAgentPRIntent(ctx context.Context, source, outcome string) {
	metrics := getSessionPublicationMetrics()
	if metrics == nil || metrics.AgentPRIntentsTotal == nil {
		return
	}
	metrics.AgentPRIntentsTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("source", source),
		attrString("outcome", outcome),
	))
}

func RecordSessionPublicationTransition(ctx context.Context, state, source string) {
	metrics := getSessionPublicationMetrics()
	if metrics == nil || metrics.TransitionsTotal == nil {
		return
	}
	metrics.TransitionsTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("state", state),
		attrString("source", source),
	))
}

func RecordSessionPublicationReconciliation(ctx context.Context, outcome string) {
	metrics := getSessionPublicationMetrics()
	if metrics == nil || metrics.ReconciliationsTotal == nil {
		return
	}
	metrics.ReconciliationsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("outcome", outcome)))
}
