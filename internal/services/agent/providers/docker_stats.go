package providers

import (
	"context"
	"encoding/json"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"

	"github.com/assembledhq/143/internal/services/agent"
)

// Stats fetches a one-shot runtime sample for the given sandbox container via
// the Docker stats API. Stream=false primes a 1s server-side delta so both
// CPUStats and PreCPUStats are populated, which is what `docker stats` itself
// uses to compute live CPU%.
//
// Returns an error that satisfies errors.Is(err, agent.ErrSandboxNotFound)
// when Docker reports the container is gone, so the sampler can evict its
// in-memory registry entry instead of polling forever.
//
// gVisor (runsc) caveat: under runsc, Docker can return zero values for
// MemoryStats.Stats["inactive_file"] and ThrottlingData. memoryWorkingSet
// falls back to MemoryStats.Usage in that case rather than over-subtracting.
func (d *DockerProvider) Stats(ctx context.Context, sb *agent.Sandbox) (agent.RuntimeStats, error) {
	resp, err := d.client.ContainerStats(ctx, sb.ID, false)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return agent.RuntimeStats{}, fmt.Errorf("container stats: %w: %w", agent.ErrSandboxNotFound, err)
		}
		return agent.RuntimeStats{}, fmt.Errorf("container stats: %w", err)
	}
	defer resp.Body.Close()

	var s container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return agent.RuntimeStats{}, fmt.Errorf("decode stats: %w", err)
	}

	return agent.RuntimeStats{
		MemoryBytes:      memoryWorkingSet(s.MemoryStats),
		MemoryLimitBytes: s.MemoryStats.Limit,
		CPUCores:         cpuCores(s.CPUStats, s.PreCPUStats),
	}, nil
}

// memoryWorkingSet returns the working-set memory: usage minus the page cache
// portion that the kernel can cheaply reclaim. This matches what `docker
// stats` shows in the MEM USAGE column. Tries cgroup v2's `inactive_file`
// first, then cgroup v1's `total_inactive_file`, then a coarse `cache`
// fallback. The `cache` branch over-subtracts (cache covers both active and
// inactive page cache), so the result understates the true working set on
// older runtimes that only report `cache`; treat it as a lower bound.
func memoryWorkingSet(m container.MemoryStats) uint64 {
	if m.Usage == 0 {
		return 0
	}
	if v, ok := m.Stats["inactive_file"]; ok && v < m.Usage {
		return m.Usage - v
	}
	if v, ok := m.Stats["total_inactive_file"]; ok && v < m.Usage {
		return m.Usage - v
	}
	if v, ok := m.Stats["cache"]; ok && v < m.Usage {
		return m.Usage - v
	}
	return m.Usage
}

// cpuCores converts a delta CPU sample into the average number of full cores
// used during the window. Returns 0 when either delta is non-positive (e.g.
// PreCPUStats was zeroed by a one-shot endpoint, or the container exited).
func cpuCores(cur, prev container.CPUStats) float64 {
	cpuDelta := float64(cur.CPUUsage.TotalUsage) - float64(prev.CPUUsage.TotalUsage)
	sysDelta := float64(cur.SystemUsage) - float64(prev.SystemUsage)
	if cpuDelta <= 0 || sysDelta <= 0 {
		return 0
	}
	online := float64(cur.OnlineCPUs)
	if online == 0 {
		online = float64(len(cur.CPUUsage.PercpuUsage))
	}
	if online == 0 {
		return 0
	}
	return (cpuDelta / sysDelta) * online
}
