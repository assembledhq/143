package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
)

type stubQueueHealthStore struct {
	samples []db.JobQueueHealthSample
	err     error
}

func (s *stubQueueHealthStore) QueueHealthSamples(ctx context.Context) ([]db.JobQueueHealthSample, error) {
	return s.samples, s.err
}

type stubWorkerLoadStore struct {
	samples []db.WorkerLoadSample
	err     error
}

func (s *stubWorkerLoadStore) WorkerLoadSamples(ctx context.Context) ([]db.WorkerLoadSample, error) {
	return s.samples, s.err
}

type stubWorkerHeartbeatStore struct {
	health db.WorkerHeartbeatHealth
	err    error
}

func (s *stubWorkerHeartbeatStore) WorkerHeartbeatHealth(ctx context.Context, staleBefore time.Time) (db.WorkerHeartbeatHealth, error) {
	return s.health, s.err
}

func TestRunQueueHealthSamplerEmitsStructuredSamples(t *testing.T) {
	t.Parallel()

	store := &stubQueueHealthStore{samples: []db.JobQueueHealthSample{
		{
			Queue:                    "agent",
			JobType:                  "run_agent",
			PendingRunnable:          3,
			PendingDeferred:          2,
			Running:                  1,
			DeadLetter:               0,
			OldestRunnableAgeSeconds: 42,
		},
	}}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunQueueHealthSampler(ctx, store, zerolog.New(logs), time.Hour)
		close(done)
	}()
	require.Eventually(t, func() bool {
		return bytes.Contains(logs.Bytes(), []byte("platform health: job queue sample"))
	}, time.Second, 10*time.Millisecond, "queue sampler should emit an initial health sample")
	cancel()
	<-done

	var event map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event), "queue health sample should be valid JSON")
	require.Equal(t, "platform health: job queue sample", event["message"], "queue sampler should use the canonical log message")
	require.Equal(t, "agent", event["queue"], "queue sampler should include queue")
	require.Equal(t, "run_agent", event["job_type"], "queue sampler should include job type")
	require.Equal(t, float64(3), event["pending_runnable"], "queue sampler should include runnable pending count")
	require.Equal(t, float64(2), event["pending_deferred"], "queue sampler should include deferred pending count")
	require.Equal(t, float64(1), event["running"], "queue sampler should include running count")
	require.Equal(t, float64(0), event["dead_letter"], "queue sampler should include dead-letter count")
	require.Equal(t, float64(42), event["oldest_runnable_age_seconds"], "queue sampler should include oldest runnable age")
}

func TestRunControlPlaneHealthAlertsEmitsQueueAndWorkerHeartbeatWarnings(t *testing.T) {
	t.Parallel()

	queueStore := &stubQueueHealthStore{samples: []db.JobQueueHealthSample{
		{
			Queue:                    "agent",
			JobType:                  "push_pr_changes",
			PendingRunnable:          1,
			OldestRunnableAgeSeconds: 901,
		},
	}}
	heartbeatStore := &stubWorkerHeartbeatStore{health: db.WorkerHeartbeatHealth{
		ActiveWorkers:             2,
		FreshWorkers:              0,
		StaleWorkers:              2,
		NewestHeartbeatAgeSeconds: 901,
	}}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunControlPlaneHealthAlerts(ctx, queueStore, heartbeatStore, zerolog.New(logs), time.Hour)
		close(done)
	}()
	require.Eventually(t, func() bool {
		events := parseJSONLogEvents(t, logs.Bytes())
		var sawQueue bool
		var sawHeartbeat bool
		for _, event := range events {
			switch event["message"] {
			case "platform alert: runnable job queue age exceeded threshold":
				sawQueue = true
			case "platform alert: no fresh worker heartbeats":
				sawHeartbeat = true
			}
		}
		return sawQueue && sawHeartbeat
	}, time.Second, 10*time.Millisecond, "control-plane alerts should emit initial queue and worker heartbeat warnings")
	cancel()
	<-done

	events := parseJSONLogEvents(t, logs.Bytes())
	var queueEvent map[string]any
	var heartbeatEvent map[string]any
	for _, event := range events {
		switch event["message"] {
		case "platform alert: runnable job queue age exceeded threshold":
			queueEvent = event
		case "platform alert: no fresh worker heartbeats":
			heartbeatEvent = event
		}
	}
	require.NotNil(t, queueEvent, "control-plane alerts should include queue-age warning")
	require.Equal(t, "agent", queueEvent["queue"], "queue alert should include queue")
	require.Equal(t, "push_pr_changes", queueEvent["job_type"], "queue alert should include job type")
	require.Equal(t, float64(901), queueEvent["oldest_runnable_age_seconds"], "queue alert should include oldest runnable age")
	require.NotNil(t, heartbeatEvent, "control-plane alerts should include worker-heartbeat warning")
	require.Equal(t, float64(2), heartbeatEvent["active_workers"], "heartbeat alert should include active worker count")
	require.Equal(t, float64(0), heartbeatEvent["fresh_workers"], "heartbeat alert should include fresh worker count")
	require.Equal(t, float64(901), heartbeatEvent["newest_heartbeat_age_seconds"], "heartbeat alert should include newest heartbeat age")
}

func TestRunWorkerLoadSamplerEmitsStructuredSamples(t *testing.T) {
	t.Parallel()

	store := &stubWorkerLoadStore{samples: []db.WorkerLoadSample{
		{
			WorkerNodeID:          "worker-1",
			NodeStatus:            "active",
			RunningSessions:       2,
			TurnHeldSessions:      1,
			SandboxContainers:     3,
			ActivePreviews:        4,
			PreviewHeldContainers: 2,
			RunningJobs:           5,
			RunningSessionJobs:    2,
		},
	}}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunWorkerLoadSampler(ctx, store, zerolog.New(logs), time.Hour)
		close(done)
	}()
	require.Eventually(t, func() bool {
		return bytes.Contains(logs.Bytes(), []byte("platform health: worker load sample"))
	}, time.Second, 10*time.Millisecond, "worker load sampler should emit an initial health sample")
	cancel()
	<-done

	events := parseJSONLogEvents(t, logs.Bytes())
	var event map[string]any
	var totalEvent map[string]any
	for _, candidate := range events {
		switch candidate["message"] {
		case "platform health: worker load sample":
			event = candidate
		case "platform health: worker load total sample":
			totalEvent = candidate
		}
	}
	require.NotNil(t, event, "worker load sampler should emit a per-worker sample")
	require.Equal(t, "platform health: worker load sample", event["message"], "worker load sampler should use the canonical log message")
	require.Equal(t, "worker-1", event["worker_node_id"], "worker load sampler should include worker node id")
	require.Equal(t, "active", event["node_status"], "worker load sampler should include node status")
	require.Equal(t, float64(2), event["running_sessions"], "worker load sampler should include running session count")
	require.Equal(t, float64(1), event["turn_held_sessions"], "worker load sampler should include turn-held session count")
	require.Equal(t, float64(3), event["sandbox_containers"], "worker load sampler should include sandbox container count")
	require.Equal(t, float64(4), event["active_previews"], "worker load sampler should include active preview count")
	require.Equal(t, float64(2), event["preview_held_containers"], "worker load sampler should include preview-held container count")
	require.Equal(t, float64(5), event["running_jobs"], "worker load sampler should include running job count")
	require.Equal(t, float64(2), event["running_session_jobs"], "worker load sampler should include running session job count")
	require.NotNil(t, totalEvent, "worker load sampler should emit a fleet total sample")
	require.Equal(t, float64(2), totalEvent["running_sessions"], "worker load total should include running session count")
	require.Equal(t, float64(4), totalEvent["active_previews"], "worker load total should include active preview count")
	require.Equal(t, float64(3), totalEvent["sandbox_containers"], "worker load total should include sandbox container count")
}

func TestRunHostResourceSamplerEmitsStructuredSamples(t *testing.T) {
	t.Parallel()

	reader := &stubHostResourceReader{samples: []hostResourceSample{
		{cpu: hostCPUSample{idle: 80, total: 100}, memoryUtil: 0.25},
		{cpu: hostCPUSample{idle: 90, total: 200}, memoryUtil: 0.50},
	}}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runHostResourceSampler(ctx, reader, zerolog.New(logs), "worker-1", 10*time.Millisecond)
		close(done)
	}()
	require.Eventually(t, func() bool {
		count := 0
		for _, event := range parseJSONLogEvents(t, logs.Bytes()) {
			if event["message"] == "platform health: host resource sample" {
				count++
			}
		}
		return count >= 2
	}, time.Second, 10*time.Millisecond, "host resource sampler should emit an initial resource sample")
	cancel()
	<-done

	events := parseJSONLogEvents(t, logs.Bytes())
	require.NotEmpty(t, events, "host resource sampler should write at least one event")
	var event map[string]any
	for _, candidate := range events {
		if candidate["message"] != "platform health: host resource sample" {
			continue
		}
		if candidate["host_cpu_util"] == float64(0.9) {
			event = candidate
			break
		}
	}
	require.NotNil(t, event, "host resource sampler should emit the computed second sample")
	require.Equal(t, "platform health: host resource sample", event["message"], "host resource sampler should use the canonical log message")
	require.Equal(t, "worker-1", event["worker_node_id"], "host resource sampler should include worker node id")
	require.Equal(t, float64(0.9), event["host_cpu_util"], "host resource sampler should compute CPU utilization from consecutive samples")
	require.Equal(t, float64(0.5), event["host_memory_util"], "host resource sampler should include memory utilization")
}

// syncBuffer is a thread-safe bytes.Buffer wrapper for capturing zerolog output
// when a writer goroutine and an Eventually reader goroutine touch the same buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

type stubHostResourceReader struct {
	samples []hostResourceSample
	index   int
}

func (s *stubHostResourceReader) ReadHostResourceSample(context.Context) (hostResourceSample, error) {
	if s.index >= len(s.samples) {
		return s.samples[len(s.samples)-1], nil
	}
	sample := s.samples[s.index]
	s.index++
	return sample, nil
}

func parseJSONLogEvents(t *testing.T, raw []byte) []map[string]any {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event), "log event should be valid JSON")
		events = append(events, event)
	}
	return events
}
