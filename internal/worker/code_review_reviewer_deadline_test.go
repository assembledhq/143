package worker

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewReviewerDeadlineSelectionAndTerminality(t *testing.T) {
	t.Parallel()

	baseConfig := models.DefaultCodeReviewPolicyConfig()
	baseConfig.AgentRoster.Reviewers = []models.AgentType{
		models.AgentTypeCodex,
		models.AgentTypeClaudeCode,
		models.AgentTypeOpenCode,
	}
	baseConfig.AgentRoster.ReviewerModels = []string{
		models.DefaultCodexModel,
		models.DefaultClaudeCodeModel,
		models.OpenCodeModelGPT55,
	}
	baseConfig.AgentRoster.ReviewerReasoningEfforts = []models.ReasoningEffort{
		models.ReasoningEffortHigh,
		models.ReasoningEffortHigh,
		models.ReasoningEffortHigh,
	}
	baseConfig.AgentRoster.TimeoutSeconds = 1800

	createdAt := time.Date(2026, time.September, 2, 14, 0, 0, 0, time.UTC)
	completedAt := createdAt.Add(10 * time.Minute)
	lateCompletion := createdAt.Add(40 * time.Minute)
	orgID := uuid.New()

	tests := []struct {
		name                     string
		reviewerCount            int
		results                  []models.CodeReviewAgentResult
		expectedSelections       []codeReviewReviewerSelection
		expectedTerminalTimedOut bool
		expectedTerminalInFlight bool
		expectedDispatchDeadline *time.Time
	}{
		{
			name:          "satisfied quorum skips unused reviewers after the deadline",
			reviewerCount: 1,
			results: []models.CodeReviewAgentResult{
				codeReviewReviewerResultForDeadlineTest(0, models.AgentTypeCodex, models.CodeReviewAgentResultStatusCompleted, completedAt, "", false),
			},
			expectedSelections:       nil,
			expectedTerminalTimedOut: true,
			expectedTerminalInFlight: true,
			expectedDispatchDeadline: timePtr(completedAt.Add(30 * time.Minute)),
		},
		{
			name:          "expired deadline does not start unused fallback while an existing reviewer is still running",
			reviewerCount: 2,
			results: []models.CodeReviewAgentResult{
				codeReviewReviewerResultForDeadlineTest(0, models.AgentTypeCodex, models.CodeReviewAgentResultStatusCompleted, completedAt, "", false),
				codeReviewReviewerResultForDeadlineTest(1, models.AgentTypeClaudeCode, models.CodeReviewAgentResultStatusRunning, time.Time{}, "", false),
			},
			expectedSelections:       nil,
			expectedTerminalTimedOut: false,
			expectedTerminalInFlight: false,
			expectedDispatchDeadline: timePtr(completedAt.Add(30 * time.Minute)),
		},
		{
			name:          "expired deadline finishes once the running reviewer settles without creating another attempt",
			reviewerCount: 2,
			results: []models.CodeReviewAgentResult{
				codeReviewReviewerResultForDeadlineTest(0, models.AgentTypeCodex, models.CodeReviewAgentResultStatusCompleted, completedAt, "", false),
				codeReviewReviewerResultForDeadlineTest(1, models.AgentTypeClaudeCode, models.CodeReviewAgentResultStatusTimedOut, lateCompletion, "reviewer timed out", false),
			},
			expectedSelections:       nil,
			expectedTerminalTimedOut: true,
			expectedTerminalInFlight: false,
			expectedDispatchDeadline: timePtr(lateCompletion.Add(30 * time.Minute)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := baseConfig
			cfg.AgentRoster.ReviewerCount = tt.reviewerCount

			selections, err := resolveCodeReviewReviewerAvailability(context.Background(), &Services{
				CodingAgents: codeReviewAgentAvailabilityStub{available: map[models.AgentType]bool{
					models.AgentTypeCodex:      true,
					models.AgentTypeClaudeCode: true,
					models.AgentTypeOpenCode:   true,
				}},
			}, orgID, cfg, tt.results, true)

			require.NoError(t, err, "deadline-expired reviewer selection should not fail")
			require.Equal(t, tt.expectedSelections, selections, "deadline-expired reviewer selection should not manufacture new reviewer attempts")
			require.Equal(t, tt.expectedTerminalTimedOut, codeReviewReviewerRosterTerminal(cfg, tt.results, true), "timed-out reviewer phase should terminate only after all started reviewers settle")
			require.Equal(t, tt.expectedTerminalInFlight, codeReviewReviewerRosterTerminal(cfg, tt.results, false), "in-flight reviewer phase should keep waiting until quorum or roster exhaustion is proven")
			if tt.expectedDispatchDeadline != nil {
				actualDeadline := codeReviewOrchestratorDispatchDeadline(cfg, models.CodeReviewSessionMetadata{CreatedAt: createdAt}, tt.results)
				require.Equal(t, *tt.expectedDispatchDeadline, actualDeadline, "orchestrator dispatch deadline should stay anchored to persisted reviewer completions")
			}
		})
	}
}

func codeReviewReviewerResultForDeadlineTest(index int, agentType models.AgentType, status models.CodeReviewAgentResultStatus, completedAt time.Time, errMsg string, unusable bool) models.CodeReviewAgentResult {
	state := codeReviewReviewerStructuredResult{
		ReviewerKey:   codeReviewReviewerKey(index, agentType),
		ReviewerIndex: index,
		ReadOnly:      true,
	}
	if !completedAt.IsZero() {
		state.CompletedAt = completedAt.Format(time.RFC3339)
	}
	if unusable {
		state.ReadOnlyViolation = true
		state.Error = firstNonEmpty(errMsg, "no usable output")
	} else if errMsg != "" {
		state.Error = errMsg
	}
	return models.CodeReviewAgentResult{
		Role:             models.CodeReviewAgentRoleReviewer,
		AgentProvider:    string(agentType),
		Status:           status,
		StructuredResult: marshalCodeReviewReviewerStructuredResult(state),
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}
