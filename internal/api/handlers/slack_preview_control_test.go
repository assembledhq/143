package handlers

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSlackPreviewControl_BranchPreviewTargetForRepositoryKinds(t *testing.T) {
	t.Parallel()

	repoID := uuid.New()
	tests := []struct {
		name           string
		target         models.SlackPreviewTarget
		expectedKind   models.PreviewSourceType
		expectedBranch string
		expectedSHA    string
	}{
		{
			name: "branch target",
			target: models.SlackPreviewTarget{
				Kind:         models.SlackPreviewTargetBranch,
				RepositoryID: repoID,
				Branch:       "feature/slack",
				ConfigName:   "web",
			},
			expectedKind:   models.PreviewSourceTypeManual,
			expectedBranch: "feature/slack",
		},
		{
			name: "commit target",
			target: models.SlackPreviewTarget{
				Kind:         models.SlackPreviewTargetCommit,
				RepositoryID: repoID,
				CommitSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
			expectedKind: models.PreviewSourceTypeManual,
			expectedSHA:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			name: "repository target",
			target: models.SlackPreviewTarget{
				Kind:         models.SlackPreviewTargetRepository,
				RepositoryID: repoID,
			},
			expectedKind: models.PreviewSourceTypeManual,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			control := &SlackPreviewControl{}
			got, err := control.branchPreviewTarget(context.Background(), uuid.New(), tt.target)

			require.NoError(t, err, "branchPreviewTarget should support repository-like Slack preview targets")
			require.Equal(t, repoID, got.RepositoryID, "branchPreviewTarget should preserve repository scope")
			require.Equal(t, tt.expectedKind, got.SourceType, "branchPreviewTarget should use manual source type")
			require.Equal(t, tt.expectedBranch, got.Branch, "branchPreviewTarget should preserve branch")
			require.Equal(t, tt.expectedSHA, got.CommitSHA, "branchPreviewTarget should preserve commit SHA")
			require.NotEmpty(t, got.SourceID, "branchPreviewTarget should generate a stable source id")
		})
	}
}

func TestSlackPreviewSourceIDIncludesResolvedCommit(t *testing.T) {
	t.Parallel()

	repoID := uuid.New()

	withoutHead := slackPreviewSourceID(repoID, "main", "", "")
	withHead := slackPreviewSourceID(repoID, "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "web")

	require.NotEqual(t, withoutHead, withHead, "Slack branch preview source IDs should change after the branch head is resolved")
	require.Contains(t, withHead, ":aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:", "Slack branch preview source IDs should include the resolved commit")
	require.Contains(t, withHead, ":web", "Slack branch preview source IDs should include the resolved preview config")
}
