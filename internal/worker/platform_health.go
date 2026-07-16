package worker

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
)

type queueHealthStore interface {
	QueueHealthSamples(ctx context.Context) ([]db.JobQueueHealthSample, error)
}

type workerLoadStore interface {
	WorkerLoadSamples(ctx context.Context) ([]db.WorkerLoadSample, error)
}

type runningJobStore interface {
	RunningJobSamples(ctx context.Context) ([]db.RunningJobSample, error)
}

type runningJobSampleKey struct {
	workerNodeID string
	jobType      string
}

type workerHeartbeatHealthStore interface {
	WorkerHeartbeatHealth(ctx context.Context, staleBefore time.Time) (db.WorkerHeartbeatHealth, error)
}

type previewHealthStore interface {
	PreviewHealthSample(ctx context.Context) (db.PreviewHealthSample, error)
}

const (
	controlPlaneQueueAgeAlertThreshold      = 10 * time.Minute
	controlPlaneWorkerHeartbeatStaleTimeout = 2 * time.Minute
)

// RunQueueHealthSampler emits low-volume structured logs that feed the
// platform health dashboard. It is worker-local but queries the shared job
// table for platform-wide queue pressure.
func RunQueueHealthSampler(ctx context.Context, store queueHealthStore, logger zerolog.Logger, interval time.Duration) {
	if store == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	emitQueueHealthSample(ctx, store, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emitQueueHealthSample(ctx, store, logger)
		}
	}
}

func emitQueueHealthSample(ctx context.Context, store queueHealthStore, logger zerolog.Logger) {
	samples, err := store.QueueHealthSamples(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("platform health: failed to sample job queue")
		return
	}
	for _, sample := range samples {
		logger.Info().
			Str("job_channel", sample.Channel).
			Str("queue", sample.Queue).
			Str("job_type", sample.JobType).
			Int64("pending_runnable", sample.PendingRunnable).
			Int64("pending_deferred", sample.PendingDeferred).
			Int64("running", sample.Running).
			Int64("dead_letter", sample.DeadLetter).
			Float64("oldest_runnable_age_seconds", sample.OldestRunnableAgeSeconds).
			Msg("platform health: job queue sample")
	}
}

// RunPreviewHealthSampler emits the compact preview lifecycle snapshot used by
// the preview health dashboard.
func RunPreviewHealthSampler(ctx context.Context, store previewHealthStore, logger zerolog.Logger, interval time.Duration) {
	if store == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	emitPreviewHealthSample(ctx, store, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emitPreviewHealthSample(ctx, store, logger)
		}
	}
}

func emitPreviewHealthSample(ctx context.Context, store previewHealthStore, logger zerolog.Logger) {
	sample, err := store.PreviewHealthSample(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("preview health: failed to sample lifecycle")
		return
	}
	logger.Info().
		Int64("active_previews", sample.ActivePreviews).
		Int64("previews_started", sample.PreviewsStarted).
		Int64("previews_ready", sample.PreviewsReady).
		Int64("previews_failed_unavailable", sample.PreviewsFailedOrUnavailable).
		Float64("startup_p50_seconds", sample.StartupP50Seconds).
		Float64("startup_p95_seconds", sample.StartupP95Seconds).
		Int64("session_prewarm_queued", sample.SessionPrewarmQueued).
		Int64("session_prewarm_running", sample.SessionPrewarmRunning).
		Int64("session_prewarm_skipped", sample.SessionPrewarmSkipped).
		Int64("session_prewarm_failed", sample.SessionPrewarmFailed).
		Msg("preview health: lifecycle sample")
}

// RunControlPlaneHealthAlerts emits warning-level operational alerts from an
// API-capable process so worker-fleet failures are still observable when the
// worker processes themselves are down.
func RunControlPlaneHealthAlerts(ctx context.Context, queues queueHealthStore, workers workerHeartbeatHealthStore, logger zerolog.Logger, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	emitControlPlaneHealthAlerts(ctx, queues, workers, logger, time.Now)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emitControlPlaneHealthAlerts(ctx, queues, workers, logger, time.Now)
		}
	}
}

func emitControlPlaneHealthAlerts(ctx context.Context, queues queueHealthStore, workers workerHeartbeatHealthStore, logger zerolog.Logger, now func() time.Time) {
	if queues != nil {
		samples, err := queues.QueueHealthSamples(ctx)
		if err != nil {
			logger.Warn().Err(err).Msg("platform alert: failed to sample job queue")
		} else {
			for _, sample := range samples {
				if sample.PendingRunnable == 0 || sample.OldestRunnableAgeSeconds < controlPlaneQueueAgeAlertThreshold.Seconds() {
					continue
				}
				logger.Warn().
					Str("job_channel", sample.Channel).
					Str("queue", sample.Queue).
					Str("job_type", sample.JobType).
					Int64("pending_runnable", sample.PendingRunnable).
					Float64("oldest_runnable_age_seconds", sample.OldestRunnableAgeSeconds).
					Float64("threshold_seconds", controlPlaneQueueAgeAlertThreshold.Seconds()).
					Msg("platform alert: runnable job queue age exceeded threshold")
			}
		}
	}
	if workers == nil {
		return
	}
	health, err := workers.WorkerHeartbeatHealth(ctx, now().Add(-controlPlaneWorkerHeartbeatStaleTimeout))
	if err != nil {
		logger.Warn().Err(err).Msg("platform alert: failed to sample worker heartbeats")
		return
	}
	if health.ActiveWorkers == 0 || health.FreshWorkers > 0 {
		return
	}
	logger.Warn().
		Int64("active_workers", health.ActiveWorkers).
		Int64("fresh_workers", health.FreshWorkers).
		Int64("stale_workers", health.StaleWorkers).
		Float64("newest_heartbeat_age_seconds", health.NewestHeartbeatAgeSeconds).
		Float64("stale_after_seconds", controlPlaneWorkerHeartbeatStaleTimeout.Seconds()).
		Msg("platform alert: no fresh worker heartbeats")
}

// RunWorkerLoadSampler emits low-volume structured logs that feed the primary
// operations dashboard with current worker load across sessions and previews.
func RunWorkerLoadSampler(ctx context.Context, store workerLoadStore, logger zerolog.Logger, interval time.Duration) {
	if store == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	emitWorkerLoadSample(ctx, store, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emitWorkerLoadSample(ctx, store, logger)
		}
	}
}

func emitWorkerLoadSample(ctx context.Context, store workerLoadStore, logger zerolog.Logger) {
	samples, err := store.WorkerLoadSamples(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("platform health: failed to sample worker load")
		return
	}
	var total db.WorkerLoadSample
	for _, sample := range samples {
		total.RunningSessions += sample.RunningSessions
		total.TurnHeldSessions += sample.TurnHeldSessions
		total.SandboxContainers += sample.SandboxContainers
		total.ActivePreviews += sample.ActivePreviews
		total.PreviewHeldContainers += sample.PreviewHeldContainers
		total.RunningJobs += sample.RunningJobs
		total.RunningSessionJobs += sample.RunningSessionJobs
		total.ActiveUsageContainers += sample.ActiveUsageContainers
		total.ActiveMemoryAllocated += sample.ActiveMemoryAllocated
		total.ActiveCPUAllocated += sample.ActiveCPUAllocated
		total.ActiveDiskAllocated += sample.ActiveDiskAllocated
		logger.Info().
			Str("worker_node_id", sample.WorkerNodeID).
			Str("node_status", sample.NodeStatus).
			Int64("running_sessions", sample.RunningSessions).
			Int64("turn_held_sessions", sample.TurnHeldSessions).
			Int64("sandbox_containers", sample.SandboxContainers).
			Int64("active_previews", sample.ActivePreviews).
			Int64("preview_held_containers", sample.PreviewHeldContainers).
			Int64("running_jobs", sample.RunningJobs).
			Int64("running_session_jobs", sample.RunningSessionJobs).
			Int64("active_usage_containers", sample.ActiveUsageContainers).
			Int64("active_memory_allocated_mb", sample.ActiveMemoryAllocated).
			Float64("active_cpu_allocated", sample.ActiveCPUAllocated).
			Int64("active_disk_allocated_mb", sample.ActiveDiskAllocated).
			Msg("platform health: worker load sample")
	}
	logger.Info().
		Int64("running_sessions", total.RunningSessions).
		Int64("turn_held_sessions", total.TurnHeldSessions).
		Int64("sandbox_containers", total.SandboxContainers).
		Int64("active_previews", total.ActivePreviews).
		Int64("preview_held_containers", total.PreviewHeldContainers).
		Int64("running_jobs", total.RunningJobs).
		Int64("running_session_jobs", total.RunningSessionJobs).
		Int64("active_usage_containers", total.ActiveUsageContainers).
		Int64("active_memory_allocated_mb", total.ActiveMemoryAllocated).
		Float64("active_cpu_allocated", total.ActiveCPUAllocated).
		Int64("active_disk_allocated_mb", total.ActiveDiskAllocated).
		Msg("platform health: worker load total sample")
}

// RunRunningJobSampler emits current running jobs grouped by worker and job
// type. This complements RunWorkerLoadSampler's per-worker totals with enough
// dimensionality for Grafana action tables.
func RunRunningJobSampler(ctx context.Context, store runningJobStore, logger zerolog.Logger, interval time.Duration) {
	if store == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	previous := make(map[runningJobSampleKey]struct{})
	previous = emitRunningJobSamples(ctx, store, logger, previous)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			previous = emitRunningJobSamples(ctx, store, logger, previous)
		}
	}
}

func emitRunningJobSamples(ctx context.Context, store runningJobStore, logger zerolog.Logger, previous map[runningJobSampleKey]struct{}) map[runningJobSampleKey]struct{} {
	samples, err := store.RunningJobSamples(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("platform health: failed to sample running jobs")
		return previous
	}
	current := make(map[runningJobSampleKey]struct{}, len(samples))
	var total int64
	for _, sample := range samples {
		key := runningJobSampleKey{workerNodeID: sample.WorkerNodeID, jobType: sample.JobType}
		current[key] = struct{}{}
		total += sample.Running
		logger.Info().
			Str("worker_node_id", sample.WorkerNodeID).
			Str("job_type", sample.JobType).
			Int64("running", sample.Running).
			Msg("platform health: running job sample")
	}
	for key := range previous {
		if _, ok := current[key]; ok {
			continue
		}
		logger.Info().
			Str("worker_node_id", key.workerNodeID).
			Str("job_type", key.jobType).
			Int64("running", 0).
			Msg("platform health: running job sample")
	}
	logger.Info().
		Int64("running_jobs", total).
		Msg("platform health: running job total sample")
	return current
}

type hostResourceReader interface {
	ReadHostResourceSample(ctx context.Context) (hostResourceSample, error)
}

type procHostResourceReader struct{}

type hostResourceSample struct {
	cpu        hostCPUSample
	memoryUtil float64
}

type hostCPUSample struct {
	idle  uint64
	total uint64
}

// RunHostResourceSampler emits worker host CPU/RAM utilization for capacity
// planning. It reads Linux /proc files, which are available on production
// worker hosts and inside the worker container.
func RunHostResourceSampler(ctx context.Context, logger zerolog.Logger, workerNodeID string, interval time.Duration) {
	runHostResourceSampler(ctx, procHostResourceReader{}, logger, workerNodeID, interval)
}

func runHostResourceSampler(ctx context.Context, reader hostResourceReader, logger zerolog.Logger, workerNodeID string, interval time.Duration) {
	if reader == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var previous *hostCPUSample
	emitHostResourceSample(ctx, reader, logger, workerNodeID, &previous)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emitHostResourceSample(ctx, reader, logger, workerNodeID, &previous)
		}
	}
}

func emitHostResourceSample(ctx context.Context, reader hostResourceReader, logger zerolog.Logger, workerNodeID string, previous **hostCPUSample) {
	sample, err := reader.ReadHostResourceSample(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("platform health: failed to sample host resources")
		return
	}
	cpuUtil := 0.0
	if previous != nil && *previous != nil {
		cpuUtil = cpuUtilization(**previous, sample.cpu)
	}
	if previous != nil {
		cpu := sample.cpu
		*previous = &cpu
	}
	logger.Info().
		Str("worker_node_id", workerNodeID).
		Float64("host_cpu_util", cpuUtil).
		Float64("host_memory_util", sample.memoryUtil).
		Msg("platform health: host resource sample")
}

func (procHostResourceReader) ReadHostResourceSample(ctx context.Context) (hostResourceSample, error) {
	cpu, err := readProcStatCPU(ctx)
	if err != nil {
		return hostResourceSample{}, err
	}
	memoryUtil, err := readProcMemInfoUtil(ctx)
	if err != nil {
		return hostResourceSample{}, err
	}
	return hostResourceSample{cpu: cpu, memoryUtil: memoryUtil}, nil
}

func readProcStatCPU(ctx context.Context) (hostCPUSample, error) {
	select {
	case <-ctx.Done():
		return hostCPUSample{}, ctx.Err()
	default:
	}
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return hostCPUSample{}, fmt.Errorf("read /proc/stat: %w", err)
	}
	line, _, ok := strings.Cut(string(raw), "\n")
	if !ok {
		line = string(raw)
	}
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return hostCPUSample{}, fmt.Errorf("parse /proc/stat cpu line")
	}
	var values []uint64
	for _, field := range fields[1:] {
		value, parseErr := strconv.ParseUint(field, 10, 64)
		if parseErr != nil {
			return hostCPUSample{}, fmt.Errorf("parse /proc/stat cpu value: %w", parseErr)
		}
		values = append(values, value)
	}
	var total uint64
	for _, value := range values {
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return hostCPUSample{idle: idle, total: total}, nil
}

func readProcMemInfoUtil(ctx context.Context) (float64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("read /proc/meminfo: %w", err)
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("parse /proc/meminfo value: %w", parseErr)
		}
		values[key] = value
	}
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return 0, fmt.Errorf("parse /proc/meminfo MemTotal")
	}
	if available > total {
		available = total
	}
	return float64(total-available) / float64(total), nil
}

func cpuUtilization(previous, current hostCPUSample) float64 {
	if current.total < previous.total || current.idle < previous.idle {
		return 0
	}
	totalDelta := current.total - previous.total
	if totalDelta == 0 {
		return 0
	}
	idleDelta := current.idle - previous.idle
	if idleDelta > totalDelta {
		return 0
	}
	return float64(totalDelta-idleDelta) / float64(totalDelta)
}
