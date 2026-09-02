package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type codeReviewRankedAvailabilityStub struct {
	unavailable map[string]bool
	checked     []string
}

func (s *codeReviewRankedAvailabilityStub) IsAgentAvailable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, agent models.AgentType, model string) (bool, error) {
	key := fmt.Sprintf("%s:%s", agent, model)
	s.checked = append(s.checked, key)
	return !s.unavailable[key], nil
}

func codeReviewRankedConfigForTest() models.CodeReviewPolicyConfig {
	cfg := models.DefaultCodeReviewPolicyConfig()
	cfg.AgentRoster.ReviewerCount = 2
	cfg.AgentRoster.RequireReviewerQuorum = 2
	cfg.AgentRoster.Reviewers = []models.AgentType{models.AgentTypeOpenCode, models.AgentTypeCodex, models.AgentTypeClaudeCode, models.AgentTypeOpenCode}
	cfg.AgentRoster.ReviewerModels = []string{models.OpenCodeModelGPT55, models.DefaultCodexModel, models.DefaultClaudeCodeModel, "glm-5.2"}
	cfg.AgentRoster.ReviewerReasoningEfforts = []models.ReasoningEffort{models.ReasoningEffortHigh, models.ReasoningEffortXHigh, models.ReasoningEffortMax, models.ReasoningEffortLow}
	return cfg
}

type codeReviewRankedAttemptForTest struct {
	index        int
	status       models.CodeReviewAgentResultStatus
	unavailable  bool
	unusable     bool
	findingCount int
}

func codeReviewRankedResultsForTest(cfg models.CodeReviewPolicyConfig, attempts []codeReviewRankedAttemptForTest) ([]models.CodeReviewAgentResult, []models.SessionThread) {
	results := make([]models.CodeReviewAgentResult, 0, len(attempts))
	threads := make([]models.SessionThread, 0, len(attempts))
	for _, attempt := range attempts {
		threadID := uuid.New()
		agentType := cfg.AgentRoster.Reviewers[attempt.index]
		state := codeReviewReviewerStructuredResult{
			ReviewerKey:       codeReviewReviewerKey(attempt.index, agentType),
			ReviewerIndex:     attempt.index,
			ThreadID:          threadID.String(),
			ReadOnly:          true,
			ReadOnlyViolation: attempt.unusable,
			Unavailable:       attempt.unavailable,
			FindingCount:      attempt.findingCount,
		}
		if attempt.unusable || attempt.status == models.CodeReviewAgentResultStatusFailed {
			state.Error = "reviewer did not produce usable output"
		}
		threadStatus := models.ThreadStatusRunning
		if codeReviewReviewerResultTerminal(attempt.status) {
			state.CompletedAt = time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
			threadStatus = models.ThreadStatusCompleted
		}
		results = append(results, models.CodeReviewAgentResult{
			ID: uuid.New(), AgentProvider: string(agentType), AgentModel: codeReviewReviewerAgentModel(cfg, attempt.index, agentType),
			Role: models.CodeReviewAgentRoleReviewer, Status: attempt.status,
			StructuredResult: marshalCodeReviewReviewerStructuredResult(state),
		})
		threads = append(threads, models.SessionThread{ID: threadID, Status: threadStatus})
	}
	return results, threads
}

func TestCodeReviewRankedReviewerScheduling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		attempts         []codeReviewRankedAttemptForTest
		unavailable      []int
		expected         []codeReviewReviewerSelection
		expectedChecks   []int
		expectedTerminal bool
		expectedPhase    codeReviewAgentPhase
	}{
		{
			name: "starts only the first two models",
			expected: []codeReviewReviewerSelection{
				{Index: 0, AgentType: models.AgentTypeOpenCode, Available: true},
				{Index: 1, AgentType: models.AgentTypeCodex, Available: true},
			},
			expectedChecks: []int{0, 1},
		},
		{
			name:        "skips unavailable first choice without consuming a reviewer slot",
			unavailable: []int{0},
			expected: []codeReviewReviewerSelection{
				{Index: 0, AgentType: models.AgentTypeOpenCode, Available: false},
				{Index: 1, AgentType: models.AgentTypeCodex, Available: true},
				{Index: 2, AgentType: models.AgentTypeClaudeCode, Available: true},
			},
			expectedChecks: []int{0, 1, 2},
		},
		{
			name: "does not launch fallbacks while the requested reviewers are running",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusRunning},
				{index: 1, status: models.CodeReviewAgentResultStatusQueued},
			},
			expected:      []codeReviewReviewerSelection{},
			expectedPhase: codeReviewAgentPhaseReviewers,
		},
		{
			name: "completed reviews with findings still occupy their slots",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusCompleted, findingCount: 1},
				{index: 1, status: models.CodeReviewAgentResultStatusRunning},
			},
			expected:      []codeReviewReviewerSelection{},
			expectedPhase: codeReviewAgentPhaseReviewers,
		},
		{
			name: "replaces a failed model without repeating the completed review",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusFailed},
				{index: 1, status: models.CodeReviewAgentResultStatusCompleted},
			},
			expected:       []codeReviewReviewerSelection{{Index: 2, AgentType: models.AgentTypeClaudeCode, Available: true}},
			expectedChecks: []int{2},
		},
		{
			name: "replaces a terminal attempt without usable output",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusCompleted, unusable: true},
				{index: 1, status: models.CodeReviewAgentResultStatusRunning},
			},
			expected:       []codeReviewReviewerSelection{{Index: 2, AgentType: models.AgentTypeClaudeCode, Available: true}},
			expectedChecks: []int{2},
		},
		{
			name: "moves on after a failed fallback without duplicating the running attempt",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusFailed},
				{index: 1, status: models.CodeReviewAgentResultStatusRunning},
				{index: 2, status: models.CodeReviewAgentResultStatusFailed},
			},
			expected:       []codeReviewReviewerSelection{{Index: 3, AgentType: models.AgentTypeOpenCode, Available: true}},
			expectedChecks: []int{3},
		},
		{
			name: "finishes when enough reviews complete and leaves unused spares idle",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusFailed},
				{index: 1, status: models.CodeReviewAgentResultStatusCompleted},
				{index: 2, status: models.CodeReviewAgentResultStatusCompleted},
			},
			expected:         []codeReviewReviewerSelection{},
			expectedTerminal: true,
		},
		{
			name: "exhausts the ranked pool without retrying failed models",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusFailed},
				{index: 1, status: models.CodeReviewAgentResultStatusFailed},
				{index: 2, status: models.CodeReviewAgentResultStatusFailed},
				{index: 3, status: models.CodeReviewAgentResultStatusFailed},
			},
			expected:         []codeReviewReviewerSelection{},
			expectedTerminal: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := codeReviewRankedConfigForTest()
			results, threads := codeReviewRankedResultsForTest(cfg, tt.attempts)
			availability := &codeReviewRankedAvailabilityStub{unavailable: make(map[string]bool)}
			modelKey := func(index int) string {
				return fmt.Sprintf("%s:%s", cfg.AgentRoster.Reviewers[index], cfg.AgentRoster.ReviewerModels[index])
			}
			for _, index := range tt.unavailable {
				availability.unavailable[modelKey(index)] = true
			}
			var expectedChecks []string
			for _, index := range tt.expectedChecks {
				expectedChecks = append(expectedChecks, modelKey(index))
			}
			selections, err := resolveCodeReviewReviewerAvailability(context.Background(), &Services{CodingAgents: availability}, uuid.New(), cfg, results, false)
			require.NoError(t, err, "ranked reviewer selection should resolve the remaining slots")
			require.Equal(t, tt.expected, selections, "only the next needed ranked choices should be selected")
			require.Equal(t, expectedChecks, availability.checked, "used and unnecessary models should not trigger new credential lookups")
			require.Equal(t, tt.expectedTerminal, codeReviewReviewerRosterTerminal(cfg, results, false), "the phase should finish only after the requested reviews complete or the ranked pool is exhausted")
			require.Equal(t, tt.expectedPhase, codeReviewInFlightAgentPhaseFromState(cfg, results, threads), "unused fallbacks should not disrupt waiting for running reviewers, but a missing replacement must permit dispatch")
		})
	}
}

func TestCodeReviewRankedReviewerQuorum(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		attempts []codeReviewRankedAttemptForTest
		expected struct{ required, completed, failures int }
	}{
		{
			name: "available fallback keeps the configured quorum after an authentication skip",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusFailed, unavailable: true},
				{index: 1, status: models.CodeReviewAgentResultStatusCompleted},
				{index: 2, status: models.CodeReviewAgentResultStatusCompleted},
			},
			expected: struct{ required, completed, failures int }{2, 2, 1},
		},
		{
			name: "capacity failures never lower required quorum",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusFailed, unavailable: true},
				{index: 1, status: models.CodeReviewAgentResultStatusFailed},
				{index: 2, status: models.CodeReviewAgentResultStatusFailed, unavailable: true},
				{index: 3, status: models.CodeReviewAgentResultStatusCompleted},
			},
			expected: struct{ required, completed, failures int }{2, 1, 3},
		},
		{
			name: "legacy quorum clamp applies only after authenticated choices are exhausted",
			attempts: []codeReviewRankedAttemptForTest{
				{index: 0, status: models.CodeReviewAgentResultStatusFailed, unavailable: true},
				{index: 1, status: models.CodeReviewAgentResultStatusFailed, unavailable: true},
				{index: 2, status: models.CodeReviewAgentResultStatusFailed, unavailable: true},
				{index: 3, status: models.CodeReviewAgentResultStatusCompleted},
			},
			expected: struct{ required, completed, failures int }{1, 1, 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := codeReviewRankedConfigForTest()
			results, _ := codeReviewRankedResultsForTest(cfg, tt.attempts)
			completed, failures := codeReviewReviewerEvidence(results)
			actual := struct{ required, completed, failures int }{codeReviewRequiredReviewerQuorum(cfg, results), completed, failures}
			require.Equal(t, tt.expected, actual, "fallback failures remain evidence but only usable reviews satisfy quorum")
		})
	}
}
