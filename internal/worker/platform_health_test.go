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

type stubRunningJobStore struct {
	samples []db.RunningJobSample
	err     error
}

func (s *stubRunningJobStore) RunningJobSamples(ctx context.Context) ([]db.RunningJobSample, error) {
	return s.samples, s.err
}

type sequenceRunningJobStore struct {
	mu      sync.Mutex
	calls   int
	samples [][]db.RunningJobSample
}

func (s *sequenceRunningJobStore) RunningJobSamples(ctx context.Context) ([]db.RunningJobSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.calls
	if index >= len(s.samples) {
		index = len(s.samples) - 1
	}
	s.calls++
	return append([]db.RunningJobSample(nil), s.samples[index]...), nil
}

type stubWorkerHeartbeatStore struct {
	health db.WorkerHeartbeatHealth
	err    error
}

func (s *stubWorkerHeartbeatStore) WorkerHeartbeatHealth(ctx context.Context, staleBefore time.Time) (db.WorkerHeartbeatHealth, error) {
	return s.health, s.err
}

type stubPreviewHealthStore struct {
	sample db.PreviewHealthSample
	err    error
}

func (s *stubPreviewHealthStore) PreviewHealthSample(ctx context.Context) (db.PreviewHealthSample, error) {
	return s.sample, s.err
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

func TestRunPreviewHealthSamplerEmitsStructuredSample(t *testing.T) {
	t.Parallel()

	store := &stubPreviewHealthStore{sample: db.PreviewHealthSample{
		ActivePreviews:              4,
		PreviewsStarted:             8,
		PreviewsReady:               7,
		PreviewsFailedOrUnavailable: 1,
		StartupP50Seconds:           23,
		StartupP95Seconds:           61,
		SessionPrewarmQueued:        2,
		SessionPrewarmRunning:       1,
		SessionPrewarmSkipped:       3,
		SessionPrewarmFailed:        4,
	}}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunPreviewHealthSampler(ctx, store, zerolog.New(logs), time.Hour)
		close(done)
	}()
	require.Eventually(t, func() bool {
		return bytes.Contains(logs.Bytes(), []byte("preview health: lifecycle sample"))
	}, time.Second, 10*time.Millisecond, "preview health sampler should emit an initial lifecycle sample")
	cancel()
	<-done

	var event map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event), "preview health sample should be valid JSON")
	require.Equal(t, "preview health: lifecycle sample", event["message"], "preview health sampler should use the canonical log message")
	require.Equal(t, float64(4), event["active_previews"], "preview health sampler should include active preview count")
	require.Equal(t, float64(8), event["previews_started"], "preview health sampler should include preview starts")
	require.Equal(t, float64(7), event["previews_ready"], "preview health sampler should include ready previews")
	require.Equal(t, float64(1), event["previews_failed_unavailable"], "preview health sampler should include failed or unavailable previews")
	require.Equal(t, float64(23), event["startup_p50_seconds"], "preview health sampler should include startup p50")
	require.Equal(t, float64(61), event["startup_p95_seconds"], "preview health sampler should include startup p95")
	require.Equal(t, float64(2), event["session_prewarm_queued"], "preview health sampler should include queued session prewarm count")
	require.Equal(t, float64(1), event["session_prewarm_running"], "preview health sampler should include running session prewarm count")
	require.Equal(t, float64(3), event["session_prewarm_skipped"], "preview health sampler should include skipped session prewarm count")
	require.Equal(t, float64(4), event["session_prewarm_failed"], "preview health sampler should include failed session prewarm count")
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
			ActiveUsageContainers: 2,
			ActiveMemoryAllocated: 6144,
			ActiveCPUAllocated:    4,
			ActiveDiskAllocated:   20480,
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
	require.Equal(t, float64(2), event["active_usage_containers"], "worker load sampler should include active usage-event container count")
	require.Equal(t, float64(6144), event["active_memory_allocated_mb"], "worker load sampler should include allocated memory across active containers")
	require.Equal(t, float64(4), event["active_cpu_allocated"], "worker load sampler should include allocated CPU across active containers")
	require.Equal(t, float64(20480), event["active_disk_allocated_mb"], "worker load sampler should include allocated disk across active containers")
	require.NotNil(t, totalEvent, "worker load sampler should emit a fleet total sample")
	require.Equal(t, float64(2), totalEvent["running_sessions"], "worker load total should include running session count")
	require.Equal(t, float64(4), totalEvent["active_previews"], "worker load total should include active preview count")
	require.Equal(t, float64(3), totalEvent["sandbox_containers"], "worker load total should include sandbox container count")
	require.Equal(t, float64(6144), totalEvent["active_memory_allocated_mb"], "worker load total should include allocated memory across active containers")
	require.Equal(t, float64(4), totalEvent["active_cpu_allocated"], "worker load total should include allocated CPU across active containers")
	require.Equal(t, float64(20480), totalEvent["active_disk_allocated_mb"], "worker load total should include allocated disk across active containers")
}

func TestRunRunningJobSamplerEmitsStructuredSamples(t *testing.T) {
	t.Parallel()

	store := &stubRunningJobStore{samples: []db.RunningJobSample{
		{WorkerNodeID: "worker-1", JobType: "run_agent", Running: 2},
		{WorkerNodeID: "worker-2", JobType: "start_preview", Running: 1},
	}}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunRunningJobSampler(ctx, store, zerolog.New(logs), time.Hour)
		close(done)
	}()
	require.Eventually(t, func() bool {
		return bytes.Contains(logs.Bytes(), []byte("platform health: running job sample"))
	}, time.Second, 10*time.Millisecond, "running job sampler should emit an initial health sample")
	cancel()
	<-done

	events := parseJSONLogEvents(t, logs.Bytes())
	require.Len(t, events, 3, "running job sampler should emit one log row per worker/job type plus one fleet total")
	require.Equal(t, "platform health: running job sample", events[0]["message"], "running job sampler should use the canonical log message")
	require.Equal(t, "worker-1", events[0]["worker_node_id"], "running job sampler should include worker node id")
	require.Equal(t, "run_agent", events[0]["job_type"], "running job sampler should include job type")
	require.Equal(t, float64(2), events[0]["running"], "running job sampler should include running job count")
	var totalEvent map[string]any
	for _, event := range events {
		if event["message"] == "platform health: running job total sample" {
			totalEvent = event
		}
	}
	require.NotNil(t, totalEvent, "running job sampler should emit a fleet total sample")
	require.Equal(t, float64(3), totalEvent["running_jobs"], "running job total sample should include current running job count")
}

func TestRunRunningJobSamplerEmitsZeroForClearedGroups(t *testing.T) {
	t.Parallel()

	store := &sequenceRunningJobStore{samples: [][]db.RunningJobSample{
		{{WorkerNodeID: "worker-1", JobType: "run_agent", Running: 2}},
		{},
	}}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunRunningJobSampler(ctx, store, zerolog.New(logs), 10*time.Millisecond)
		close(done)
	}()
	require.Eventually(t, func() bool {
		events := parseJSONLogEvents(t, logs.Bytes())
		var sawInitialGroup bool
		var sawClearedGroup bool
		var sawZeroTotal bool
		for _, event := range events {
			if event["message"] == "platform health: running job sample" &&
				event["worker_node_id"] == "worker-1" &&
				event["job_type"] == "run_agent" &&
				event["running"] == float64(2) {
				sawInitialGroup = true
			}
			if event["message"] == "platform health: running job sample" &&
				event["worker_node_id"] == "worker-1" &&
				event["job_type"] == "run_agent" &&
				event["running"] == float64(0) {
				sawClearedGroup = true
			}
			if event["message"] == "platform health: running job total sample" &&
				event["running_jobs"] == float64(0) {
				sawZeroTotal = true
			}
		}
		return sawInitialGroup && sawClearedGroup && sawZeroTotal
	}, time.Second, 10*time.Millisecond, "running job sampler should emit zero samples when running groups clear")
	cancel()
	<-done
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
