package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestRunAgentIncludesAutomationReviewerMetadataGuidance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		origin          models.SessionOrigin
		continueSession bool
	}{
		{name: "initial automation", origin: models.SessionOriginAutomation},
		{name: "initial linked legacy session", origin: models.SessionOriginIssueTrigger},
		{name: "continued linked legacy session", origin: models.SessionOriginIssueTrigger, continueSession: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := testOrg()
			issue := testIssue(orgID)
			run := testRun(orgID, issue.ID)
			runID := uuid.New()
			run.Origin = tt.origin
			run.AutomationRunID = &runID
			run.CapabilitySnapshot = []models.AgentCapabilitySnapshotItem{{ID: models.AgentCapabilityPublishing, AccessLevel: models.AgentCapabilityAccessWrite}}
			d := defaultDeps()
			d.issues.issue = issue
			d.automationRuns = &mockAutomationRunUpdater{policy: models.AutomationPublishPolicyPullRequest}
			if tt.continueSession {
				run.Status = models.SessionStatusIdle
				run.CurrentTurn = 1
				run.SnapshotKey = nil
				d.messages.messages = []models.SessionMessage{{ID: 1, SessionID: run.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "Continue the implementation."}}
			}
			prepared, executed := false, false
			d.adapter.prepareFn = func(_ context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
				prepared = true
				require.Contains(t, input.IntegrationSkills, prompts.AutomationPublicationIntent(), "initial automatic publisher must receive the reviewer metadata handoff despite being excluded from ordinary PR handoff")
				return &agent.AgentPrompt{SystemPrompt: input.IntegrationSkills}, nil
			}
			d.adapter.executeFn = func(_ context.Context, _ *agent.Sandbox, prompt *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executed = true
				require.Contains(t, prompt.SystemPrompt, "awaiting_automatic_publication", "executed agent prompt must explain staging does not publish early")
				return &agent.AgentResult{Summary: "Completed", ExitCode: 0}, nil
			}
			orchestrator := buildOrchestrator(d)
			var err error
			if tt.continueSession {
				err = orchestrator.ContinueSession(context.Background(), run, nil)
			} else {
				err = orchestrator.RunAgent(context.Background(), run)
			}
			require.NoError(t, err, "linked automatic publication turns should execute with metadata guidance")
			require.True(t, prepared && executed, "regression should exercise preparation and actual execution paths")
		})
	}
}

func TestAgentTurnsTolerateUnavailableAutomationReviewerGuidance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		continueSession bool
	}{
		{name: "initial execution"},
		{name: "continued execution", continueSession: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := testOrg()
			issue := testIssue(orgID)
			issue.Source = models.IssueSourceManual
			session := testRun(orgID, issue.ID)
			runID := uuid.New()
			session.Origin = models.SessionOriginAutomation
			session.AutomationRunID = &runID
			session.CapabilitySnapshot = []models.AgentCapabilitySnapshotItem{{ID: models.AgentCapabilityPublishing, AccessLevel: models.AgentCapabilityAccessWrite}}
			d := defaultDeps()
			d.issues.issue = issue
			d.automationRuns = &mockAutomationRunUpdater{policyErr: errors.New("temporary policy lookup failure")}
			if tt.continueSession {
				session.Status = models.SessionStatusIdle
				session.CurrentTurn = 1
				session.SnapshotKey = nil
				d.messages.messages = []models.SessionMessage{{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "Continue the implementation."}}
			}
			prepared, executed := false, false
			d.adapter.prepareFn = func(_ context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
				prepared = true
				require.NotContains(t, input.IntegrationSkills, prompts.AutomationPublicationIntent(), "failed optional lookup must omit metadata guidance")
				return &agent.AgentPrompt{SystemPrompt: input.IntegrationSkills}, nil
			}
			d.adapter.executeFn = func(_ context.Context, _ *agent.Sandbox, prompt *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executed = true
				require.NotContains(t, prompt.SystemPrompt, prompts.AutomationPublicationIntent(), "agent execution must continue without unavailable optional guidance")
				return &agent.AgentResult{Summary: "Completed", ExitCode: 0}, nil
			}
			orchestrator := buildOrchestrator(d)
			var err error
			if tt.continueSession {
				err = orchestrator.ContinueSession(context.Background(), session, nil)
			} else {
				err = orchestrator.RunAgent(context.Background(), session)
			}
			require.NoError(t, err, "optional reviewer hint lookup must not fail initial or continued execution")
			require.True(t, prepared && executed, "regression must reach both prompt preparation and execution")
		})
	}
}
