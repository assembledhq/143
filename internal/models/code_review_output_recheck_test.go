package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildCodeReviewFinalReviewBodyEvidenceRecheck(t *testing.T) {
	t.Parallel()

	const actionURL = "https://143.test/code-reviews?recheck=90d8a47d-d87e-4780-90af-040f5144685a"
	const action = "[Re-check evidence](" + actionURL + ")"
	const reviewNowURL = "https://143.test/code-reviews?review_now=90d8a47d-d87e-4780-90af-040f5144685a"
	const sessionURL = "https://143.test/sessions/90d8a47d-d87e-4780-90af-040f5144685a"
	const detailLink = "[View full review](" + sessionURL + ")"
	const requestLink = "[Request review](" + reviewNowURL + ")"
	const suffix = " Other approval requirements still apply."
	const genericNextSteps = "**Next steps:** Address any blockers, then request another review or ask a human reviewer to decide."
	const evidenceNextSteps = "**Next steps:** Add the missing evidence under **Testing** or **Evidence** in the PR description or a comment, then request an evidence recheck." + suffix
	const findingNextSteps = "**Next steps:** If you have evidence that addresses these findings, add it under **Testing** or **Evidence** in the PR description or a comment, then request an evidence recheck." + suffix
	const checkNextSteps = "**Next steps:** Once updated CI results are available, request an evidence recheck." + suffix
	tests := []struct {
		name              string
		reasons           []CodeReviewRiskReason
		configure         func(*CodeReviewFinalReviewInput)
		expectedNextSteps string
		expectedActions   int
		expectedLinks     string
	}{
		{
			name:              "missing evidence",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonDescriptionFailed}},
			expectedNextSteps: evidenceNextSteps, expectedActions: 1,
			expectedLinks: action + " · " + detailLink,
		},
		{
			name:              "reviewer findings can be addressed with evidence conditionally",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonBlockingFindings, Actual: 1}},
			expectedNextSteps: findingNextSteps, expectedActions: 1,
			expectedLinks: action + " · " + detailLink,
		},
		{
			name:              "CI results",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonChecksFailing}},
			expectedNextSteps: checkNextSteps, expectedActions: 1,
			expectedLinks: action + " · " + detailLink,
		},
		{
			name:              "named required CI check",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: "tests"}},
			expectedNextSteps: checkNextSteps, expectedActions: 1,
			expectedLinks: action + " · " + detailLink,
		},
		{
			name:              "several evidence blockers offer one action",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonChecksFailing}, {Code: CodeReviewRiskReasonBlockingFindings, Actual: 1}, {Code: CodeReviewRiskReasonDescriptionFailed}},
			expectedNextSteps: evidenceNextSteps, expectedActions: 1,
			expectedLinks: action + " · " + detailLink,
		},
		{
			name:              "other policy blockers still apply",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 20, Limit: 10}, {Code: CodeReviewRiskReasonDescriptionFailed}},
			expectedNextSteps: evidenceNextSteps, expectedActions: 1,
			expectedLinks: action + " · " + detailLink,
		},
		{
			name:              "policy alone cannot be resolved with evidence",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 20, Limit: 10}},
			expectedNextSteps: genericNextSteps,
			expectedLinks:     requestLink + " · " + detailLink,
		},
		{
			name:              "unavailable action",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonDescriptionFailed}},
			configure:         func(input *CodeReviewFinalReviewInput) { input.EvidenceRecheckURL = "" },
			expectedNextSteps: genericNextSteps,
			expectedLinks:     requestLink + " · " + detailLink,
		},
		{
			name:              "operational failure needs a full retry",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonDescriptionFailed}, {Code: CodeReviewRiskReasonOrchestratorSynthesisInvalid}},
			configure:         func(input *CodeReviewFinalReviewInput) { input.OperationalSummary = "The final synthesis timed out." },
			expectedNextSteps: "**Next steps:** Retry the automated review to regenerate the final synthesis, or ask a human reviewer to review the available evidence.",
			expectedLinks:     requestLink + " · " + detailLink,
		},
		{
			name:    "approval does not advertise a recheck",
			reasons: []CodeReviewRiskReason{{Code: CodeReviewRiskReasonDescriptionFailed}},
			configure: func(input *CodeReviewFinalReviewInput) {
				input.Decision = CodeReviewDecisionApproved
				input.Acceptable = true
			},
			expectedLinks: detailLink,
		},
		{
			name:    "acceptable comment-only result has no evidence blockers",
			reasons: []CodeReviewRiskReason{{Code: CodeReviewRiskReasonDescriptionFailed}},
			configure: func(input *CodeReviewFinalReviewInput) {
				input.Decision = CodeReviewDecisionCommentOnly
				input.Acceptable = true
			},
			expectedLinks: detailLink,
		},
		{
			name:              "no structured evidence reason",
			expectedNextSteps: genericNextSteps,
			expectedLinks:     requestLink + " · " + detailLink,
		},
		{
			name:              "legacy blocked result retains full review action",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonOperationalRisk, Subject: "deployment requires operator approval"}},
			configure:         func(input *CodeReviewFinalReviewInput) { input.Decision = CodeReviewDecisionBlocked },
			expectedNextSteps: genericNextSteps,
			expectedLinks:     requestLink + " · " + detailLink,
		},
		{
			name:              "unavailable request action leaves only detail link",
			reasons:           []CodeReviewRiskReason{{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 20, Limit: 10}},
			configure:         func(input *CodeReviewFinalReviewInput) { input.ReviewNowURL = "" },
			expectedNextSteps: genericNextSteps,
			expectedLinks:     detailLink,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := CodeReviewFinalReviewInput{
				Decision: CodeReviewDecisionNeedsHumanReview, RiskReasons: tt.reasons, EvidenceRecheckURL: actionURL,
				SessionURL: sessionURL, ReviewNowURL: reviewNowURL,
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
			require.NotContains(t, nextSteps, "](", "next steps should leave navigation in the grouped footer")
			require.Equal(t, tt.expectedActions, strings.Count(body, action), "the comment should offer at most one applicable recheck action")
			_, footer, found := strings.Cut(body, "<!-- 143-code-review-footer:start -->\n")
			require.True(t, found, "the body should end in a grouped navigation footer")
			require.Equal(t, tt.expectedLinks+"\n<!-- 143-code-review-footer:end -->", footer, "the footer should expose only the applicable action and detail link")
			for _, reason := range tt.reasons {
				if reason.Code == CodeReviewRiskReasonFilesLimitExceeded {
					require.Contains(t, body, "This change touches 20 files; the policy limit is 10.", "offering an evidence action must preserve unrelated policy blockers")
				}
			}
		})
	}
}
