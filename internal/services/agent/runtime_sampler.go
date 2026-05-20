package agent

import (
	"context"
	"errors"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/metrics"
)

// runtimeSamplerMaxConcurrency caps the number of in-flight stats calls so a
// single tick can't fan out unboundedly when many containers are active.
const runtimeSamplerMaxConcurrency = 8

// runtimeSampleTimeout bounds one Stats call. Docker's stream=false primes
// for ~1s server-side, so 5s leaves room for slow daemons without making the
// whole tick slip when one container is wedged.
const runtimeSampleTimeout = 5 * time.Second

// RuntimeSampler periodically polls per-container resource usage and records
// it against BillingMetrics. The sampler is host-local: it asks UsageTracker
// for sandboxes started on this process, then asks the provider (when it
// implements RuntimeStatsProvider) for a one-shot stats sample. Containers
// owned by other workers are sampled by their own samplers.
type RuntimeSampler struct {
	tracker  *UsageTracker
	provider RuntimeStatsProvider
	metrics  *metrics.BillingMetrics
	interval time.Duration
	logger   zerolog.Logger
	nodeID   string
}

// NewRuntimeSampler builds a sampler that ticks every interval. interval <= 0
// disables sampling. Pass a provider that implements RuntimeStatsProvider; if
// the provider can't sample (e.g. e2b in the future), pass nil and the
// sampler will no-op.
func NewRuntimeSampler(
	tracker *UsageTracker,
	provider RuntimeStatsProvider,
	m *metrics.BillingMetrics,
	interval time.Duration,
	logger zerolog.Logger,
	nodeID ...string,
) *RuntimeSampler {
	var resolvedNodeID string
	if len(nodeID) > 0 {
		resolvedNodeID = nodeID[0]
	}
	return &RuntimeSampler{
		tracker:  tracker,
		provider: provider,
		metrics:  m,
		interval: interval,
		logger:   logger.With().Str("component", "runtime_sampler").Logger(),
		nodeID:   resolvedNodeID,
	}
}

// Run blocks until ctx is canceled, sampling every interval. Returns
// immediately when sampling is disabled (interval <= 0, no provider, etc.)
// so callers can wire it into errgroups unconditionally.
func (s *RuntimeSampler) Run(ctx context.Context) {
	if s.interval <= 0 || s.provider == nil || s.tracker == nil || s.metrics == nil {
		s.logger.Info().Msg("runtime sampler disabled")
		return
	}
	s.logger.Info().Dur("interval", s.interval).Msg("runtime sampler started")
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *RuntimeSampler) tick(ctx context.Context) {
	active := s.tracker.Snapshot()
	if len(active) == 0 {
		s.logAggregateHealthSample(0, 0, runtimeSampleAggregate{})
		return
	}

	sem := make(chan struct{}, runtimeSamplerMaxConcurrency)
	// Each goroutine sends exactly one runtimeSampleResult: either the
	// normal return value of sampleOne, or — if sampleOne panics before
	// returning — a sampleFailed result from the deferred recovery. The
	// channel is therefore buffered to exactly len(active) and closed
	// after wg.Wait, so the aggregation loop terminates cleanly without
	// blocking on a missing send.
	results := make(chan runtimeSampleResult, len(active))
	var wg sync.WaitGroup
	for _, c := range active {
		wg.Add(1)
		sem <- struct{}{}
		go func(c ActiveContainer) {
			defer wg.Done()
			defer func() { <-sem }()
			// A bug in the provider's Stats() must not bring down the
			// worker. Recover so a single bad sample only loses one
			// data point; the next tick keeps sampling other containers.
			defer func() {
				if r := recover(); r != nil {
					s.logger.Error().
						Interface("panic", r).
						Str("container_id", c.Sandbox.ID).
						Bytes("stack", debug.Stack()).
						Msg("runtime stats sample panicked")
					results <- runtimeSampleResult{sampleFailed: true}
				}
			}()
			results <- s.sampleOne(ctx, c)
		}(c)
	}
	wg.Wait()
	close(results)

	var sampleFailures int
	var agg runtimeSampleAggregate
	for result := range results {
		if result.sampleFailed {
			sampleFailures++
			continue
		}
		agg.observedSamples++
		agg.memorySum += result.memoryUtil
		agg.cpuSum += result.cpuUtil
		if result.memoryUtil > agg.maxMemoryUtil {
			agg.maxMemoryUtil = result.memoryUtil
		}
		if result.cpuUtil > agg.maxCPUUtil {
			agg.maxCPUUtil = result.cpuUtil
		}
	}
	if agg.observedSamples > 0 {
		agg.meanMemoryUtil = agg.memorySum / float64(agg.observedSamples)
		agg.meanCPUUtil = agg.cpuSum / float64(agg.observedSamples)
	}
	s.logAggregateHealthSample(len(active), sampleFailures, agg)
}

// runtimeSampleAggregate carries the rollup statistics emitted on each tick.
// Mean is included alongside max so a dashboard can tell "one container at
// 95%" from "every container at 95%" — the max collapses both cases otherwise.
type runtimeSampleAggregate struct {
	maxMemoryUtil   float64
	maxCPUUtil      float64
	meanMemoryUtil  float64
	meanCPUUtil     float64
	memorySum       float64
	cpuSum          float64
	observedSamples int
}

func (s *RuntimeSampler) logAggregateHealthSample(activeContainers, sampleFailures int, agg runtimeSampleAggregate) {
	minMemoryHeadroom, minCPUHeadroom := 0.0, 0.0
	meanMemoryHeadroom, meanCPUHeadroom := 0.0, 0.0
	if activeContainers == 0 {
		minMemoryHeadroom, minCPUHeadroom = 1, 1
		meanMemoryHeadroom, meanCPUHeadroom = 1, 1
	} else if agg.observedSamples > 0 {
		minMemoryHeadroom = clamp01(1 - agg.maxMemoryUtil)
		minCPUHeadroom = clamp01(1 - agg.maxCPUUtil)
		meanMemoryHeadroom = clamp01(1 - agg.meanMemoryUtil)
		meanCPUHeadroom = clamp01(1 - agg.meanCPUUtil)
	}

	event := s.logger.Info()
	if s.nodeID != "" {
		event = event.Str("worker_node_id", s.nodeID)
	}
	event.
		Int("active_containers", activeContainers).
		Int("sample_failures", sampleFailures).
		Float64("max_memory_util", agg.maxMemoryUtil).
		Float64("max_cpu_util", agg.maxCPUUtil).
		Float64("mean_memory_util", agg.meanMemoryUtil).
		Float64("mean_cpu_util", agg.meanCPUUtil).
		Float64("min_memory_headroom", minMemoryHeadroom).
		Float64("min_cpu_headroom", minCPUHeadroom).
		Float64("mean_memory_headroom", meanMemoryHeadroom).
		Float64("mean_cpu_headroom", meanCPUHeadroom).
		Msg("platform health: runtime sample")
}

type runtimeSampleResult struct {
	sampleFailed bool
	memoryUtil   float64
	cpuUtil      float64
}

func (s *RuntimeSampler) sampleOne(ctx context.Context, c ActiveContainer) runtimeSampleResult {
	sampleCtx, cancel := context.WithTimeout(ctx, runtimeSampleTimeout)
	defer cancel()

	stats, err := s.provider.Stats(sampleCtx, c.Sandbox)
	if err != nil {
		// Containers race with stats sampling: a container that exits
		// between Snapshot() and Stats() returns "no such container".
		// That's expected, not actionable — log at debug. When the
		// runtime tells us the container is gone, evict the entry from
		// the registry so future ticks don't keep polling for a ghost.
		if errors.Is(err, ErrSandboxNotFound) {
			s.tracker.Forget(c.Sandbox.ID)
		}
		s.logger.Debug().Err(err).
			Str("container_id", c.Sandbox.ID).
			Msg("runtime stats sample failed")
		return runtimeSampleResult{sampleFailed: !errors.Is(err, ErrSandboxNotFound)}
	}

	memMiB := float64(stats.MemoryBytes) / (1024 * 1024)
	memUtil := 0.0
	if stats.MemoryLimitBytes > 0 {
		memUtil = clamp01(float64(stats.MemoryBytes) / float64(stats.MemoryLimitBytes))
	}
	cpuUtil := 0.0
	if c.CPULimit > 0 {
		cpuUtil = clamp01(stats.CPUCores / c.CPULimit)
	}
	s.metrics.RecordSample(ctx, c.Sandbox.OrgID, memMiB, stats.CPUCores, memUtil, cpuUtil)
	return runtimeSampleResult{memoryUtil: memUtil, cpuUtil: cpuUtil}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
