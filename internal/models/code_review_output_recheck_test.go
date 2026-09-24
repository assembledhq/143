package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildCodeReviewFinalReviewBodyEvidenceRecheck(t *testing.T) {
	t.Parallel()

	const actionURL = "https://143.test/code-reviews?recheck=90d8a47d-d87e-4780-90af-040f5144685a"
	const action = "[Re-check PR evidence](" + actionURL + ")"
	const suffix = " Open 143 to confirm the request. Any remaining approval requirements still apply."
	const genericNextSteps = "**Next steps:** Review the explanation and evidence above, address any blockers, then request another automated review or ask a human reviewer to decide."
	const evidenceNextSteps = "**Next steps:** Add the missing evidence under **Testing** or **Evidence** in the PR description or a comment, then " + action + "." + suffix
	const findingNextSteps = "**Next steps:** If you have evidence that addresses these findings, add it under **Testing** or **Evidence** in the PR description or a comment, then " + action + "." + suffix
	const checkNextSteps = "**Next steps:** Once updated CI results are available, " + action + "." + suffix
	tests := []struct {
		name              string
		reasons           []CodeReviewRiskReason
		configure         func(*CodeReviewFinalReviewInput)
		expectedNextSteps string
		expectedActions   int
	}{
		{
			name:              "missing evidence",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonDescriptionFailed}},
			expectedNextSteps: evidenceNextSteps, expectedActions: 1,
		},
		{
			name:              "reviewer findings can be addressed with evidence conditionally",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonBlockingFindings, Actual: 1}},
			expectedNextSteps: findingNextSteps, expectedActions: 1,
		},
		{
			name:              "CI results",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonChecksFailing}},
			expectedNextSteps: checkNextSteps, expectedActions: 1,
		},
		{
			name:              "named required CI check",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: "tests"}},
			expectedNextSteps: checkNextSteps, expectedActions: 1,
		},
		{
			name:              "several evidence blockers offer one action",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonChecksFailing}, {Code: CodeReviewRiskReasonBlockingFindings, Actual: 1}, {Code: CodeReviewRiskReasonDescriptionFailed}},
			expectedNextSteps: evidenceNextSteps, expectedActions: 1,
		},
		{
			name:              "other policy blockers still apply",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 20, Limit: 10}, {Code: CodeReviewRiskReasonDescriptionFailed}},
			expectedNextSteps: evidenceNextSteps, expectedActions: 1,
		},
		{
			name:              "policy alone cannot be resolved with evidence",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 20, Limit: 10}},
			expectedNextSteps: genericNextSteps,
		},
		{
			name:              "unavailable action",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonDescriptionFailed}},
			configure:         func(input *CodeReviewFinalReviewInput) { input.EvidenceRecheckURL = "" },
			expectedNextSteps: genericNextSteps,
		},
		{
			name:              "operational failure needs a full retry",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonDescriptionFailed}, {Code: CodeReviewRiskReasonOrchestratorSynthesisInvalid}},
			configure:         func(input *CodeReviewFinalReviewInput) { input.OperationalSummary = "The final synthesis timed out." },
			expectedNextSteps: "**Next steps:** Retry the automated review to regenerate the final synthesis, or ask a human reviewer to review the available evidence directly.",
		},
		{
			name: "approval does not advertise a recheck",
			configure: func(input *CodeReviewFinalReviewInput) {
				input.Decision = CodeReviewDecisionApproved
				input.Acceptable = true
			},
		},
		{
			name: "acceptable comment-only result has no evidence blockers",
			configure: func(input *CodeReviewFinalReviewInput) {
				input.Decision = CodeReviewDecisionCommentOnly
				input.Acceptable = true
			},
		},
		{
			name:              "no structured evidence reason",
			expectedNextSteps: genericNextSteps,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := CodeReviewFinalReviewInput{
				Decision: CodeReviewDecisionNeedsHumanReview, RiskReasons: tt.reasons, EvidenceRecheckURL: actionURL,
			}
			if tt.configure != nil {
				tt.configure(&input)
			}
			body := BuildCodeReviewFinalReviewBody(input)
			var nextSteps string
			for paragraph := range strings.SplitSeq(body, "\n\n") {
				if strings.HasPrefix(paragraph, "**Next steps:**") {
					nextSteps = paragraph
				}
			}
			require.Equal(t, tt.expectedNextSteps, nextSteps, "next steps should explain the applicable evidence action without promising approval")
			require.Equal(t, tt.expectedActions, strings.Count(body, "[Re-check PR evidence]"), "the comment should offer at most one applicable recheck action")
			for _, reason := range tt.reasons {
				if reason.Code == CodeReviewRiskReasonFilesLimitExceeded {
					require.Contains(t, body, "This change touches 20 files; the policy limit is 10.", "offering an evidence action must preserve unrelated policy blockers")
				}
			}
		})
	}
}
