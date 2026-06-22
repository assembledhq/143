package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

func TestPagerDutyMetrics_NewAndRecorders(t *testing.T) {
	t.Parallel()

	metrics, err := NewPagerDutyMetrics()
	require.NoError(t, err, "PagerDuty metric construction should succeed")
	require.NotNil(t, metrics, "PagerDuty metric construction should return a metrics bundle")

	require.NotPanics(t, func() {
		metrics.RecordWebhookEvent(context.Background(), "incident.triggered", "queued")
		metrics.RecordIngestJob(context.Background(), "processed")
		metrics.RecordAutomationMatch(context.Background(), "incident.triggered", "matched")
		metrics.RecordWriteback(context.Background(), "note", "sent")
		metrics.RecordAPIRequest(context.Background(), "list_incidents", "ok")
	}, "PagerDuty recorders should accept observations")

	var nilMetrics *PagerDutyMetrics
	require.NotPanics(t, func() {
		nilMetrics.RecordWebhookEvent(context.Background(), "incident.triggered", "queued")
		nilMetrics.RecordIngestJob(context.Background(), "processed")
		nilMetrics.RecordAutomationMatch(context.Background(), "incident.triggered", "matched")
		nilMetrics.RecordWriteback(context.Background(), "note", "sent")
		nilMetrics.RecordAPIRequest(context.Background(), "list_incidents", "ok")
	}, "nil PagerDuty metrics receiver should be a no-op")
}

func TestPagerDutyMetrics_InstrumentInitFailures(t *testing.T) {
	t.Parallel()

	for _, failAt := range []int{1, 2, 3, 4, 5} {
		meter := &pagerDutyFailingMeter{failAt: failAt}
		metrics, err := newPagerDutyMetrics(meter)
		require.Error(t, err, "constructor should surface PagerDuty instrument initialization failures")
		require.Nil(t, metrics, "constructor should not return a partially initialized PagerDuty metrics bundle")
	}
}

type pagerDutyFailingMeter struct {
	noop.Meter
	failAt int
	calls  int
}

func (m *pagerDutyFailingMeter) nextErr() error {
	m.calls++
	if m.calls == m.failAt {
		return errors.New("instrument init failed")
	}
	return nil
}

func (m *pagerDutyFailingMeter) Int64Counter(string, ...otelmetric.Int64CounterOption) (otelmetric.Int64Counter, error) {
	if err := m.nextErr(); err != nil {
		return nil, err
	}
	return noop.Int64Counter{}, nil
}
