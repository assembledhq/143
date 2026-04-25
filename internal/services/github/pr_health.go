package github

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
)

// detectIndeterminateSignals reports whether GitHub's response leaves
// mergeability or test results still being computed. Mergeability is
// indeterminate when GitHub returns mergeable=null without an explicit
// conflict label (it computes the value lazily after pushes and base merges).
// Tests are indeterminate when any test-category check_run is queued, waiting,
// or in progress. Callers use these flags to avoid clobbering prior actionable
// state on the same head SHA with a transient-blank snapshot.
func detectIndeterminateSignals(mergeable *bool, githubState string, checkRuns []gitHubCheckRun) (mergeStateIndeterminate, testsIndeterminate bool) {
	state := strings.ToLower(strings.TrimSpace(githubState))
	mergeStateIndeterminate = mergeable == nil && state != "dirty" && state != "blocked"
	for _, check := range checkRuns {
		if classifyCheckRunCategory(check.Name) != models.PullRequestCheckCategoryTest {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(check.Status)) {
		case "in_progress", "queued", "waiting":
			testsIndeterminate = true
			return
		}
	}
	return
}

// shouldSkipIndeterminateSnapshotWrite returns true when persisting the new
// summary would regress prior actionable state on the same head SHA. It
// fires only if (a) GitHub's signals are still being computed, (b) the prior
// snapshot is for the same head SHA, and (c) the regression would erase a
// prior conflict signal or drop a prior failing-test count while reruns are
// still in flight.
//
// Callers should treat a true return as "return nil from the sync." That
// intentionally also skips UpdateCIStatus and publishPullRequestUpdated for
// this cycle — there is no new information to broadcast, and the next sync
// will emit those side effects once GitHub finishes computing.
//
// One deliberate tradeoff: if a test rerun on the same SHA fixes one of N
// failing checks while another rerun is still in_progress, the new count
// (N-1) is technically accurate but we hold onto the prior N until *all*
// reruns finish. That's preferable to flickering the "Fix tests" button
// off and back on as reruns complete one by one.
func shouldSkipIndeterminateSnapshotWrite(
	mergeStateIndeterminate, testsIndeterminate bool,
	newHeadSHA string,
	newFailingTestCount int,
	prior models.PullRequestHealthCurrent,
) bool {
	if !mergeStateIndeterminate && !testsIndeterminate {
		return false
	}
	if prior.HeadSHA != newHeadSHA {
		return false
	}
	var priorSummary models.PullRequestHealthSummary
	if err := json.Unmarshal(prior.SummaryJSON, &priorSummary); err != nil {
		return false
	}
	priorConflicted := priorSummary.HasConflicts || priorSummary.MergeState == models.PullRequestMergeStateConflicted
	if mergeStateIndeterminate && priorConflicted {
		return true
	}
	if testsIndeterminate && priorSummary.FailingTestCount > newFailingTestCount {
		return true
	}
	return false
}

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
