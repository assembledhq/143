package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildCodeReviewFinalReviewBody(t *testing.T) {
	t.Parallel()

	body := BuildCodeReviewFinalReviewBody(CodeReviewFinalReviewInput{
		Decision:      CodeReviewDecisionCommentOnly,
		Acceptable:    false,
		RiskReasons:   []string{"required GitHub checks are not passing", "review agents unavailable"},
		SessionURL:    "https://143.dev/sessions/sess_123",
		PolicyVersion: 3,
		HeadSHA:       "abc123",
		Summary:       "Review completed without automated approval.",
	})

	require.Contains(t, body, "143 Code Reviewer did not approve this PR", "non-approval body should be explicit")
	require.Contains(t, body, "Risk: needs human review", "non-approval body should include risk classification")
	require.Contains(t, body, "Policy version: 3", "body should include policy version")
	require.Contains(t, body, "- required GitHub checks are not passing", "body should include withholding reasons")
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
