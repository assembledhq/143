package models

import "testing"

import "github.com/stretchr/testify/require"

func TestPullRequestMergeStateValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		state     PullRequestMergeState
		expectErr bool
	}{
		{name: "blocked", state: PullRequestMergeStateBlocked},
		{name: "clean", state: PullRequestMergeStateClean},
		{name: "conflicted", state: PullRequestMergeStateConflicted},
		{name: "behind", state: PullRequestMergeStateBehind},
		{name: "mergeability pending", state: PullRequestMergeStateMergeabilityPending},
		{name: "unknown", state: PullRequestMergeStateUnknown},
		{name: "invalid", state: PullRequestMergeState("broken"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.state.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unsupported merge states")
				return
			}
			require.NoError(t, err, "Validate should accept supported merge states")
		})
	}
}

func TestPullRequestCheckCategoryValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		category  PullRequestCheckCategory
		expectErr bool
	}{
		{name: "test", category: PullRequestCheckCategoryTest},
		{name: "lint", category: PullRequestCheckCategoryLint},
		{name: "build", category: PullRequestCheckCategoryBuild},
		{name: "deploy", category: PullRequestCheckCategoryDeploy},
		{name: "unknown", category: PullRequestCheckCategoryUnknown},
		{name: "invalid", category: PullRequestCheckCategory("oops"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.category.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unsupported check categories")
				return
			}
			require.NoError(t, err, "Validate should accept supported check categories")
		})
	}
}

func TestPullRequestHealthEnrichmentStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    PullRequestHealthEnrichmentStatus
		expectErr bool
	}{
		{name: "not requested", status: PullRequestHealthEnrichmentStatusNotRequested},
		{name: "pending", status: PullRequestHealthEnrichmentStatusPending},
		{name: "ready", status: PullRequestHealthEnrichmentStatusReady},
		{name: "failed", status: PullRequestHealthEnrichmentStatusFailed},
		{name: "stale", status: PullRequestHealthEnrichmentStatusStale},
		{name: "invalid", status: PullRequestHealthEnrichmentStatus("oops"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unsupported enrichment statuses")
				return
			}
			require.NoError(t, err, "Validate should accept supported enrichment statuses")
		})
	}
}

func TestPullRequestMergeWhenReadyStateValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		state     PullRequestMergeWhenReadyState
		expectErr bool
	}{
		{name: "off", state: PullRequestMergeWhenReadyStateOff},
		{name: "queued", state: PullRequestMergeWhenReadyStateQueued},
		{name: "merging", state: PullRequestMergeWhenReadyStateMerging},
		{name: "succeeded", state: PullRequestMergeWhenReadyStateSucceeded},
		{name: "failed", state: PullRequestMergeWhenReadyStateFailed},
		{name: "cancelled", state: PullRequestMergeWhenReadyStateCancelled},
		{name: "invalid", state: PullRequestMergeWhenReadyState("oops"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.state.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unsupported merge-when-ready states")
				return
			}
			require.NoError(t, err, "Validate should accept supported merge-when-ready states")
		})
	}
}

func TestPullRequestCheckStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    PullRequestCheckStatus
		expectErr bool
	}{
		{name: "passed", status: PullRequestCheckStatusPassed},
		{name: "failed", status: PullRequestCheckStatusFailed},
		{name: "pending", status: PullRequestCheckStatusPending},
		{name: "invalid", status: PullRequestCheckStatus("oops"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unsupported check statuses")
				return
			}
			require.NoError(t, err, "Validate should accept supported check statuses")
		})
	}
}

func TestPullRequestRepairActionTypeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		action    PullRequestRepairActionType
		expectErr bool
	}{
		{name: "fix tests", action: PullRequestRepairActionTypeFixTests},
		{name: "resolve conflicts", action: PullRequestRepairActionTypeResolveConflicts},
		{name: "invalid", action: PullRequestRepairActionType("oops"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.action.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unsupported repair actions")
				return
			}
			require.NoError(t, err, "Validate should accept supported repair actions")
		})
	}
}

func TestPullRequestRepairWorkspaceModeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mode      PullRequestRepairWorkspaceMode
		expectErr bool
	}{
		{name: "snapshot continuation", mode: PullRequestRepairWorkspaceModeSnapshotContinuation},
		{name: "pr head reconstruction", mode: PullRequestRepairWorkspaceModePRHeadReconstruction},
		{name: "invalid", mode: PullRequestRepairWorkspaceMode("child_revision"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.mode.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unsupported repair workspace modes")
				return
			}
			require.NoError(t, err, "Validate should accept supported repair workspace modes")
		})
	}
}
