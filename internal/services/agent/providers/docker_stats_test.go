package providers

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

func TestDockerProvider_Stats_DecodesAndReportsWorkingSet(t *testing.T) {
	t.Parallel()

	// Build a stats payload that exercises the working-set computation
	// (Usage minus inactive_file) and the CPU% delta math.
	const payload = `{
		"cpu_stats": {
			"cpu_usage": {"total_usage": 4000000000},
			"system_cpu_usage": 8000000000,
			"online_cpus": 4
		},
		"precpu_stats": {
			"cpu_usage": {"total_usage": 2000000000},
			"system_cpu_usage": 6000000000,
			"online_cpus": 4
		},
		"memory_stats": {
			"usage": 1073741824,
			"limit": 4294967296,
			"stats": {"inactive_file": 268435456}
		}
	}`

	mock := &mockDockerClient{
		containerStatsFn: func(ctx context.Context, id string, stream bool) (container.StatsResponseReader, error) {
			require.Equal(t, "ctr-1", id)
			require.False(t, stream, "Stats must use stream=false so PreCPUStats is primed")
			return container.StatsResponseReader{Body: io.NopCloser(bytes.NewReader([]byte(payload)))}, nil
		},
	}
	p := NewDockerProvider(mock, zerolog.Nop())

	stats, err := p.Stats(context.Background(), &agent.Sandbox{ID: "ctr-1"})
	require.NoError(t, err)

	// Working set = 1 GiB - 256 MiB = 768 MiB.
	require.Equal(t, uint64(1073741824-268435456), stats.MemoryBytes)
	require.Equal(t, uint64(4294967296), stats.MemoryLimitBytes)

	// CPU delta = 2s, system delta = 2s, online = 4 → 4.0 cores used.
	require.InDelta(t, 4.0, stats.CPUCores, 1e-9)
}

func TestMemoryWorkingSet(t *testing.T) {
	t.Parallel()
	t.Run("subtracts inactive_file when present (cgroup v2)", func(t *testing.T) {
		t.Parallel()
		got := memoryWorkingSet(container.MemoryStats{
			Usage: 1024,
			Stats: map[string]uint64{"inactive_file": 256},
		})
		require.Equal(t, uint64(768), got)
	})
	t.Run("falls back to total_inactive_file (cgroup v1)", func(t *testing.T) {
		t.Parallel()
		got := memoryWorkingSet(container.MemoryStats{
			Usage: 1024,
			Stats: map[string]uint64{"total_inactive_file": 128},
		})
		require.Equal(t, uint64(896), got)
	})
	t.Run("falls back to cache when inactive keys missing", func(t *testing.T) {
		t.Parallel()
		got := memoryWorkingSet(container.MemoryStats{
			Usage: 1024,
			Stats: map[string]uint64{"cache": 512},
		})
		require.Equal(t, uint64(512), got)
	})
	t.Run("returns Usage when no breakdown available (gVisor)", func(t *testing.T) {
		t.Parallel()
		got := memoryWorkingSet(container.MemoryStats{Usage: 1024})
		require.Equal(t, uint64(1024), got)
	})
	t.Run("returns 0 when usage is 0", func(t *testing.T) {
		t.Parallel()
		got := memoryWorkingSet(container.MemoryStats{})
		require.Equal(t, uint64(0), got)
	})
}

func TestCPUCores(t *testing.T) {
	t.Parallel()
	t.Run("computes cores from delta", func(t *testing.T) {
		t.Parallel()
		cur := container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 3_000_000_000},
			SystemUsage: 4_000_000_000,
			OnlineCPUs:  4,
		}
		prev := container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 1_000_000_000},
			SystemUsage: 2_000_000_000,
			OnlineCPUs:  4,
		}
		// (2 / 2) * 4 = 4 cores.
		require.InDelta(t, 4.0, cpuCores(cur, prev), 1e-9)
	})
	t.Run("returns 0 when current matches previous (idle window)", func(t *testing.T) {
		t.Parallel()
		s := container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 1_000_000_000},
			SystemUsage: 2_000_000_000,
			OnlineCPUs:  4,
		}
		require.Equal(t, 0.0, cpuCores(s, s))
	})
	t.Run("returns 0 when OnlineCPUs is unset and percpu is empty", func(t *testing.T) {
		t.Parallel()
		cur := container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 3_000_000_000},
			SystemUsage: 4_000_000_000,
		}
		prev := container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 1_000_000_000},
			SystemUsage: 2_000_000_000,
		}
		require.Equal(t, 0.0, cpuCores(cur, prev))
	})
	t.Run("derives core count from PercpuUsage length", func(t *testing.T) {
		t.Parallel()
		cur := container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 3_000_000_000, PercpuUsage: []uint64{1, 2}},
			SystemUsage: 4_000_000_000,
		}
		prev := container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 1_000_000_000},
			SystemUsage: 2_000_000_000,
		}
		// (2 / 2) * 2 = 2 cores.
		require.InDelta(t, 2.0, cpuCores(cur, prev), 1e-9)
	})
}
