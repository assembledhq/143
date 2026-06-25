package agent_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

func TestSnapshotTarExcludeFlags(t *testing.T) {
	t.Parallel()

	t.Run("session checkpoint keeps .git, drops regenerable caches", func(t *testing.T) {
		t.Parallel()
		flags := agent.SnapshotTarExcludeFlags(false)
		require.Equal(t, "--exclude=__pycache__ --exclude=.pytest_cache", flags)
		require.NotContains(t, flags, ".git", "session checkpoints must retain .git for resume")
	})

	t.Run("preview snapshot also drops .git", func(t *testing.T) {
		t.Parallel()
		flags := agent.SnapshotTarExcludeFlags(true)
		require.Equal(t, "--exclude=.git --exclude=__pycache__ --exclude=.pytest_cache", flags)
	})
}

func TestSnapshotTarCompressFlag_PrefersZstdWithGzipFallback(t *testing.T) {
	t.Parallel()
	// The flag is a shell substitution embedded into the tar command. It must
	// pick zstd when available and fall back to gzip so snapshotting survives a
	// sandbox image that predates the zstd addition.
	require.Contains(t, agent.SnapshotTarCompressFlag, "command -v zstd")
	require.Contains(t, agent.SnapshotTarCompressFlag, "--zstd")
	require.Contains(t, agent.SnapshotTarCompressFlag, "-z")
}
