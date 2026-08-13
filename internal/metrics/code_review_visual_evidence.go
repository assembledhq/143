package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	codeReviewVisualEvidenceOnce sync.Once
	codeReviewVisualEvidence     struct {
		captures      otelmetric.Int64Counter
		images        otelmetric.Int64Counter
		satisfactions otelmetric.Int64Counter
		bytes         otelmetric.Int64Histogram
		fetchDuration otelmetric.Float64Histogram
	}
)

func initCodeReviewVisualEvidenceMetrics() {
	codeReviewVisualEvidenceOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/code_review_visual_evidence")
		captures, capturesErr := meter.Int64Counter("code_review.visual_evidence.captures", otelmetric.WithUnit("{capture}"))
		if capturesErr == nil {
			codeReviewVisualEvidence.captures = captures
		}
		images, imagesErr := meter.Int64Counter("code_review.visual_evidence.images", otelmetric.WithUnit("{image}"))
		if imagesErr == nil {
			codeReviewVisualEvidence.images = images
		}
		satisfactions, satisfactionsErr := meter.Int64Counter("code_review.visual_evidence.satisfactions", otelmetric.WithUnit("{satisfaction}"))
		if satisfactionsErr == nil {
			codeReviewVisualEvidence.satisfactions = satisfactions
		}
		bytesHistogram, bytesErr := meter.Int64Histogram("code_review.visual_evidence.image_bytes", otelmetric.WithUnit("By"))
		if bytesErr == nil {
			codeReviewVisualEvidence.bytes = bytesHistogram
		}
		fetchDuration, durationErr := meter.Float64Histogram("code_review.visual_evidence.fetch_duration", otelmetric.WithUnit("s"))
		if durationErr == nil {
			codeReviewVisualEvidence.fetchDuration = fetchDuration
		}
	})
}

// RecordCodeReviewVisualEvidenceSatisfaction records the bounded evidence
// class and GitHub surface used by a validated description assessment.
func RecordCodeReviewVisualEvidenceSatisfaction(ctx context.Context, basis, surface string) {
	initCodeReviewVisualEvidenceMetrics()
	if codeReviewVisualEvidence.satisfactions == nil {
		return
	}
	codeReviewVisualEvidence.satisfactions.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("basis", basis), attrString("surface", surface),
	))
}

// RecordCodeReviewVisualEvidenceCapture records bounded manifest-level state.
// Organization, repository, source URLs, and untrusted PR copy are never metric
// attributes.
func RecordCodeReviewVisualEvidenceCapture(ctx context.Context, discovered, persisted int, complete, overflow, restored bool) {
	initCodeReviewVisualEvidenceMetrics()
	if codeReviewVisualEvidence.captures == nil {
		return
	}
	codeReviewVisualEvidence.captures.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("complete", boolMetricValue(complete)),
		attrString("overflow", boolMetricValue(overflow)),
		attrString("restored", boolMetricValue(restored)),
		attrString("discovered_bucket", visualEvidenceCountBucket(discovered)),
		attrString("persisted_bucket", visualEvidenceCountBucket(persisted)),
	))
}

// RecordCodeReviewVisualEvidenceImage records one manifest item's bounded
// outcome without emitting its URL, author, caption, or tenant identifiers.
func RecordCodeReviewVisualEvidenceImage(ctx context.Context, surface, status, hostClass string, byteSize int64, durationSeconds float64, fetched, deduplicated bool) {
	initCodeReviewVisualEvidenceMetrics()
	attributes := otelmetric.WithAttributes(
		attrString("surface", surface), attrString("status", status), attrString("host_class", hostClass),
		attrString("fetched", boolMetricValue(fetched)), attrString("deduplicated", boolMetricValue(deduplicated)),
	)
	if codeReviewVisualEvidence.images != nil {
		codeReviewVisualEvidence.images.Add(ctx, 1, attributes)
	}
	if codeReviewVisualEvidence.bytes != nil && byteSize > 0 {
		codeReviewVisualEvidence.bytes.Record(ctx, byteSize, attributes)
	}
	if codeReviewVisualEvidence.fetchDuration != nil && durationSeconds > 0 {
		codeReviewVisualEvidence.fetchDuration.Record(ctx, durationSeconds, attributes)
	}
}

func boolMetricValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func visualEvidenceCountBucket(count int) string {
	switch {
	case count <= 0:
		return "0"
	case count == 1:
		return "1"
	case count <= 4:
		return "2_4"
	case count <= 16:
		return "5_16"
	case count <= 32:
		return "17_32"
	default:
		return "33_plus"
	}
}
