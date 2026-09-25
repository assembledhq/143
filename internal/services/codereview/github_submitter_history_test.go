package codereview

import (
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewHistoryFooterComposition(t *testing.T) {
	t.Parallel()

	decidedAt := time.Date(2026, time.September, 25, 12, 23, 16, 0, time.UTC)
	entry := "- <relative-time datetime=\"2026-09-25T12:23:16Z\">Sep 25, 2026 at 8:23 AM EDT</relative-time> — **Not approved — needs human review**"
	history := codeReviewHistoryStartMarker + "\n<details>\n<summary>Review history</summary>\n\n" + entry + "\n\n</details>\n" + codeReviewHistoryEndMarker
	legacyHistory := codeReviewHistoryStartMarker + "\nHistory of 143 code reviews:\n- `2026-09-25T12:23:16Z` — **Not approved — needs human review**\n" + codeReviewHistoryEndMarker
	tests := []struct {
		name     string
		body     string
		footer   models.CodeReviewCommentFooter
		compose  func(string) string
		expected string
	}{
		{
			name: "completed history precedes the current evidence action footer",
			body: "Current assessment. [Evidence](https://example.com/screenshot.png)",
			footer: models.CodeReviewCommentFooter{
				DetailURL:          "https://143.test/code-reviews?session=current",
				DetailLabel:        "View full review",
				EvidenceRecheckURL: "https://143.test/code-reviews?recheck=current",
			},
			compose: func(body string) string {
				return withCodeReviewHistory(body, "Previous assessment.\n\n"+legacyHistory, SubmitReviewDecisionNeedsHumanReview, decidedAt)
			},
			expected: "Current assessment. [Evidence](https://example.com/screenshot.png)\n\n" + history,
		},
		{
			name: "active reassessment history precedes the current follow footer",
			body: "Previous assessment.\n\n" + legacyHistory,
			footer: models.CodeReviewCommentFooter{
				DetailURL:   "https://143.test/code-reviews?session=current",
				DetailLabel: "Follow review",
			},
			compose: func(body string) string {
				return WithCodeReviewReassessmentHistory(body, "52f2563abcdef", decidedAt, "")
			},
			expected: "Previous assessment.\n\n" + codeReviewHistoryStartMarker + "\n<details>\n<summary>Review history</summary>\n\n" +
				entry + "\n- <relative-time datetime=\"2026-09-25T12:23:16Z\">Sep 25, 2026 at 8:23 AM EDT</relative-time> — **Reassessment started** for `52f2563`\n\n</details>\n" + codeReviewHistoryEndMarker,
		},
		{
			name: "no history preserves the current footer",
			body: "Current assessment.",
			footer: models.CodeReviewCommentFooter{
				DetailURL:    "https://143.test/code-reviews?session=current",
				DetailLabel:  "View full review",
				ReviewNowURL: "https://143.test/code-reviews?review_now=current",
			},
			compose: func(body string) string {
				return withCodeReviewHistory(body, "Previous assessment.", "", time.Time{})
			},
			expected: "Current assessment.",
		},
		{
			name: "unmarked legacy body preserves evidence and navigation links",
			body: "Current assessment. [Evidence](https://example.com/screenshot.png)\n\n[View the full review](https://143.test/code-reviews?session=current)",
			compose: func(body string) string {
				return withCodeReviewHistory(body, "Previous assessment.", SubmitReviewDecisionNeedsHumanReview, decidedAt)
			},
			expected: "Current assessment. [Evidence](https://example.com/screenshot.png)\n\n[View the full review](https://143.test/code-reviews?session=current)\n\n" + history,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := models.WithCodeReviewCommentFooter(tt.body, tt.footer)
			expected := models.WithCodeReviewCommentFooter(tt.expected, tt.footer)
			actual := tt.compose(body)

			require.Equal(t, expected, actual, "history should precede the current action footer without changing review or evidence content")
			require.Equal(t, expected, tt.compose(actual), "repeating history composition should not duplicate history entries or action footers")
		})
	}
}

func TestCodeReviewHistoryEntriesPreserveLegacyAndDisclosureBodies(t *testing.T) {
	t.Parallel()

	entry := "- <relative-time datetime=\"2026-09-25T12:23:16Z\">Sep 25, 2026 at 8:23 AM EDT</relative-time> — **Not approved — needs human review**"
	tests := []struct {
		name     string
		body     string
		expected []string
	}{
		{
			name:     "normalizes legacy timestamp entries",
			body:     codeReviewHistoryStartMarker + "\nHistory of 143 code reviews:\n- `2026-09-25T12:23:16Z` — **Not approved — needs human review**\n" + codeReviewHistoryEndMarker,
			expected: []string{entry},
		},
		{
			name:     "reads entries inside disclosure without treating markup as entries",
			body:     codeReviewHistoryStartMarker + "\n<details>\n<summary>Review history</summary>\n\n" + entry + "\n\n</details>\n" + codeReviewHistoryEndMarker,
			expected: []string{entry},
		},
		{
			name: "keeps malformed history out of the parsed entries",
			body: codeReviewHistoryStartMarker + "\n<details>\n<summary>Review history</summary>\n\n" + entry,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, codeReviewHistoryEntries(tt.body), "history parsing should retain the existing marker and legacy timestamp contract")
		})
	}
}

func TestNormalizeCodeReviewHistoryEntryEasternTime(t *testing.T) {
	t.Parallel()

	const suffix = " — **Reassessment started** for `52f2563` — [Follow the review session](https://143.test/sessions/current)"
	tests := []struct {
		name     string
		entry    string
		expected string
	}{
		{
			name:     "refreshes UTC fallback without changing the instant or historical link",
			entry:    `- <relative-time datetime="2026-09-25T12:23:16Z">Sep 25, 2026 at 12:23 PM UTC</relative-time>` + suffix,
			expected: `- <relative-time datetime="2026-09-25T12:23:16Z">Sep 25, 2026 at 8:23 AM EDT</relative-time>` + suffix,
		},
		{
			name:     "uses standard time and the previous local date in winter",
			entry:    `- <relative-time datetime="2026-01-02T01:00:00Z">Jan 2, 2026 at 1:00 AM UTC</relative-time>` + suffix,
			expected: `- <relative-time datetime="2026-01-02T01:00:00Z">Jan 1, 2026 at 8:00 PM EST</relative-time>` + suffix,
		},
		{
			name:     "canonicalizes an offset timestamp while preserving its instant",
			entry:    `- <relative-time datetime="2026-09-25T08:23:16-04:00">Sep 25, 2026 at 8:23 AM EDT</relative-time>` + suffix,
			expected: `- <relative-time datetime="2026-09-25T12:23:16Z">Sep 25, 2026 at 8:23 AM EDT</relative-time>` + suffix,
		},
		{
			name:     "leaves an invalid timestamp unchanged",
			entry:    `- <relative-time datetime="not-a-timestamp">Recorded previously</relative-time>` + suffix,
			expected: `- <relative-time datetime="not-a-timestamp">Recorded previously</relative-time>` + suffix,
		},
		{
			name:     "leaves a missing closing tag unchanged",
			entry:    `- <relative-time datetime="2026-09-25T12:23:16Z">Sep 25, 2026 at 12:23 PM UTC` + suffix,
			expected: `- <relative-time datetime="2026-09-25T12:23:16Z">Sep 25, 2026 at 12:23 PM UTC` + suffix,
		},
		{
			name:     "leaves a malformed opening tag unchanged",
			entry:    `- <relative-time datetime="2026-09-25T12:23:16Z>Sep 25, 2026 at 12:23 PM UTC</relative-time>` + suffix,
			expected: `- <relative-time datetime="2026-09-25T12:23:16Z>Sep 25, 2026 at 12:23 PM UTC</relative-time>` + suffix,
		},
		{
			name:     "leaves other HTML formats unchanged",
			entry:    `- <relative-time title="Recorded previously" datetime="2026-09-25T12:23:16Z">Sep 25, 2026 at 12:23 PM UTC</relative-time>` + suffix,
			expected: `- <relative-time title="Recorded previously" datetime="2026-09-25T12:23:16Z">Sep 25, 2026 at 12:23 PM UTC</relative-time>` + suffix,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := normalizeCodeReviewHistoryEntry(tt.entry)
			require.Equal(t, tt.expected, actual, "normalization should update only known timestamp markup and preserve historical content")
			require.Equal(t, tt.expected, normalizeCodeReviewHistoryEntry(actual), "timestamp normalization should remain stable across repeated refreshes")
		})
	}
}

func TestCodeReviewHistoryRetryDoesNotDuplicateRelativeTimeEntries(t *testing.T) {
	t.Parallel()

	decidedAt := time.Date(2026, time.September, 25, 12, 23, 16, 0, time.UTC)
	const easternEntry = `- <relative-time datetime="2026-09-25T12:23:16Z">Sep 25, 2026 at 8:23 AM EDT</relative-time> — **Not approved — needs human review**`
	tests := []struct {
		name  string
		entry string
	}{
		{
			name:  "old UTC fallback is refreshed before deduplication",
			entry: `- <relative-time datetime="2026-09-25T12:23:16Z">Sep 25, 2026 at 12:23 PM UTC</relative-time> — **Not approved — needs human review**`,
		},
		{
			name:  "existing Eastern fallback stays deduplicated",
			entry: easternEntry,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			previousBody := "Previous assessment.\n\n" + codeReviewHistoryStartMarker + "\nHistory of 143 code reviews:\n" + tt.entry + "\n" + codeReviewHistoryEndMarker
			expected := "Current assessment.\n\n" + codeReviewHistoryStartMarker + "\n<details>\n<summary>Review history</summary>\n\n" + easternEntry + "\n\n</details>\n" + codeReviewHistoryEndMarker
			actual := withCodeReviewHistory("Current assessment.", previousBody, SubmitReviewDecisionNeedsHumanReview, decidedAt)

			require.Equal(t, expected, actual, "a retry should retain exactly one historical decision after refreshing the fallback timezone")
			require.Equal(t, expected, withCodeReviewHistory("Current assessment.", actual, SubmitReviewDecisionNeedsHumanReview, decidedAt), "subsequent retries should preserve the same rendered history")
		})
	}
}
