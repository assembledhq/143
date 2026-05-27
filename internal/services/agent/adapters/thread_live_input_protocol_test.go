package adapters

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestOneShotResumeAdaptersDeclareTurnBoundLiveInputProtocol(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		adapter agent.AgentAdapter
	}{
		{name: "Codex", adapter: NewCodexAdapter(zerolog.Nop())},
		{name: "Claude Code", adapter: NewClaudeCodeAdapter(zerolog.Nop())},
		{name: "Gemini CLI", adapter: NewGeminiCLIAdapter(zerolog.Nop())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, ok := tt.adapter.(agent.ThreadRuntimeLiveInputProtocolProvider)
			require.True(t, ok, "adapter should explicitly declare its thread live-input protocol")

			protocol := provider.ThreadRuntimeLiveInputProtocol()
			require.Equal(t, agent.ThreadRuntimeLiveInputProtocolTurnBoundResume, protocol.Mode, "one-shot resume adapters should use provider-native turn-bound resume instead of raw live stdin")
			require.False(t, protocol.DeliversToOpenHandle, "one-shot resume adapters should not claim open-handle delivery")
			require.NotEmpty(t, protocol.Description, "protocol declaration should include operator-facing context")
			require.Contains(t, []models.AgentType{models.AgentTypeCodex, models.AgentTypeClaudeCode, models.AgentTypeGeminiCLI}, tt.adapter.Name(), "test should only cover the one-shot resume adapters")
		})
	}
}
