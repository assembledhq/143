package agent

import (
	"context"

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
		o.logger.Warn().Str("session_id", session.ID.String()).Msg("omitting optional automatic publication metadata guidance because policy is unavailable")
		return nil
	}
	policy, err := o.automationRuns.AutomaticPublishPolicy(ctx, session.OrgID, *session.AutomationRunID)
	if err != nil {
		o.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("omitting optional automatic publication metadata guidance because policy lookup failed")
		return nil
	}
	if policy == models.AutomationPublishPolicyPullRequest {
		input.IntegrationSkills += "\n" + prompts.AutomationPublicationIntent()
	}
	return nil
}
