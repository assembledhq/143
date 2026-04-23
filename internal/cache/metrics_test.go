package cache

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
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

type failingMeter struct {
	noop.Meter
	failAt int
	calls  int
}

func (m *failingMeter) nextErr() error {
	m.calls++
	if m.calls == m.failAt {
		return errors.New("instrument init failed")
	}
	return nil
}

func (m *failingMeter) Int64Counter(string, ...otelmetric.Int64CounterOption) (otelmetric.Int64Counter, error) {
	if err := m.nextErr(); err != nil {
		return nil, err
	}
	return noop.Int64Counter{}, nil
}

func (m *failingMeter) Float64Histogram(string, ...otelmetric.Float64HistogramOption) (otelmetric.Float64Histogram, error) {
	if err := m.nextErr(); err != nil {
		return nil, err
	}
	return noop.Float64Histogram{}, nil
}

func (m *failingMeter) Int64Histogram(string, ...otelmetric.Int64HistogramOption) (otelmetric.Int64Histogram, error) {
	if err := m.nextErr(); err != nil {
		return nil, err
	}
	return noop.Int64Histogram{}, nil
}

func TestMetrics_NewMetrics_InstrumentInitFailures(t *testing.T) {
	t.Parallel()

	for _, failAt := range []int{1, 2, 3, 4, 5} {
		meter := &failingMeter{failAt: failAt}
		metrics, err := newMetrics(meter)
		require.Error(t, err, "constructor should surface instrument initialization failures")
		require.Nil(t, metrics, "constructor should not return a partially initialized metrics bundle")
	}
}
