package adapters

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestDefaultMap(t *testing.T) {
	t.Parallel()

	adapters := DefaultMap(zerolog.Nop())

	require.Len(t, adapters, 6, "DefaultMap should expose every shipped adapter")
	require.NotNil(t, adapters[models.AgentTypeClaudeCode], "DefaultMap should include Claude Code")
	require.NotNil(t, adapters[models.AgentTypeGeminiCLI], "DefaultMap should include Gemini CLI")
	require.NotNil(t, adapters[models.AgentTypeCodex], "DefaultMap should include Codex")
	require.NotNil(t, adapters[models.AgentTypeAmp], "DefaultMap should include Amp")
	require.NotNil(t, adapters[models.AgentTypePi], "DefaultMap should include Pi")
	require.NotNil(t, adapters[models.AgentTypeOpenCode], "DefaultMap should include OpenCode")
}
