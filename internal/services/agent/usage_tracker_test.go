package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// mockContainerUsageStore records calls for verification.
type mockContainerUsageStore struct {
	startCalls []models.ContainerUsageEvent
	stopCalls  []stopCall
}

type stopCall struct {
	EventID    uuid.UUID
	StoppedAt  time.Time
	ExitReason string
}

func (m *mockContainerUsageStore) RecordStart(_ context.Context, event *models.ContainerUsageEvent) error {
	m.startCalls = append(m.startCalls, *event)
	return nil
}

func (m *mockContainerUsageStore) RecordStop(_ context.Context, eventID uuid.UUID, stoppedAt time.Time, exitReason string) error {
	m.stopCalls = append(m.stopCalls, stopCall{EventID: eventID, StoppedAt: stoppedAt, ExitReason: exitReason})
	return nil
}

// testBillingMetrics creates BillingMetrics for testing. Uses the global
// no-op MeterProvider which is the OTel default when no provider is configured.
func testBillingMetrics(t *testing.T) *metrics.BillingMetrics {
	t.Helper()
	m, err := metrics.NewBillingMetrics(nil)
	require.NoError(t, err)
	return m
}

func TestUsageTracker_ContainerLifecycle(t *testing.T) {
	t.Parallel()

	store := &mockContainerUsageStore{}
	tracker := agent.NewUsageTracker(store, testBillingMetrics(t), zerolog.Nop())

	orgID := uuid.New()
	sessionID := uuid.New()
	sandbox := &agent.Sandbox{ID: "ctr-abc", Provider: "docker", WorkDir: "/workspace"}
	cfg := agent.SandboxConfig{
		Image:         "143-sandbox:latest",
		CPULimit:      2,
		MemoryLimitMB: 4096,
	}

	startedAt := time.Now().Add(-2 * time.Minute)
	eventID := tracker.ContainerStarted(context.Background(), orgID, sessionID, sandbox, cfg, startedAt)
	require.Len(t, store.startCalls, 1)
	require.Equal(t, orgID, store.startCalls[0].OrgID)
	require.Equal(t, sessionID, store.startCalls[0].SessionID)
	require.Equal(t, "ctr-abc", store.startCalls[0].ContainerID)
	require.Equal(t, "docker", store.startCalls[0].Provider)
	require.Equal(t, 2.0, store.startCalls[0].CPULimit)
	require.Equal(t, 4096, store.startCalls[0].MemoryLimitMB)
	require.Equal(t, startedAt, store.startCalls[0].StartedAt, "DB and caller should use the same timestamp")

	tracker.ContainerStopped(context.Background(), orgID, eventID, startedAt, "completed")
	require.Len(t, store.stopCalls, 1)
	require.Equal(t, eventID, store.stopCalls[0].EventID)
	require.Equal(t, "completed", store.stopCalls[0].ExitReason)
}

func TestUsageTracker_NilStoreAndMetrics(t *testing.T) {
	t.Parallel()

	// Should not panic when both store and metrics are nil.
	tracker := agent.NewUsageTracker(nil, nil, zerolog.Nop())

	orgID := uuid.New()
	sessionID := uuid.New()
	sandbox := &agent.Sandbox{ID: "ctr-xyz", Provider: "docker", WorkDir: "/workspace"}
	cfg := agent.DefaultSandboxConfig()

	startedAt := time.Now()
	eventID := tracker.ContainerStarted(context.Background(), orgID, sessionID, sandbox, cfg, startedAt)
	tracker.ContainerStopped(context.Background(), orgID, eventID, startedAt, "completed")
	// No panic = success.
}
