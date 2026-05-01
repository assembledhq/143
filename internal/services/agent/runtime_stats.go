package agent

import (
	"context"
	"errors"
)

// RuntimeStats is a one-shot runtime resource usage sample for a sandbox.
// CPUCores is the number of full cores observed during the sample window
// (Docker's stream=false mode primes a 1s delta server-side). All fields are
// best-effort: under gVisor (runsc) some kernels return 0 for memory or CPU
// because gVisor's stat surface is partial; callers must tolerate zeroes.
type RuntimeStats struct {
	MemoryBytes      uint64  // working-set memory (usage minus inactive page cache)
	MemoryLimitBytes uint64  // cgroup memory limit reported by the runtime
	CPUCores         float64 // cores used during the sample window
}

// ErrSandboxNotFound signals that a sandbox the caller asked about no
// longer exists in the underlying runtime (Docker returned 404, etc.).
// Providers that implement RuntimeStatsProvider must return errors that
// satisfy errors.Is(err, ErrSandboxNotFound) for this case so the sampler
// can evict the entry from its registry instead of polling forever.
var ErrSandboxNotFound = errors.New("sandbox not found")

// RuntimeStatsProvider is an optional capability a SandboxProvider can expose
// to support live resource sampling. The runtime sampler does a type
// assertion against this interface; providers that don't implement it are
// silently skipped instead of breaking the sampling loop.
type RuntimeStatsProvider interface {
	Stats(ctx context.Context, sb *Sandbox) (RuntimeStats, error)
}
