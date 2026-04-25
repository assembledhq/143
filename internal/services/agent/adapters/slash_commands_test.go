package adapters

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestEnsureSlashCommandsInPrompt(t *testing.T) {
	t.Parallel()

	commands := []models.SessionInputCommand{
		{
			Kind:      "command",
			AgentType: models.AgentTypeClaudeCode,
			Name:      "review",
			Token:     "/review",
			Display:   "/review",
			Arguments: "focus on auth",
		},
	}

	tests := []struct {
		name     string
		message  string
		commands []models.SessionInputCommand
		want     string
	}{
		{
			name:     "no commands returns message unchanged",
			message:  "fix the bug",
			commands: nil,
			want:     "fix the bug",
		},
		{
			name:     "command already present at start is preserved verbatim",
			message:  "/review focus on auth\n\nfix the bug",
			commands: commands,
			want:     "/review focus on auth\n\nfix the bug",
		},
		{
			name:     "command present at start of new line is recognized",
			message:  "context line\n/review focus",
			commands: commands,
			want:     "context line\n/review focus",
		},
		{
			name:     "missing command is prepended",
			message:  "fix the bug",
			commands: commands,
			want:     "/review focus on auth\n\nfix the bug",
		},
		{
			name:    "missing command on empty message",
			message: "",
			commands: []models.SessionInputCommand{
				{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "clear", Token: "/clear", Display: "/clear"},
			},
			want: "/clear",
		},
		{
			name:    "mid-line slash does not count as present — token gets prepended",
			message: "use the dir/review folder",
			commands: []models.SessionInputCommand{
				{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "review", Token: "/review", Display: "/review"},
			},
			want: "/review\n\nuse the dir/review folder",
		},
		{
			name:    "prefix match (/reviewer) does not count as /review presence",
			message: "/reviewer hello",
			commands: []models.SessionInputCommand{
				{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "review", Token: "/review", Display: "/review"},
			},
			want: "/review\n\n/reviewer hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, EnsureSlashCommandsInPrompt(tt.message, tt.commands))
		})
	}
}

func TestEnsureSlashCommandsInPromptIsIdempotent(t *testing.T) {
	t.Parallel()

	commands := []models.SessionInputCommand{
		{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "review", Token: "/review", Display: "/review", Arguments: "focus"},
	}
	first := EnsureSlashCommandsInPrompt("fix the bug", commands)
	second := EnsureSlashCommandsInPrompt(first, commands)
	require.Equal(t, first, second, "applying the helper twice should not duplicate the token")
}
