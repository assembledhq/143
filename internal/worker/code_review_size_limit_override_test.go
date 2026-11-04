package worker

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewOverrideSizeLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		context  *codereviewsvc.ReviewRequestContext
		expected bool
	}{
		{name: "automatic review has no request context"},
		{name: "plain request does not opt in", context: &codereviewsvc.ReviewRequestContext{Body: "Please look again."}},
		{name: "override without a bound head stays off", context: &codereviewsvc.ReviewRequestContext{OverrideSizeLimits: true}},
		{name: "override bound to an older head stays off", context: &codereviewsvc.ReviewRequestContext{OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "old-head"}},
		{name: "override bound to the reviewed head applies", context: &codereviewsvc.ReviewRequestContext{OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head"}, expected: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, codeReviewOverrideSizeLimits(runCodeReviewPayload{HeadSHA: "head", RequestContext: tt.context}), "the override should apply only to the head it was requested on")
		})
	}
}

func TestCodeReviewStableFailureReviewBodyDisclosesOverriddenLimits(t *testing.T) {
	t.Parallel()

	policy := models.DefaultCodeReviewPolicyConfig()
	policy.RiskPolicy.MaxAdditions = 100
	policy.RiskPolicy.AllowSizeLimitOverride = true
	policy.RiskPolicy.BlockedPathPatterns = []string{"migrations/**"}
	changedFiles := []codereviewsvc.PullRequestFile{{Filename: "migrations/1.sql", Additions: 101}}
	job := runCodeReviewPayload{SessionID: uuid.New(), HeadSHA: "head", RequestContext: &codereviewsvc.ReviewRequestContext{OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head"}}
	risk := codeReviewStableDeterministicRisk(policy, job, models.PullRequest{}, changedFiles, true)
	require.Equal(t, []models.CodeReviewRiskReason{{Code: models.CodeReviewRiskReasonBlockedPath, Subject: "migrations/1.sql"}}, risk.ReasonDetails, "the blocked path should be the remaining stable blocker")

	decision := models.EvaluateCodeReviewDecision(policy, risk)
	body := codeReviewStableFailureReviewBody(&Services{FrontendURL: "https://143.test"}, job, policy, decision, changedFiles)

	require.Contains(t, body, "**Size limits overridden for this review:** This change has 101 additions; the policy limit is 100.", "an early stop should still disclose the skipped limits")
	require.Equal(t, []models.CodeReviewRiskReason{{Code: models.CodeReviewRiskReasonAdditionsLimitExceeded, Actual: 101, Limit: 100}}, decision.OverriddenSizeLimits, "an early stop should still record the skipped limits")
}

func TestCodeReviewSizeLimitOverrideURL(t *testing.T) {
	t.Parallel()

	const reviewNow = "https://143.dev/code-reviews?review_now=review"
	tests := []struct {
		name     string
		allowed  bool
		target   string
		expected string
	}{
		{name: "policy off offers no link", target: reviewNow},
		{name: "review scheduling off offers no link", allowed: true},
		{name: "allowed link preselects the override", allowed: true, target: reviewNow, expected: "https://143.dev/code-reviews?override_size_limits=true&review_now=review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := models.DefaultCodeReviewPolicyConfig()
			policy.RiskPolicy.AllowSizeLimitOverride = tt.allowed
			require.Equal(t, tt.expected, codeReviewSizeLimitOverrideURL(policy, tt.target), "the link should appear only when an override can take effect")
		})
	}
}
