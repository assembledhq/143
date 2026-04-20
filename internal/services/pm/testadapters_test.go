package pm

import (
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// singleTestAdapterMap routes every standard agent type to the given adapter.
// Simplifies tests that exercise a single mock adapter regardless of the
// DefaultAgentType picked by the resolver.
func singleTestAdapterMap(a agent.AgentAdapter) map[models.AgentType]agent.AgentAdapter {
	return map[models.AgentType]agent.AgentAdapter{
		models.AgentTypeClaudeCode: a,
		models.AgentTypeCodex:      a,
		models.AgentTypeGeminiCLI:  a,
	}
}
