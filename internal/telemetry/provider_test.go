package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitMeterProvider_PrometheusOnly(t *testing.T) {
	t.Parallel()

	provider, shutdown, err := InitMeterProvider(context.Background(), Config{
		ServiceName:       "test",
		PrometheusEnabled: true,
	})
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.NotNil(t, shutdown)

	err = shutdown(context.Background())
	require.NoError(t, err)
}

func TestInitMeterProvider_NoExporters(t *testing.T) {
	t.Parallel()

	provider, shutdown, err := InitMeterProvider(context.Background(), Config{
		ServiceName: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, provider)

	err = shutdown(context.Background())
	require.NoError(t, err)
}

func TestInitMeterProvider_DefaultExportInterval(t *testing.T) {
	t.Parallel()

	// ExportInterval=0 should default to 30s without error.
	provider, shutdown, err := InitMeterProvider(context.Background(), Config{
		ServiceName:       "test",
		PrometheusEnabled: true,
	})
	require.NoError(t, err)
	require.NotNil(t, provider)

	err = shutdown(context.Background())
	require.NoError(t, err)
}
