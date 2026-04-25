package github

import (
	"encoding/json"
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

func TestDetectIndeterminateSignals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		mergeable        *bool
		githubState      string
		checkRuns        []gitHubCheckRun
		expectMergeIndet bool
		expectTestsIndet bool
	}{
		{
			name:             "definitive merge state and completed tests",
			mergeable:        boolPtr(false),
			githubState:      "dirty",
			checkRuns:        []gitHubCheckRun{{Name: "unit tests", Status: "completed", Conclusion: "failure"}},
			expectMergeIndet: false,
			expectTestsIndet: false,
		},
		{
			name:             "mergeable null without explicit conflict label",
			mergeable:        nil,
			githubState:      "unknown",
			checkRuns:        []gitHubCheckRun{{Name: "unit tests", Status: "completed", Conclusion: "failure"}},
			expectMergeIndet: true,
			expectTestsIndet: false,
		},
		{
			name:             "mergeable null but state dirty is still definitive",
			mergeable:        nil,
			githubState:      "dirty",
			expectMergeIndet: false,
			expectTestsIndet: false,
		},
		{
			name:             "mergeable null but state blocked is still definitive",
			mergeable:        nil,
			githubState:      "blocked",
			expectMergeIndet: false,
			expectTestsIndet: false,
		},
		{
			name:        "in-progress test check is indeterminate",
			mergeable:   boolPtr(true),
			githubState: "clean",
			checkRuns: []gitHubCheckRun{
				{Name: "unit tests", Status: "in_progress"},
			},
			expectMergeIndet: false,
			expectTestsIndet: true,
		},
		{
			name:        "queued test check is indeterminate",
			mergeable:   boolPtr(true),
			githubState: "clean",
			checkRuns: []gitHubCheckRun{
				{Name: "playwright e2e", Status: "queued"},
			},
			expectMergeIndet: false,
			expectTestsIndet: true,
		},
		{
			name:        "waiting test check is indeterminate",
			mergeable:   boolPtr(true),
			githubState: "clean",
			checkRuns: []gitHubCheckRun{
				{Name: "vitest", Status: "waiting"},
			},
			expectMergeIndet: false,
			expectTestsIndet: true,
		},
		{
			name:        "in-progress lint check is not test indeterminate",
			mergeable:   boolPtr(true),
			githubState: "clean",
			checkRuns: []gitHubCheckRun{
				{Name: "eslint", Status: "in_progress"},
			},
			expectMergeIndet: false,
			expectTestsIndet: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mergeIndet, testsIndet := detectIndeterminateSignals(tt.mergeable, tt.githubState, tt.checkRuns)
			require.Equal(t, tt.expectMergeIndet, mergeIndet, "merge state indeterminate flag should match")
			require.Equal(t, tt.expectTestsIndet, testsIndet, "tests indeterminate flag should match")
		})
	}
}

func TestShouldSkipIndeterminateSnapshotWrite(t *testing.T) {
	t.Parallel()

	makePrior := func(t *testing.T, headSHA string, summary models.PullRequestHealthSummary) models.PullRequestHealthCurrent {
		t.Helper()
		raw, err := json.Marshal(summary)
		require.NoError(t, err)
		return models.PullRequestHealthCurrent{HeadSHA: headSHA, SummaryJSON: raw}
	}

	conflictedPrior := models.PullRequestHealthSummary{
		MergeState:   models.PullRequestMergeStateConflicted,
		HasConflicts: true,
	}
	cleanPrior := models.PullRequestHealthSummary{
		MergeState:   models.PullRequestMergeStateClean,
		HasConflicts: false,
	}
	twoFailingPrior := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateClean,
		FailingTestCount: 2,
	}

	tests := []struct {
		name            string
		mergeIndet      bool
		testsIndet      bool
		newHeadSHA      string
		newFailingCount int
		prior           models.PullRequestHealthCurrent
		expectSkip      bool
	}{
		{
			name:       "no indeterminate signals never skips",
			mergeIndet: false,
			testsIndet: false,
			newHeadSHA: "sha-1",
			prior:      makePrior(t, "sha-1", conflictedPrior),
			expectSkip: false,
		},
		{
			name:       "merge indeterminate on same SHA with conflicted prior skips",
			mergeIndet: true,
			newHeadSHA: "sha-1",
			prior:      makePrior(t, "sha-1", conflictedPrior),
			expectSkip: true,
		},
		{
			name:       "merge indeterminate on same SHA with non-conflicted prior writes",
			mergeIndet: true,
			newHeadSHA: "sha-1",
			prior:      makePrior(t, "sha-1", cleanPrior),
			expectSkip: false,
		},
		{
			name:       "merge indeterminate on different SHA writes",
			mergeIndet: true,
			newHeadSHA: "sha-new",
			prior:      makePrior(t, "sha-old", conflictedPrior),
			expectSkip: false,
		},
		{
			name:            "tests indeterminate on same SHA with regression skips",
			testsIndet:      true,
			newHeadSHA:      "sha-1",
			newFailingCount: 1,
			prior:           makePrior(t, "sha-1", twoFailingPrior),
			expectSkip:      true,
		},
		{
			name:            "tests indeterminate on same SHA with equal count writes",
			testsIndet:      true,
			newHeadSHA:      "sha-1",
			newFailingCount: 2,
			prior:           makePrior(t, "sha-1", twoFailingPrior),
			expectSkip:      false,
		},
		{
			name:            "tests indeterminate on same SHA with higher count writes",
			testsIndet:      true,
			newHeadSHA:      "sha-1",
			newFailingCount: 3,
			prior:           makePrior(t, "sha-1", twoFailingPrior),
			expectSkip:      false,
		},
		{
			name:            "tests indeterminate on different SHA writes",
			testsIndet:      true,
			newHeadSHA:      "sha-new",
			newFailingCount: 0,
			prior:           makePrior(t, "sha-old", twoFailingPrior),
			expectSkip:      false,
		},
		{
			name:       "corrupt prior summary JSON falls through to write",
			mergeIndet: true,
			newHeadSHA: "sha-1",
			prior:      models.PullRequestHealthCurrent{HeadSHA: "sha-1", SummaryJSON: []byte(`{not json`)},
			expectSkip: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			skip := shouldSkipIndeterminateSnapshotWrite(tt.mergeIndet, tt.testsIndet, tt.newHeadSHA, tt.newFailingCount, tt.prior)
			require.Equal(t, tt.expectSkip, skip, "skip decision should match the expected outcome")
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
