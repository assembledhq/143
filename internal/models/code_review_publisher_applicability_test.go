package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func publisherTestFallback() *CodeReviewDescriptionApplicability {
	return &CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"**/*.tsx"}}
}

func TestCodeReviewDescriptionApplicabilityPublisherResultValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		applicability CodeReviewDescriptionApplicability
		message       string
	}{
		{name: "publisher result with paths fallback", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, Fallback: publisherTestFallback()}},
		{name: "publisher result with thresholded paths fallback", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, Fallback: &CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPaths, MinFilesChanged: 3, PathPatterns: []string{"web/**"}}}},
		{name: "paths without fallback is unchanged", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"web/**"}}},
		{name: "publisher result without fallback", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher}, message: "publisher_result description applicability requires a paths fallback"},
		{name: "publisher result with nontrivial fallback", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, Fallback: &CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityNontrivial, MinFilesChanged: 2}}, message: "publisher_result description applicability requires a paths fallback"},
		{name: "publisher result with nested publisher fallback", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, Fallback: &CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, Fallback: publisherTestFallback()}}, message: "publisher_result description applicability requires a paths fallback"},
		{name: "publisher result with top-level path patterns", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, PathPatterns: []string{"web/**"}, Fallback: publisherTestFallback()}, message: "publisher_result description applicability keeps thresholds and path patterns in its fallback"},
		{name: "publisher result with top-level threshold", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, MinLinesChanged: 10, Fallback: publisherTestFallback()}, message: "publisher_result description applicability keeps thresholds and path patterns in its fallback"},
		{name: "fallback with negative threshold", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, Fallback: &CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPaths, MinFilesChanged: -1}}, message: "description applicability thresholds must not be negative"},
		{name: "paths with a fallback", applicability: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPaths, Fallback: publisherTestFallback()}, message: "only publisher_result description applicability may declare a fallback"},
		{name: "unknown kind", applicability: CodeReviewDescriptionApplicability{Kind: "render_change"}, message: `invalid CodeReviewDescriptionApplicabilityKind: "render_change"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := DefaultCodeReviewPolicyConfig()
			config.DescriptionPolicy.Requirements = []CodeReviewDescriptionRequirement{{Key: "ui_evidence", Title: "Screens", Required: true, EvidenceKind: CodeReviewDescriptionEvidenceKindVisual, AppliesWhen: tt.applicability}}
			err := config.Validate()
			if tt.message == "" {
				require.NoError(t, tt.applicability.Validate(), "valid applicability should pass")
				require.NoError(t, err, "a policy with valid applicability should pass")
				return
			}
			require.EqualError(t, tt.applicability.Validate(), tt.message, "invalid applicability should explain the violation")
			var fieldErr *CodeReviewPolicyValidationError
			require.ErrorAs(t, err, &fieldErr, "policy validation should reject the invalid applicability")
			require.Equal(t, CodeReviewPolicyFieldDescriptionPolicy, fieldErr.Field, "the error should point at description_policy")
		})
	}
}

func TestCodeReviewDescriptionApplicabilityPublisherResultPersistence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		patch       string
		expected    CodeReviewDescriptionApplicability
		expectError bool
	}{
		{
			name:     "publisher result with fallback round trips",
			patch:    `{"description_policy":{"requirements":[{"key":"ui_evidence","title":"Screens","prompt":"Show it","required":true,"evidence_kind":"visual","applies_when":{"kind":"publisher_result","fallback":{"kind":"paths","path_patterns":["**/*.tsx"]}}}]}}`,
			expected: CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, Fallback: publisherTestFallback()},
		},
		{
			name:        "publisher result without fallback is rejected",
			patch:       `{"description_policy":{"requirements":[{"key":"ui_evidence","title":"Screens","prompt":"Show it","required":true,"evidence_kind":"visual","applies_when":{"kind":"publisher_result"}}]}}`,
			expectError: true,
		},
		{
			name:        "unknown fallback field is rejected",
			patch:       `{"description_policy":{"requirements":[{"key":"ui_evidence","title":"Screens","prompt":"Show it","required":true,"evidence_kind":"visual","applies_when":{"kind":"publisher_result","fallback":{"kind":"paths","globs":["**/*.tsx"]}}}]}}`,
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			patched, err := ApplyCodeReviewPolicyMergePatch(DefaultCodeReviewPolicyConfig(), json.RawMessage(tt.patch))
			if tt.expectError {
				require.Error(t, err, "invalid publisher applicability must not be saved")
				return
			}
			require.NoError(t, err, "valid publisher applicability should save")
			require.Equal(t, tt.expected, patched.DescriptionPolicy.Requirements[0].AppliesWhen, "publisher applicability and its fallback should survive patching and resolution")
		})
	}
}

const previousDefaultDescriptionPolicyJSON = `{"requirements":[{"key":"description","title":"Understandable description","prompt":"Explain what is changing and why clearly enough for a reviewer to understand the intent.","required":true,"evidence_kind":"general","applies_when":{}},{"key":"testing","title":"Testing evidence","prompt":"Describe the testing or validation evidence for nontrivial changes.","required":true,"evidence_kind":"general","applicability":"nontrivial","applies_when":{"kind":"nontrivial","min_files_changed":2,"min_lines_changed":31}},{"key":"ui_evidence","title":"Screenshots or preview link","prompt":"Include screenshots or a preview link for frontend or UI-visible changes. Existing screenshots are acceptable when they still accurately represent the affected UI or workflow. Do not require a new screenshot merely because the pull request head changed. Require updated visual evidence only when the diff materially changes the rendered state shown by the existing evidence. Treat small changes that do not materially alter rendered output, non-visible changes, test-only changes, refactors, and unrelated changes as not applicable.","required":true,"evidence_kind":"visual","applicability":"paths","applies_when":{"kind":"paths","path_patterns":["frontend/**","apps/web/**","**/app/**","**/components/**","**/pages/**","**/*.tsx","**/*.jsx","**/*.css"]}}]}`

func TestDefaultDescriptionRequirementsChangeOnlyUIEvidenceApplicability(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(DefaultCodeReviewPolicyConfig().DescriptionPolicy)
	require.NoError(t, err, "default description policy should encode")
	var current, previous struct {
		Requirements []map[string]json.RawMessage `json:"requirements"`
	}
	require.NoError(t, json.Unmarshal(encoded, &current), "current defaults should decode")
	require.NoError(t, json.Unmarshal([]byte(previousDefaultDescriptionPolicyJSON), &previous), "previous defaults should decode")
	require.Len(t, current.Requirements, len(previous.Requirements), "the default requirement list should keep its length")
	for i := range previous.Requirements {
		key := string(previous.Requirements[i]["key"])
		if key != `"ui_evidence"` {
			require.Equal(t, previous.Requirements[i], current.Requirements[i], "default requirement %s should be byte-for-byte unchanged", key)
			continue
		}
		previousApplicability, currentApplicability := previous.Requirements[i]["applies_when"], current.Requirements[i]["applies_when"]
		delete(previous.Requirements[i], "applies_when")
		delete(current.Requirements[i], "applies_when")
		require.Equal(t, previous.Requirements[i], current.Requirements[i], "ui_evidence should change only its applies_when")
		require.JSONEq(t, `{"kind":"publisher_result","fallback":`+string(previousApplicability)+`}`, string(currentApplicability), "ui_evidence should wrap the previous paths applicability as its fallback")
	}
}

func TestDefaultUIEvidenceUsesPublisherResultWithTodaysPaths(t *testing.T) {
	t.Parallel()
	var uiEvidence *CodeReviewDescriptionRequirement
	defaults := DefaultCodeReviewPolicyConfig()
	for i := range defaults.DescriptionPolicy.Requirements {
		if defaults.DescriptionPolicy.Requirements[i].Key == "ui_evidence" {
			uiEvidence = &defaults.DescriptionPolicy.Requirements[i]
		}
	}
	require.NotNil(t, uiEvidence, "the default policy should keep the ui_evidence requirement")
	require.Equal(t, CodeReviewDescriptionApplicability{
		Kind: CodeReviewDescriptionApplicabilityPublisher,
		Fallback: &CodeReviewDescriptionApplicability{
			Kind:         CodeReviewDescriptionApplicabilityPaths,
			PathPatterns: []string{"frontend/**", "apps/web/**", "**/app/**", "**/components/**", "**/pages/**", "**/*.tsx", "**/*.jsx", "**/*.css"},
		},
	}, uiEvidence.AppliesWhen, "ui_evidence should defer to the publisher and fall back to today's path patterns")
	require.Equal(t, "paths", uiEvidence.Applicability, "the legacy applicability label should stay paths so fallback prompts are unchanged")
	require.True(t, uiEvidence.Required, "ui_evidence should stay required")
	require.Equal(t, CodeReviewDescriptionEvidenceKindVisual, uiEvidence.EvidenceKind, "ui_evidence should stay visual")
	for _, requirement := range defaults.DescriptionPolicy.Requirements {
		if requirement.Key != "ui_evidence" {
			require.NotEqual(t, CodeReviewDescriptionApplicabilityPublisher, requirement.AppliesWhen.Kind, "other default requirements should keep their applicability")
		}
	}
	require.NoError(t, defaults.Validate(), "the default policy should validate")
	require.False(t, CodeReviewDescriptionApplicability{Fallback: publisherTestFallback()}.Empty(), "a fallback alone is not an empty applicability")
}

func TestCodeReviewDescriptionApplicabilityPublisherRenderChange(t *testing.T) {
	t.Parallel()
	authoritative := func(change CodeReviewUIRenderChange) *CodeReviewRiskTierSignal {
		return &CodeReviewRiskTierSignal{Enabled: true, Source: CodeReviewRiskTierSourceAuthoritative, Tier: CodeReviewRiskTierHigh, UIRenderChange: change}
	}
	publisher := CodeReviewDescriptionApplicability{Kind: CodeReviewDescriptionApplicabilityPublisher, Fallback: publisherTestFallback()}
	paths := *publisherTestFallback()
	tests := []struct {
		name          string
		applicability CodeReviewDescriptionApplicability
		signal        *CodeReviewRiskTierSignal
		expected      CodeReviewUIRenderChange
	}{
		{name: "publisher kind with false", applicability: publisher, signal: authoritative(CodeReviewUIRenderChangeFalse), expected: CodeReviewUIRenderChangeFalse},
		{name: "publisher kind with true", applicability: publisher, signal: authoritative(CodeReviewUIRenderChangeTrue), expected: CodeReviewUIRenderChangeTrue},
		{name: "publisher kind with unknown", applicability: publisher, signal: authoritative(CodeReviewUIRenderChangeUnknown)},
		{name: "publisher kind without a signal", applicability: publisher},
		{name: "publisher kind with the setting disabled", applicability: publisher, signal: &CodeReviewRiskTierSignal{UIRenderChange: CodeReviewUIRenderChangeFalse}},
		{name: "publisher kind with a stale payload", applicability: publisher, signal: &CodeReviewRiskTierSignal{Enabled: true, Source: CodeReviewRiskTierSourceStaleHead, UIRenderChange: CodeReviewUIRenderChangeFalse}},
		{name: "publisher kind with another app", applicability: publisher, signal: &CodeReviewRiskTierSignal{Enabled: true, Source: CodeReviewRiskTierSourceUntrustedApp, UIRenderChange: CodeReviewUIRenderChangeFalse}},
		{name: "paths kind ignores the publisher", applicability: paths, signal: authoritative(CodeReviewUIRenderChangeFalse)},
		{name: "publisher kind with shadow false", applicability: publisher, signal: &CodeReviewRiskTierSignal{Enabled: true, Source: CodeReviewRiskTierSourceShadowMode}},
		{name: "publisher kind with an authoritative true beside a non-authoritative run", applicability: publisher, signal: &CodeReviewRiskTierSignal{Enabled: true, Source: CodeReviewRiskTierSourceUnparsable, UIRenderChange: CodeReviewUIRenderChangeTrue}, expected: CodeReviewUIRenderChangeTrue},
		{name: "publisher kind with an authoritative false beside a non-authoritative run", applicability: publisher, signal: &CodeReviewRiskTierSignal{Enabled: true, Source: CodeReviewRiskTierSourceLookupFailed, UIRenderChange: CodeReviewUIRenderChangeFalse}},
		{name: "publisher kind with a disabled true", applicability: publisher, signal: &CodeReviewRiskTierSignal{UIRenderChange: CodeReviewUIRenderChangeTrue}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.applicability.PublisherRenderChange(tt.signal), "any authoritative true forces applicability, and false exempts only when every observation is authoritative")
		})
	}
}
