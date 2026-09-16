package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewRiskPolicySizeLimitCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		payload   string
		additions int
		deletions int
	}{
		{"defaults", `{}`, 300, 300},
		{"legacy saved policy", `{"max_lines_changed":1000}`, 1000, 1000},
		{"independent limits", `{"max_additions":100,"max_deletions":900}`, 100, 900},
		{"explicit additions win", `{"max_lines_changed":1000,"max_additions":100}`, 100, 1000},
		{"explicit deletions win", `{"max_lines_changed":1000,"max_deletions":900}`, 1000, 900},
		{"both override legacy", `{"max_lines_changed":1000,"max_additions":100,"max_deletions":900}`, 100, 900},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var risk CodeReviewRiskPolicy
			require.NoError(t, json.Unmarshal([]byte(tt.payload), &risk), "saved risk policy should decode")
			resolved := ResolveCodeReviewPolicyConfig(&CodeReviewPolicyConfig{RiskPolicy: risk})
			expected := DefaultCodeReviewPolicyConfig().RiskPolicy
			expected.MaxAdditions, expected.MaxDeletions = tt.additions, tt.deletions
			require.Equal(t, expected, resolved.RiskPolicy, "legacy limits should carry forward without overriding explicit limits")
			encoded, err := json.Marshal(resolved.RiskPolicy)
			require.NoError(t, err, "resolved risk policy should serialize")
			require.NotContains(t, string(encoded), "max_lines_changed", "new policy versions should only persist independent limits")
			require.Equal(t, resolved, ResolveCodeReviewPolicyConfig(&resolved), "resolving a policy repeatedly should preserve independent limits")
		})
	}
}

func TestEvaluateCodeReviewIndependentSizeLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		additions int
		deletions int
		expected  []CodeReviewRiskReason
	}{
		{"empty diff", 0, 0, nil},
		{"both at their limits", 100, 900, nil},
		{"additions only", 101, 0, []CodeReviewRiskReason{{Code: CodeReviewRiskReasonAdditionsLimitExceeded, Actual: 101, Limit: 100}}},
		{"deletions only", 0, 901, []CodeReviewRiskReason{{Code: CodeReviewRiskReasonDeletionsLimitExceeded, Actual: 901, Limit: 900}}},
		{"both over limits", 101, 901, []CodeReviewRiskReason{{Code: CodeReviewRiskReasonAdditionsLimitExceeded, Actual: 101, Limit: 100}, {Code: CodeReviewRiskReasonDeletionsLimitExceeded, Actual: 901, Limit: 900}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := DefaultCodeReviewPolicyConfig()
			policy.RiskPolicy.MaxAdditions, policy.RiskPolicy.MaxDeletions = 100, 900
			risk := EvaluateCodeReviewRisk(policy, CodeReviewRiskInput{Additions: tt.additions, Deletions: tt.deletions, DescriptionPassed: true})
			require.Equal(t, tt.expected, risk.ReasonDetails, "each size limit should be evaluated independently")
			require.Equal(t, len(tt.expected) == 0, risk.Acceptable, "only exceeding an individual limit should block approval")
		})
	}
}

func TestCodeReviewSizeLimitValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		additions, deletions int
		message              string
	}{
		{"zero additions", 0, 300, "max_additions must be positive"},
		{"negative additions", -1, 300, "max_additions must be positive"},
		{"zero deletions", 300, 0, "max_deletions must be positive"},
		{"negative deletions", 300, -1, "max_deletions must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := DefaultCodeReviewPolicyConfig()
			policy.RiskPolicy.MaxAdditions, policy.RiskPolicy.MaxDeletions = tt.additions, tt.deletions
			require.EqualError(t, policy.Validate(), tt.message, "both size limits must be positive")
		})
	}
}

func TestCodeReviewSizeLimitFeedback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                           string
		code                           CodeReviewRiskReasonCode
		message, explanation, fragment string
	}{
		{"additions", CodeReviewRiskReasonAdditionsLimitExceeded, "additions 301 exceeds policy limit 300", "This change has 301 additions; the policy limit is 300.", "policy-max-additions"},
		{"deletions", CodeReviewRiskReasonDeletionsLimitExceeded, "deletions 301 exceeds policy limit 300", "This change has 301 deletions; the policy limit is 300.", "policy-max-deletions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reason := CodeReviewRiskReason{Code: tt.code, Actual: 301, Limit: 300}
			require.NoError(t, tt.code.Validate(), "independent size reasons must be valid API values")
			require.True(t, IsCodeReviewStableDeterministicRiskReason(tt.code), "size blockers should support early stopping and policy analytics")
			require.Equal(t, tt.message, reason.Message(), "risk message should identify the relevant limit")
			require.Equal(t, codeReviewBlockerGroupPolicy, codeReviewRiskReasonBlockerGroup(tt.code), "size blockers belong to policy requirements")
			require.Equal(t, tt.explanation, humanizeCodeReviewRiskReason(reason, nil), "GitHub feedback should explain the individual measurement")
			require.Equal(t, tt.explanation+" [View policy setting](https://143.dev/code-reviews?tab=policy#"+tt.fragment+")", codeReviewExplanationWithSettingsLink(tt.explanation, "https://143.dev/code-reviews?tab=policy", tt.code), "feedback should link to the matching control")
		})
	}
}
