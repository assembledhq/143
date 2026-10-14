package metrics

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGitHubActivityDimensions(t *testing.T) {
	t.Parallel()
	tests := []struct{ value, expected string }{{"sandbox_gh", "sandbox_gh"}, {"session-fixture", "other"}, {"example/fixture", "other"}, {"", "other"}}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, githubActivityDimension(tt.value, "sandbox_gh"), "unknown or identifying values must collapse to a bounded metric dimension")
		})
	}
}
