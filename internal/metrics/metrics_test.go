package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewBillingMetrics(t *testing.T) {
	t.Parallel()

	m, err := NewBillingMetrics(nil)
	require.NoError(t, err)
	require.NotNil(t, m)
	require.NotNil(t, m.ContainerStartsTotal)
	require.NotNil(t, m.ContainerStopsTotal)
	require.NotNil(t, m.ContainerDurationSec)
	require.NotNil(t, m.ContainerCPUAllocated)
	require.NotNil(t, m.ContainerMemAllocated)
	require.NotNil(t, m.ContainerMinutesTotal)
}

func TestBillingMetrics_RecordStartStop(t *testing.T) {
	t.Parallel()

	m, err := NewBillingMetrics(nil)
	require.NoError(t, err)

	ctx := context.Background()
	// Should not panic.
	m.RecordStart(ctx, "org-1", "docker", 2.0, 4096)
	m.RecordStop(ctx, "org-1", "completed", 120.0, 2.0)
}

func TestNewHTTPMetrics(t *testing.T) {
	t.Parallel()

	m, err := NewHTTPMetrics()
	require.NoError(t, err)
	require.NotNil(t, m)
	require.NotNil(t, m.RequestsTotal)
	require.NotNil(t, m.RequestDuration)
	require.NotNil(t, m.RequestsInFlight)
}

func TestHTTPMetrics_RecordRequest(t *testing.T) {
	t.Parallel()

	m, err := NewHTTPMetrics()
	require.NoError(t, err)

	// Should not panic.
	m.RecordRequest(context.Background(), "GET", "/api/v1/usage", "200", 0.05)
}

func TestPRAutoRepairMetrics_Record(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	RecordPRAutoRepairDecision(ctx, "org-1", "acme/repo", "fix_tests", "started", "session_idle")
	RecordPRAutoRepairOutcome(ctx, "org-1", "acme/repo", "fix_tests", "completed")
	RecordPRAutoRepairStop(ctx, "org-1", "acme/repo")
	RecordPRAutoRepairRegret(ctx, "org-1", "acme/repo", "fix_tests", "thread_revert")
}

func TestSessionTitleMetrics_Record(t *testing.T) {
	t.Parallel()

	RecordSessionTitleDecision(context.Background(), "generated", "pivoted")
}

func TestNewMemoryMetrics(t *testing.T) {
	t.Parallel()

	m, err := NewMemoryMetrics()
	require.NoError(t, err)
	require.NotNil(t, m)
	require.NotNil(t, m.InjectionsTotal)
	require.NotNil(t, m.InjectedCount)
	require.NotNil(t, m.ReinforcedTotal)
	require.NotNil(t, m.ReinforcedCount)
}

func TestMemoryMetrics_RecordInjectionAndReinforcement(t *testing.T) {
	t.Parallel()

	m, err := NewMemoryMetrics()
	require.NoError(t, err)

	ctx := context.Background()
	// Should not panic.
	m.RecordInjection(ctx, 5)
	m.RecordReinforcement(ctx, 3)
}

func TestSlackbotMetrics_RecordObservabilityCounters(t *testing.T) {
	t.Parallel()

	m, err := NewSlackbotMetrics()
	require.NoError(t, err)
	require.NotNil(t, m)
	require.NotNil(t, m.RateLimitsTotal)
	require.NotNil(t, m.DroppedUpdatesTotal)
	require.NotNil(t, m.DedupeHitsTotal)
	require.NotNil(t, m.InstallHealthTotal)
	require.NotNil(t, m.MissingScopesTotal)
	require.NotNil(t, m.SignatureFailuresTotal)
	require.NotNil(t, m.CallbackLatency)
	require.NotNil(t, m.MessageUpdateLatency)

	ctx := context.Background()
	m.RecordRateLimit(ctx, "events_api")
	m.RecordDroppedUpdate(ctx, "progress", "dedupe")
	m.RecordDedupeHit(ctx, "events_api")
	m.RecordInstallHealth(ctx, "missing_scopes")
	m.RecordMissingScope(ctx, "chat:write")
	m.RecordSignatureFailure(ctx, "mismatch")
	m.RecordCallbackLatency(ctx, "events_api", "ok", 10)
	m.RecordMessageUpdateLatency(ctx, "chat.update", "sent", 12.5)
}
