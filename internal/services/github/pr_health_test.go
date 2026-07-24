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
			name:         "blocked branch is blocked without conflicts",
			mergeable:    boolPtr(false),
			githubState:  "blocked",
			expected:     models.PullRequestMergeStateBlocked,
			hasConflicts: false,
		},
		{
			name:         "unstable branch is blocked without conflicts",
			mergeable:    boolPtr(true),
			githubState:  "unstable",
			expected:     models.PullRequestMergeStateBlocked,
			hasConflicts: false,
		},
		{
			name:         "draft branch is blocked without conflicts",
			mergeable:    boolPtr(false),
			githubState:  "draft",
			expected:     models.PullRequestMergeStateBlocked,
			hasConflicts: false,
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
			expected:     models.PullRequestMergeStateMergeabilityPending,
			hasConflicts: false,
		},
		{
			name:         "empty null mergeability is pending",
			mergeable:    nil,
			githubState:  "",
			expected:     models.PullRequestMergeStateMergeabilityPending,
			hasConflicts: false,
		},
		{
			name:         "mergeable null with dirty state is conflicted",
			mergeable:    nil,
			githubState:  "dirty",
			expected:     models.PullRequestMergeStateConflicted,
			hasConflicts: true,
		},
		{
			name:         "mergeable null with blocked state is blocked",
			mergeable:    nil,
			githubState:  "blocked",
			expected:     models.PullRequestMergeStateBlocked,
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
		commitStatuses   []gitHubCommitStatus
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
			name:        "pending test commit status is indeterminate",
			mergeable:   boolPtr(true),
			githubState: "clean",
			commitStatuses: []gitHubCommitStatus{
				{Context: "ci/circleci: frontend_test", State: "pending"},
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

			mergeIndet, testsIndet := detectIndeterminateSignals(tt.mergeable, tt.githubState, tt.checkRuns, tt.commitStatuses)
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

func TestDetermineChecksConfirmed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		checks                   []models.PullRequestCheckSummary
		requiredChecksConfigured bool
		expected                 bool
	}{
		{
			name: "completed checks are confirmed",
			checks: []models.PullRequestCheckSummary{
				{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed},
			},
			expected: true,
		},
		{
			name: "pending checks are not confirmed",
			checks: []models.PullRequestCheckSummary{
				{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPending},
			},
			expected: false,
		},
		{
			name:                     "zero checks stay unconfirmed when base branch requires checks",
			checks:                   nil,
			requiredChecksConfigured: true,
			expected:                 false,
		},
		{
			name:                     "zero checks are confirmed when base branch has no required checks",
			checks:                   nil,
			requiredChecksConfigured: false,
			expected:                 true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			confirmed := determineChecksConfirmed(tt.checks, tt.requiredChecksConfigured)
			require.Equal(t, tt.expected, confirmed, "determineChecksConfirmed should classify check authority correctly")
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

func TestDedupeCheckRunsByName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []gitHubCheckRun
		expected []gitHubCheckRun
	}{
		{
			name:     "empty input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "single check passes through",
			input:    []gitHubCheckRun{{ID: 1, Name: "Backend Test"}},
			expected: []gitHubCheckRun{{ID: 1, Name: "Backend Test"}},
		},
		{
			name: "parallel pull_request and push runs collapse to newer ID",
			input: []gitHubCheckRun{
				{ID: 100, Name: "Backend Test", Status: "completed", Conclusion: "failure"},
				{ID: 101, Name: "Backend Test", Status: "in_progress"},
				{ID: 102, Name: "Frontend Test", Status: "completed", Conclusion: "success"},
				{ID: 103, Name: "Frontend Test", Status: "in_progress"},
				{ID: 104, Name: "Detect Changes", Status: "completed", Conclusion: "success"},
			},
			expected: []gitHubCheckRun{
				{ID: 101, Name: "Backend Test", Status: "in_progress"},
				{ID: 103, Name: "Frontend Test", Status: "in_progress"},
				{ID: 104, Name: "Detect Changes", Status: "completed", Conclusion: "success"},
			},
		},
		{
			name: "name match is case- and whitespace-insensitive",
			input: []gitHubCheckRun{
				{ID: 10, Name: "  Backend Test "},
				{ID: 11, Name: "backend test"},
			},
			expected: []gitHubCheckRun{{ID: 11, Name: "backend test"}},
		},
		{
			name: "winners appear in input order",
			input: []gitHubCheckRun{
				{ID: 5, Name: "Lint"},
				{ID: 6, Name: "Build"},
				{ID: 7, Name: "Lint"},
				{ID: 4, Name: "Test"},
			},
			expected: []gitHubCheckRun{
				{ID: 6, Name: "Build"},
				{ID: 7, Name: "Lint"},
				{ID: 4, Name: "Test"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := dedupeCheckRunsByName(tt.input)
			require.Equal(t, tt.expected, got, "dedupeCheckRunsByName should keep highest-ID check per name")
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
			name: "repository disconnected blocks sync",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				Repository:        "assembledhq/143",
				SyncStatus:        models.PullRequestHealthSyncStatusBlocked,
				SyncBlocker:       models.PullRequestHealthSyncBlockerRepositoryDisconnected,
			},
			expected: "PR #184 cannot be refreshed because assembledhq/143 is disconnected from GitHub. Reconnect the repository to update merge status, checks, and close/merge state.",
		},
		{
			name: "healthy after checks pass",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateClean,
				FailingTestCount:  0,
				ChecksConfirmed:   true,
				Checks: []models.PullRequestCheckSummary{
					{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed},
				},
			},
			expected: "PR #184 is mergeable and all required test checks are passing.",
		},
		{
			name: "no ci checks configured",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateClean,
				FailingTestCount:  0,
				ChecksConfirmed:   true,
			},
			expected: "PR #184 is mergeable. No CI checks are configured for this repository.",
		},
		{
			name: "waiting for checks confirmation",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateClean,
				FailingTestCount:  0,
			},
			expected: "PR #184 is waiting for required checks to report passing.",
		},
		{
			name: "blocked by merge requirements",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateBlocked,
				ChecksConfirmed:   true,
				Checks: []models.PullRequestCheckSummary{
					{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed},
				},
			},
			expected: "PR #184 is blocked by GitHub merge requirements.",
		},
		{
			name: "checking mergeability",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateMergeabilityPending,
				ChecksConfirmed:   true,
				Checks: []models.PullRequestCheckSummary{
					{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed},
				},
			},
			expected: "PR #184 is waiting for GitHub to finish checking mergeability.",
		},
		{
			name: "legacy unknown mergeability",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateUnknown,
				ChecksConfirmed:   true,
				Checks: []models.PullRequestCheckSummary{
					{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed},
				},
			},
			expected: "PR #184 is waiting for GitHub to finish checking mergeability.",
		},
		{
			name: "behind base branch",
			health: models.PullRequestHealthResponse{
				PullRequestNumber: 184,
				MergeState:        models.PullRequestMergeStateBehind,
				ChecksConfirmed:   true,
				Checks: []models.PullRequestCheckSummary{
					{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed},
				},
			},
			expected: "PR #184 needs the base branch updated before merging.",
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

func TestDerivePullRequestRepairActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		input                  models.PullRequestHealthResponse
		expectCanResolveConfli bool
		expectCanFixTests      bool
		expectCanMerge         bool
	}{
		{
			name: "blocked PR cannot resolve conflicts or merge",
			input: models.PullRequestHealthResponse{
				Status:           "open",
				MergeState:       models.PullRequestMergeStateBlocked,
				ChecksConfirmed:  true,
				Checks:           []models.PullRequestCheckSummary{{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed}},
				HasConflicts:     false,
				FailingTestCount: 0,
			},
			expectCanResolveConfli: false,
			expectCanFixTests:      false,
			expectCanMerge:         false,
		},
		{
			name: "clean open PR with no checks is not mergeable until checks are confirmed",
			input: models.PullRequestHealthResponse{
				Status:     "open",
				MergeState: models.PullRequestMergeStateClean,
			},
			expectCanMerge: false,
		},
		{
			name: "clean open PR with confirmed zero checks is mergeable",
			input: models.PullRequestHealthResponse{
				Status:          "open",
				MergeState:      models.PullRequestMergeStateClean,
				ChecksConfirmed: true,
			},
			expectCanMerge: true,
		},
		{
			name: "clean PR with a failing check is not mergeable and offers fix tests",
			input: models.PullRequestHealthResponse{
				Status:     "open",
				MergeState: models.PullRequestMergeStateClean,
				Checks: []models.PullRequestCheckSummary{
					{Name: "lint", Category: models.PullRequestCheckCategoryLint, Status: models.PullRequestCheckStatusFailed},
				},
			},
			expectCanFixTests: true,
			expectCanMerge:    false,
		},
		{
			name: "clean PR with only passed checks is still mergeable",
			input: models.PullRequestHealthResponse{
				Status:          "open",
				MergeState:      models.PullRequestMergeStateClean,
				ChecksConfirmed: true,
				Checks: []models.PullRequestCheckSummary{
					{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed},
					{Name: "eslint", Category: models.PullRequestCheckCategoryLint, Status: models.PullRequestCheckStatusPassed},
				},
			},
			expectCanMerge: true,
		},
		{
			name: "clean PR with passed but partial checks is not mergeable",
			input: models.PullRequestHealthResponse{
				Status:          "open",
				MergeState:      models.PullRequestMergeStateClean,
				ChecksConfirmed: false,
				Checks: []models.PullRequestCheckSummary{
					{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed},
				},
			},
			expectCanMerge: false,
		},
		{
			name: "clean PR with pending checks is not mergeable",
			input: models.PullRequestHealthResponse{
				Status:     "open",
				MergeState: models.PullRequestMergeStateClean,
				Checks: []models.PullRequestCheckSummary{
					{Name: "playwright", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPending},
				},
			},
			expectCanMerge: false,
		},
		{
			name: "clean PR with failing tests is not mergeable and offers fix tests",
			input: models.PullRequestHealthResponse{
				Status:           "open",
				MergeState:       models.PullRequestMergeStateClean,
				FailingTestCount: 2,
				Checks: []models.PullRequestCheckSummary{
					{Name: "vitest", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusFailed},
				},
			},
			expectCanFixTests: true,
			expectCanMerge:    false,
		},
		{
			name: "conflicted PR offers resolve conflicts and is not mergeable",
			input: models.PullRequestHealthResponse{
				Status:       "open",
				MergeState:   models.PullRequestMergeStateConflicted,
				HasConflicts: true,
			},
			expectCanResolveConfli: true,
			expectCanMerge:         false,
		},
		{
			name: "behind base PR is not mergeable",
			input: models.PullRequestHealthResponse{
				Status:     "open",
				MergeState: models.PullRequestMergeStateBehind,
			},
			expectCanMerge: false,
		},
		{
			name: "unknown merge state is not mergeable",
			input: models.PullRequestHealthResponse{
				Status:     "open",
				MergeState: models.PullRequestMergeStateUnknown,
			},
			expectCanMerge: false,
		},
		{
			name: "pending mergeability state is not mergeable",
			input: models.PullRequestHealthResponse{
				Status:     "open",
				MergeState: models.PullRequestMergeStateMergeabilityPending,
			},
			expectCanMerge: false,
		},
		{
			name: "closed PR is not mergeable even if otherwise clean",
			input: models.PullRequestHealthResponse{
				Status:     "closed",
				MergeState: models.PullRequestMergeStateClean,
			},
			expectCanMerge: false,
		},
		{
			name: "merged PR is not mergeable",
			input: models.PullRequestHealthResponse{
				Status:     "merged",
				MergeState: models.PullRequestMergeStateClean,
			},
			expectCanMerge: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := tt.input
			derivePullRequestRepairActions(&resp)
			require.Equal(t, tt.expectCanResolveConfli, resp.CanResolveConflicts, "CanResolveConflicts should match")
			require.Equal(t, tt.expectCanFixTests, resp.CanFixTests, "CanFixTests should match")
			require.Equal(t, tt.expectCanMerge, resp.CanMerge, "CanMerge should match")
		})
	}
}

func TestDeriveAggregateCIStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		checks   []models.PullRequestCheckSummary
		expected string
	}{
		{
			name:     "no checks means success",
			checks:   nil,
			expected: "success",
		},
		{
			name: "pending checks mean pending",
			checks: []models.PullRequestCheckSummary{
				{Name: "playwright", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPending},
			},
			expected: "pending",
		},
		{
			name: "failed checks mean failure",
			checks: []models.PullRequestCheckSummary{
				{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusFailed},
			},
			expected: "failure",
		},
		{
			name: "failure wins over pending",
			checks: []models.PullRequestCheckSummary{
				{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusFailed},
				{Name: "e2e", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPending},
			},
			expected: "failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status := deriveAggregateCIStatus(tt.checks)
			require.Equal(t, tt.expected, status, "deriveAggregateCIStatus should return the expected CI state")
		})
	}
}

func TestSelectMergeMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		settings        *gitHubRepoMergeSettings
		expectedMethod  models.PullRequestMergeMethod
		expectedAllowed bool
	}{
		{
			name:            "nil settings falls back to merge",
			settings:        nil,
			expectedMethod:  models.PullRequestMergeMethodMerge,
			expectedAllowed: true,
		},
		{
			name:            "all flags nil falls back to merge",
			settings:        &gitHubRepoMergeSettings{},
			expectedMethod:  models.PullRequestMergeMethodMerge,
			expectedAllowed: true,
		},
		{
			name: "squash allowed picks squash",
			settings: &gitHubRepoMergeSettings{
				AllowSquashMerge: boolPtr(true),
				AllowMergeCommit: boolPtr(true),
				AllowRebaseMerge: boolPtr(true),
			},
			expectedMethod:  models.PullRequestMergeMethodSquash,
			expectedAllowed: true,
		},
		{
			name: "only merge allowed picks merge",
			settings: &gitHubRepoMergeSettings{
				AllowSquashMerge: boolPtr(false),
				AllowMergeCommit: boolPtr(true),
				AllowRebaseMerge: boolPtr(false),
			},
			expectedMethod:  models.PullRequestMergeMethodMerge,
			expectedAllowed: true,
		},
		{
			name: "only rebase allowed picks rebase",
			settings: &gitHubRepoMergeSettings{
				AllowSquashMerge: boolPtr(false),
				AllowMergeCommit: boolPtr(false),
				AllowRebaseMerge: boolPtr(true),
			},
			expectedMethod:  models.PullRequestMergeMethodRebase,
			expectedAllowed: true,
		},
		{
			name: "all disallowed returns not allowed",
			settings: &gitHubRepoMergeSettings{
				AllowSquashMerge: boolPtr(false),
				AllowMergeCommit: boolPtr(false),
				AllowRebaseMerge: boolPtr(false),
			},
			expectedMethod:  "",
			expectedAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			method, ok := selectMergeMethod(tt.settings)
			require.Equal(t, tt.expectedAllowed, ok, "allowed flag should match")
			require.Equal(t, tt.expectedMethod, method, "selected method should match")
		})
	}
}

func boolPtr(v bool) *bool {
	return &v
}
