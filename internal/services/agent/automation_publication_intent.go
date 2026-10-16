package agent

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
)

func (o *Orchestrator) appendAutomationPublicationIntentPrompt(ctx context.Context, input *AgentInput, session *models.Session) error {
	if input == nil || session == nil || session.Origin != models.SessionOriginAutomation || session.AutomationRunID == nil || session.RepositoryID == nil || session.PRCreationState == models.PRCreationStateSucceeded || len(session.RevisionContext) > 0 || sessionPromptStyle(session) == PromptStyleAnswerOnly || automationTurnStateFromContext(ctx) != nil {
		return nil
	}
	hasPublishing := false
	for _, capability := range session.CapabilitySnapshot {
		if capability.ID == models.AgentCapabilityPublishing && capability.AccessLevel.Rank() >= models.AgentCapabilityAccessWrite.Rank() {
			hasPublishing = true
		}
	}
	if !hasPublishing {
		return nil
	}
	if o.automationRuns == nil {
		return fmt.Errorf("automatic publication metadata policy is unavailable")
	}
	policy, err := o.automationRuns.AutomaticPublishPolicy(ctx, session.OrgID, *session.AutomationRunID)
	if err != nil {
		return fmt.Errorf("resolve automatic publication metadata guidance: %w", err)
	}
	if policy == models.AutomationPublishPolicyPullRequest {
		input.IntegrationSkills += "\n" + prompts.AutomationPublicationIntent()
	}
	return nil
}
