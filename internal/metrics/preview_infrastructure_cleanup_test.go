package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestPreviewInfrastructureCleanupMetricsRecord(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		failed   bool
		expected map[string]int64
	}{
		{name: "successful pass", expected: map[string]int64{"pending": 2, "success": 1}},
		{name: "failed pass", failed: true, expected: map[string]int64{"pending": 2, "error": 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() {
				require.NoError(t, provider.Shutdown(context.Background()), "isolated cleanup metric provider should shut down")
			})
			m, err := newPreviewInfrastructureCleanupMetrics(provider.Meter("preview-cleanup-test"))
			require.NoError(t, err, "cleanup instruments should initialize with an isolated meter")
			m.Record(context.Background(), 2, tt.failed)
			var collected metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(context.Background(), &collected), "cleanup observations should be collectable")
			actual := map[string]int64{}
			for _, scope := range collected.ScopeMetrics {
				for _, observed := range scope.Metrics {
					switch observed.Name {
					case "preview.infrastructure.cleanup.pending":
						gauge, ok := observed.Data.(metricdata.Gauge[int64])
						require.True(t, ok, "pending cleanup should report a gauge")
						for _, point := range gauge.DataPoints {
							require.Equal(t, 0, point.Attributes.Len(), "pending cleanup must not expose resource or tenant identities")
							actual["pending"] = point.Value
						}
					case "preview.infrastructure.cleanup.passes":
						sum, ok := observed.Data.(metricdata.Sum[int64])
						require.True(t, ok, "cleanup passes should report a counter")
						for _, point := range sum.DataPoints {
							require.Equal(t, 1, point.Attributes.Len(), "cleanup pass attributes should contain only the bounded outcome")
							result, ok := point.Attributes.Value("result")
							require.True(t, ok, "cleanup pass should name its result")
							actual[result.AsString()] = point.Value
						}
					default:
						t.Fatalf("unexpected cleanup metric %q", observed.Name)
					}
				}
			}
			require.Equal(t, tt.expected, actual, "cleanup telemetry should report pending work and the exact pass outcome")
		})
	}
}

func TestPreviewInfrastructureCleanupMetricsInitializationErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		failAt int
	}{
		{name: "pending gauge", failAt: 1},
		{name: "pass counter", failAt: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := newPreviewInfrastructureCleanupMetrics(&previewCleanupFailingMeter{failAt: tt.failAt})
			require.Error(t, err, "cleanup instrument initialization failure should propagate")
			require.Nil(t, m, "initialization failure should not return a partial cleanup metric bundle")
		})
	}
	var absent *PreviewInfrastructureCleanupMetrics
	require.NotPanics(t, func() { absent.Record(context.Background(), 0, false) }, "a provider without cleanup metrics should remain usable")
}

type previewCleanupFailingMeter struct {
	noop.Meter
	failAt int
}

func (m *previewCleanupFailingMeter) Int64Gauge(string, ...otelmetric.Int64GaugeOption) (otelmetric.Int64Gauge, error) {
	if m.failAt == 1 {
		return nil, errors.New("gauge initialization failed")
	}
	return noop.Int64Gauge{}, nil
}

func (m *previewCleanupFailingMeter) Int64Counter(string, ...otelmetric.Int64CounterOption) (otelmetric.Int64Counter, error) {
	return nil, errors.New("counter initialization failed")
}
