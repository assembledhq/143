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
	TransitionsTotal     otelmetric.Int64Counter
	ReconciliationsTotal otelmetric.Int64Counter
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
		sessionPublicationMetrics = &SessionPublicationMetrics{
			TransitionsTotal:     transitions,
			ReconciliationsTotal: reconciliations,
		}
	})
	return sessionPublicationMetrics
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
