package github

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestNormalizeMergeState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mergeable    *bool
		githubState  string
		expected     models.PullRequestMergeState
		hasConflicts bool
	}{
		{
			name:         "mergeable clean branch",
			mergeable:    boolPtr(true),
			githubState:  "clean",
			expected:     models.PullRequestMergeStateClean,
			hasConflicts: false,
		},
		{
			name:         "dirty branch is conflicted",
			mergeable:    boolPtr(false),
			githubState:  "dirty",
			expected:     models.PullRequestMergeStateConflicted,
			hasConflicts: true,
		},
		{
			name:         "blocked branch is conflicted",
			mergeable:    boolPtr(false),
			githubState:  "blocked",
			expected:     models.PullRequestMergeStateConflicted,
			hasConflicts: true,
		},
		{
			name:         "non-mergeable branch is conflicted even without explicit dirty state",
			mergeable:    boolPtr(false),
			githubState:  "unstable",
			expected:     models.PullRequestMergeStateConflicted,
			hasConflicts: true,
		},
		{
			name:         "behind branch is normalized",
			mergeable:    boolPtr(true),
			githubState:  "behind",
			expected:     models.PullRequestMergeStateBehind,
			hasConflicts: false,
		},
		{
			name:         "unknown mergeability stays unknown",
			mergeable:    nil,
			githubState:  "unknown",
			expected:     models.PullRequestMergeStateUnknown,
			hasConflicts: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state, hasConflicts := normalizeMergeState(tt.mergeable, tt.githubState)
			require.Equal(t, tt.expected, state, "normalizeMergeState should map GitHub merge states to product states")
			require.Equal(t, tt.hasConflicts, hasConflicts, "normalizeMergeState should mark conflicts only for conflicting states")
		})
	}
}

func TestClassifyCheckRunCategory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		checkName string
		expected  models.PullRequestCheckCategory
	}{
		{name: "unit tests", checkName: "unit tests / api", expected: models.PullRequestCheckCategoryTest},
		{name: "playwright", checkName: "playwright e2e", expected: models.PullRequestCheckCategoryTest},
		{name: "empty", checkName: "", expected: models.PullRequestCheckCategoryUnknown},
		{name: "lint", checkName: "eslint", expected: models.PullRequestCheckCategoryLint},
		{name: "staticcheck", checkName: "staticcheck", expected: models.PullRequestCheckCategoryLint},
		{name: "build", checkName: "build frontend", expected: models.PullRequestCheckCategoryBuild},
		{name: "typecheck", checkName: "tsc typecheck", expected: models.PullRequestCheckCategoryBuild},
		{name: "deploy", checkName: "deploy preview", expected: models.PullRequestCheckCategoryDeploy},
		{name: "unknown", checkName: "codeql analyze", expected: models.PullRequestCheckCategoryUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			category := classifyCheckRunCategory(tt.checkName)
			require.Equal(t, tt.expected, category, "classifyCheckRunCategory should map check names to product categories")
		})
	}
}

func TestBuildPRHealthSummaryText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		health   models.PullRequestHealthResponse
		expected string
	}{
		{
			name: "healthy",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateClean,
				FailingTestCount:  0,
			},
			expected: "PR #184 is mergeable and all required test checks are passing.",
		},
		{
			name: "conflicts and tests",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateConflicted,
				HasConflicts:      true,
				FailingTestCount:  2,
			},
			expected: "PR #184 is blocked by conflicts and 2 failing test jobs.",
		},
		{
			name: "tests only",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateClean,
				FailingTestCount:  1,
			},
			expected: "PR #184 has 1 failing test job.",
		},
		{
			name: "conflicts only",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateConflicted,
				HasConflicts:      true,
			},
			expected: "PR #184 is blocked by merge conflicts.",
		},
		{
			name: "multiple tests only",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateClean,
				FailingTestCount:  3,
			},
			expected: "PR #184 has 3 failing test jobs.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			summary := buildPRHealthSummaryText(tt.health)
			require.Equal(t, tt.expected, summary, "buildPRHealthSummaryText should produce the compact banner summary")
		})
	}
}

func boolPtr(v bool) *bool {
	return &v
}
