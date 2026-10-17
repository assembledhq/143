package agent

import (
	"context"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/google/uuid"
)

type automaticPublicationResultStore interface {
	UpdateResultWithAutomaticPublication(context.Context, uuid.UUID, uuid.UUID, models.SessionStatus, *models.SessionResult) error
}

// persistRunResult keeps the automation coordination job in the same commit as
// its successful result. Legacy injected stores retain synchronous coordination.
func (o *Orchestrator) persistRunResult(ctx context.Context, run *models.Session, status models.SessionStatus, result *models.SessionResult) (bool, error) {
	if run.AutomationRunID != nil && run.Origin != models.SessionOriginAutomationGoalImprovement && run.InteractionMode != models.SessionInteractionModeInteractive && status == models.SessionStatusCompleted && result != nil && result.Diff != nil && strings.TrimSpace(*result.Diff) != "" {
		if store, ok := o.sessions.(automaticPublicationResultStore); ok {
			return true, store.UpdateResultWithAutomaticPublication(ctx, run.OrgID, run.ID, status, result)
		}
	}
	return false, o.sessions.UpdateResult(ctx, run.OrgID, run.ID, status, result)
}

func (o *Orchestrator) appendAutomationPublicationIntentPrompt(ctx context.Context, input *AgentInput, session *models.Session) error {
	// Linked legacy automations can retain the creation default issue_trigger.
	// The run's frozen policy still controls metadata guidance and publication.
	if input == nil || session == nil || (session.Origin != models.SessionOriginAutomation && session.Origin != models.SessionOriginIssueTrigger) || session.AutomationRunID == nil || session.RepositoryID == nil || session.PRCreationState == models.PRCreationStateSucceeded || len(session.RevisionContext) > 0 || sessionPromptStyle(session) == PromptStyleAnswerOnly || automationTurnStateFromContext(ctx) != nil {
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
