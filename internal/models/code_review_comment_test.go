package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewCommentTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		value    time.Time
		expected string
	}{
		{
			name:     "winter uses Eastern Standard Time",
			value:    time.Date(2026, time.January, 25, 12, 23, 16, 0, time.UTC),
			expected: `<relative-time datetime="2026-01-25T12:23:16Z">Jan 25, 2026 at 7:23 AM EST</relative-time>`,
		},
		{
			name:     "summer rolls back to the previous Eastern date",
			value:    time.Date(2026, time.July, 23, 2, 14, 8, 0, time.UTC),
			expected: `<relative-time datetime="2026-07-23T02:14:08Z">Jul 22, 2026 at 10:14 PM EDT</relative-time>`,
		},
		{
			name:     "instant before daylight saving starts",
			value:    time.Date(2026, time.March, 8, 6, 59, 0, 0, time.UTC),
			expected: `<relative-time datetime="2026-03-08T06:59:00Z">Mar 8, 2026 at 1:59 AM EST</relative-time>`,
		},
		{
			name:     "instant daylight saving starts",
			value:    time.Date(2026, time.March, 8, 7, 0, 0, 0, time.UTC),
			expected: `<relative-time datetime="2026-03-08T07:00:00Z">Mar 8, 2026 at 3:00 AM EDT</relative-time>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, CodeReviewCommentTime(tt.value), "comment timestamps should retain the UTC instant with a daylight-saving-aware Eastern fallback")
		})
	}
}

func TestWithCodeReviewCommentFooter(t *testing.T) {
	t.Parallel()
	const detail = "https://143.test/sessions/session-1"
	const recheck = "https://143.test/code-reviews?recheck=assessment-1"
	const request = "https://143.test/code-reviews?review_now=session-1"
	tests := []struct {
		name     string
		body     string
		footer   CodeReviewCommentFooter
		expected string
	}{
		{
			name:     "evidence takes precedence over a full request",
			body:     "Review result.",
			footer:   CodeReviewCommentFooter{DetailURL: detail, EvidenceRecheckURL: recheck, ReviewNowURL: request},
			expected: "Review result.\n\n<!-- 143-code-review-footer:start -->\n[Re-check evidence](" + recheck + ") · [View full review](" + detail + ")\n<!-- 143-code-review-footer:end -->",
		},
		{
			name:     "full request fallback",
			body:     "Review failed.",
			footer:   CodeReviewCommentFooter{DetailURL: detail, DetailLabel: "View review", ReviewNowURL: request},
			expected: "Review failed.\n\n<!-- 143-code-review-footer:start -->\n[Request review](" + request + ") · [View review](" + detail + ")\n<!-- 143-code-review-footer:end -->",
		},
		{
			name:     "assessment detail destination",
			footer:   CodeReviewCommentFooter{DetailURL: "https://143.test/code-reviews?assessment=assessment-1"},
			expected: "<!-- 143-code-review-footer:start -->\n[View assessment](https://143.test/code-reviews?assessment=assessment-1)\n<!-- 143-code-review-footer:end -->",
		},
		{
			name:     "missing URLs omit footer",
			body:     "Review result.",
			expected: "Review result.",
		},
		{
			name:     "invalid URLs omitted without dangling separator",
			body:     "Review result.",
			footer:   CodeReviewCommentFooter{DetailURL: detail, EvidenceRecheckURL: "javascript:alert(1)", ReviewNowURL: "https://143.test/\nspoof"},
			expected: "Review result.\n\n<!-- 143-code-review-footer:start -->\n[View full review](" + detail + ")\n<!-- 143-code-review-footer:end -->",
		},
		{
			name:     "URL parentheses cannot break Markdown",
			footer:   CodeReviewCommentFooter{DetailURL: "https://143.test/sessions/session(1)"},
			expected: "<!-- 143-code-review-footer:start -->\n[View full review](https://143.test/sessions/session%281%29)\n<!-- 143-code-review-footer:end -->",
		},
		{
			name:     "HTML tag mentioned in inline code does not hide the footer",
			body:     "❌ **143 Code Reviewer needs human review**\n\n**Change:** Adds a `<details>` element.",
			footer:   CodeReviewCommentFooter{DetailURL: detail},
			expected: "❌ **143 Code Reviewer needs human review**\n\n**Change:** Adds a `<details>` element.\n\n<!-- 143-code-review-footer:start -->\n[View full review](" + detail + ")\n<!-- 143-code-review-footer:end -->",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := WithCodeReviewCommentFooter(tt.body, tt.footer)
			require.Equal(t, tt.expected, actual, "comment should contain only its chosen action and available detail destination")
			require.Equal(t, actual, WithCodeReviewCommentFooter(actual, tt.footer), "repeated rendering should preserve exactly one current footer")
		})
	}
}

func TestSplitCodeReviewCommentFooterPreservesContent(t *testing.T) {
	t.Parallel()
	const trusted = "https://143.test/sessions/current"
	const footer = "<!-- 143-code-review-footer:start -->\n[View full review](https://143.test/sessions/old)\n<!-- 143-code-review-footer:end -->"
	const heading = "❌ **143 Code Reviewer needs human review**"
	tests := []struct {
		name     string
		body     string
		expected string
		links    CodeReviewCommentFooter
	}{
		{
			name:     "current footer with trailing publication marker",
			body:     heading + "\n\n" + footer + "\n\n<!-- 143-code-review:output -->",
			expected: heading + "\n\n<!-- 143-code-review:output -->",
			links:    CodeReviewCommentFooter{DetailURL: "https://143.test/sessions/old", DetailLabel: "View full review"},
		},
		{
			name:     "nested previous footer remains historical",
			body:     "<details>\n<summary>Previous review</summary>\n\n" + footer + "\n\n</details>\n\n" + footer,
			expected: "<details>\n<summary>Previous review</summary>\n\n" + footer + "\n\n</details>",
			links:    CodeReviewCommentFooter{DetailURL: "https://143.test/sessions/old", DetailLabel: "View full review"},
		},
		{
			name:     "footer-shaped code sample preserved",
			body:     heading + "\n\n```md\n" + footer + "\n```",
			expected: heading + "\n\n```md\n" + footer + "\n```",
		},
		{
			name:     "open historical disclosure is preserved",
			body:     "<details open>\n<summary>Previous review</summary>\n\n" + footer + "\n\n</details>",
			expected: "<details open>\n<summary>Previous review</summary>\n\n" + footer + "\n\n</details>",
		},
		{
			name:     "malformed marker preserved",
			body:     heading + "\n\n<!-- 143-code-review-footer:start -->\nA substantive explanation.",
			expected: heading + "\n\n<!-- 143-code-review-footer:start -->\nA substantive explanation.",
		},
		{
			name:     "foreign navigation preserved rather than treated as app action",
			body:     heading + "\n\n[View the full review](https://other.test/sessions/old)",
			expected: heading + "\n\n[View the full review](https://other.test/sessions/old)",
		},
		{
			name:     "same-origin evidence link is not generated navigation",
			body:     heading + "\n\nEvidence: [View the full review](https://143.test/sessions/old).",
			expected: heading + "\n\nEvidence: [View the full review](https://143.test/sessions/old).",
		},
		{
			name:     "legacy links inside history are preserved",
			body:     heading + "\n\n<!-- 143-code-review-history:start -->\nHistory of 143 code reviews:\n\n[Follow the review session](https://143.test/sessions/old)\n\n<!-- 143-code-review-history:end -->",
			expected: heading + "\n\n<!-- 143-code-review-history:start -->\nHistory of 143 code reviews:\n\n[Follow the review session](https://143.test/sessions/old)\n\n<!-- 143-code-review-history:end -->",
		},
		{
			name:     "legacy links in advisory disclosure preserved",
			body:     heading + "\n\n<details>\n<summary>Advisory findings</summary>\n\n[View the full review](https://143.test/sessions/old)\n\n</details>",
			expected: heading + "\n\n<details>\n<summary>Advisory findings</summary>\n\n[View the full review](https://143.test/sessions/old)\n\n</details>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body, footer := SplitCodeReviewCommentFooter(tt.body, trusted)
			require.Equal(t, tt.expected, body, "extracting current navigation must preserve substantive and historical content")
			require.Equal(t, tt.links, footer, "only recognized app navigation should become current footer controls")
		})
	}
}

func TestSplitLegacyCodeReviewCommentFooter(t *testing.T) {
	t.Parallel()
	const heading = "❌ **143 Code Reviewer needs human review**"
	const detail = "https://143.test/app/sessions/review-1"
	const recheck = "https://143.test/app/code-reviews?recheck=assessment-1"
	const request = "https://143.test/app/code-reviews?review_now=review-1"
	body := heading + "\n\n**Why:** This PR did not meet the configured approval policy; the blockers are grouped below.\n\n" +
		"**Policy thresholds:**\n- Missing screenshot. [View policy setting](https://143.test/app/code-reviews?tab=policy)\n- Too many files. [View policy setting](https://143.test/app/code-reviews?tab=policy#policy-max-files-changed)\n\n" +
		"**Review findings:**\n- Evidence: [capture](https://example.com/evidence).\n\n" +
		"**Reviewer evidence:** Codex found no blocking issues.\n\n" +
		"**Next steps:** Add the missing evidence under **Testing** or **Evidence** in the PR description or a comment, then [Re-check PR evidence](" + recheck + "). Open 143 to confirm the request. Any remaining approval requirements still apply.\n\n" +
		"**Latest assessment:** `52f2563` at 2026-09-25T12:23:16Z\n\n[View the full review](" + detail + ")\n\n" +
		"[Request Full Re-Review Now](" + request + ") · Open 143 to request a review of your latest pushed changes. If a running or completed review already covers those changes, 143 may use it instead of starting another."
	expected := heading + "\n\n**Policy thresholds:**\n- Missing screenshot.\n- Too many files.\n\n" +
		"**Review findings:**\n- Evidence: [capture](https://example.com/evidence).\n\n**Reviewers:** Codex found no blocking issues.\n\n" +
		"**Next steps:** Add the missing evidence under **Testing** or **Evidence** in the PR description or a comment, then request an evidence recheck. Other approval requirements still apply.\n\n" +
		"*Assessed `52f2563` · <relative-time datetime=\"2026-09-25T12:23:16Z\">Sep 25, 2026 at 8:23 AM EDT</relative-time>*"
	content, footer := SplitCodeReviewCommentFooter(body, detail)
	require.Equal(t, expected, content, "the known legacy layout should remove navigation without losing a blocker or evidence link")
	require.Equal(t, CodeReviewCommentFooter{DetailURL: detail, DetailLabel: "View full review", EvidenceRecheckURL: recheck, ReviewNowURL: request}, footer, "legacy actions should remain available for current-state selection")
}

func TestCodeReviewCommentDetailLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, expected string
		active              bool
	}{
		{name: "completed session", url: "https://143.test/sessions/a", expected: "View full review"},
		{name: "active session", url: "https://143.test/sessions/a", active: true, expected: "Follow review"},
		{name: "completed assessment", url: "https://143.test/code-reviews?assessment=a", expected: "View assessment"},
		{name: "active assessment", url: "https://143.test/code-reviews?assessment=a", active: true, expected: "Follow assessment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, CodeReviewCommentDetailLabel(tt.url, tt.active), "detail link should name the displayed review kind and lifecycle")
		})
	}
}
