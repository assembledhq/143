package slackbot

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestResolveSlackContext(t *testing.T) {
	t.Parallel()

	repoID := uuid.New()
	branch := "main"

	tests := []struct {
		name            string
		input           SlackContextResolveInput
		expectedRouting SlackRoutingMode
		expectedBranch  string
		expectedSummary SlackSessionContextSummary
		checkSummary    bool
		expectedMissing []MissingSlackContext
	}{
		{
			name: "inherits defaults and start override",
			input: SlackContextResolveInput{
				Text: "<@U143> start fix the dashboard",
				Settings: models.EffectiveSlackChannelSettings{
					DefaultRepositoryID: &repoID,
					DefaultBranch:       &branch,
					RoutingMode:         models.SlackRoutingModeAuto,
				},
			},
			expectedRouting: SlackRoutingModeStartWork,
			expectedBranch:  "main",
		},
		{
			name: "ask override produces answer only routing",
			input: SlackContextResolveInput{
				Text: "<@U143> ask why did CI fail?",
				Settings: models.EffectiveSlackChannelSettings{
					DefaultRepositoryID: &repoID,
					RoutingMode:         models.SlackRoutingModeStartWork,
				},
			},
			expectedRouting: SlackRoutingModeAnswerOnly,
		},
		{
			name: "answer only does not require a repository default",
			input: SlackContextResolveInput{
				Text:     "<@U143> ask what happened in this thread?",
				Settings: models.EffectiveSlackChannelSettings{},
			},
			expectedRouting: SlackRoutingModeAnswerOnly,
		},
		{
			name: "preview without target reports missing target",
			input: SlackContextResolveInput{
				Text:     "create a preview",
				Settings: models.EffectiveSlackChannelSettings{RoutingMode: models.SlackRoutingModeAuto},
			},
			expectedRouting: SlackRoutingModeAuto,
			expectedMissing: []MissingSlackContext{
				{Kind: "repository", Reason: "Choose a repository before starting durable work from Slack."},
				{Kind: "preview_target", Reason: "Choose a branch, PR, session, or repository before creating a preview."},
			},
		},
		{
			name: "fix this PR without PR reference reports missing PR",
			input: SlackContextResolveInput{
				Text: "fix this PR",
				Settings: models.EffectiveSlackChannelSettings{
					DefaultRepositoryID: &repoID,
					RoutingMode:         models.SlackRoutingModeAuto,
				},
			},
			expectedRouting: SlackRoutingModeAuto,
			expectedMissing: []MissingSlackContext{{Kind: "pull_request", Reason: "Choose the pull request to repair."}},
		},
		{
			name: "hydrates PR and preview references into ack summary",
			input: SlackContextResolveInput{
				Text: "check this preview and PR",
				Settings: models.EffectiveSlackChannelSettings{
					DefaultRepositoryID: &repoID,
					RoutingMode:         models.SlackRoutingModeAuto,
				},
				References: []SlackContextReference{
					{Kind: SlackContextPullRequest, Value: "https://github.com/acme/api/pull/42"},
					{Kind: SlackContextPreview, Value: "https://preview.example.com"},
				},
			},
			expectedRouting: SlackRoutingModeAuto,
			expectedSummary: SlackSessionContextSummary{
				PullRequestURL: "https://github.com/acme/api/pull/42",
				PreviewURL:     "https://preview.example.com",
			},
			checkSummary: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := ResolveSlackContext(tt.input)

			require.Equal(t, tt.expectedRouting, actual.RoutingMode, "resolver should return expected routing mode")
			require.Equal(t, tt.expectedBranch, actual.Branch, "resolver should return expected branch")
			if tt.checkSummary {
				require.Equal(t, tt.expectedSummary, actual.ContextSummary, "resolver should return expected context summary")
			}
			require.Equal(t, tt.expectedMissing, actual.Missing, "resolver should return expected missing context")
		})
	}
}
