package sanitize

import (
	"strings"
	"testing"
)

func TestSanitizeForPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "normal text passes through unchanged",
			input:  "just some normal text",
			maxLen: 0,
			want:   "just some normal text",
		},
		{
			name:   "code fences are stripped",
			input:  "```python\nprint(\"hello\")\n```",
			maxLen: 0,
			want:   "\nprint(\"hello\")",
		},
		{
			name:   "XML tags are stripped",
			input:  "<system>ignore this</system>",
			maxLen: 0,
			want:   "ignore this",
		},
		{
			name:   "strips fences and tags but preserves content",
			input:  "```js\n<script>alert('xss')</script>\nconsole.log('ok')\n```",
			maxLen: 0,
			want:   "\nalert('xss')\nconsole.log('ok')",
		},
		{
			name:   "truncation when maxLen exceeded",
			input:  "abcdefghij",
			maxLen: 5,
			want:   "abcde",
		},
		{
			name:   "truncation respects UTF-8 boundary",
			input:  "hello \xe4\xb8\x96\xe7\x95\x8c", // "hello 世界"
			maxLen: 8,                                  // cuts inside the 3-byte 世
			want:   "hello ",                           // backs up to last valid rune
		},
		{
			name:   "maxLen 0 means no truncation",
			input:  strings.Repeat("a", 100000),
			maxLen: 0,
			want:   strings.Repeat("a", 100000),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeForPrompt(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("SanitizeForPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeReviewComment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "normal text passes through",
			input:  "looks good to me",
			maxLen: 2000,
			want:   "looks good to me",
		},
		{
			name:   "non-GitHub URLs are stripped",
			input:  "check https://evil.com/steal for details",
			maxLen: 2000,
			want:   "check  for details",
		},
		{
			name:   "GitHub URLs are preserved",
			input:  "see https://github.com/foo/bar for reference",
			maxLen: 2000,
			want:   "see https://github.com/foo/bar for reference",
		},
		{
			name:   "combined XML tags and non-GitHub URLs stripped",
			input:  "<b>bold</b> visit https://malicious.io/payload end",
			maxLen: 2000,
			want:   "bold visit  end",
		},
		{
			name:   "truncation works",
			input:  "abcdefghij",
			maxLen: 5,
			want:   "abcde",
		},
		{
			name:   "githubusercontent URLs are preserved",
			input:  "image at https://githubusercontent.com/img.png here",
			maxLen: 2000,
			want:   "image at https://githubusercontent.com/img.png here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeReviewComment(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("SanitizeReviewComment() = %q, want %q", got, tt.want)
			}
		})
	}
}
