package mcp

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/integration"
)

type reviewerPullRequestCreator struct {
	*mockPullRequestCreator
	params integration.CreatePullRequestParams
}

func (c *reviewerPullRequestCreator) CreatePullRequest(_ context.Context, params integration.CreatePullRequestParams) (*integration.CreatePullRequestResult, error) {
	c.params = params
	return &integration.CreatePullRequestResult{Status: "awaiting_automatic_publication", SessionID: "session"}, nil
}

func TestPRCreateCLIForwardsTeamReviewerArray(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		args     []string
		expected []string
	}{
		{name: "explicit comma-separated teams", args: []string{"pr", "create", "--team-reviewers", "acme/team,other-team"}, expected: []string{"acme/team", "other-team"}},
		{name: "no inferred reviewer defaults", args: []string{"pr", "create"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			creator := &reviewerPullRequestCreator{mockPullRequestCreator: &mockPullRequestCreator{name: "session"}}
			registry := integration.NewRegistry()
			registry.RegisterPullRequestCreator(creator)
			var stdout, stderr bytes.Buffer
			code := RunCLI(context.Background(), NewToolRegistry(registry), tt.args, &stdout, &stderr)
			require.Equal(t, 0, code, "CLI should accept the optional array flag using its existing schema parser")
			require.Empty(t, stderr.String(), "successful metadata request should not print an error")
			require.Equal(t, tt.expected, creator.params.TeamReviewers, "CLI and MCP dispatch should preserve only explicitly requested teams")
			require.Contains(t, stdout.String(), "awaiting_automatic_publication", "tool must preserve the typed metadata-only response")
		})
	}
}
