package cache

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetrics_NewAndRecorders(t *testing.T) {
	t.Parallel()

	metrics, err := NewMetrics()
	require.NoError(t, err, "metric construction should succeed")
	require.NotNil(t, metrics, "metric construction should return a metrics bundle")

	require.NotPanics(t, func() {
		metrics.RecordCommand(context.Background(), "xadd", 0.01)
		metrics.RecordFallback(context.Background(), "polling")
		metrics.RecordCleanupBatch(context.Background(), 5)
		metrics.RecordLogEntryBytes(context.Background(), 42)
	}, "recorders should accept observations")

	var nilMetrics *Metrics
	require.NotPanics(t, func() {
		nilMetrics.RecordCommand(context.Background(), "ping", 0.01)
		nilMetrics.RecordFallback(context.Background(), "disabled")
		nilMetrics.RecordCleanupBatch(context.Background(), 1)
		nilMetrics.RecordLogEntryBytes(context.Background(), 1)
	}, "nil metrics receiver should be a no-op")
}
