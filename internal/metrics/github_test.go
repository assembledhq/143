package metrics

import (
	"context"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"testing"
)

func TestGitHubActivityDimensions(t *testing.T) {
	t.Parallel()
	tests := []struct{ value, expected string }{{"sandbox_gh", "sandbox_gh"}, {"session-fixture", "other"}, {"example/fixture", "other"}, {"", "other"}}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, githubActivityDimension(tt.value, "sandbox_gh"), "unknown or identifying values must collapse to a bounded metric dimension")
		})
	}
}

func TestGitHubActivityEmission(t *testing.T) {
	t.Parallel()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background()), "metric fixture should shut down") })
	counter, err := provider.Meter("github-activity-test").Int64Counter("github.activity")
	require.NoError(t, err, "activity counter should be created")
	recordGitHubActivity(context.Background(), counter, "sandbox_gh", "core", "success", "remote")
	recordGitHubActivity(context.Background(), counter, "sandbox_token", "", "hit", "cache_hit")
	recordGitHubActivity(context.Background(), counter, "pr_health", "", "deferred", "deferral")
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected), "activity events should reach the SDK reader")
	type point struct {
		count      int64
		dimensions attribute.Set
	}
	actual := make(map[string]point)
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != "github.activity" {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			require.True(t, ok, "activity should be an integer counter")
			for _, value := range sum.DataPoints {
				kind, ok := value.Attributes.Value("kind")
				require.True(t, ok, "every activity point needs its bounded kind")
				actual[kind.AsString()] = point{value.Value, value.Attributes}
			}
		}
	}
	expected := map[string]point{
		"remote":    {1, attribute.NewSet(attribute.String("caller", "sandbox_gh"), attribute.String("resource", "core"), attribute.String("outcome", "success"), attribute.String("kind", "remote"))},
		"cache_hit": {1, attribute.NewSet(attribute.String("caller", "sandbox_token"), attribute.String("resource", "other"), attribute.String("outcome", "hit"), attribute.String("kind", "cache_hit"))},
		"deferral":  {1, attribute.NewSet(attribute.String("caller", "pr_health"), attribute.String("resource", "other"), attribute.String("outcome", "deferred"), attribute.String("kind", "deferral"))},
	}
	require.Equal(t, expected, actual, "remote calls, cache hits, and deferrals must emit separate bounded activity points")
}
