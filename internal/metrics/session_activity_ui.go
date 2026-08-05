package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	sessionActivityUIOnce   sync.Once
	sessionActivityUIEvents otelmetric.Int64Counter
)

func getSessionActivityUIEvents() otelmetric.Int64Counter {
	sessionActivityUIOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/session_activity_ui")
		counter, err := meter.Int64Counter(
			"session_activity_ui.events",
			otelmetric.WithDescription("Privacy-safe session activity UI interactions and viewport integrity events"),
			otelmetric.WithUnit("{event}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		sessionActivityUIEvents = counter
	})
	return sessionActivityUIEvents
}

// RecordSessionActivityUIEvent records only bounded product/correctness
// dimensions. Transcript content, activity labels, commands, paths, and phase
// identifiers are deliberately absent from this contract.
func RecordSessionActivityUIEvent(ctx context.Context, event, detail, status, reason, trigger, viewportClass, toolCountBucket, durationBucket, valueBucket string) {
	counter := getSessionActivityUIEvents()
	if counter == nil {
		return
	}
	counter.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("event", event),
		attrString("detail", detail),
		attrString("status", status),
		attrString("reason", reason),
		attrString("trigger", trigger),
		attrString("viewport_class", viewportClass),
		attrString("tool_count_bucket", toolCountBucket),
		attrString("duration_bucket", durationBucket),
		attrString("value_bucket", valueBucket),
	))
}
