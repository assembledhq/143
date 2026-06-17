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
func detectIndeterminateSignals(mergeable *bool, githubState string, checkRuns []gitHubCheckRun, commitStatuses []gitHubCommitStatus) (mergeStateIndeterminate, testsIndeterminate bool) {
	state := strings.ToLower(strings.TrimSpace(githubState))
	mergeStateIndeterminate = mergeable == nil && !isDefinitiveNullMergeabilityState(state)
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
	for _, status := range commitStatuses {
		if classifyCheckRunCategory(status.Context) != models.PullRequestCheckCategoryTest {
			continue
		}
		if normalizeCommitStatus(status) == models.PullRequestCheckStatusPending {
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

func isDefinitiveNullMergeabilityState(githubState string) bool {
	switch strings.ToLower(strings.TrimSpace(githubState)) {
	case "behind", "dirty", "blocked", "draft", "unstable", "has_hooks":
		return true
	default:
		return false
	}
}

func normalizeMergeState(mergeable *bool, githubState string) (models.PullRequestMergeState, bool) {
	state := strings.ToLower(strings.TrimSpace(githubState))
	switch {
	case state == "behind":
		return models.PullRequestMergeStateBehind, false
	case state == "dirty":
		return models.PullRequestMergeStateConflicted, true
	case state == "blocked" || state == "draft" || state == "unstable" || state == "has_hooks":
		return models.PullRequestMergeStateBlocked, false
	case mergeable == nil:
		return models.PullRequestMergeStateMergeabilityPending, false
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

func normalizeCheckRunStatus(check gitHubCheckRun) models.PullRequestCheckStatus {
	switch strings.ToLower(strings.TrimSpace(check.Status)) {
	case "queued", "waiting", "in_progress", "requested", "pending":
		return models.PullRequestCheckStatusPending
	}

	switch strings.ToLower(strings.TrimSpace(check.Conclusion)) {
	case "success", "neutral", "skipped":
		return models.PullRequestCheckStatusPassed
	case "failure", "cancelled", "timed_out", "startup_failure", "stale", "action_required":
		return models.PullRequestCheckStatusFailed
	default:
		if strings.EqualFold(check.Status, "completed") {
			return models.PullRequestCheckStatusFailed
		}
		return models.PullRequestCheckStatusPending
	}
}

func normalizeCommitStatus(status gitHubCommitStatus) models.PullRequestCheckStatus {
	switch strings.ToLower(strings.TrimSpace(status.State)) {
	case "success":
		return models.PullRequestCheckStatusPassed
	case "failure", "error":
		return models.PullRequestCheckStatusFailed
	default:
		return models.PullRequestCheckStatusPending
	}
}

func commitStatusProvider(context string) string {
	name := strings.ToLower(strings.TrimSpace(context))
	switch {
	case strings.Contains(name, "circleci"):
		return "circleci"
	case strings.Contains(name, "github"):
		return "github"
	default:
		return "github_status"
	}
}

func normalizeStoredCheckStatus(status models.PullRequestCheckStatus) models.PullRequestCheckStatus {
	switch status {
	case models.PullRequestCheckStatusPassed,
		models.PullRequestCheckStatusFailed,
		models.PullRequestCheckStatusPending:
		return status
	default:
		return models.PullRequestCheckStatusPending
	}
}

func normalizeStoredCheckSummaries(summary *models.PullRequestHealthSummary) {
	if summary == nil {
		return
	}

	remainingFailedTests := summary.FailingTestCount
	for _, check := range summary.Checks {
		if classifyStoredCheckStatus(check) == models.PullRequestCheckStatusFailed && check.Category == models.PullRequestCheckCategoryTest && remainingFailedTests > 0 {
			remainingFailedTests--
		}
	}

	for i := range summary.Checks {
		if summary.Checks[i].Status.Validate() == nil {
			continue
		}
		if summary.Checks[i].Category == models.PullRequestCheckCategoryTest && remainingFailedTests > 0 {
			summary.Checks[i].Status = models.PullRequestCheckStatusFailed
			remainingFailedTests--
			continue
		}
		summary.Checks[i].Status = models.PullRequestCheckStatusPending
	}
}

func determineChecksConfirmed(checks []models.PullRequestCheckSummary, requiredChecksConfigured bool) bool {
	if len(checks) > 0 {
		for _, check := range checks {
			if classifyStoredCheckStatus(check) != models.PullRequestCheckStatusPassed && classifyStoredCheckStatus(check) != models.PullRequestCheckStatusFailed {
				return false
			}
		}
		return true
	}
	return !requiredChecksConfigured
}

func classifyStoredCheckStatus(check models.PullRequestCheckSummary) models.PullRequestCheckStatus {
	return normalizeStoredCheckStatus(check.Status)
}

func deriveAggregateCIStatus(checks []models.PullRequestCheckSummary) string {
	hasPending := false
	for _, check := range checks {
		switch classifyStoredCheckStatus(check) {
		case models.PullRequestCheckStatusFailed:
			return "failure"
		case models.PullRequestCheckStatusPending:
			hasPending = true
		}
	}
	if hasPending {
		return "pending"
	}
	return "success"
}

func buildPRHealthSummaryText(health models.PullRequestHealthResponse) string {
	conflicted := health.HasConflicts || health.MergeState == models.PullRequestMergeStateConflicted
	switch {
	case conflicted && health.FailingTestCount > 0:
		return fmt.Sprintf("PR #%d is blocked by conflicts and %d failing test jobs.", health.PullRequestNumber, health.FailingTestCount)
	case conflicted:
		return fmt.Sprintf("PR #%d is blocked by merge conflicts.", health.PullRequestNumber)
	case health.MergeState == models.PullRequestMergeStateBlocked:
		return fmt.Sprintf("PR #%d is blocked by GitHub merge requirements.", health.PullRequestNumber)
	case health.MergeState == models.PullRequestMergeStateMergeabilityPending || health.MergeState == models.PullRequestMergeStateUnknown:
		return fmt.Sprintf("PR #%d is waiting for GitHub to finish checking mergeability.", health.PullRequestNumber)
	case health.MergeState == models.PullRequestMergeStateBehind:
		return fmt.Sprintf("PR #%d needs the base branch updated before merging.", health.PullRequestNumber)
	case health.FailingTestCount == 1:
		return fmt.Sprintf("PR #%d has 1 failing test job.", health.PullRequestNumber)
	case health.FailingTestCount > 1:
		return fmt.Sprintf("PR #%d has %d failing test jobs.", health.PullRequestNumber, health.FailingTestCount)
	case !health.ChecksConfirmed:
		return fmt.Sprintf("PR #%d is waiting for required checks to report passing.", health.PullRequestNumber)
	case len(health.Checks) == 0:
		return fmt.Sprintf("PR #%d is mergeable. No CI checks are configured for this repository.", health.PullRequestNumber)
	default:
		return fmt.Sprintf("PR #%d is mergeable and all required test checks are passing.", health.PullRequestNumber)
	}
}
