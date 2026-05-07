package handlers

import (
	"fmt"

	"github.com/assembledhq/143/internal/models"
)

func parseReasoningEffortForAgent(agentType models.AgentType, raw string) (*models.ReasoningEffort, error) {
	reasoningEffort := models.ReasoningEffort(raw)
	if err := reasoningEffort.Validate(); err != nil {
		return nil, err
	}
	if reasoningEffort != "" && !agentType.SupportsReasoningEffortLevel(reasoningEffort) {
		return nil, fmt.Errorf("reasoning_effort is not supported for agent_type %q", agentType)
	}
	if reasoningEffort == "" {
		return nil, nil
	}
	return &reasoningEffort, nil
}
