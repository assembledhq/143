package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	prAutoRepairOnce    sync.Once
	prAutoRepairMetrics *PRAutoRepairMetrics
)

type PRAutoRepairMetrics struct {
	DecisionsTotal otelmetric.Int64Counter
	OutcomesTotal  otelmetric.Int64Counter
	StopsTotal     otelmetric.Int64Counter
	RegretsTotal   otelmetric.Int64Counter
}

func getPRAutoRepairMetrics() *PRAutoRepairMetrics {
	prAutoRepairOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/pr_auto_repair")
		decisions, _ := meter.Int64Counter("pr_auto_repair.decisions", otelmetric.WithUnit("{decision}"))
		outcomes, _ := meter.Int64Counter("pr_auto_repair.outcomes", otelmetric.WithUnit("{outcome}"))
		stops, _ := meter.Int64Counter("pr_auto_repair.stops", otelmetric.WithUnit("{stop}"))
		regrets, _ := meter.Int64Counter("pr_auto_repair.regrets", otelmetric.WithUnit("{regret}"))
		prAutoRepairMetrics = &PRAutoRepairMetrics{
			DecisionsTotal: decisions,
			OutcomesTotal:  outcomes,
			StopsTotal:     stops,
			RegretsTotal:   regrets,
		}
	})
	return prAutoRepairMetrics
}

func RecordPRAutoRepairDecision(ctx context.Context, orgID, repository, action, status, reason string) {
	m := getPRAutoRepairMetrics()
	if m == nil || m.DecisionsTotal == nil {
		return
	}
	m.DecisionsTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("org_id", orgID),
		attrString("repository", repository),
		attrString("action_type", action),
		attrString("status", status),
		attrString("reason", reason),
	))
}

func RecordPRAutoRepairOutcome(ctx context.Context, orgID, repository, action, outcome string) {
	m := getPRAutoRepairMetrics()
	if m == nil || m.OutcomesTotal == nil {
		return
	}
	m.OutcomesTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("org_id", orgID),
		attrString("repository", repository),
		attrString("action_type", action),
		attrString("outcome", outcome),
	))
}

func RecordPRAutoRepairStop(ctx context.Context, orgID, repository string) {
	m := getPRAutoRepairMetrics()
	if m == nil || m.StopsTotal == nil {
		return
	}
	m.StopsTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("org_id", orgID),
		attrString("repository", repository),
	))
}

func RecordPRAutoRepairRegret(ctx context.Context, orgID, repository, action, reason string) {
	m := getPRAutoRepairMetrics()
	if m == nil || m.RegretsTotal == nil {
		return
	}
	m.RegretsTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("org_id", orgID),
		attrString("repository", repository),
		attrString("action_type", action),
		attrString("reason", reason),
	))
}
