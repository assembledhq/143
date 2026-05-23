package preview

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/sandbox"
)

func TestShouldReassignPreviewWorker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		deadTargetNode     string
		reservationOwner   string
		claimingWorkerNode string
		expected           bool
	}{
		{
			name:               "reassigns first fallback claim from dead target",
			deadTargetNode:     "worker-a",
			reservationOwner:   "worker-a",
			claimingWorkerNode: "worker-b",
			expected:           true,
		},
		{
			name:               "reassigns second fallback claim when prior claimant died before completion",
			deadTargetNode:     "worker-a",
			reservationOwner:   "worker-b",
			claimingWorkerNode: "worker-c",
			expected:           true,
		},
		{
			name:               "does not reassign when claiming worker already owns reservation",
			deadTargetNode:     "worker-a",
			reservationOwner:   "worker-b",
			claimingWorkerNode: "worker-b",
			expected:           false,
		},
		{
			name:               "does not reassign when claim is not dead-target fallback",
			deadTargetNode:     "",
			reservationOwner:   "worker-a",
			claimingWorkerNode: "worker-b",
			expected:           false,
		},
		{
			name:               "does not reassign without claiming worker identity",
			deadTargetNode:     "worker-a",
			reservationOwner:   "worker-a",
			claimingWorkerNode: "",
			expected:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := shouldReassignPreviewWorker(tt.deadTargetNode, tt.reservationOwner, tt.claimingWorkerNode)
			require.Equal(t, tt.expected, actual, "shouldReassignPreviewWorker should match the expected fallback ownership behavior")
		})
	}
}

type startRunnerFileReader struct {
	content string
	err     error
}

func (r startRunnerFileReader) ListDir(context.Context, string, string, string) ([]sandbox.FileEntry, error) {
	panic("not used")
}

func (r startRunnerFileReader) ReadFile(context.Context, string, string, string) (string, bool, error) {
	return r.content, false, r.err
}

func (r startRunnerFileReader) ReadFileContext(context.Context, string, string, string, int, int, int) (sandbox.FileContextResult, error) {
	panic("not used")
}

func TestStartRunnerReadWorkspacePreviewConfig_ParseError(t *testing.T) {
	t.Parallel()

	runner := &StartRunner{
		fileReader: startRunnerFileReader{content: `{"preview":{"version":{}}}`},
		logger:     zerolog.Nop(),
	}

	cfg, err := runner.readWorkspacePreviewConfig(
		context.Background(),
		&agent.Sandbox{ID: "container-1", WorkDir: "/home/sandbox/repo"},
		uuid.New(),
	)

	require.Error(t, err, "invalid committed preview config should surface instead of being treated as missing config")
	require.ErrorIs(t, err, ErrInvalidConfig, "invalid committed preview config should use the shared invalid-config sentinel")
	require.Contains(t, err.Error(), repoconfig.ConfigPath, "invalid config error should name the repo config path")
	require.Nil(t, cfg, "invalid committed preview config should not return a fallback config")
}
