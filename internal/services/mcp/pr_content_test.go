package mcp

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/integration"
)

type contentPullRequestCreator struct {
	*mockPullRequestCreator
	params integration.CreatePullRequestParams
	calls  int
}

func (c *contentPullRequestCreator) CreatePullRequest(_ context.Context, params integration.CreatePullRequestParams) (*integration.CreatePullRequestResult, error) {
	c.calls++
	c.params = params
	return &integration.CreatePullRequestResult{Status: "awaiting_automatic_publication", SessionID: "session"}, nil
}

func TestPRCreateCLIForwardsStagedText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		args     []string
		expected integration.CreatePullRequestParams
	}{
		{
			name:     "title body file and teams",
			args:     []string{"pr", "create", "--title", "feature-brief: Plan exports", "--body-file", " /tmp/pr-description.md ", "--team-reviewers", "acme/team"},
			expected: integration.CreatePullRequestParams{Title: new("feature-brief: Plan exports"), BodyFile: "/tmp/pr-description.md", TeamReviewers: []string{"acme/team"}},
		},
		{
			name:     "inline body",
			args:     []string{"pr", "create", "--body", "Source plan: docs/plans/exports.md"},
			expected: integration.CreatePullRequestParams{Body: new("Source plan: docs/plans/exports.md")},
		},
		{name: "no staged text", args: []string{"pr", "create"}, expected: integration.CreatePullRequestParams{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			creator := &contentPullRequestCreator{mockPullRequestCreator: &mockPullRequestCreator{name: "session"}}
			registry := integration.NewRegistry()
			registry.RegisterPullRequestCreator(creator)
			var stdout, stderr bytes.Buffer
			code := RunCLI(context.Background(), NewToolRegistry(registry), tt.args, &stdout, &stderr)
			require.Equal(t, 0, code, "CLI should accept staged pull request text flags")
			require.Empty(t, stderr.String(), "successful staging should not print an error")
			require.Equal(t, tt.expected, creator.params, "CLI should forward exactly the staged text and leave body_file for the sandbox client")
		})
	}
}

func TestPRCreateCLIRejectsInvalidStagedText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{name: "blank title", args: []string{"pr", "create", "--title", "  "}, message: "title must not be empty"},
		{name: "body and body file", args: []string{"pr", "create", "--body", "inline", "--body-file", "/tmp/pr.md"}, message: "body and body_file are mutually exclusive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			creator := &contentPullRequestCreator{mockPullRequestCreator: &mockPullRequestCreator{name: "session"}}
			registry := integration.NewRegistry()
			registry.RegisterPullRequestCreator(creator)
			var stdout, stderr bytes.Buffer
			code := RunCLI(context.Background(), NewToolRegistry(registry), tt.args, &stdout, &stderr)
			require.Equal(t, 1, code, "invalid staged text should fail the command")
			require.Contains(t, stderr.String(), tt.message, "the error should explain the invalid staged text")
			require.Equal(t, 0, creator.calls, "invalid staged text must not reach the internal API")
		})
	}
}
