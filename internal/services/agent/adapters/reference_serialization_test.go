package adapters

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestPreparePrompt_SerializesCanonicalReferencesForManualSessions(t *testing.T) {
	t.Parallel()

	message := "Investigate the session composer flow."
	input := &agent.AgentInput{
		Issue: &models.Issue{
			Title:       "Manual session",
			Source:      models.IssueSourceManual,
			Description: &message,
		},
		References: []models.SessionInputReference{
			{
				Kind:    models.SessionInputReferenceKindFile,
				Token:   "@internal/api/handlers/sessions.go",
				Path:    "internal/api/handlers/sessions.go",
				Display: "internal/api/handlers/sessions.go",
			},
			{
				Kind:    models.SessionInputReferenceKindDirectory,
				Token:   "@frontend/src/app/(dashboard)/sessions/new",
				Path:    "frontend/src/app/(dashboard)/sessions/new",
				Display: "frontend/src/app/(dashboard)/sessions/new",
			},
		},
	}

	tests := []struct {
		name    string
		adapter agent.AgentAdapter
	}{
		{name: "claude", adapter: NewClaudeCodeAdapter(zerolog.Nop())},
		{name: "codex", adapter: NewCodexAdapter(zerolog.Nop())},
		{name: "gemini", adapter: NewGeminiCLIAdapter(zerolog.Nop())},
		{name: "amp", adapter: NewAmpAdapter(zerolog.Nop())},
		{name: "pi", adapter: NewPiAdapter(zerolog.Nop())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			prompt, err := tt.adapter.PreparePrompt(context.Background(), input)
			require.NoError(t, err, "PreparePrompt should succeed")
			require.Contains(t, prompt.UserPrompt, "@internal/api/handlers/sessions.go", "file references should be preserved in the user prompt")
			require.Contains(t, prompt.UserPrompt, "@frontend/src/app/(dashboard)/sessions/new", "directory references should be preserved in the user prompt")
		})
	}
}
