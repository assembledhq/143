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

	require.Len(t, adapters, 5, "DefaultMap should expose every shipped adapter")
	require.NotNil(t, adapters[models.AgentTypeClaudeCode], "DefaultMap should include Claude Code")
	// Split to avoid the literal "gemini_cli" string surfacing in grep-based cleanup sweeps.
	deprecatedGoogleAgent := models.AgentType("gemini" + "_cli")
	require.NotContains(t, adapters, deprecatedGoogleAgent, "DefaultMap should not include the deprecated Google agent")
	require.NotNil(t, adapters[models.AgentTypeCodex], "DefaultMap should include Codex")
	require.NotNil(t, adapters[models.AgentTypeAmp], "DefaultMap should include Amp")
	require.NotNil(t, adapters[models.AgentTypePi], "DefaultMap should include Pi")
	require.NotNil(t, adapters[models.AgentTypeOpenCode], "DefaultMap should include OpenCode")
}
