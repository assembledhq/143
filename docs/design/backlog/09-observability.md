# Design: Observability & Impact Measurement

> **Status:** Backlog | **Last reviewed:** 2026-05-06

This document describes how 143.dev measures the real-world impact of deployed fixes (Step 6 in the overall flow).

## Overview

After a fix is deployed, 143.dev automatically runs an experiment:

1. Capture baseline metrics from before the deploy
2. Wait for an observation window after the deploy
3. Compare before/after metrics
4. Classify the outcome
5. Close the customer loop (update issue status, report results)

## Experiment Lifecycle

```
Deploy detected
      │
      ▼
┌─────────────┐     ┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│   Capture   │────▶│   Observe    │────▶│   Evaluate   │────▶│   Classify   │
│   Baseline  │     │   Window     │     │   Metrics    │     │   Outcome    │
└─────────────┘     └──────────────┘     └──────────────┘     └──────────────┘
```

### States

| Status | Description |
|--------|-------------|
| `baseline` | Collecting pre-deploy metrics (retroactive) |
| `observing` | Waiting for post-deploy observation window to complete |
| `completed` | Evaluation done, outcome classified |

## Baseline Capture

When a deploy is detected, the system retroactively captures baseline metrics for the period before the deploy.

**Baseline window**: The 7 days before deploy (configurable per org).

Metrics are fetched from the same integrations that provided the issue:

### Sentry Metrics

Query Sentry's API for the specific issue:

```go
func (o *ObservabilityService) GetSentryBaseline(ctx context.Context, issue *models.Issue, window TimeWindow) (*SentryMetrics, error) {
    // GET /api/0/issues/{issue_id}/events/?start={window.Start}&end={window.End}
    // Count events, unique users, frequency
    return &SentryMetrics{
        ErrorCount:      eventCount,
        AffectedUsers:   uniqueUsers,
        ErrorsPerHour:   float64(eventCount) / window.Hours(),
    }
}
```

### Support Metrics

Query the support tool API for tickets matching the issue:

```go
func (o *ObservabilityService) GetSupportBaseline(ctx context.Context, issue *models.Issue, window TimeWindow) (*SupportMetrics, error) {
    // Search for tickets created in the window that match the issue fingerprint
    return &SupportMetrics{
        TicketCount:     ticketCount,
        TicketsPerDay:   float64(ticketCount) / window.Days(),
    }
}
```

### Datadog Metrics

When a Datadog integration is configured (`DD_API_KEY` and `DD_APP_KEY` set), the system can pull production metrics directly from Datadog for experiment evaluation. This is the recommended approach for teams already using Datadog for APM/infrastructure monitoring.

```go
func (o *ObservabilityService) GetDatadogBaseline(ctx context.Context, issue *models.Issue, window TimeWindow) (*DatadogMetrics, error) {
    // Use Datadog Metrics Query API:
    // POST https://api.datadoghq.com/api/v1/query
    // Query examples:
    //   avg:trace.http.request.errors{service:my-app}.as_rate()
    //   avg:trace.http.request.duration{service:my-app,resource_name:/api/users}.as_count()
    //   avg:system.cpu.user{host:prod-*}

    queries := o.buildDatadogQueries(issue, window)
    results, _ := o.ddClient.QueryMetrics(ctx, window.Start, window.End, queries)

    return &DatadogMetrics{
        ErrorRate:    extractSeries(results, "error_rate"),
        LatencyP50:  extractSeries(results, "latency_p50"),
        LatencyP99:  extractSeries(results, "latency_p99"),
        Throughput:  extractSeries(results, "throughput"),
    }
}
```

**Metric query configuration**: Admins configure which Datadog metrics to track per repo/service in the integration settings:

```json
{
  "provider": "datadog",
  "config": {
    "service_name": "my-api",
    "metrics": [
      {
        "name": "error_rate",
        "query": "sum:trace.http.request.errors{service:my-api}.as_rate()",
        "type": "lower_is_better"
      },
      {
        "name": "latency_p99",
        "query": "avg:trace.http.request.duration.by.resource_name.99p{service:my-api}",
        "type": "lower_is_better"
      },
      {
        "name": "throughput",
        "query": "sum:trace.http.request.hits{service:my-api}.as_rate()",
        "type": "higher_is_better"
      }
    ]
  }
}
```

This allows experiments to compare real production latency, error rates, and throughput before and after a fix deploys — far more granular than Sentry event counts alone.

### Custom Metrics (Push API)

Orgs can also push custom metrics via API for sources not covered by built-in integrations:

```
POST /api/v1/experiments/:id/metrics
{
  "type": "baseline",  // or "observation"
  "metrics": {
    "latency_p50_ms": 120,
    "latency_p99_ms": 890,
    "error_rate_pct": 2.3,
    "throughput_rps": 1500
  }
}
```

This supports integrating with Grafana, CloudWatch, or any other monitoring system via a simple webhook or script.

## Observation Window

After deploy, the system waits for the observation window to complete before evaluating.

**Observation window**: 7 days after deploy (configurable, must be at least 24 hours).

A scheduled job (`evaluate_experiment`) runs hourly and checks for experiments whose observation window has elapsed.

## Metric Collection

The same metric queries used for baseline are run for the observation window:

```go
type ExperimentMetrics struct {
    // Sentry metrics
    ErrorCount     int
    ErrorsPerHour  float64
    AffectedUsers  int

    // Support metrics
    TicketCount    int
    TicketsPerDay  float64

    // Datadog metrics (if integrated)
    DatadogMetrics map[string]float64  // e.g., "error_rate", "latency_p99", "throughput"

    // Custom metrics (if provided via push API)
    CustomMetrics  map[string]float64
}
```

## Evaluation & Comparison

The evaluator compares baseline vs observation metrics:

```go
type MetricComparison struct {
    MetricName      string
    BaselineValue   float64
    ObservationValue float64
    ChangePercent   float64   // negative = improvement
    IsImprovement   bool
}
```

### Change Calculation

```go
func computeChange(baseline, observation float64) float64 {
    if baseline == 0 {
        if observation == 0 {
            return 0
        }
        return 100 // went from 0 to something — regression
    }
    return ((observation - baseline) / baseline) * 100
}
```

### Significance Threshold

Changes must exceed a minimum threshold to be considered meaningful:

| Metric | Improvement Threshold | Regression Threshold |
|--------|-----------------------|---------------------|
| Error count | -20% | +10% |
| Error rate (per hour) | -20% | +10% |
| Affected users | -20% | +10% |
| Ticket count | -20% | +10% |
| Tickets per day | -20% | +10% |
| Latency (p50, p99) | -10% | +15% |
| Custom metrics | -15% | +10% |

Thresholds are configurable per org.

## Outcome Classification

Based on the metric comparisons, the experiment is classified:

### `success`

At least one primary metric (error rate OR ticket volume) improved beyond the threshold, and no metric regressed.

### `no_change`

No metrics changed beyond the threshold in either direction. The fix may have been correct but didn't materially move the needle (e.g., the issue was already low-frequency).

### `regression`

Any primary metric worsened beyond the regression threshold. This is flagged prominently in the UI and may trigger an alert.

### `inconclusive`

- Insufficient data (too few events in baseline or observation)
- Mixed signals (some metrics improved, others regressed)
- External factors may have influenced the data

```go
func (o *ObservabilityService) ClassifyOutcome(comparisons []MetricComparison) (string, string) {
    hasImprovement := false
    hasRegression := false
    hasSufficientData := true

    for _, c := range comparisons {
        if c.BaselineValue < minDataThreshold {
            hasSufficientData = false
            continue
        }
        if c.IsImprovement && abs(c.ChangePercent) > improvementThreshold {
            hasImprovement = true
        }
        if !c.IsImprovement && abs(c.ChangePercent) > regressionThreshold {
            hasRegression = true
        }
    }

    if !hasSufficientData {
        return "inconclusive", "Insufficient data for reliable comparison"
    }
    if hasRegression {
        return "regression", "One or more metrics worsened after deploy"
    }
    if hasImprovement {
        return "success", "Metrics improved after deploy"
    }
    return "no_change", "No significant change in metrics"
}
```

## Outcome Storage

The experiment record is updated with:

```json
{
  "baseline_metrics": {
    "error_count": 342,
    "errors_per_hour": 2.03,
    "affected_users": 89,
    "ticket_count": 12,
    "tickets_per_day": 1.7
  },
  "observation_metrics": {
    "error_count": 45,
    "errors_per_hour": 0.27,
    "affected_users": 11,
    "ticket_count": 2,
    "tickets_per_day": 0.29
  },
  "outcome": "success",
  "outcome_details": {
    "comparisons": [
      { "metric": "errors_per_hour", "baseline": 2.03, "observation": 0.27, "change_pct": -86.7, "improved": true },
      { "metric": "tickets_per_day", "baseline": 1.7, "observation": 0.29, "change_pct": -82.9, "improved": true }
    ],
    "summary": "Error rate dropped 87% and support ticket volume dropped 83% after deploy."
  }
}
```

## Closing the Customer Loop

After outcome classification:

### On Success

1. Update the issue status to `fixed` (if not already)
2. Add a comment to the GitHub PR with the impact summary
3. If the original source was a support ticket, optionally send a response (configurable):
   - "This issue has been fixed in the latest release. Error rate dropped 87%."
4. Log the success in the audit trail

### On Regression

1. Flag the experiment as a regression in the dashboard
2. Send an alert (Slack, email) to the configured notification channel
3. The admin may choose to revert the PR

### On No Change / Inconclusive

1. Log the result
2. The issue stays in `fixed` status but is marked as "impact unverified"
3. No automated customer communication

## Dashboard Integration

The experiments page shows:

- **Success rate**: % of experiments classified as `success`
- **Total impact**: aggregate metrics (total errors eliminated, tickets prevented)
- **Timeline**: chart of experiment outcomes over time
- **Per-experiment detail**: before/after metrics, outcome classification, linked PR and issue
- **Datadog link**: deep links to the relevant Datadog dashboard/APM traces for the observation window

## Logging in Mezmo

All experiment lifecycle events (baseline capture, observation start/end, outcome classification) are logged as structured events to Mezmo with the `component: "observability"` tag. This enables:

- Searching for all evaluation activity for a given issue or PR
- Alerting on experiment failures (e.g., unable to fetch Datadog metrics)
- Auditing experiment outcomes over time

## Production Feedback Loop

After outcome classification, the system triggers a production feedback analysis job. This job generates learnings from the production outcome that are injected into future agent runs for similar issues. See [18-fix-quality-feedback.md](18-fix-quality-feedback.md) for the full design.

When an experiment is classified as `no_change` or `regression`, an LLM analyzes why the fix was ineffective and generates a generalized learning. These learnings are stored in the `production_learnings` table and surfaced in agent prompts and in the `.143/learned-conventions.md` file.
