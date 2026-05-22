package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/services/agent"
)

type fakeStatsProvider struct {
	mu       sync.Mutex
	calls    []string
	stats    agent.RuntimeStats
	err      error
	panicMsg string
	delay    time.Duration
	called   atomic.Int64
}

func (f *fakeStatsProvider) Stats(ctx context.Context, sb *agent.Sandbox) (agent.RuntimeStats, error) {
	f.called.Add(1)
	f.mu.Lock()
	f.calls = append(f.calls, sb.ID)
	d := f.delay
	stats := f.stats
	err := f.err
	panicMsg := f.panicMsg
	f.mu.Unlock()
	if panicMsg != "" {
		panic(panicMsg)
	}
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return agent.RuntimeStats{}, ctx.Err()
		}
	}
	return stats, err
}

func newMetrics(t *testing.T) *metrics.BillingMetrics {
	t.Helper()
	m, err := metrics.NewBillingMetrics(nil)
	require.NoError(t, err)
	return m
}

func TestRuntimeSampler_RunDisabledByZeroInterval(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	prov := &fakeStatsProvider{}
	s := agent.NewRuntimeSampler(tracker, prov, newMetrics(t), 0, zerolog.Nop())

	// With a non-positive interval, Run should return immediately even
	// without ctx cancellation.
	done := make(chan struct{})
	go func() {
		s.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return when interval <= 0")
	}
	require.Equal(t, int64(0), prov.called.Load())
}

func TestRuntimeSampler_SamplesActiveContainers(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	prov := &fakeStatsProvider{
		stats: agent.RuntimeStats{
			MemoryBytes:      512 * 1024 * 1024,
			MemoryLimitBytes: 2048 * 1024 * 1024,
			CPUCores:         0.75,
		},
	}
	orgID := uuid.New()
	sessionID := uuid.New()
	cfg := agent.SandboxConfig{CPULimit: 1.5, MemoryLimitMB: 2048}
	sb := &agent.Sandbox{ID: "ctr-A", Provider: "docker", OrgID: orgID.String()}
	tracker.ContainerStarted(context.Background(), orgID, sessionID, sb, cfg, time.Now())

	s := agent.NewRuntimeSampler(tracker, prov, newMetrics(t), 20*time.Millisecond, zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	require.Eventually(t, func() bool { return prov.called.Load() > 0 }, time.Second, 10*time.Millisecond)
	cancel()

	prov.mu.Lock()
	defer prov.mu.Unlock()
	require.Contains(t, prov.calls, "ctr-A")
}

func TestRuntimeSampler_LogsAggregateHealthSample(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	prov := &fakeStatsProvider{
		stats: agent.RuntimeStats{
			MemoryBytes:      768 * 1024 * 1024,
			MemoryLimitBytes: 1024 * 1024 * 1024,
			CPUCores:         0.50,
		},
	}
	orgID := uuid.New()
	sessionID := uuid.New()
	cfg := agent.SandboxConfig{CPULimit: 1, MemoryLimitMB: 1024}
	sb := &agent.Sandbox{ID: "ctr-health", Provider: "docker", OrgID: orgID.String()}
	tracker.ContainerStarted(context.Background(), orgID, sessionID, sb, cfg, time.Now())

	logs := &syncBuffer{}
	s := agent.NewRuntimeSampler(tracker, prov, newMetrics(t), 10*time.Millisecond, zerolog.New(logs))
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "platform health: runtime sample")
	}, time.Second, 10*time.Millisecond, "runtime sampler should emit aggregate platform-health logs")
	cancel()

	var event map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var candidate map[string]any
		require.NoError(t, json.Unmarshal(line, &candidate), "runtime sampler should emit JSON logs")
		if candidate["message"] == "platform health: runtime sample" {
			event = candidate
			break
		}
	}
	require.NotNil(t, event, "runtime health log should be present")
	require.Equal(t, "platform health: runtime sample", event["message"], "runtime health log should have the canonical message")
	require.Equal(t, float64(1), event["active_containers"], "runtime health log should include active container count")
	require.Equal(t, float64(0), event["sample_failures"], "runtime health log should include sample failure count")
	require.InDelta(t, 0.75, event["max_memory_util"].(float64), 0.001, "runtime health log should include max memory utilization")
	require.InDelta(t, 0.50, event["max_cpu_util"].(float64), 0.001, "runtime health log should include max CPU utilization")
	require.InDelta(t, 0.75, event["mean_memory_util"].(float64), 0.001, "runtime health log should include mean memory utilization to distinguish single-container outliers from across-the-board pressure")
	require.InDelta(t, 0.50, event["mean_cpu_util"].(float64), 0.001, "runtime health log should include mean CPU utilization")
	require.InDelta(t, 0.25, event["min_memory_headroom"].(float64), 0.001, "runtime health log should include lowest remaining memory headroom")
	require.InDelta(t, 0.50, event["min_cpu_headroom"].(float64), 0.001, "runtime health log should include lowest remaining CPU headroom")
	require.InDelta(t, 0.25, event["mean_memory_headroom"].(float64), 0.001, "runtime health log should include mean remaining memory headroom")
	require.InDelta(t, 0.50, event["mean_cpu_headroom"].(float64), 0.001, "runtime health log should include mean remaining CPU headroom")
}

func TestRuntimeSampler_LogsAggregateHealthSampleWhenIdle(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	prov := &fakeStatsProvider{}

	logs := &syncBuffer{}
	s := agent.NewRuntimeSampler(tracker, prov, newMetrics(t), 10*time.Millisecond, zerolog.New(logs))
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "platform health: runtime sample")
	}, time.Second, 10*time.Millisecond, "runtime sampler should emit idle platform-health logs")
	cancel()

	var event map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var candidate map[string]any
		require.NoError(t, json.Unmarshal(line, &candidate), "runtime sampler should emit JSON logs")
		if candidate["message"] == "platform health: runtime sample" {
			event = candidate
			break
		}
	}
	require.NotNil(t, event, "runtime health log should be present even with no active containers")
	require.Equal(t, float64(0), event["active_containers"], "idle runtime health log should report zero active containers")
	require.Equal(t, float64(0), event["sample_failures"], "idle runtime health log should report zero sample failures")
	require.Equal(t, float64(0), event["max_memory_util"], "idle runtime health log should report zero memory utilization")
	require.Equal(t, float64(0), event["max_cpu_util"], "idle runtime health log should report zero CPU utilization")
	require.Equal(t, float64(0), event["mean_memory_util"], "idle runtime health log should report zero mean memory utilization")
	require.Equal(t, float64(0), event["mean_cpu_util"], "idle runtime health log should report zero mean CPU utilization")
	require.Equal(t, float64(1), event["min_memory_headroom"], "idle runtime health log should report full memory headroom")
	require.Equal(t, float64(1), event["min_cpu_headroom"], "idle runtime health log should report full CPU headroom")
	require.Equal(t, float64(1), event["mean_memory_headroom"], "idle runtime health log should report full mean memory headroom")
	require.Equal(t, float64(1), event["mean_cpu_headroom"], "idle runtime health log should report full mean CPU headroom")
	require.Equal(t, int64(0), prov.called.Load(), "idle runtime sampler should not call stats provider")
}

func TestRuntimeSampler_StoppedContainerIsNotSampled(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	prov := &fakeStatsProvider{}
	orgID := uuid.New()
	sessionID := uuid.New()
	cfg := agent.SandboxConfig{CPULimit: 1, MemoryLimitMB: 1024}
	sb := &agent.Sandbox{ID: "ctr-B", Provider: "docker", OrgID: orgID.String()}

	startedAt := time.Now()
	eventID := tracker.ContainerStarted(context.Background(), orgID, sessionID, sb, cfg, startedAt)
	tracker.ContainerStopped(context.Background(), orgID, sessionID, eventID, sb.ID, startedAt, "ok")

	require.Empty(t, tracker.Snapshot(), "stopped container must be removed from registry")

	s := agent.NewRuntimeSampler(tracker, prov, newMetrics(t), 5*time.Millisecond, zerolog.Nop())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	require.Equal(t, int64(0), prov.called.Load(), "stopped container must not be sampled")
}

func TestRuntimeSampler_StatsErrorDoesNotPanic(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	prov := &fakeStatsProvider{err: errors.New("no such container")}
	orgID := uuid.New()
	sessionID := uuid.New()
	cfg := agent.SandboxConfig{CPULimit: 1, MemoryLimitMB: 1024}
	sb := &agent.Sandbox{ID: "ctr-gone", Provider: "docker", OrgID: orgID.String()}
	tracker.ContainerStarted(context.Background(), orgID, sessionID, sb, cfg, time.Now())

	s := agent.NewRuntimeSampler(tracker, prov, newMetrics(t), 10*time.Millisecond, zerolog.Nop())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	// Should have tried at least once and tolerated the error.
	require.Greater(t, prov.called.Load(), int64(0))
}

func TestRuntimeSampler_RunDisabledByNilProvider(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	s := agent.NewRuntimeSampler(tracker, nil, newMetrics(t), 10*time.Millisecond, zerolog.Nop())
	done := make(chan struct{})
	go func() {
		s.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return when provider is nil")
	}
}

func TestRuntimeSampler_RunDisabledByNilTrackerOrMetrics(t *testing.T) {
	t.Parallel()
	prov := &fakeStatsProvider{}

	// nil tracker.
	s1 := agent.NewRuntimeSampler(nil, prov, newMetrics(t), 10*time.Millisecond, zerolog.Nop())
	done1 := make(chan struct{})
	go func() {
		s1.Run(context.Background())
		close(done1)
	}()
	select {
	case <-done1:
	case <-time.After(time.Second):
		t.Fatal("Run did not return when tracker is nil")
	}

	// nil metrics.
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	s2 := agent.NewRuntimeSampler(tracker, prov, nil, 10*time.Millisecond, zerolog.Nop())
	done2 := make(chan struct{})
	go func() {
		s2.Run(context.Background())
		close(done2)
	}()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("Run did not return when metrics are nil")
	}

	require.Equal(t, int64(0), prov.called.Load(), "nil deps must short-circuit before any provider call")
}

func TestRuntimeSampler_PanicInStatsDoesNotCrashWorker(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	prov := &fakeStatsProvider{panicMsg: "boom"}
	orgID := uuid.New()
	sessionID := uuid.New()
	cfg := agent.SandboxConfig{CPULimit: 1, MemoryLimitMB: 1024}
	sb := &agent.Sandbox{ID: "ctr-panic", Provider: "docker", OrgID: orgID.String()}
	tracker.ContainerStarted(context.Background(), orgID, sessionID, sb, cfg, time.Now())

	s := agent.NewRuntimeSampler(tracker, prov, newMetrics(t), 10*time.Millisecond, zerolog.Nop())
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	// Recovered panics inside the sampler must let Run() return cleanly
	// when ctx expires; if the recover were missing, the goroutine would
	// crash the test binary instead.
	s.Run(ctx)
	require.Greater(t, prov.called.Load(), int64(0), "sampler should have attempted at least one panicking call")
}

func TestRuntimeSampler_NotFoundEvictsRegistryEntry(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	// Wrap ErrSandboxNotFound so errors.Is matches — same shape Stats()
	// returns from real providers when Docker reports the container is gone.
	prov := &fakeStatsProvider{err: wrapNotFound()}

	orgID := uuid.New()
	sessionID := uuid.New()
	cfg := agent.SandboxConfig{CPULimit: 1, MemoryLimitMB: 1024}
	sb := &agent.Sandbox{ID: "ctr-ghost", Provider: "docker", OrgID: orgID.String()}
	tracker.ContainerStarted(context.Background(), orgID, sessionID, sb, cfg, time.Now())
	require.Len(t, tracker.Snapshot(), 1)

	s := agent.NewRuntimeSampler(tracker, prov, newMetrics(t), 5*time.Millisecond, zerolog.Nop())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	require.Empty(t, tracker.Snapshot(), "ErrSandboxNotFound must evict the registry entry")
}

// wrapNotFound returns an error that errors.Is matches against
// agent.ErrSandboxNotFound — same shape providers produce in production.
func wrapNotFound() error {
	return wrapErr("docker stats", agent.ErrSandboxNotFound)
}

func wrapErr(prefix string, err error) error {
	return &wrappedErr{prefix: prefix, err: err}
}

type wrappedErr struct {
	prefix string
	err    error
}

func (w *wrappedErr) Error() string { return w.prefix + ": " + w.err.Error() }
func (w *wrappedErr) Unwrap() error { return w.err }

func TestUsageTrackerSnapshot_IsCopy(t *testing.T) {
	t.Parallel()
	tracker := agent.NewUsageTracker(nil, newMetrics(t), zerolog.Nop())
	orgID := uuid.New()
	sessionID := uuid.New()
	cfg := agent.SandboxConfig{CPULimit: 2, MemoryLimitMB: 4096}

	for i := 0; i < 3; i++ {
		sb := &agent.Sandbox{ID: "ctr-" + uuid.NewString(), OrgID: orgID.String()}
		tracker.ContainerStarted(context.Background(), orgID, sessionID, sb, cfg, time.Now())
	}
	snap := tracker.Snapshot()
	require.Len(t, snap, 3)
	// Mutating the snapshot must not corrupt the tracker's state.
	snap[0] = agent.ActiveContainer{}
	require.Len(t, tracker.Snapshot(), 3)
}
