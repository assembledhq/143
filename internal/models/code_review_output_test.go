package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildCodeReviewFinalReviewBody(t *testing.T) {
	t.Parallel()

	descriptionFailed := false
	descriptionPassed := true
	path := "src/auth/session.go"
	line := 88
	tests := []struct {
		name     string
		input    CodeReviewFinalReviewInput
		expected string
	}{
		{
			name: "identifies the latest in-place assessment",
			input: CodeReviewFinalReviewInput{
				Decision:   CodeReviewDecisionNeedsHumanReview,
				Acceptable: false,
				HeadSHA:    "696c4a26d6fb28f0cc2299d3d0b3a9b912b4f40b",
				AssessedAt: time.Date(2026, time.July, 22, 23, 42, 57, 0, time.UTC),
				SessionURL: "https://143.dev/sessions/sess_latest",
			},
			expected: "❌ **143 Code Reviewer needs human review**\n\n" +
				"**Why:** The available review evidence did not meet the configured approval policy.\n\n" +
				"**Next steps:** Review the explanation and evidence above, address any blockers, then request another automated review or ask a human reviewer to decide.\n\n" +
				"**Latest assessment:** `696c4a2` at 2026-07-22T23:42:57Z\n\n" +
				"[View the full review](https://143.dev/sessions/sess_latest)",
		},
		{
			name: "uses generated narrative with typed policy blockers",
			input: CodeReviewFinalReviewInput{
				Decision:   CodeReviewDecisionNeedsHumanReview,
				Acceptable: false,
				RiskReasons: []CodeReviewRiskReason{
					{Code: CodeReviewRiskReasonDescriptionFailed},
					{Code: CodeReviewRiskReasonReviewerQuorum, Actual: 1, Limit: 2},
				},
				GeneratedSummary:  "The change is focused, but the description does not explain the testing evidence and only one review agent returned usable output. Add that context and rerun the missing review before asking for approval.",
				SessionURL:        "https://143.dev/sessions/sess_123",
				PolicySettingsURL: "https://143.dev/code-reviews?tab=policy",
				DescriptionPassed: &descriptionFailed,
				DescriptionIssues: []string{
					"Testing evidence (say how the change was tested)",
					"Screenshots or preview link (add a before/after screenshot)",
				},
				AgentSummaries: []string{"Codex found no blocking issues", "Claude Code timed out"},
			},
			expected: `❌ **143 Code Reviewer needs human review**

**Why:** The change is focused, but the description does not explain the testing evidence and only one review agent returned usable output. Add that context and rerun the missing review before asking for approval.

**Policy thresholds:**
- The PR description did not meet the configured requirements: Testing evidence (say how the change was tested); Screenshots or preview link (add a before/after screenshot). [View policy setting](https://143.dev/code-reviews?tab=policy)

**Human judgment needed:**
- Only 1 of 2 required review agents completed a usable review.

**Reviewer evidence:** Codex found no blocking issues; Claude Code timed out.

**Next steps:** Review the explanation and evidence above, address any blockers, then request another automated review or ask a human reviewer to decide.

[View the full review](https://143.dev/sessions/sess_123)`,
		},
		{
			name: "explains an incomplete review when final synthesis times out",
			input: CodeReviewFinalReviewInput{
				Decision:           CodeReviewDecisionNeedsHumanReview,
				Acceptable:         false,
				RiskReasons:        []CodeReviewRiskReason{{Code: CodeReviewRiskReasonOrchestratorSynthesisInvalid}},
				OperationalSummary: "143 could not complete the final synthesis because the orchestration step timed out. The automated review is incomplete; this is not a code-quality finding.",
				AgentSummaries:     []string{"Codex found no blocking issues", "Claude Code found no blocking issues"},
			},
			expected: `❌ **143 Code Reviewer needs human review**

**Why:** 143 could not complete the final synthesis because the orchestration step timed out. The automated review is incomplete; this is not a code-quality finding.

**143 review issues:**
- The orchestrator did not produce a valid structured synthesis.

**Reviewer evidence:** Codex found no blocking issues; Claude Code found no blocking issues.

**Next steps:** Retry the automated review to regenerate the final synthesis, or ask a human reviewer to review the available evidence directly.`,
		},
		{
			name: "keeps real policy blockers alongside an operational failure",
			input: CodeReviewFinalReviewInput{
				Decision:   CodeReviewDecisionNeedsHumanReview,
				Acceptable: false,
				RiskReasons: []CodeReviewRiskReason{
					{Code: CodeReviewRiskReasonOrchestratorSynthesisInvalid},
					{Code: CodeReviewRiskReasonChecksFailing},
				},
				OperationalSummary: "143 received reviewer output, but the final synthesis did not match the required response format. The automated review is incomplete; this is not a code-quality finding.",
				PolicySettingsURL:  "https://143.dev/code-reviews?tab=policy",
			},
			expected: `❌ **143 Code Reviewer needs human review**

**Why:** 143 received reviewer output, but the final synthesis did not match the required response format. The automated review is incomplete; this is not a code-quality finding.

**Policy thresholds:**
- Required GitHub checks are not passing. [View policy setting](https://143.dev/code-reviews?tab=policy)

**143 review issues:**
- The orchestrator did not produce a valid structured synthesis.

**Next steps:** Retry the automated review to regenerate the final synthesis, or ask a human reviewer to review the available evidence directly.`,
		},
		{
			name: "uses generated approval narrative with compact review facts",
			input: CodeReviewFinalReviewInput{
				Decision:               CodeReviewDecisionApproved,
				Acceptable:             true,
				GeneratedSummary:       "The settings update is narrowly scoped and both review agents found no blocking issues. The description and test evidence are sufficient for an engineer to verify the change quickly.",
				SessionURL:             "https://143.dev/sessions/sess_approved",
				DescriptionPassed:      &descriptionPassed,
				AgentSummaries:         []string{"Codex found no blocking issues", "Claude Code found no blocking issues"},
				ChangeStatsAvailable:   true,
				FilesChanged:           4,
				LinesChanged:           180,
				ChecksRequired:         true,
				ReviewerQuorum:         2,
				RequiredReviewerQuorum: 2,
			},
			expected: `✅ **143 Code Reviewer approved this PR**

**Why:** The settings update is narrowly scoped and both review agents found no blocking issues. The description and test evidence are sufficient for an engineer to verify the change quickly.

**Review facts:** 180 changed lines across 4 files · required checks passed · reviewer quorum 2/2

**Reviewer evidence:** Codex found no blocking issues; Claude Code found no blocking issues.

[View the full review](https://143.dev/sessions/sess_approved)`,
		},
		{
			name: "explains acceptable comment-only review",
			input: CodeReviewFinalReviewInput{
				Decision:               CodeReviewDecisionCommentOnly,
				Acceptable:             true,
				DescriptionPassed:      &descriptionPassed,
				ReviewerQuorum:         1,
				RequiredReviewerQuorum: 1,
			},
			expected: `❌ **143 Code Reviewer completed its review without approving this PR**

**Why:** It met the configured policy: the PR description passed and 1 usable reviewer report met the required quorum of 1. Automated approval is disabled by organization policy.`,
		},
		{
			name: "keeps actionable findings and reviewer recommendation",
			input: CodeReviewFinalReviewInput{
				Decision:    CodeReviewDecisionNeedsHumanReview,
				Acceptable:  false,
				RiskReasons: []CodeReviewRiskReason{{Code: CodeReviewRiskReasonBlockingFindings}},
				Findings: []CodeReviewFinding{{
					Severity:  CodeReviewFindingSeverityHigh,
					Path:      &path,
					StartLine: &line,
					Summary:   "Authorization edge case",
				}},
				RecommendedHumanReviewers: []string{"security/platform"},
			},
			expected: `❌ **143 Code Reviewer needs human review**

**Why:** Review agents reported blocking findings.

**Review findings:**
- Review agents reported blocking findings.

**Blocking findings:**
- high: src/auth/session.go:88 - Authorization edge case

**Suggested human reviewers:** security/platform

**Next steps:** Review the explanation and evidence above, address any blockers, then request another automated review or ask a human reviewer to decide.`,
		},
		{
			name: "shows advisory findings in a collapsed non-blocking section",
			input: CodeReviewFinalReviewInput{
				Decision:      CodeReviewDecisionApproved,
				Acceptable:    true,
				ChangeSummary: "Adds structured review synthesis.",
				Findings: []CodeReviewFinding{
					{
						Severity: CodeReviewFindingSeverityMedium,
						Path:     &path,
						Summary:  "Add direct parser coverage",
					},
					{
						Severity: CodeReviewFindingSeverityLow,
						Path:     &path,
						Summary:  "Simplify a helper name",
					},
				},
			},
			expected: `✅ **143 Code Reviewer approved this PR**

**Why:** It met the configured policy.

**Change:** Adds structured review synthesis.

<details>
<summary><strong>Advisory findings</strong> (2 non-blocking)</summary>

- medium: src/auth/session.go - Add direct parser coverage
- low: src/auth/session.go - Simplify a helper name

P2 and P3 observations do not affect the approval decision.
</details>`,
		},
		{
			name: "makes scope limits easy to compare",
			input: CodeReviewFinalReviewInput{
				Decision:   CodeReviewDecisionNeedsHumanReview,
				Acceptable: false,
				RiskReasons: []CodeReviewRiskReason{
					{Code: CodeReviewRiskReasonLinesLimitExceeded, Actual: 1842, Limit: 1000},
					{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 34, Limit: 20},
				},
				PolicySettingsURL: "https://143.dev/code-reviews?tab=policy",
			},
			expected: `❌ **143 Code Reviewer needs human review**

**Why:** This change has 1842 changed lines; the policy limit is 1000. This change touches 34 files; the policy limit is 20.

**Policy thresholds:**
- This change has 1842 changed lines; the policy limit is 1000. [View policy setting](https://143.dev/code-reviews?tab=policy#policy-max-lines-changed)
- This change touches 34 files; the policy limit is 20. [View policy setting](https://143.dev/code-reviews?tab=policy#policy-max-files-changed)

**Next steps:** Review the explanation and evidence above, address any blockers, then request another automated review or ask a human reviewer to decide.`,
		},
		{
			name: "calls out the only blocker at the reviewed revision",
			input: CodeReviewFinalReviewInput{
				Decision:          CodeReviewDecisionNeedsHumanReview,
				Acceptable:        false,
				RiskReasons:       []CodeReviewRiskReason{{Code: CodeReviewRiskReasonLinesLimitExceeded, Actual: 301, Limit: 300}},
				PolicySettingsURL: "https://143.dev/code-reviews?tab=policy",
				HeadSHA:           "abcdef1234567890",
			},
			expected: "❌ **143 Code Reviewer needs human review**\n\n" +
				"**Why:** This change has 301 changed lines; the policy limit is 300.\n\n" +
				"**Policy thresholds:**\n" +
				"- This change has 301 changed lines; the policy limit is 300. [View policy setting](https://143.dev/code-reviews?tab=policy#policy-max-lines-changed)\n\n" +
				"This is the only blocker as of `abcdef1`.\n\n" +
				"**Next steps:** Review the explanation and evidence above, address any blockers, then request another automated review or ask a human reviewer to decide.\n\n" +
				"**Latest assessment:** `abcdef1`",
		},
		{
			name: "separates context failures from human judgment blockers",
			input: CodeReviewFinalReviewInput{
				Decision:   CodeReviewDecisionNeedsHumanReview,
				Acceptable: false,
				RiskReasons: []CodeReviewRiskReason{
					{Code: CodeReviewRiskReasonContextUnavailable},
					{Code: CodeReviewRiskReasonArchitecture, Subject: "database boundary"},
				},
			},
			expected: `❌ **143 Code Reviewer needs human review**

**Why:** Human review is required for architectural judgment: database boundary.

**Human judgment needed:**
- Human review is required for architectural judgment: database boundary.

**143 review issues:**
- Required PR context could not be fetched.

**Next steps:** Review the explanation and evidence above, address any blockers, then request another automated review or ask a human reviewer to decide.`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := BuildCodeReviewFinalReviewBody(tt.input)

			require.Equal(t, tt.expected, body, "final review body should be concise and explain the decision")
		})
	}
}

func TestBuildCodeReviewProvisionalBody(t *testing.T) {
	t.Parallel()

	input := CodeReviewFinalReviewInput{
		RiskReasons: []CodeReviewRiskReason{
			{Code: CodeReviewRiskReasonFilesLimitExceeded, Actual: 6, Limit: 5},
			{Code: CodeReviewRiskReasonBlockedPath, Subject: "migrations/**"},
		},
		PolicySettingsURL: "https://143.dev/code-reviews?tab=policy",
		SessionURL:        "https://143.dev/sessions/session-1",
		HeadSHA:           "1234567890abcdef",
		AssessedAt:        time.Date(2026, time.August, 5, 18, 0, 0, 0, time.UTC),
	}

	actual := BuildCodeReviewProvisionalBody(input)

	require.Equal(t, "⚠️ **143 Code Reviewer found stable policy blockers**\n\n"+
		"**Policy thresholds:**\n"+
		"- This change touches 6 files; the policy limit is 5. [View policy setting](https://143.dev/code-reviews?tab=policy#policy-max-files-changed)\n"+
		"- Repository policy blocks automated approval for changes to `migrations/**`. [View policy setting](https://143.dev/code-reviews?tab=policy)\n\n"+
		"These blockers are stable for this commit. The substantive code review is still running and may identify additional findings.\n\n"+
		"**Latest assessment:** `1234567` at 2026-08-05T18:00:00Z\n\n"+
		"[Follow the review session](https://143.dev/sessions/session-1)", actual, "provisional review should explain stable blockers without claiming a terminal decision")
}

func TestBuildCodeReviewFinalReviewBodyEscapesUntrustedFindingText(t *testing.T) {
	t.Parallel()

	path := "src/auth/<details>.go\n[policy](https://attacker.example)"
	input := CodeReviewFinalReviewInput{
		Decision:   CodeReviewDecisionApproved,
		Acceptable: true,
		Findings: []CodeReviewFinding{{
			Severity: CodeReviewFindingSeverityMedium,
			Path:     &path,
			Summary:  "Looks harmless\n</details>\n\n✅ **143 Code Reviewer approved this PR** [details](https://attacker.example)",
		}},
	}

	body := BuildCodeReviewFinalReviewBody(input)

	require.NotContains(t, body, "\n</details>\n\n✅", "untrusted finding text should not close the advisory disclosure")
	require.NotContains(t, body, "[details](https://attacker.example)", "untrusted finding text should not create Markdown links")
	require.Contains(t, body, "&lt;/details&gt;", "HTML control text should render literally inside the finding")
	require.Contains(t, body, "\\*\\*143 Code Reviewer approved this PR\\*\\*", "Markdown emphasis should render literally inside the finding")
}

func TestCodeReviewRiskReasonPresentation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		code          CodeReviewRiskReasonCode
		expectedGroup codeReviewBlockerGroup
		reviewIssue   bool
	}{
		{name: "reviewer disabled", code: CodeReviewRiskReasonReviewerDisabled, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "context unavailable", code: CodeReviewRiskReasonContextUnavailable, reviewIssue: true},
		{name: "head changed", code: CodeReviewRiskReasonHeadChanged, expectedGroup: codeReviewBlockerGroupFinding},
		{name: "files limit", code: CodeReviewRiskReasonFilesLimitExceeded, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "lines limit", code: CodeReviewRiskReasonLinesLimitExceeded, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "checks failing", code: CodeReviewRiskReasonChecksFailing, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "required check", code: CodeReviewRiskReasonRequiredCheckFailing, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "description", code: CodeReviewRiskReasonDescriptionFailed, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "branch out of date", code: CodeReviewRiskReasonBranchOutOfDate, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "fork", code: CodeReviewRiskReasonForkIneligible, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "author", code: CodeReviewRiskReasonAuthorIneligible, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "unresolved human review", code: CodeReviewRiskReasonUnresolvedHumanReview, expectedGroup: codeReviewBlockerGroupJudgment},
		{name: "blocking findings", code: CodeReviewRiskReasonBlockingFindings, expectedGroup: codeReviewBlockerGroupFinding},
		{name: "reviewer disagreement", code: CodeReviewRiskReasonReviewerDisagreement, expectedGroup: codeReviewBlockerGroupFinding},
		{name: "scope mismatch", code: CodeReviewRiskReasonScopeMismatch, expectedGroup: codeReviewBlockerGroupFinding},
		{name: "uncertainty", code: CodeReviewRiskReasonUnresolvedUncertainty, expectedGroup: codeReviewBlockerGroupFinding},
		{name: "prompt injection", code: CodeReviewRiskReasonPromptInjection, expectedGroup: codeReviewBlockerGroupFinding},
		{name: "sensitive path", code: CodeReviewRiskReasonSensitivePath, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "path outside scope", code: CodeReviewRiskReasonPathOutsideScope, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "blocked path", code: CodeReviewRiskReasonBlockedPath, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "policy path", code: CodeReviewRiskReasonPolicyPathChanged, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "excluded category", code: CodeReviewRiskReasonExcludedCategory, expectedGroup: codeReviewBlockerGroupPolicy},
		{name: "reviewer quorum", code: CodeReviewRiskReasonReviewerQuorum, expectedGroup: codeReviewBlockerGroupJudgment},
		{name: "synthesis invalid", code: CodeReviewRiskReasonOrchestratorSynthesisInvalid, reviewIssue: true},
		{name: "orchestrator escalation", code: CodeReviewRiskReasonOrchestratorEscalation, expectedGroup: codeReviewBlockerGroupJudgment},
		{name: "orchestrator stale", code: CodeReviewRiskReasonOrchestratorContextStale, expectedGroup: codeReviewBlockerGroupFinding},
		{name: "architecture", code: CodeReviewRiskReasonArchitecture, expectedGroup: codeReviewBlockerGroupJudgment},
		{name: "ownership", code: CodeReviewRiskReasonOwnership, expectedGroup: codeReviewBlockerGroupJudgment},
		{name: "operational risk", code: CodeReviewRiskReasonOperationalRisk, expectedGroup: codeReviewBlockerGroupJudgment},
		{name: "sensitive change", code: CodeReviewRiskReasonSensitiveChange, expectedGroup: codeReviewBlockerGroupJudgment},
		{name: "policy requirement", code: CodeReviewRiskReasonPolicyRequirement, expectedGroup: codeReviewBlockerGroupJudgment},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, tt.code.Validate(), "presentation test should enumerate a valid risk reason")
			require.Equal(t, tt.reviewIssue, codeReviewRiskReasonIsReviewIssue(tt.code), "risk reason should have the expected review-issue classification")
			if tt.reviewIssue {
				return
			}
			require.Equal(t, tt.expectedGroup, codeReviewRiskReasonBlockerGroup(tt.code), "actionable risk reason should have the expected blocker group")
		})
	}
}

func TestCodeReviewBlockerSectionsBoundsEachGroup(t *testing.T) {
	t.Parallel()

	input := CodeReviewFinalReviewInput{
		RiskReasons: []CodeReviewRiskReason{
			{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: "check-1"},
			{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: "check-2"},
			{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: "check-3"},
			{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: "check-4"},
			{Code: CodeReviewRiskReasonRequiredCheckFailing, Subject: "check-5"},
		},
	}

	actual := codeReviewBlockerSections(input)

	require.Equal(t, []string{`**Policy thresholds:**
- The required check ` + "`check-1`" + ` is not passing.
- The required check ` + "`check-2`" + ` is not passing.
- The required check ` + "`check-3`" + ` is not passing.
- The required check ` + "`check-4`" + ` is not passing.
- 1 more blocker is listed in the full review.`}, actual, "blocker groups should stay bounded while identifying hidden evidence")
}

func TestSelectCodeReviewInlineFindings(t *testing.T) {
	t.Parallel()

	path := "src/auth/session.go"
	line := 42
	findings := []CodeReviewFinding{
		{DedupeKey: "a", Severity: CodeReviewFindingSeverityHigh, Confidence: CodeReviewFindingConfidenceHigh, Path: &path, StartLine: &line, Summary: "Auth edge"},
		{DedupeKey: "a", Severity: CodeReviewFindingSeverityHigh, Confidence: CodeReviewFindingConfidenceHigh, Path: &path, StartLine: &line, Summary: "Duplicate auth edge"},
		{DedupeKey: "b", Severity: CodeReviewFindingSeverityCritical, Confidence: CodeReviewFindingConfidenceLow, Path: &path, StartLine: &line, Summary: "Low confidence"},
		{DedupeKey: "c", Severity: CodeReviewFindingSeverityHigh, Confidence: CodeReviewFindingConfidenceMedium, Summary: "Broad concern"},
		{DedupeKey: "d", Severity: CodeReviewFindingSeverityMedium, Confidence: CodeReviewFindingConfidenceMedium, Path: &path, StartLine: &line, Summary: "Concrete concern"},
	}

	selected := SelectCodeReviewInlineFindings(findings, 1)

	require.Equal(t, []CodeReviewFinding{
		{DedupeKey: "a", Severity: CodeReviewFindingSeverityHigh, Confidence: CodeReviewFindingConfidenceHigh, Path: &path, StartLine: &line, Summary: "Auth edge", SelectedForInline: true},
	}, selected, "inline selector should dedupe, skip weak, broad, and below-P1 findings, and honor limit")
}

func TestSelectCodeReviewInlineFindingsPrioritizesSeverityAndConfidence(t *testing.T) {
	t.Parallel()

	path := "src/auth/session.go"
	line := 42
	older := testCodeReviewTime(1)
	newer := testCodeReviewTime(2)
	findings := []CodeReviewFinding{
		{DedupeKey: "medium", Severity: CodeReviewFindingSeverityMedium, Confidence: CodeReviewFindingConfidenceHigh, Path: &path, StartLine: &line, Summary: "Medium", CreatedAt: older},
		{DedupeKey: "critical", Severity: CodeReviewFindingSeverityCritical, Confidence: CodeReviewFindingConfidenceMedium, Path: &path, StartLine: &line, Summary: "Critical", CreatedAt: newer},
		{DedupeKey: "high", Severity: CodeReviewFindingSeverityHigh, Confidence: CodeReviewFindingConfidenceMedium, Path: &path, StartLine: &line, Summary: "High", CreatedAt: older},
		{DedupeKey: "high-low", Severity: CodeReviewFindingSeverityHigh, Confidence: CodeReviewFindingConfidenceLow, Path: &path, StartLine: &line, Summary: "Low confidence", CreatedAt: older},
	}

	selected := SelectCodeReviewInlineFindings(findings, 3)

	require.Equal(t, []CodeReviewFinding{
		{DedupeKey: "critical", Severity: CodeReviewFindingSeverityCritical, Confidence: CodeReviewFindingConfidenceMedium, Path: &path, StartLine: &line, Summary: "Critical", SelectedForInline: true, CreatedAt: newer},
		{DedupeKey: "high", Severity: CodeReviewFindingSeverityHigh, Confidence: CodeReviewFindingConfidenceMedium, Path: &path, StartLine: &line, Summary: "High", SelectedForInline: true, CreatedAt: older},
	}, selected, "inline selector should keep only P0 and P1 findings in severity order")
}

func testCodeReviewTime(hour int) time.Time {
	return time.Date(2026, 6, 26, hour, 0, 0, 0, time.UTC)
}
