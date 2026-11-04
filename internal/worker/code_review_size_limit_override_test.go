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

	request := codereviewsvc.ReviewRequestContext{
		OverrideSizeLimits:        true,
		OverrideSizeLimitsHeadSHA: "head",
		OverrideSizeLimitsBaseSHA: "base",
		OverrideSizeLimitsBaseRef: "main",
	}
	tests := []struct {
		name       string
		context    *codereviewsvc.ReviewRequestContext
		headSHA    string
		prBase     string
		healthBase string
		expected   bool
	}{
		{name: "automatic review has no request context", headSHA: "head", prBase: "base"},
		{name: "plain request does not opt in", context: &codereviewsvc.ReviewRequestContext{Body: "Please look again."}, headSHA: "head", prBase: "base"},
		{name: "unbound override stays off", context: &codereviewsvc.ReviewRequestContext{OverrideSizeLimits: true}, headSHA: "head", prBase: "base"},
		{name: "legacy head-only override stays off", context: &codereviewsvc.ReviewRequestContext{OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head"}, headSHA: "head", prBase: "base"},
		{name: "head changed", context: &request, headSHA: "new-head", prBase: "base"},
		{name: "PR base changed", context: &request, headSHA: "head", prBase: "new-base", healthBase: "base"},
		{name: "health base changed", context: &request, headSHA: "head", prBase: "base", healthBase: "new-base"},
		{name: "base unknown", context: &request, headSHA: "head"},
		{name: "base available from health", context: &request, headSHA: "head", healthBase: "base", expected: true},
		{name: "original commits match", context: &request, headSHA: "head", prBase: "base", expected: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := runCodeReviewPayload{HeadSHA: tt.headSHA, RequestContext: tt.context}
			pr := models.PullRequest{BaseSHA: &tt.prBase}
			health := &models.PullRequestHealthResponse{BaseSHA: tt.healthBase}
			require.Equal(t, tt.expected, codeReviewOverrideSizeLimits(job, pr, health), "the override should require the original comparison commits and a bound base branch")
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
	job := runCodeReviewPayload{SessionID: uuid.New(), HeadSHA: "head", RequestContext: &codereviewsvc.ReviewRequestContext{OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head", OverrideSizeLimitsBaseSHA: "base", OverrideSizeLimitsBaseRef: "main"}}
	base := "base"
	risk := codeReviewStableDeterministicRisk(policy, job, models.PullRequest{BaseSHA: &base}, nil, changedFiles, true)
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
