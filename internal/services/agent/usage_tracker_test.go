package agent_test

import (
	"context"
	"fmt"
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
	startCalls     []models.ContainerUsageEvent
	recordStartErr error
	stopCalls      []stopCall
	recordStopErr  error
}

type stopCall struct {
	EventID    uuid.UUID
	StoppedAt  time.Time
	ExitReason string
}

func (m *mockContainerUsageStore) RecordStart(_ context.Context, event *models.ContainerUsageEvent) error {
	m.startCalls = append(m.startCalls, *event)
	return m.recordStartErr
}

func (m *mockContainerUsageStore) RecordStop(_ context.Context, eventID uuid.UUID, stoppedAt time.Time, exitReason string) error {
	m.stopCalls = append(m.stopCalls, stopCall{EventID: eventID, StoppedAt: stoppedAt, ExitReason: exitReason})
	return m.recordStopErr
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
		DiskLimitGB:   10,
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
	require.Equal(t, 10240, store.startCalls[0].DiskLimitMB)
	require.Equal(t, startedAt, store.startCalls[0].StartedAt, "DB and caller should use the same timestamp")

	tracker.ContainerStopped(context.Background(), orgID, sessionID, eventID, sandbox.ID, startedAt, "completed")
	require.Len(t, store.stopCalls, 1)
	require.Equal(t, eventID, store.stopCalls[0].EventID)
	require.Equal(t, "completed", store.stopCalls[0].ExitReason)
	require.Empty(t, tracker.Snapshot(), "ContainerStopped must remove the entry from the sampler registry")
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
	tracker.ContainerStopped(context.Background(), orgID, sessionID, eventID, sandbox.ID, startedAt, "completed")
	// No panic = success.
}

func TestUsageTracker_RecordStopFailureLogsSessionID(t *testing.T) {
	t.Parallel()

	// When RecordStop fails, the tracker logs with session_id so the failure
	// can be traced back to the owning session in Grafana. This test covers
	// both the sessionID != uuid.Nil branch and the uuid.Nil guard branch.
	store := &mockContainerUsageStore{recordStopErr: fmt.Errorf("db down")}
	tracker := agent.NewUsageTracker(store, testBillingMetrics(t), zerolog.Nop())

	orgID := uuid.New()
	sessionID := uuid.New()
	eventID := uuid.New()
	startedAt := time.Now().Add(-time.Minute)

	// Happy case: sessionID set — should include session_id in the error log.
	tracker.ContainerStopped(context.Background(), orgID, sessionID, eventID, "ctr-fail-1", startedAt, "failed")
	require.Len(t, store.stopCalls, 1, "RecordStop should be invoked even when it errors")

	// Nil-session guard: RecordStop still called but session_id field skipped.
	tracker.ContainerStopped(context.Background(), orgID, uuid.Nil, eventID, "ctr-fail-2", startedAt, "failed")
	require.Len(t, store.stopCalls, 2)
}

func TestUsageTracker_RecordStopSkippedOnNilEventID(t *testing.T) {
	t.Parallel()

	// When ContainerStarted failed (and returned uuid.Nil), ContainerStopped
	// must skip the DB write instead of spuriously logging a failure.
	store := &mockContainerUsageStore{recordStopErr: fmt.Errorf("should not be called")}
	tracker := agent.NewUsageTracker(store, testBillingMetrics(t), zerolog.Nop())

	tracker.ContainerStopped(context.Background(), uuid.New(), uuid.New(), uuid.Nil, "ctr-nil-event", time.Now(), "failed")
	require.Empty(t, store.stopCalls, "RecordStop should be skipped when eventID is Nil")
}

func TestUsageTracker_RecordStartFailureDoesNotLeaveActiveContainer(t *testing.T) {
	t.Parallel()

	store := &mockContainerUsageStore{recordStartErr: fmt.Errorf("db down")}
	tracker := agent.NewUsageTracker(store, testBillingMetrics(t), zerolog.Nop())

	eventID := tracker.ContainerStarted(
		context.Background(),
		uuid.New(),
		uuid.New(),
		&agent.Sandbox{ID: "ctr-start-failed", Provider: "docker", WorkDir: "/workspace"},
		agent.DefaultSandboxConfig(),
		time.Now(),
	)

	require.Equal(t, uuid.Nil, eventID, "ContainerStarted should return Nil when DB start recording fails")
	require.Empty(t, tracker.Snapshot(), "failed start recording should not leave a container in the sampler registry")
}
