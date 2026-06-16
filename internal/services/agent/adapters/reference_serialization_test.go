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
		Manual:      true,
		UserMessage: message,
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
		{name: "amp", adapter: NewAmpAdapter(zerolog.Nop())},
		{name: "pi", adapter: NewPiAdapter(zerolog.Nop())},
		{name: "opencode", adapter: NewOpenCodeAdapter(zerolog.Nop())},
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

func TestPreparePrompt_SerializesMaterializedAttachmentsForManualSessions(t *testing.T) {
	t.Parallel()

	message := "This is the error."
	input := &agent.AgentInput{
		Issue: &models.Issue{
			Title:       "Manual session",
			Source:      models.IssueSourceManual,
			Description: &message,
		},
		Manual:      true,
		UserMessage: message,
		Attachments: []agent.AgentAttachment{
			{
				OriginalURL:  "/api/v1/uploads/files/00000000-0000-0000-0000-000000000001/2026-05/screenshot.png",
				LocalPath:    "/home/sandbox/.143/attachments/turn-3/attachment-1-screenshot.png",
				ContentType:  "image/png",
				MessageIndex: 1,
			},
			{
				OriginalURL:  "https://example.com/external.png",
				Error:        "external attachments are not fetched in v1",
				MessageIndex: 1,
			},
		},
	}

	tests := []struct {
		name    string
		adapter agent.AgentAdapter
	}{
		{name: "claude", adapter: NewClaudeCodeAdapter(zerolog.Nop())},
		{name: "codex", adapter: NewCodexAdapter(zerolog.Nop())},
		{name: "amp", adapter: NewAmpAdapter(zerolog.Nop())},
		{name: "pi", adapter: NewPiAdapter(zerolog.Nop())},
		{name: "opencode", adapter: NewOpenCodeAdapter(zerolog.Nop())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			prompt, err := tt.adapter.PreparePrompt(context.Background(), input)
			require.NoError(t, err, "PreparePrompt should succeed")
			require.Contains(t, prompt.UserPrompt, "## Attached files", "prompt should include an attachment section")
			require.Contains(t, prompt.UserPrompt, "/home/sandbox/.143/attachments/turn-3/attachment-1-screenshot.png", "prompt should include sandbox-local attachment paths")
			require.Contains(t, prompt.UserPrompt, "external attachments are not fetched in v1", "prompt should include unresolved attachment warnings")
		})
	}
}
