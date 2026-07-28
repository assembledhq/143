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
				DescriptionPassed: &descriptionFailed,
				DescriptionIssues: []string{
					"Testing evidence (say how the change was tested)",
					"Screenshots or preview link (add a before/after screenshot)",
				},
				AgentSummaries: []string{"Codex found no blocking issues", "Claude Code timed out"},
			},
			expected: `❌ **143 Code Reviewer needs human review**

**Why:** The change is focused, but the description does not explain the testing evidence and only one review agent returned usable output. Add that context and rerun the missing review before asking for approval.

**Policy blockers:**
- The PR description did not meet the configured requirements: Testing evidence (say how the change was tested); Screenshots or preview link (add a before/after screenshot).
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
			},
			expected: `❌ **143 Code Reviewer needs human review**

**Why:** 143 received reviewer output, but the final synthesis did not match the required response format. The automated review is incomplete; this is not a code-quality finding.

**Policy blockers:**
- Required GitHub checks are not passing.

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

**Blocking findings:**
- high: src/auth/session.go:88 - Authorization edge case

**Suggested human reviewers:** security/platform

**Next steps:** Review the explanation and evidence above, address any blockers, then request another automated review or ask a human reviewer to decide.`,
		},
		{
			name: "summarizes advisory findings without publishing their details",
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

**Advisory notes:** 2 non-blocking observations are available in the full review. P2 and P3 observations do not affect the approval decision.`,
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
			},
			expected: `❌ **143 Code Reviewer needs human review**

**Why:** This change has 1842 changed lines; the policy limit is 1000. This change touches 34 files; the policy limit is 20.

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
