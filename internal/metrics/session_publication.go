package metrics

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
	PrePRReviewLoopsTotal     otelmetric.Int64Counter
	PrePRReviewFixesTotal     otelmetric.Int64Counter
	PrePRReviewStaleTotal     otelmetric.Int64Counter
	AutomaticPROverridesTotal otelmetric.Int64Counter
	PublicationFailuresTotal  otelmetric.Int64Counter
	PublicationLatency        otelmetric.Float64Histogram
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
		prePRReviewLoops, err := meter.Int64Counter("pre_pr_review_loops", otelmetric.WithDescription("Pre-PR review loops by terminal outcome and pass count"), otelmetric.WithUnit("{loop}"))
		if err != nil {
			otel.Handle(err)
			return
		}
		prePRReviewFixes, err := meter.Int64Counter("pre_pr_review_fixes", otelmetric.WithDescription("Pre-PR review loops that changed the workspace"), otelmetric.WithUnit("{fix}"))
		if err != nil {
			otel.Handle(err)
			return
		}
		prePRReviewStale, err := meter.Int64Counter("pre_pr_review_stale", otelmetric.WithDescription("Pre-PR review evidence invalidated by workspace movement"), otelmetric.WithUnit("{review}"))
		if err != nil {
			otel.Handle(err)
			return
		}
		automaticPROverrides, err := meter.Int64Counter("automatic_pr_manual_override", otelmetric.WithDescription("Manual overrides of automatic PR policy by direction"), otelmetric.WithUnit("{override}"))
		if err != nil {
			otel.Handle(err)
			return
		}
		publicationFailures, err := meter.Int64Counter("pr_publication_failures", otelmetric.WithDescription("PR publication failures by terminality"), otelmetric.WithUnit("{failure}"))
		if err != nil {
			otel.Handle(err)
			return
		}
		publicationLatency, err := meter.Float64Histogram("pr_publication_after_review_seconds", otelmetric.WithDescription("Seconds from publication intent to completed PR publication"), otelmetric.WithUnit("s"))
		if err != nil {
			otel.Handle(err)
			return
		}
		sessionPublicationMetrics = &SessionPublicationMetrics{
			TransitionsTotal:          transitions,
			ReconciliationsTotal:      reconciliations,
			AgentPRIntentsTotal:       agentPRIntents,
			AgentPRIntentMissingTotal: agentPRIntentMissing,
			PrePRReviewLoopsTotal:     prePRReviewLoops,
			PrePRReviewFixesTotal:     prePRReviewFixes,
			PrePRReviewStaleTotal:     prePRReviewStale,
			AutomaticPROverridesTotal: automaticPROverrides,
			PublicationFailuresTotal:  publicationFailures,
			PublicationLatency:        publicationLatency,
		}
	})
	return sessionPublicationMetrics
}

func RecordPrePRReviewLoop(ctx context.Context, outcome string, passes int) {
	metrics := getSessionPublicationMetrics()
	if metrics == nil || metrics.PrePRReviewLoopsTotal == nil {
		return
	}
	metrics.PrePRReviewLoopsTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("outcome", outcome),
		attribute.Int("passes", passes),
	))
}

func RecordPrePRReviewFix(ctx context.Context) {
	metrics := getSessionPublicationMetrics()
	if metrics != nil && metrics.PrePRReviewFixesTotal != nil {
		metrics.PrePRReviewFixesTotal.Add(ctx, 1)
	}
}

func RecordPrePRReviewStale(ctx context.Context) {
	metrics := getSessionPublicationMetrics()
	if metrics != nil && metrics.PrePRReviewStaleTotal != nil {
		metrics.PrePRReviewStaleTotal.Add(ctx, 1)
	}
}

func RecordAutomaticPRManualOverride(ctx context.Context, direction string) {
	metrics := getSessionPublicationMetrics()
	if metrics != nil && metrics.AutomaticPROverridesTotal != nil {
		metrics.AutomaticPROverridesTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("direction", direction)))
	}
}

func RecordPRPublicationFailure(ctx context.Context, terminal bool) {
	metrics := getSessionPublicationMetrics()
	if metrics != nil && metrics.PublicationFailuresTotal != nil {
		metrics.PublicationFailuresTotal.Add(ctx, 1, otelmetric.WithAttributes(attribute.Bool("terminal", terminal)))
	}
}

func RecordPRPublicationLatency(ctx context.Context, elapsed time.Duration) {
	metrics := getSessionPublicationMetrics()
	if metrics != nil && metrics.PublicationLatency != nil {
		metrics.PublicationLatency.Record(ctx, elapsed.Seconds())
	}
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
