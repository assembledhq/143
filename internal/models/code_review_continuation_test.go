package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewContinuationPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		policy    *CodeReviewContinuationPolicy
		valid     bool
		effective CodeReviewContinuationPolicy
	}{
		{"legacy absent", nil, true, CodeReviewContinuationPolicy{}},
		{"off", &CodeReviewContinuationPolicy{}, true, CodeReviewContinuationPolicy{}},
		{"manual rechecks", &CodeReviewContinuationPolicy{Enabled: true}, true, CodeReviewContinuationPolicy{Enabled: true}},
		{"automatic", &CodeReviewContinuationPolicy{Enabled: true, AutomaticEvidenceRechecks: true}, true, CodeReviewContinuationPolicy{Enabled: true, AutomaticEvidenceRechecks: true}},
		{"automatic without continuation", &CodeReviewContinuationPolicy{AutomaticEvidenceRechecks: true}, false, CodeReviewContinuationPolicy{AutomaticEvidenceRechecks: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.policy.Validate()
			require.Equal(t, tt.valid, err == nil, "continuation settings should validate consistently")
			require.Equal(t, tt.effective, tt.policy.Effective(), "legacy settings should resolve disabled")
		})
	}
}

func TestContinuationPolicyMergePatch(t *testing.T) {
	t.Parallel()
	current := DefaultCodeReviewPolicyConfig()
	current.ContinuationPolicy = &CodeReviewContinuationPolicy{Enabled: true}
	tests := []struct {
		name, patch string
		expected    CodeReviewContinuationPolicy
	}{
		{"unrelated", `{"enabled":true}`, CodeReviewContinuationPolicy{Enabled: true}},
		{"nested automatic", `{"continuation_policy":{"automatic_evidence_rechecks":true}}`, CodeReviewContinuationPolicy{Enabled: true, AutomaticEvidenceRechecks: true}},
		{"explicit reset", `{"continuation_policy":{"enabled":false}}`, CodeReviewContinuationPolicy{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ApplyCodeReviewPolicyMergePatch(current, json.RawMessage(tt.patch))
			require.NoError(t, err, "policy patch should merge presence-aware continuation settings")
			require.Equal(t, tt.expected, got.ContinuationPolicy.Effective(), "policy patch should preserve or update only supplied fields")
		})
	}
}
