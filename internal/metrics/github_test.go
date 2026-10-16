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

func TestGitHubVisualAssetActivityEmission(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, resource, expectedResource string }{
		{name: "missing resource remains unknown", expectedResource: "other"},
		{name: "actual core header remains core", resource: "core", expectedResource: "core"},
		{name: "unrecognized resource remains bounded", resource: "asset-specific", expectedResource: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() {
				require.NoError(t, provider.Shutdown(context.Background()), "asset metric fixture must release its own reader")
			})
			counter, err := provider.Meter("github-asset-test").Int64Counter("github.activity")
			require.NoError(t, err, "the isolated asset activity counter should initialize")
			recordGitHubActivity(context.Background(), counter, "visual_asset", tt.resource, "success", "remote")
			var collected metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(context.Background(), &collected), "the asset activity point should reach the SDK reader")
			type point struct {
				count      int64
				dimensions attribute.Set
			}
			var actual []point
			for _, scope := range collected.ScopeMetrics {
				for _, metric := range scope.Metrics {
					sum, ok := metric.Data.(metricdata.Sum[int64])
					require.True(t, ok, "asset activity must remain an integer counter")
					for _, value := range sum.DataPoints {
						actual = append(actual, point{value.Value, value.Attributes})
					}
				}
			}
			expected := []point{{1, attribute.NewSet(attribute.String("caller", "visual_asset"), attribute.String("resource", tt.expectedResource), attribute.String("outcome", "success"), attribute.String("kind", "remote"))}}
			require.Equal(t, expected, actual, "asset requests need a fixed caller label while missing resource headers remain other rather than core")
		})
	}
}
