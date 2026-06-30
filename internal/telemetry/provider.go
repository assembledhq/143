// Package telemetry initializes OpenTelemetry metrics and tracing.
//
// It configures a MeterProvider that exports via OTLP (push to any OTel-compatible
// backend) and optionally via Prometheus (pull via /metrics for backwards compat).
//
// Usage:
//
//	provider, shutdown, err := telemetry.InitMeterProvider(ctx, telemetry.Config{...})
//	defer shutdown(ctx)
//	// All OTel instruments registered against the global meter now export to
//	// both OTLP and Prometheus.
package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// Config holds telemetry configuration.
type Config struct {
	// ServiceName identifies this service in traces and metrics.
	ServiceName string

	// OTLPEndpoint is the OTLP HTTP endpoint (e.g. "localhost:4318" or
	// "otel-collector:4318"). Leave empty to disable OTLP export.
	OTLPEndpoint string

	// OTLPInsecure disables TLS for the OTLP connection. Use for local dev.
	OTLPInsecure bool

	// ExportInterval controls how often metrics are pushed to OTLP.
	// Defaults to 30s if zero.
	ExportInterval time.Duration

	// PrometheusEnabled controls whether a Prometheus exporter is also
	// registered, allowing /metrics scraping alongside OTLP push.
	// Defaults to true for backwards compatibility.
	PrometheusEnabled bool
}

// InitMeterProvider creates and registers a global OTel MeterProvider.
// The returned shutdown function must be called on application exit to flush
// any pending exports.
//
// The provider supports two export paths simultaneously:
//   - OTLP push (if OTLPEndpoint is set): sends to any OTel Collector or backend
//   - Prometheus pull (if PrometheusEnabled): exposes /metrics endpoint
//
// This means you can run both Prometheus scraping AND push to Datadog/Grafana
// Cloud at the same time — no code changes, just config.
func InitMeterProvider(ctx context.Context, cfg Config) (*metric.MeterProvider, func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create OTel resource: %w", err)
	}

	exportInterval := cfg.ExportInterval
	if exportInterval == 0 {
		exportInterval = 30 * time.Second
	}

	var opts []metric.Option
	opts = append(opts, metric.WithResource(res))

	// OTLP exporter (push metrics to collector/backend).
	if cfg.OTLPEndpoint != "" {
		exporterOpts := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpoint(cfg.OTLPEndpoint),
		}
		if cfg.OTLPInsecure {
			exporterOpts = append(exporterOpts, otlpmetrichttp.WithInsecure())
		}

		otlpExporter, err := otlpmetrichttp.New(ctx, exporterOpts...)
		if err != nil {
			return nil, nil, fmt.Errorf("create OTLP metric exporter: %w", err)
		}
		opts = append(opts, metric.WithReader(
			metric.NewPeriodicReader(otlpExporter, metric.WithInterval(exportInterval)),
		))
	}

	// Prometheus exporter (backwards-compatible /metrics endpoint).
	if cfg.PrometheusEnabled {
		promExp, err := promexporter.New()
		if err != nil {
			return nil, nil, fmt.Errorf("create Prometheus exporter: %w", err)
		}
		opts = append(opts, metric.WithReader(promExp))
	}

	provider := metric.NewMeterProvider(opts...)
	otel.SetMeterProvider(provider)

	shutdown := func(ctx context.Context) error {
		return provider.Shutdown(ctx)
	}

	return provider, shutdown, nil
}
