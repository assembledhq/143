package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	codeReviewPolicyOnce   sync.Once
	codeReviewPolicyEvents otelmetric.Int64Counter
)

func getCodeReviewPolicyEvents() otelmetric.Int64Counter {
	codeReviewPolicyOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/code_review_policy")
		codeReviewPolicyEvents, _ = meter.Int64Counter("code_review_policy.events", otelmetric.WithUnit("{event}"))
	})
	return codeReviewPolicyEvents
}

// RecordCodeReviewPolicyEvent records bounded, privacy-safe adoption metadata.
// Prompt contents, excerpts, repository IDs, and organization IDs are never attributes.
func RecordCodeReviewPolicyEvent(ctx context.Context, event, scope, source, exampleKey, characterBucket, subsection string, configured bool) {
	counter := getCodeReviewPolicyEvents()
	if counter == nil {
		return
	}
	counter.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("event", event), attrString("scope", scope), attrString("source", source),
		attrString("example_key", exampleKey), attrString("character_bucket", characterBucket),
		attrString("subsection", subsection), attrString("configured", map[bool]string{true: "true", false: "false"}[configured]),
	))
}
