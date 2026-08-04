package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestActivityPhaseMetricsInboxReconciliationSeparatesRunsFromBatches(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()), "test meter provider should shut down cleanly")
	})
	meter := provider.Meter("activity-phase-metrics-test")
	runs, err := meter.Int64Counter("test.inbox_batch_reconciliation_runs", metric.WithUnit("{run}"))
	require.NoError(t, err, "test should create the reconciliation run counter")
	batches, err := meter.Int64Counter("test.inbox_batches_reconciled", metric.WithUnit("{batch}"))
	require.NoError(t, err, "test should create the reconciled batch counter")
	phaseMetrics := &ActivityPhaseMetrics{
		InboxBatchReconciliationRuns: runs,
		InboxBatchesReconciled:       batches,
	}

	ctx := context.Background()
	phaseMetrics.recordInboxDeliveryBatchReconciliation(ctx, "completed", 0)
	phaseMetrics.recordInboxDeliveryBatchReconciliation(ctx, "failed", 0)
	phaseMetrics.recordInboxDeliveryBatchReconciliation(ctx, "abandoned", 3)

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &collected), "test should collect activity phase reconciliation metrics")
	require.Equal(t, int64(3), metricInt64Sum(collected, "test.inbox_batch_reconciliation_runs"), "every reconciliation attempt should count as one run")
	require.Equal(t, int64(3), metricInt64Sum(collected, "test.inbox_batches_reconciled"), "only actual abandoned batches should contribute to the batch count")
}

func TestActivityPhaseMetricsRecordsMissingAssociations(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()), "test meter provider should shut down cleanly")
	})
	counter, err := provider.Meter("activity-phase-association-test").Int64Counter("test.missing_associations", metric.WithUnit("{entry}"))
	require.NoError(t, err, "test should create the missing association counter")
	phaseMetrics := &ActivityPhaseMetrics{MissingPhaseAssociations: counter}

	phaseMetrics.recordMissingActivityPhaseAssociations(context.Background(), "tool_use", 2)
	phaseMetrics.recordMissingActivityPhaseAssociations(context.Background(), "message", 0)

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected), "test should collect missing association metrics")
	require.Equal(t, int64(2), metricInt64Sum(collected, "test.missing_associations"), "only missing expected associations should be counted")
}

func metricInt64Sum(resource metricdata.ResourceMetrics, name string) int64 {
	var total int64
	for _, scope := range resource.ScopeMetrics {
		for _, observed := range scope.Metrics {
			if observed.Name != name {
				continue
			}
			sum, ok := observed.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, point := range sum.DataPoints {
				total += point.Value
			}
		}
	}
	return total
}
