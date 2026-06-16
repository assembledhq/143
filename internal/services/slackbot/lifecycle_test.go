package slackbot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRenderSessionStatus(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	link := models.SlackSessionLink{SessionID: sessionID}

	tests := []struct {
		name     string
		input    SlackSessionRenderInput
		expected string
	}{
		{
			name: "starting status includes inferred context and join action",
			input: SlackSessionRenderInput{
				Session:    models.Session{ID: sessionID},
				Link:       link,
				State:      SessionLifecycleStarting,
				SessionURL: "https://143.test/sessions/" + sessionID.String(),
				Context: SlackSessionContextSummary{
					RepositoryName: "assembledhq/143",
					Branch:         "main",
				},
				RoutingMode: SlackRoutingModeStartWork,
			},
			expected: "Starting a 143 session\n\nRepo: assembledhq/143\nBranch: main\nMode: Start work\n\nSession: https://143.test/sessions/" + sessionID.String(),
		},
		{
			name: "failed status includes summary and session link",
			input: SlackSessionRenderInput{
				Session:    models.Session{ID: sessionID},
				Link:       link,
				State:      SessionLifecycleFailed,
				SessionURL: "https://143.test/sessions/" + sessionID.String(),
				Summary:    "Tests failed before the PR could be opened.",
			},
			expected: "Failed\nTests failed before the PR could be opened.\n\nSession: https://143.test/sessions/" + sessionID.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := RenderSessionStatus(tt.input)
			require.Equal(t, tt.expected, actual.Text, "status renderer should return expected fallback text")
			require.NotEmpty(t, actual.Blocks, "status renderer should include Block Kit blocks")
			require.Equal(t, "Join session", actual.Blocks[1].Elements[0]["text"].(map[string]string)["text"], "status renderer should expose a Join session action")
		})
	}
}

func TestRenderFinalResponse(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	diffStats := json.RawMessage(`{"files_changed":2,"added":10,"removed":3}`)
	pr := &models.PullRequest{
		GitHubPRURL: "https://github.com/assembledhq/143/pull/42",
		Status:      models.PullRequestStatusOpen,
		CIStatus:    models.PullRequestCIStatusPending,
	}

	tests := []struct {
		name          string
		content       string
		input         SlackSessionRenderInput
		expectedParts []string
	}{
		{
			name:    "final response includes outcome details and actions",
			content: "Implemented the Slack lifecycle renderer.",
			input: SlackSessionRenderInput{
				Session:    models.Session{ID: sessionID},
				Link:       models.SlackSessionLink{SessionID: sessionID},
				SessionURL: "https://143.test/sessions/" + sessionID.String(),
				Outcome: SlackSessionOutcome{
					BranchURL:   "https://github.com/assembledhq/143/tree/slackbot-lifecycle",
					PullRequest: pr,
					PreviewURL:  "https://preview.143.test",
					DiffStats:   diffStats,
				},
			},
			expectedParts: []string{
				"Implemented the Slack lifecycle renderer.",
				"Session: https://143.test/sessions/" + sessionID.String(),
				"Branch: https://github.com/assembledhq/143/tree/slackbot-lifecycle",
				"PR: https://github.com/assembledhq/143/pull/42 (open, CI pending)",
				"Preview: https://preview.143.test",
				"Changes: 2 files, +10/-3",
			},
		},
		{
			name:    "team final response labels unmapped team session",
			content: "Done.",
			input: SlackSessionRenderInput{
				Session:    models.Session{ID: sessionID},
				Link:       models.SlackSessionLink{SessionID: sessionID, TeamSession: true},
				SessionURL: "https://143.test/sessions/" + sessionID.String(),
			},
			expectedParts: []string{
				"Done.",
				"_This is a team session started from Slack without a linked 143 user._",
			},
		},
		{
			name:    "long final response is truncated",
			content: strings.Repeat("x", 2500),
			input: SlackSessionRenderInput{
				Session:    models.Session{ID: sessionID},
				Link:       models.SlackSessionLink{SessionID: sessionID},
				SessionURL: "https://143.test/sessions/" + sessionID.String(),
			},
			expectedParts: []string{
				"[Truncated in Slack]",
				"Session: https://143.test/sessions/" + sessionID.String(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := RenderFinalResponse(tt.content, tt.input)
			for _, expected := range tt.expectedParts {
				require.Contains(t, actual.Text, expected, "final renderer should include expected content")
			}
			require.NotEmpty(t, actual.Blocks, "final renderer should include Block Kit blocks")
			require.Equal(t, "Join session", actual.Blocks[1].Elements[0]["text"].(map[string]string)["text"], "final renderer should expose a Join session action")
		})
	}
}
