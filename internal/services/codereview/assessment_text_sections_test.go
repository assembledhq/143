package codereview

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitReviewEvidenceSections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, before, after string
		sameIntent          bool
		afterEligible       bool
	}{
		{"appended Testing section", "Purpose\n", "Purpose\n## Testing\nUnit tests passed\n", true, true},
		{"changed Testing evidence", "Purpose\n## Testing\nUnit tests failed\n", "Purpose\n## Testing\nUnit tests passed\n", true, true},
		{"changed purpose", "Purpose\n## Testing\nUnit tests failed\n", "New purpose\n## Testing\nUnit tests passed\n", false, true},
		{"later intent heading", "Purpose\n## Testing\nFailed\n## Design\nOld design\n", "Purpose\n## Testing\nPassed\n## Design\nNew design\n", false, true},
		{"nested intent heading", "Purpose\n## Testing\nFailed\n### Design\nOld design\n", "Purpose\n## Testing\nPassed\n### Design\nNew design\n", false, true},
		{"fenced logs", "Purpose\n## Test Logs\n```text\nfail\n```\n", "Purpose\n## Test Logs\n```text\npass\n```\n", true, true},
		{"HTML comment boundary", "Purpose\n", "Purpose\n<!--\n## Testing\nnew intent\n-->\n", false, false},
		{"HTML block boundary", "Purpose\n", "Purpose\n<div>\n## Testing\nnew intent\n</div>\n", false, false},
		{"fence closer with suffix", "Purpose\n", "Purpose\n## Testing\n```text\nlog\n```more\n## Design\nnew intent\n", false, false},
		{"duplicate evidence heading", "Purpose\n", "Purpose\n## Testing\none\n## Testing\ntwo\n", false, false},
		{"setext ambiguity", "Purpose\n", "Purpose\nTesting\n-------\nresult\n", false, false},
		{"single dash setext boundary", "Purpose\n", "Purpose\n## Testing\npassed\n-\nnew intent\n", false, false},
		{"single equals setext boundary", "Purpose\n", "Purpose\n## Evidence\nproof\n=\nnew intent\n", false, false},
		{"literal code prose unchanged", "Purpose with `code`\n", "Purpose with `code`\n## Testing\npassed\n", true, true},
		{"literal escaped prose unchanged", "Purpose with \\*literal\\*\n", "Purpose with \\*literal\\*\n## Testing\npassed\n", true, true},
		{"literal code prose changes", "Purpose with `old`\n## Testing\nfailed\n", "Purpose with `new`\n## Testing\npassed\n", false, true},
		{"literal escaped prose changes", "Purpose with \\*old\\*\n## Testing\nfailed\n", "Purpose with \\*new\\*\n## Testing\npassed\n", false, true},
		{"image inside literal code ambiguous", "Purpose\n", "Purpose `![alt](a.png)`\n## Testing\npassed\n", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			beforeIntent, _, err := splitReviewEvidenceSections(tt.before)
			require.NoError(t, err, "baseline Markdown should parse")
			afterIntent, _, err := splitReviewEvidenceSections(tt.after)
			require.Equal(t, tt.afterEligible, err == nil, "ambiguous Markdown should disable reuse")
			require.Equal(t, tt.sameIntent, err == nil && beforeIntent == afterIntent, "only bounded evidence text may change without changing intent")
		})
	}
}
