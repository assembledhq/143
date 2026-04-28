package adapters

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestDefaultMap(t *testing.T) {
	t.Parallel()

	adapters := DefaultMap(zerolog.Nop())

	require.Len(t, adapters, 5, "DefaultMap should expose every shipped adapter")
	require.NotNil(t, adapters[models.AgentTypeClaudeCode], "DefaultMap should include Claude Code")
	require.NotNil(t, adapters[models.AgentTypeGeminiCLI], "DefaultMap should include Gemini CLI")
	require.NotNil(t, adapters[models.AgentTypeCodex], "DefaultMap should include Codex")
	require.NotNil(t, adapters[models.AgentTypeAmp], "DefaultMap should include Amp")
	require.NotNil(t, adapters[models.AgentTypePi], "DefaultMap should include Pi")
	require.Equal(t, []models.SessionReviewMode{models.SessionReviewModeDefault, models.SessionReviewModeSecurity}, agent.AdapterReviewModes(adapters[models.AgentTypeClaudeCode]), "Claude Code should advertise the native review modes through the shared registry")
	require.Equal(t, []models.SessionReviewMode{models.SessionReviewModeDefault}, agent.AdapterReviewModes(adapters[models.AgentTypeCodex]), "Codex should advertise its native review mode through the shared registry")
}
