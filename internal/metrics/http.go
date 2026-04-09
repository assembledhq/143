package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// HTTP attribute keys.
var (
	AttrHTTPMethod = attribute.Key("http.request.method")
	AttrHTTPRoute  = attribute.Key("http.route")
	AttrHTTPStatus = attribute.Key("http.response.status_code")
)

// HTTPMetrics holds OTel instruments for HTTP request observability.
type HTTPMetrics struct {
	RequestsTotal    otelmetric.Int64Counter
	RequestDuration  otelmetric.Float64Histogram
	RequestsInFlight otelmetric.Int64UpDownCounter
}

// NewHTTPMetrics creates and registers HTTP OTel instruments.
func NewHTTPMetrics() (*HTTPMetrics, error) {
	meter := otel.Meter("github.com/assembledhq/143/http")

	total, err := meter.Int64Counter("http.server.request.count",
		otelmetric.WithDescription("Total number of HTTP requests"),
		otelmetric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	duration, err := meter.Float64Histogram("http.server.request.duration",
		otelmetric.WithDescription("Duration of HTTP requests"),
		otelmetric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	inFlight, err := meter.Int64UpDownCounter("http.server.active_requests",
		otelmetric.WithDescription("Number of HTTP requests currently being processed"),
		otelmetric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	return &HTTPMetrics{
		RequestsTotal:    total,
		RequestDuration:  duration,
		RequestsInFlight: inFlight,
	}, nil
}

// RecordRequest records a completed HTTP request.
func (m *HTTPMetrics) RecordRequest(ctx context.Context, method, route, status string, durationSec float64) {
	attrs := otelmetric.WithAttributes(
		AttrHTTPMethod.String(method),
		AttrHTTPRoute.String(route),
		AttrHTTPStatus.String(status),
	)
	routeAttrs := otelmetric.WithAttributes(
		AttrHTTPMethod.String(method),
		AttrHTTPRoute.String(route),
	)
	m.RequestsTotal.Add(ctx, 1, attrs)
	m.RequestDuration.Record(ctx, durationSec, routeAttrs)
}
