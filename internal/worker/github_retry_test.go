package worker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGitHubRateLimitRetryAfter(t *testing.T) {
	t.Parallel()

	zero := time.Duration(0)
	short := 17 * time.Second
	long := 117 * time.Second
	tests := []struct {
		name     string
		upstream *time.Duration
		retryKey string
		expected time.Duration
	}{
		{name: "missing hint uses floor and jitter", retryKey: "00000000-0000-0000-0000-000000000143", expected: 82 * time.Second},
		{name: "zero hint uses floor and jitter", upstream: &zero, retryKey: "00000000-0000-0000-0000-000000000143", expected: 82 * time.Second},
		{name: "short hint uses floor and jitter", upstream: &short, retryKey: "00000000-0000-0000-0000-000000000143", expected: 82 * time.Second},
		{name: "long hint preserves upstream wait and adds jitter", upstream: &long, retryKey: "00000000-0000-0000-0000-000000000143", expected: 139 * time.Second},
		{name: "different retry key receives different stable jitter", retryKey: "11111111-2222-3333-4444-555555555555", expected: 78 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := githubRateLimitRetryAfter(tt.upstream, tt.retryKey)
			require.NotNil(t, actual, "rate-limit policy should always return an explicit delay")
			require.Equal(t, tt.expected, *actual, "rate-limit policy should apply the expected floor and deterministic jitter")
		})
	}
}
