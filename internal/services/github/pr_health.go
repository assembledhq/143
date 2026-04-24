package github

import (
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
)

func normalizeMergeState(mergeable *bool, githubState string) (models.PullRequestMergeState, bool) {
	state := strings.ToLower(strings.TrimSpace(githubState))
	switch {
	case mergeable == nil:
		return models.PullRequestMergeStateUnknown, false
	case state == "behind":
		return models.PullRequestMergeStateBehind, false
	case state == "dirty" || state == "blocked":
		return models.PullRequestMergeStateConflicted, true
	case mergeable != nil && !*mergeable:
		return models.PullRequestMergeStateConflicted, true
	case mergeable != nil && *mergeable:
		return models.PullRequestMergeStateClean, false
	default:
		return models.PullRequestMergeStateUnknown, false
	}
}

func classifyCheckRunCategory(checkName string) models.PullRequestCheckCategory {
	name := strings.ToLower(strings.TrimSpace(checkName))
	switch {
	case name == "":
		return models.PullRequestCheckCategoryUnknown
	case containsAny(name, "test", "tests", "jest", "vitest", "playwright", "cypress", "pytest", "rspec", "go test", "e2e", "integration"):
		return models.PullRequestCheckCategoryTest
	case containsAny(name, "lint", "eslint", "golangci", "rubocop", "ruff", "staticcheck"):
		return models.PullRequestCheckCategoryLint
	case containsAny(name, "build", "compile", "typecheck", "tsc"):
		return models.PullRequestCheckCategoryBuild
	case containsAny(name, "deploy", "release", "ship", "publish", "preview"):
		return models.PullRequestCheckCategoryDeploy
	default:
		return models.PullRequestCheckCategoryUnknown
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func buildPRHealthSummaryText(health models.PullRequestHealthResponse) string {
	conflicted := health.HasConflicts || health.MergeState == models.PullRequestMergeStateConflicted
	switch {
	case conflicted && health.FailingTestCount > 0:
		return fmt.Sprintf("PR #%d is blocked by conflicts and %d failing test jobs.", health.PullRequestNumber, health.FailingTestCount)
	case conflicted:
		return fmt.Sprintf("PR #%d is blocked by merge conflicts.", health.PullRequestNumber)
	case health.FailingTestCount == 1:
		return fmt.Sprintf("PR #%d has 1 failing test job.", health.PullRequestNumber)
	case health.FailingTestCount > 1:
		return fmt.Sprintf("PR #%d has %d failing test jobs.", health.PullRequestNumber, health.FailingTestCount)
	default:
		return fmt.Sprintf("PR #%d is mergeable and all required test checks are passing.", health.PullRequestNumber)
	}
}
