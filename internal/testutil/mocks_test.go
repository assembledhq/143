package testutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

func TestMockSandboxProvider_CreateDefaultReturnsConfiguredDirs(t *testing.T) {
	t.Parallel()

	m := NewMockSandboxProvider()
	cfg := agent.SandboxConfig{WorkDir: "/home/sandbox/backend", HomeDir: "/home/sandbox"}

	sb, err := m.Create(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, "test-sandbox", sb.ID)
	require.Equal(t, "mock", sb.Provider)
	require.Equal(t, cfg.WorkDir, sb.WorkDir, "default Create should propagate WorkDir from cfg")
	require.Equal(t, cfg.HomeDir, sb.HomeDir, "default Create should propagate HomeDir from cfg")
}
