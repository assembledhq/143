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
			var roundTrip CodeReviewRiskPolicy
			require.NoError(t, json.Unmarshal(encoded, &roundTrip), "new workers should decode the compatibility payload")
			require.Equal(t, resolved.RiskPolicy, ResolveCodeReviewPolicyConfig(&CodeReviewPolicyConfig{RiskPolicy: roundTrip}).RiskPolicy, "the legacy projection must not collapse independent limits on new workers")
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

func TestCodeReviewRiskPolicyLegacySerialization(t *testing.T) {
	t.Parallel()
	// This is the old binary's JSON shape. It ignores both independent limits.
	type legacyRiskPolicy struct {
		MaxFilesChanged      int  `json:"max_files_changed"`
		MaxLinesChanged      int  `json:"max_lines_changed"`
		RequirePassingChecks bool `json:"require_passing_checks"`
	}
	tests := []struct {
		name                                      string
		additions, deletions, expectedLegacyLimit int
	}{
		{"custom limit above default", 1000, 1000, 1000},
		{"custom limit below default", 100, 100, 100},
		{"additions are stricter", 100, 900, 100},
		{"deletions are stricter", 900, 100, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := DefaultCodeReviewPolicyConfig()
			policy.RiskPolicy.MaxAdditions, policy.RiskPolicy.MaxDeletions = tt.additions, tt.deletions
			policy.RiskPolicy.RequirePassingChecks = true
			encoded, err := json.Marshal(policy.RiskPolicy)
			require.NoError(t, err, "new policies should serialize for both worker versions")
			var legacy legacyRiskPolicy
			require.NoError(t, json.Unmarshal(encoded, &legacy), "an old worker should decode the policy")
			require.Equal(t, legacyRiskPolicy{MaxFilesChanged: 5, MaxLinesChanged: tt.expectedLegacyLimit, RequirePassingChecks: true}, legacy, "old workers should retain a safe configured limit and unrelated safeguards instead of defaulting to 300")
			require.LessOrEqual(t, legacy.MaxLinesChanged, tt.additions, "an old worker's combined budget must not exceed the additions limit")
			require.LessOrEqual(t, legacy.MaxLinesChanged, tt.deletions, "an old worker's combined budget must not exceed the deletions limit")

			var current CodeReviewRiskPolicy
			require.NoError(t, json.Unmarshal(encoded, &current), "new workers should still decode the payload")
			require.Equal(t, policy.RiskPolicy, ResolveCodeReviewPolicyConfig(&CodeReviewPolicyConfig{RiskPolicy: current}).RiskPolicy, "new workers should preserve the independent limits and all other effective policy fields")

			// An old API may save the policy during rollback, discarding unknown fields.
			oldSave, err := json.Marshal(legacy)
			require.NoError(t, err, "an old API should be able to reserialize its policy")
			require.NoError(t, json.Unmarshal(oldSave, &current), "new workers should accept an old API's saved policy after rolling forward")
			require.Equal(t, CodeReviewRiskPolicy{MaxFilesChanged: 5, MaxAdditions: tt.expectedLegacyLimit, MaxDeletions: tt.expectedLegacyLimit, RequirePassingChecks: true}, current, "old saves should roll forward into conservative independent limits")
		})
	}
}
