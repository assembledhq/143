package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

type PagerDutyMetrics struct {
	WebhookEventsTotal     otelmetric.Int64Counter
	IngestJobsTotal        otelmetric.Int64Counter
	AutomationMatchesTotal otelmetric.Int64Counter
	WritebacksTotal        otelmetric.Int64Counter
	APIRequestsTotal       otelmetric.Int64Counter
}

func NewPagerDutyMetrics() (*PagerDutyMetrics, error) {
	return newPagerDutyMetrics(otel.Meter("github.com/assembledhq/143/pagerduty"))
}

func newPagerDutyMetrics(meter otelmetric.Meter) (*PagerDutyMetrics, error) {
	webhooks, err := meter.Int64Counter("pagerduty_webhook_events_total",
		otelmetric.WithDescription("PagerDuty webhook events by normalized event type and result"),
		otelmetric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	ingestJobs, err := meter.Int64Counter("pagerduty_ingest_jobs_total",
		otelmetric.WithDescription("PagerDuty ingest job outcomes"),
		otelmetric.WithUnit("{job}"),
	)
	if err != nil {
		return nil, err
	}
	automationMatches, err := meter.Int64Counter("pagerduty_automation_matches_total",
		otelmetric.WithDescription("PagerDuty automation trigger match outcomes by event type"),
		otelmetric.WithUnit("{match}"),
	)
	if err != nil {
		return nil, err
	}
	writebacks, err := meter.Int64Counter("pagerduty_writebacks_total",
		otelmetric.WithDescription("PagerDuty writeback outcomes by writeback kind"),
		otelmetric.WithUnit("{writeback}"),
	)
	if err != nil {
		return nil, err
	}
	apiRequests, err := meter.Int64Counter("pagerduty_api_requests_total",
		otelmetric.WithDescription("PagerDuty REST API request outcomes by endpoint"),
		otelmetric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}
	return &PagerDutyMetrics{
		WebhookEventsTotal:     webhooks,
		IngestJobsTotal:        ingestJobs,
		AutomationMatchesTotal: automationMatches,
		WritebacksTotal:        writebacks,
		APIRequestsTotal:       apiRequests,
	}, nil
}

func (m *PagerDutyMetrics) RecordWebhookEvent(ctx context.Context, eventType, result string) {
	if m == nil {
		return
	}
	m.WebhookEventsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("event_type", eventType), attrString("result", result)))
}

func (m *PagerDutyMetrics) RecordIngestJob(ctx context.Context, result string) {
	if m == nil {
		return
	}
	m.IngestJobsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("result", result)))
}

func (m *PagerDutyMetrics) RecordAutomationMatch(ctx context.Context, eventType, result string) {
	if m == nil {
		return
	}
	m.AutomationMatchesTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("event_type", eventType), attrString("result", result)))
}

func (m *PagerDutyMetrics) RecordWriteback(ctx context.Context, kind, result string) {
	if m == nil {
		return
	}
	m.WritebacksTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("kind", kind), attrString("result", result)))
}

func (m *PagerDutyMetrics) RecordAPIRequest(ctx context.Context, endpoint, result string) {
	if m == nil {
		return
	}
	m.APIRequestsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("endpoint", endpoint), attrString("result", result)))
}
