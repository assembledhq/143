package metrics

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

const mirrorMeterName = "github.com/assembledhq/143/credentials"

// MirrorCounterReader returns the current cumulative drift and failure totals
// from the dual-write coding-credentials mirror. Implemented by the
// CodingCredentialStore via its MirrorDriftCount / MirrorFailureCount methods.
type MirrorCounterReader func() (drift, failure uint64)

// MirrorMetrics holds the OTel registrations for the unified-coding-credentials
// mirror's drift and failure counters. The underlying counts are kept as
// in-process atomic uint64s on the credential store; this wiring exposes them
// through the standard telemetry pipeline so dashboards and alerts can fire
// on a sustained non-zero rate during the dual-write rollout.
type MirrorMetrics struct {
	reg otelmetric.Registration
}

// NewMirrorMetrics registers two observable Int64 counters that read from the
// supplied callback on each export. The callback is expected to return the
// cumulative totals — a process restart resets the in-process counters, which
// the exporter sees as a counter reset (the correct semantics).
func NewMirrorMetrics(read MirrorCounterReader) (*MirrorMetrics, error) {
	if read == nil {
		return nil, errors.New("mirror metrics: nil reader")
	}
	meter := otel.Meter(mirrorMeterName)

	drift, err := meter.Int64ObservableCounter("credentials.mirror.drift",
		otelmetric.WithDescription("Detected legacy-row drift cases (e.g. dual-set Anthropic API key + subscription) during dual-write."),
		otelmetric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	failure, err := meter.Int64ObservableCounter("credentials.mirror.failure",
		otelmetric.WithDescription("Mirror-write failures returned to the legacy coding-credentials store during dual-write."),
		otelmetric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	reg, err := meter.RegisterCallback(func(_ context.Context, o otelmetric.Observer) error {
		d, f := read()
		o.ObserveInt64(drift, int64(d))   // #nosec G115 -- atomic counter, far below int64 max
		o.ObserveInt64(failure, int64(f)) // #nosec G115 -- atomic counter, far below int64 max
		return nil
	}, drift, failure)
	if err != nil {
		return nil, err
	}
	return &MirrorMetrics{reg: reg}, nil
}
