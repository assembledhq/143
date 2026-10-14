package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newCodeReviewUncertaintyReason() CodeReviewUncertaintyReason {
	return CodeReviewUncertaintyReason{
		MissingEvidence:    "The generated role map for the new reviewer capability is absent.",
		MaterialImpact:     "Without the role mapping, the changed authorization check may grant viewers write access.",
		ResolutionEvidence: "The generated role map showing which roles receive the new capability.",
	}
}

func TestValidateCodeReviewUncertaintyReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		unresolved bool
		reasons    []CodeReviewUncertaintyReason
		errorText  string
	}{
		{name: "historical false without reasons"},
		{name: "false with empty reasons", reasons: []CodeReviewUncertaintyReason{}},
		{name: "material evidence gap", unresolved: true, reasons: []CodeReviewUncertaintyReason{newCodeReviewUncertaintyReason()}},
		{name: "bare true", unresolved: true, errorText: "requires at least one complete uncertainty reason"},
		{name: "contradictory reasons", reasons: []CodeReviewUncertaintyReason{newCodeReviewUncertaintyReason()}, errorText: "require unresolved_uncertainty=true"},
		{name: "missing evidence", unresolved: true, reasons: []CodeReviewUncertaintyReason{{MaterialImpact: "Viewer authorization is unproven.", ResolutionEvidence: "The role map."}}, errorText: "missing missing_evidence"},
		{name: "blank impact", unresolved: true, reasons: []CodeReviewUncertaintyReason{{MissingEvidence: "The role map.", MaterialImpact: " \n ", ResolutionEvidence: "The role map."}}, errorText: "missing material_impact"},
		{name: "missing resolution", unresolved: true, reasons: []CodeReviewUncertaintyReason{{MissingEvidence: "The role map.", MaterialImpact: "Viewer authorization is unproven."}}, errorText: "missing resolution_evidence"},
		{name: "generic impact", unresolved: true, reasons: []CodeReviewUncertaintyReason{{MissingEvidence: "The role map.", MaterialImpact: "Important uncertainty remained.", ResolutionEvidence: "The role map."}}, errorText: "material_impact must explain the concrete evidence and impact"},
		{name: "multiline generic punctuation", unresolved: true, reasons: []CodeReviewUncertaintyReason{{MissingEvidence: "The role map.", MaterialImpact: "Important\nuncertainty\n remained \n.\n", ResolutionEvidence: "The role map."}}, errorText: "material_impact must explain the concrete evidence and impact"},
		{name: "multiline generic impact", unresolved: true, reasons: []CodeReviewUncertaintyReason{{MissingEvidence: "The role map.", MaterialImpact: "Important\n\tuncertainty\r\n remained.", ResolutionEvidence: "The role map."}}, errorText: "material_impact must explain the concrete evidence and impact"},
		{name: "oversized explanation", unresolved: true, reasons: []CodeReviewUncertaintyReason{{MissingEvidence: strings.Repeat("a", 1_001), MaterialImpact: "Viewer authorization is unproven.", ResolutionEvidence: "The role map."}}, errorText: "missing_evidence exceeds 1000 characters"},
		{name: "too many reasons", unresolved: true, reasons: make([]CodeReviewUncertaintyReason, 11), errorText: "exceed limit of 10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCodeReviewUncertaintyReasons(tt.unresolved, tt.reasons)
			if tt.errorText != "" {
				require.ErrorContains(t, err, tt.errorText, "incomplete or contradictory uncertainty must be rejected")
				return
			}
			require.NoError(t, err, "compatible empty or complete uncertainty evidence should be accepted")
		})
	}
}

func TestCodeReviewUncertaintyRiskPresentation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		reasons  []CodeReviewUncertaintyReason
		expected []CodeReviewRiskReason
	}{
		{name: "historical generic fallback", expected: []CodeReviewRiskReason{{Code: CodeReviewRiskReasonUnresolvedUncertainty}}},
		{name: "concrete material gap", reasons: []CodeReviewUncertaintyReason{newCodeReviewUncertaintyReason()}, expected: []CodeReviewRiskReason{{Code: CodeReviewRiskReasonUnresolvedUncertainty, Subject: newCodeReviewUncertaintyReason().Message()}}},
		{name: "all gaps preserved", reasons: []CodeReviewUncertaintyReason{newCodeReviewUncertaintyReason(), {MissingEvidence: "The base-version blob of the removed auth guard is unavailable.", MaterialImpact: "The replacement may remove the viewer restriction.", ResolutionEvidence: "The auth guard at the pinned merge base."}}, expected: []CodeReviewRiskReason{{Code: CodeReviewRiskReasonUnresolvedUncertainty, Subject: newCodeReviewUncertaintyReason().Message()}, {Code: CodeReviewRiskReasonUnresolvedUncertainty, Subject: "Missing evidence: The base-version blob of the removed auth guard is unavailable. Why it matters: The replacement may remove the viewer restriction. To resolve: The auth guard at the pinned merge base."}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := DefaultCodeReviewPolicyConfig()
			policy.Enabled = true
			risk := EvaluateCodeReviewRisk(policy, CodeReviewRiskInput{DescriptionPassed: true, ChecksPassing: true, UpToDate: true, UnresolvedUncertainty: true, UncertaintyReasons: tt.reasons})
			require.Equal(t, tt.expected, risk.ReasonDetails, "every validated material gap should survive as a typed risk reason")
			require.False(t, risk.Acceptable, "uncertainty must continue to withhold approval")
			body := BuildCodeReviewFinalReviewBody(CodeReviewFinalReviewInput{Decision: CodeReviewDecisionNeedsHumanReview, RiskReasons: risk.ReasonDetails})
			for _, reason := range tt.expected {
				if reason.Subject != "" {
					require.Equal(t, reason.Subject, reason.Message(), "API risk text should show the explanation directly")
					require.Contains(t, body, "- "+reason.Subject, "GitHub review should expose missing evidence, material impact and resolution")
				} else {
					require.Equal(t, "orchestrator reported unresolved uncertainty", reason.Message(), "historical API reasons must remain renderable")
					require.Contains(t, body, "The automated review could not resolve a material uncertainty.", "historical GitHub reasons need a readable fallback")
				}
			}
		})
	}
}

func TestCodeReviewContradictoryUncertaintyRisk(t *testing.T) {
	t.Parallel()
	policy := DefaultCodeReviewPolicyConfig()
	policy.Enabled = true
	risk := EvaluateCodeReviewRisk(policy, CodeReviewRiskInput{DescriptionPassed: true, ChecksPassing: true, UpToDate: true, UncertaintyReasons: []CodeReviewUncertaintyReason{newCodeReviewUncertaintyReason()}})
	require.Equal(t, []CodeReviewRiskReason{{Code: CodeReviewRiskReasonOrchestratorSynthesisInvalid}}, risk.ReasonDetails, "false uncertainty with nonempty reasons must fail closed in risk evaluation")
	require.False(t, risk.Acceptable, "contradictory reasons must never produce approval")
}

func TestCodeReviewHistoricalUncertaintyWhitespace(t *testing.T) {
	t.Parallel()
	reason := CodeReviewUncertaintyReason{MissingEvidence: "\n The\t generated  role map.\n", MaterialImpact: " Viewers\r\nmay get write access. ", ResolutionEvidence: "Inspect\n the pinned map.\t"}
	expected := "Missing evidence: The generated role map. Why it matters: Viewers may get write access. To resolve: Inspect the pinned map."
	require.Equal(t, expected, reason.Message(), "historical structured reasons must publish as a single readable line")
	historical := CodeReviewRiskReason{Code: CodeReviewRiskReasonUnresolvedUncertainty, Subject: "Missing evidence:\nThe generated role map.\tWhy it matters: Viewers may get write access. To resolve: Inspect the pinned map."}
	require.Equal(t, expected, historical.Message(), "historical persisted risk subjects must render on one line")
	require.Contains(t, BuildCodeReviewFinalReviewBody(CodeReviewFinalReviewInput{Decision: CodeReviewDecisionNeedsHumanReview, RiskReasons: []CodeReviewRiskReason{historical}}), "- "+expected+"\n", "historical published risk subjects must not create injected multiline bullets")
	policy := DefaultCodeReviewPolicyConfig()
	policy.Enabled = true
	risk := EvaluateCodeReviewRisk(policy, CodeReviewRiskInput{DescriptionPassed: true, ChecksPassing: true, UpToDate: true, UnresolvedUncertainty: true, UncertaintyReasons: []CodeReviewUncertaintyReason{reason}})
	require.Equal(t, []CodeReviewRiskReason{{Code: CodeReviewRiskReasonUnresolvedUncertainty, Subject: expected}}, risk.ReasonDetails, "persisted risk subjects must collapse embedded whitespace")
	require.Contains(t, BuildCodeReviewFinalReviewBody(CodeReviewFinalReviewInput{Decision: CodeReviewDecisionNeedsHumanReview, RiskReasons: risk.ReasonDetails}), "- "+expected+"\n", "published review must keep the complete explanation in one bullet")
}
