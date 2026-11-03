package worker

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewOverrideSizeLimits(t *testing.T) {
	t.Parallel()

	require.False(t, codeReviewOverrideSizeLimits(nil), "an automatic review has no request context")
	require.False(t, codeReviewOverrideSizeLimits(&codereviewsvc.ReviewRequestContext{Body: "Please look again."}), "a plain request should not opt in")
	require.True(t, codeReviewOverrideSizeLimits(&codereviewsvc.ReviewRequestContext{OverrideSizeLimits: true}), "an opted-in request should be recognized")
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
