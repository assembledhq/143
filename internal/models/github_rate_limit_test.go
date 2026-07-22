package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitHubRateLimitResourceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resource  GitHubRateLimitResource
		expectErr bool
	}{
		{name: "core", resource: GitHubRateLimitResourceCore},
		{name: "graphql", resource: GitHubRateLimitResourceGraphQL},
		{name: "search", resource: GitHubRateLimitResourceSearch},
		{name: "unknown", resource: GitHubRateLimitResourceUnknown},
		{name: "invalid", resource: GitHubRateLimitResource("code_review"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.resource.Validate()
			if tt.expectErr {
				require.Error(t, err, "unknown GitHub rate-limit resources should be rejected")
				return
			}
			require.NoError(t, err, "known GitHub rate-limit resources should validate")
		})
	}
}
