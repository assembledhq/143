package preview

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyLaunchFailure_InstallFailed(t *testing.T) {
	t.Parallel()

	failure := ClassifyLaunchFailure(fmt.Errorf("%w: npm ci exited with code 1", ErrInstallFailed))

	require.Equal(t, "PREVIEW_INSTALL_FAILED", failure.Code, "install failures should get a dedicated preview start error code")
	require.Contains(t, failure.Message, "preview.install", "install failure message should point users at the install config")
	require.Contains(t, failure.Message, "npm ci exited with code 1", "install failure message should include provider details")
}

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
