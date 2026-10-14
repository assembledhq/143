package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewAgentDeadlineUsesCapturedStart(t *testing.T) {
	t.Parallel()

	capturedZone := time.FixedZone("captured", 2*60*60)
	captured := time.Date(2026, time.January, 2, 3, 4, 5, 6, capturedZone)
	tests := []struct {
		name     string
		timeout  int
		expected time.Time
	}{
		{
			name:     "positive timeout retains captured start",
			timeout:  90,
			expected: time.Date(2026, time.January, 2, 3, 5, 35, 6, capturedZone),
		},
		{
			name:     "zero timeout preserves existing thirty minute default",
			expected: time.Date(2026, time.January, 2, 3, 34, 5, 6, capturedZone),
		},
		{
			name:     "negative timeout preserves existing thirty minute default",
			timeout:  -1,
			expected: time.Date(2026, time.January, 2, 3, 34, 5, 6, capturedZone),
		},
		{
			name:     "long captured timeout does not restart from observation time",
			timeout:  3600,
			expected: time.Date(2026, time.January, 2, 4, 4, 5, 6, capturedZone),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var policy CodeReviewPolicyConfig
			policy.AgentRoster.TimeoutSeconds = tt.timeout
			require.Equal(t, tt.expected, CodeReviewAgentDeadline(policy, captured), "review and recovery must use the same captured timeout without renewing its timestamp")
		})
	}
}
