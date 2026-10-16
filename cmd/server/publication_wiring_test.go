package main

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/publicationintent"
	"github.com/assembledhq/143/internal/worker"
)

type publicationWiringPR struct {
	*ghservice.PRService
	policy ghservice.PublicationExecutionPolicy
}

func (p *publicationWiringPR) SetPublicationExecutionPolicy(policy ghservice.PublicationExecutionPolicy) {
	p.policy = policy
}

type publicationWiringOrchestrator struct {
	*agent.Orchestrator
	coordinator publicationintent.PublicationIntentCoordinator
}

func (o *publicationWiringOrchestrator) SetPublicationIntentCoordinator(coordinator publicationintent.PublicationIntentCoordinator) {
	o.coordinator = coordinator
}

func TestWirePublicationCoordinatorSharesExecutionPolicy(t *testing.T) {
	t.Parallel()

	type executionGate struct {
		Source             models.SessionPublicationSource
		PublicationEnabled bool
		ReviewEnabled      bool
	}
	tests := []struct {
		name                        string
		agentEnabled, reviewEnabled bool
		expected                    []executionGate
	}{
		{
			name:         "publication and review enabled",
			agentEnabled: true, reviewEnabled: true,
			expected: []executionGate{
				{models.SessionPublicationSourceAgentTool, true, true},
				{models.SessionPublicationSourceAutomation, true, true},
				{models.SessionPublicationSourceUser, true, true},
				{models.SessionPublicationSourceBackend, true, true},
			},
		},
		{
			name:          "agent switch preserves automatic and explicit publication",
			reviewEnabled: true,
			expected: []executionGate{
				{models.SessionPublicationSourceAgentTool, false, false},
				{models.SessionPublicationSourceAutomation, true, true},
				{models.SessionPublicationSourceUser, true, true},
				{models.SessionPublicationSourceBackend, true, true},
			},
		},
		{
			name:         "review switch parks every source without disabling publication",
			agentEnabled: true,
			expected: []executionGate{
				{models.SessionPublicationSourceAgentTool, true, false},
				{models.SessionPublicationSourceAutomation, true, false},
				{models.SessionPublicationSourceUser, true, false},
				{models.SessionPublicationSourceBackend, true, false},
			},
		},
		{
			name: "both switches disabled preserve automation publication authority",
			expected: []executionGate{
				{models.SessionPublicationSourceAgentTool, false, false},
				{models.SessionPublicationSourceAutomation, true, false},
				{models.SessionPublicationSourceUser, true, false},
				{models.SessionPublicationSourceBackend, true, false},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pr := &publicationWiringPR{}
			orchestrator := &publicationWiringOrchestrator{}
			services := &worker.Services{PR: pr, Orchestrator: orchestrator}
			cfg := &config.Config{AgentPRPublicationEnabled: tt.agentEnabled, PrePRReviewEnabled: tt.reviewEnabled}

			wirePublicationCoordinator(cfg, services, &worker.Stores{}, zerolog.Nop())

			require.IsType(t, &publicationintent.Coordinator{}, services.PublicationIntents, "both runtimes must receive a functioning publication coordinator")
			require.Same(t, services.PublicationIntents, pr.policy, "PR execution must consult the coordinator assigned to the runtime")
			require.Same(t, services.PublicationIntents, orchestrator.coordinator, "automatic publication must use the same coordinator as PR execution")
			actual := make([]executionGate, 0, len(tt.expected))
			for _, expected := range tt.expected {
				actual = append(actual, executionGate{
					Source:             expected.Source,
					PublicationEnabled: pr.policy.PublicationExecutionEnabled(expected.Source),
					ReviewEnabled:      orchestrator.coordinator.ReviewExecutionEnabled(expected.Source),
				})
			}
			require.Equal(t, tt.expected, actual, "runtime switches must preserve independent automation, explicit user, and backend publication policy")
		})
	}
}
