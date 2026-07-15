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
			name: "explains non-approval in plain language",
			input: CodeReviewFinalReviewInput{
				Decision:          CodeReviewDecisionNeedsHumanReview,
				Acceptable:        false,
				RiskReasons:       []string{"PR description policy did not pass", "reviewer quorum 1 is below policy requirement 2"},
				SessionURL:        "https://143.dev/sessions/sess_123",
				DescriptionPassed: &descriptionFailed,
				DescriptionIssues: []string{
					"Testing evidence (say how the change was tested)",
					"Screenshots or preview link (add a before/after screenshot)",
				},
				AgentSummaries: []string{"Codex found no blocking issues", "Claude Code timed out"},
			},
			expected: `143 Code Reviewer did not approve this PR

Why: The PR description did not meet the configured requirements: Testing evidence (say how the change was tested); Screenshots or preview link (add a before/after screenshot). Only 1 of 2 required review agents completed a usable review. Reviewer evidence: Codex found no blocking issues; Claude Code timed out.

Address the items above and request another review, or ask a human reviewer to decide.

[View the full review](https://143.dev/sessions/sess_123)`,
		},
		{
			name: "explains approval with concrete policy evidence",
			input: CodeReviewFinalReviewInput{
				Decision:               CodeReviewDecisionApproved,
				Acceptable:             true,
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
			expected: `143 Code Reviewer approved this PR

Why: It met the configured policy: 180 changed lines across 4 files, the PR description passed, required checks passed, and 2 usable reviewer reports met the required quorum of 2. Reviewer evidence: Codex found no blocking issues; Claude Code found no blocking issues.

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
			expected: `143 Code Reviewer completed its review without approving this PR

Why: It met the configured policy: the PR description passed and 1 usable reviewer report met the required quorum of 1. Automated approval is disabled by repository policy.`,
		},
		{
			name: "keeps actionable findings and reviewer recommendation",
			input: CodeReviewFinalReviewInput{
				Decision:    CodeReviewDecisionNeedsHumanReview,
				Acceptable:  false,
				RiskReasons: []string{"review agents reported blocking findings"},
				Findings: []CodeReviewFinding{{
					Severity:  CodeReviewFindingSeverityHigh,
					Path:      &path,
					StartLine: &line,
					Summary:   "Authorization edge case",
				}},
				RecommendedHumanReviewers: []string{"security/platform"},
			},
			expected: `143 Code Reviewer did not approve this PR

Why: Review agents reported blocking findings.

Review findings:
- high: src/auth/session.go:88 - Authorization edge case

Suggested human reviewers: security/platform

Address the items above and request another review, or ask a human reviewer to decide.`,
		},
		{
			name: "makes scope limits easy to compare",
			input: CodeReviewFinalReviewInput{
				Decision:    CodeReviewDecisionNeedsHumanReview,
				Acceptable:  false,
				RiskReasons: []string{"changed lines 1842 exceeds policy limit 1000", "changed files 34 exceeds policy limit 20"},
			},
			expected: `143 Code Reviewer did not approve this PR

Why: This change has 1842 changed lines; the policy limit is 1000. This change touches 34 files; the policy limit is 20.

Address the items above and request another review, or ask a human reviewer to decide.`,
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
		{DedupeKey: "a", Confidence: CodeReviewFindingConfidenceHigh, Path: &path, StartLine: &line, Summary: "Auth edge"},
		{DedupeKey: "a", Confidence: CodeReviewFindingConfidenceHigh, Path: &path, StartLine: &line, Summary: "Duplicate auth edge"},
		{DedupeKey: "b", Confidence: CodeReviewFindingConfidenceLow, Path: &path, StartLine: &line, Summary: "Low confidence"},
		{DedupeKey: "c", Confidence: CodeReviewFindingConfidenceMedium, Summary: "Broad concern"},
		{DedupeKey: "d", Confidence: CodeReviewFindingConfidenceMedium, Path: &path, StartLine: &line, Summary: "Concrete concern"},
	}

	selected := SelectCodeReviewInlineFindings(findings, 1)

	require.Equal(t, []CodeReviewFinding{
		{DedupeKey: "a", Confidence: CodeReviewFindingConfidenceHigh, Path: &path, StartLine: &line, Summary: "Auth edge", SelectedForInline: true},
	}, selected, "inline selector should dedupe, skip weak/broad findings, and honor limit")
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
		{DedupeKey: "high-low", Severity: CodeReviewFindingSeverityHigh, Confidence: CodeReviewFindingConfidenceLow, Path: &path, StartLine: &line, Summary: "Low confidence", CreatedAt: older},
	}

	selected := SelectCodeReviewInlineFindings(findings, 1)

	require.Equal(t, []CodeReviewFinding{
		{DedupeKey: "critical", Severity: CodeReviewFindingSeverityCritical, Confidence: CodeReviewFindingConfidenceMedium, Path: &path, StartLine: &line, Summary: "Critical", SelectedForInline: true, CreatedAt: newer},
	}, selected, "inline selector should prefer the most severe concrete finding")
}

func testCodeReviewTime(hour int) time.Time {
	return time.Date(2026, 6, 26, hour, 0, 0, 0, time.UTC)
}
