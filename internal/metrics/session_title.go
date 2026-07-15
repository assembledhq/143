package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	sessionTitleOnce     sync.Once
	sessionTitleDecision otelmetric.Int64Counter
)

func getSessionTitleDecisionCounter() otelmetric.Int64Counter {
	sessionTitleOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/session_title")
		sessionTitleDecision, _ = meter.Int64Counter("session_title.decisions", otelmetric.WithUnit("{decision}"))
	})
	return sessionTitleDecision
}

// RecordSessionTitleDecision records bounded-cardinality title outcomes. It
// intentionally excludes session and organization identifiers.
func RecordSessionTitleDecision(ctx context.Context, source, action string) {
	counter := getSessionTitleDecisionCounter()
	if counter == nil {
		return
	}
	counter.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("title_source", source),
		attrString("action", action),
	))
}
