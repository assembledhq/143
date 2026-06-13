// Package adapters contains implementations of the agent.AgentAdapter interface
// for specific coding agent CLIs.
package adapters

import (
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// DefaultMap returns the canonical agent adapter map: every shipped adapter
// keyed by its AgentType. It is the single source of truth for "what agents
// does this build support" and is consumed both by the orchestrator (for
// agent execution) and by the API router's session-review service (for
// capability lookup). Adapter constructors take only a logger, so calling
// this twice during boot is safe and cheap — but having one factory means
// adding a new agent only requires editing one place.
func DefaultMap(logger zerolog.Logger) map[models.AgentType]agent.AgentAdapter {
	return map[models.AgentType]agent.AgentAdapter{
		models.AgentTypeClaudeCode: NewClaudeCodeAdapter(logger),
		models.AgentTypeGeminiCLI:  NewGeminiCLIAdapter(logger),
		models.AgentTypeCodex:      NewCodexAdapter(logger),
		models.AgentTypeAmp:        NewAmpAdapter(logger),
		models.AgentTypePi:         NewPiAdapter(logger),
		models.AgentTypeOpenCode:   NewOpenCodeAdapter(logger),
	}
}
