package agent_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestRunAgentIncludesAutomationReviewerMetadataGuidance(t *testing.T) {
	t.Parallel()
	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	runID := uuid.New()
	run.Origin = models.SessionOriginAutomation
	run.AutomationRunID = &runID
	run.CapabilitySnapshot = []models.AgentCapabilitySnapshotItem{{ID: models.AgentCapabilityPublishing, AccessLevel: models.AgentCapabilityAccessWrite}}
	d := defaultDeps()
	d.automationRuns = &mockAutomationRunUpdater{policy: models.AutomationPublishPolicyPullRequest}
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
	require.NoError(t, buildOrchestrator(d).RunAgent(context.Background(), run), "ordinary automatic publication run should complete with metadata guidance")
	require.True(t, prepared && executed, "regression should exercise preparation and actual execution paths")
}
