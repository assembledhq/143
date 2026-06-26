package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildCodeReviewFinalReviewBody(t *testing.T) {
	t.Parallel()

	descriptionPassed := false
	path := "src/auth/session.go"
	line := 88
	body := BuildCodeReviewFinalReviewBody(CodeReviewFinalReviewInput{
		Decision:          CodeReviewDecisionCommentOnly,
		Acceptable:        false,
		RiskReasons:       []string{"required GitHub checks are not passing", "review agents unavailable"},
		SessionURL:        "https://143.dev/sessions/sess_123",
		PolicyVersion:     3,
		HeadSHA:           "abc123",
		Summary:           "Review completed without automated approval.",
		DescriptionPassed: &descriptionPassed,
		AgentSummaries:    []string{"Codex clean", "Claude Code reported 1 finding(s)"},
		Findings: []CodeReviewFinding{{
			Severity:  CodeReviewFindingSeverityHigh,
			Path:      &path,
			StartLine: &line,
			Summary:   "Authorization edge case",
		}},
		RecommendedHumanReviewers: []string{"security/platform"},
		Checklist:                 []string{"Required checks: not passing"},
	})

	require.Contains(t, body, "143 Code Reviewer did not approve this PR", "non-approval body should be explicit")
	require.Contains(t, body, "Risk: needs human review", "non-approval body should include risk classification")
	require.Contains(t, body, "Description: failed", "body should include description policy result")
	require.Contains(t, body, "Review agents: Codex clean, Claude Code reported 1 finding(s)", "body should summarize reviewer evidence")
	require.Contains(t, body, "Policy version: 3", "body should include policy version")
	require.Contains(t, body, "Review session: https://143.dev/sessions/sess_123", "body should include review session link")
	require.Contains(t, body, "- required GitHub checks are not passing", "body should include withholding reasons")
	require.Contains(t, body, "high: src/auth/session.go:88 - Authorization edge case", "body should include grouped findings")
	require.Contains(t, body, "Recommended human reviewers: security/platform", "body should include recommended reviewers")
	require.Contains(t, body, "- Required checks: not passing", "body should include approval checklist")
}

func TestBuildCodeReviewFinalReviewBodyUsesTemplate(t *testing.T) {
	t.Parallel()

	body := BuildCodeReviewFinalReviewBody(CodeReviewFinalReviewInput{
		Decision:      CodeReviewDecisionApproved,
		Acceptable:    true,
		PolicyVersion: 8,
		HeadSHA:       "abc123",
		Summary:       "Evidence is clean.",
		Template:      "{{ .Decision }}|{{ .Risk }}|v{{ .PolicyVersion }}|{{ .HeadSHA }}|{{ .Summary }}",
	})

	require.Equal(t, "approved|acceptable|v8|abc123|Evidence is clean.", body, "final review renderer should honor policy templates")
}

func TestBuildCodeReviewFinalReviewBodyFallsBackForInvalidTemplate(t *testing.T) {
	t.Parallel()

	body := BuildCodeReviewFinalReviewBody(CodeReviewFinalReviewInput{
		Decision: CodeReviewDecisionApproved,
		Template: "{{",
	})

	require.Contains(t, body, "143 Code Reviewer approved this PR", "invalid final review templates should fall back to the built-in body")
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
