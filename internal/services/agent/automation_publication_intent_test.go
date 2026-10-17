package agent

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
)

type publicationPromptRunPolicy struct {
	policy models.AutomationPublishPolicy
	err    error
	scope  *[]uuid.UUID
}

func (s publicationPromptRunPolicy) OnSessionComplete(context.Context, *models.Session, models.SessionStatus) error {
	return nil
}
func (s publicationPromptRunPolicy) AutomaticPublishPolicy(_ context.Context, orgID, runID uuid.UUID) (models.AutomationPublishPolicy, error) {
	if s.scope != nil {
		*s.scope = append(*s.scope, orgID, runID)
	}
	return s.policy, s.err
}

func TestAutomationPublicationMetadataPromptGate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		policy                models.AutomationPublishPolicy
		policyErr             error
		edit                  func(*models.Session)
		perTarget, wantPrompt bool
		missingPolicy         bool
	}{
		{name: "configured per-run publishing", policy: models.AutomationPublishPolicyPullRequest, wantPrompt: true},
		{name: "linked legacy per-run publishing", policy: models.AutomationPublishPolicyPullRequest, wantPrompt: true, edit: func(s *models.Session) { s.Origin = models.SessionOriginIssueTrigger }},
		{name: "legacy session requires run link", policy: models.AutomationPublishPolicyPullRequest, edit: func(s *models.Session) { s.Origin, s.AutomationRunID = models.SessionOriginIssueTrigger, nil }},
		{name: "legacy none policy", policy: models.AutomationPublishPolicyNone, edit: func(s *models.Session) { s.Origin = models.SessionOriginIssueTrigger }},
		{name: "legacy missing publishing grant", policy: models.AutomationPublishPolicyPullRequest, edit: func(s *models.Session) { s.Origin, s.CapabilitySnapshot = models.SessionOriginIssueTrigger, nil }},
		{name: "legacy read-only publishing grant", policy: models.AutomationPublishPolicyPullRequest, edit: func(s *models.Session) {
			s.Origin = models.SessionOriginIssueTrigger
			s.CapabilitySnapshot[0].AccessLevel = models.AgentCapabilityAccessRead
		}},
		{name: "legacy per-target turn", policy: models.AutomationPublishPolicyPullRequest, perTarget: true, edit: func(s *models.Session) { s.Origin = models.SessionOriginIssueTrigger }},
		{name: "linked manual origin stays excluded", policy: models.AutomationPublishPolicyPullRequest, edit: func(s *models.Session) { s.Origin = models.SessionOriginManual }},
		{name: "linked goal improvement stays excluded", policy: models.AutomationPublishPolicyPullRequest, edit: func(s *models.Session) { s.Origin = models.SessionOriginAutomationGoalImprovement }},
		{name: "none policy", policy: models.AutomationPublishPolicyNone},
		{name: "lookup failure only omits optional guidance", policyErr: errors.New("lookup failed")},
		{name: "missing lookup only omits optional guidance", missingPolicy: true},
		{name: "missing grant", edit: func(s *models.Session) { s.CapabilitySnapshot = nil }},
		{name: "read-only grant", edit: func(s *models.Session) { s.CapabilitySnapshot[0].AccessLevel = models.AgentCapabilityAccessRead }},
		{name: "per-target turn", perTarget: true},
		{name: "existing publication", edit: func(s *models.Session) { s.PRCreationState = models.PRCreationStateSucceeded }},
		{name: "ordinary session", edit: func(s *models.Session) { s.Origin = models.SessionOriginManual }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runID, repoID := uuid.New(), uuid.New()
			session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: models.SessionOriginAutomation, AutomationRunID: &runID, RepositoryID: &repoID, CapabilitySnapshot: []models.AgentCapabilitySnapshotItem{{ID: models.AgentCapabilityPublishing, AccessLevel: models.AgentCapabilityAccessWrite}}}
			if tt.edit != nil {
				tt.edit(session)
			}
			ctx := context.Background()
			if tt.perTarget {
				ctx = withAutomationTurnState(ctx, &automationTurnState{})
			}
			input := &AgentInput{IntegrationSkills: "existing skills"}
			var logs bytes.Buffer
			var policyScope []uuid.UUID
			orchestrator := &Orchestrator{automationRuns: publicationPromptRunPolicy{policy: tt.policy, err: tt.policyErr, scope: &policyScope}, logger: zerolog.New(&logs)}
			if tt.missingPolicy {
				orchestrator.automationRuns = nil
			}
			err := orchestrator.appendAutomationPublicationIntentPrompt(ctx, input, session)
			require.NoError(t, err, "optional metadata guidance must not fail the agent turn when its policy lookup fails")
			if tt.policyErr != nil || tt.missingPolicy {
				require.Contains(t, logs.String(), `"level":"warn"`, "omitting optional guidance must leave a diagnostic warning")
			}
			expected := "existing skills"
			if tt.wantPrompt {
				expected += "\n" + prompts.AutomationPublicationIntent()
				require.Equal(t, []uuid.UUID{session.OrgID, *session.AutomationRunID}, policyScope, "guidance must resolve frozen policy through the exact tenant-scoped automation link")
			}
			require.Equal(t, expected, input.IntegrationSkills, "only authorized automatic publishers should receive metadata-only guidance")
		})
	}
}
