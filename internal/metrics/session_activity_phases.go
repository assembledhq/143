package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	activityPhaseOnce    sync.Once
	activityPhaseMetrics *ActivityPhaseMetrics
)

type ActivityPhaseMetrics struct {
	StartsTotal                  otelmetric.Int64Counter
	TerminalTransitionsTotal     otelmetric.Int64Counter
	StrandedPhasesTotal          otelmetric.Int64Counter
	ReconciliationRuns           otelmetric.Int64Counter
	ReconciledPhases             otelmetric.Int64Counter
	InboxBatchReconciliationRuns otelmetric.Int64Counter
	InboxBatchesReconciled       otelmetric.Int64Counter
}

func getActivityPhaseMetrics() *ActivityPhaseMetrics {
	activityPhaseOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/session_activity_phases")
		starts, err := meter.Int64Counter(
			"session_activity_phase.starts",
			otelmetric.WithDescription("Durable activity phase starts by trigger kind"),
			otelmetric.WithUnit("{phase}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		terminals, err := meter.Int64Counter(
			"session_activity_phase.terminal_transitions",
			otelmetric.WithDescription("Durable activity phase terminal transitions by status and boundary reason"),
			otelmetric.WithUnit("{phase}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		stranded, err := meter.Int64Counter(
			"session_activity_phase.stranded_detected",
			otelmetric.WithDescription("Running activity phases detected without a valid runtime lease"),
			otelmetric.WithUnit("{phase}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		runs, err := meter.Int64Counter(
			"session_activity_phase.reconciliation_runs",
			otelmetric.WithDescription("Bounded stranded activity-phase reconciliation runs by outcome"),
			otelmetric.WithUnit("{run}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		reconciled, err := meter.Int64Counter(
			"session_activity_phase.reconciled",
			otelmetric.WithDescription("Running activity phases terminally reconciled after runtime loss"),
			otelmetric.WithUnit("{phase}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		batchRuns, err := meter.Int64Counter(
			"session_activity_phase.inbox_batch_reconciliation_runs",
			otelmetric.WithDescription("Acknowledged inbox delivery batch reconciliation runs by outcome"),
			otelmetric.WithUnit("{run}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		batches, err := meter.Int64Counter(
			"session_activity_phase.inbox_batches_reconciled",
			otelmetric.WithDescription("Acknowledged inbox delivery batches terminally reconciled"),
			otelmetric.WithUnit("{batch}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		activityPhaseMetrics = &ActivityPhaseMetrics{
			StartsTotal:                  starts,
			TerminalTransitionsTotal:     terminals,
			StrandedPhasesTotal:          stranded,
			ReconciliationRuns:           runs,
			ReconciledPhases:             reconciled,
			InboxBatchReconciliationRuns: batchRuns,
			InboxBatchesReconciled:       batches,
		}
	})
	return activityPhaseMetrics
}

func RecordActivityPhaseStarted(ctx context.Context, triggerKind string) {
	metrics := getActivityPhaseMetrics()
	if metrics != nil && metrics.StartsTotal != nil {
		metrics.StartsTotal.Add(ctx, 1, otelmetric.WithAttributes(attrString("trigger_kind", triggerKind)))
	}
}

func RecordActivityPhaseTerminal(ctx context.Context, status, reason string) {
	metrics := getActivityPhaseMetrics()
	if metrics != nil && metrics.TerminalTransitionsTotal != nil {
		metrics.TerminalTransitionsTotal.Add(ctx, 1, otelmetric.WithAttributes(
			attrString("status", status),
			attrString("reason", reason),
		))
	}
}

func RecordStrandedActivityPhases(ctx context.Context, count int64) {
	metrics := getActivityPhaseMetrics()
	if metrics != nil && metrics.StrandedPhasesTotal != nil && count > 0 {
		metrics.StrandedPhasesTotal.Add(ctx, count)
	}
}

func RecordInboxDeliveryBatchReconciliation(ctx context.Context, outcome string, count int64) {
	metrics := getActivityPhaseMetrics()
	if metrics == nil {
		return
	}
	metrics.recordInboxDeliveryBatchReconciliation(ctx, outcome, count)
}

func (metrics *ActivityPhaseMetrics) recordInboxDeliveryBatchReconciliation(ctx context.Context, outcome string, count int64) {
	if metrics.InboxBatchReconciliationRuns != nil {
		metrics.InboxBatchReconciliationRuns.Add(ctx, 1, otelmetric.WithAttributes(attrString("outcome", outcome)))
	}
	if count > 0 && metrics.InboxBatchesReconciled != nil {
		metrics.InboxBatchesReconciled.Add(ctx, count)
	}
}

func RecordActivityPhaseReconciliation(ctx context.Context, outcome string, reconciled int64) {
	metrics := getActivityPhaseMetrics()
	if metrics == nil {
		return
	}
	if metrics.ReconciliationRuns != nil {
		metrics.ReconciliationRuns.Add(ctx, 1, otelmetric.WithAttributes(attrString("outcome", outcome)))
	}
	if reconciled > 0 && metrics.ReconciledPhases != nil {
		metrics.ReconciledPhases.Add(ctx, reconciled)
	}
}
