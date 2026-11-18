package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type retryProgressAdapter struct {
	AgentAdapter
	execute func(chan<- LogEntry) (*AgentResult, error)
}

func (a retryProgressAdapter) Execute(_ context.Context, _ *Sandbox, _ *AgentPrompt, logCh chan<- LogEntry) (*AgentResult, error) {
	return a.execute(logCh)
}

func TestCredentialRetryUpdatesOriginalRuntimeTracker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		retry         string
		entry         LogEntry
		priorTool     bool
		expectedType  models.RuntimeProgressType
		expectedPower models.RuntimeProgressStrength
		toolActive    bool
	}{
		{name: "token refresh assistant output", retry: "token", entry: LogEntry{Level: "output", Message: "Review complete"}, expectedType: models.RuntimeProgressTypeAssistantOutput, expectedPower: models.RuntimeProgressStrengthWeak},
		{name: "token refresh tool start", retry: "token", entry: LogEntry{Level: "tool_use", Metadata: map[string]any{"tool": "command_execution", "item_id": "review-tool", "status": "in_progress"}}, expectedType: models.RuntimeProgressTypeToolUse, expectedPower: models.RuntimeProgressStrengthWeak, toolActive: true},
		{name: "token refresh tool completion", retry: "token", entry: LogEntry{Level: "output", Metadata: map[string]any{"type": "tool_result", "item_id": "review-tool"}}, priorTool: true, expectedType: models.RuntimeProgressTypeToolResult, expectedPower: models.RuntimeProgressStrengthStrong},
		{name: "fallback credential assistant output", retry: "fallback", entry: LogEntry{Level: "output", Message: "Review complete"}, expectedType: models.RuntimeProgressTypeAssistantOutput, expectedPower: models.RuntimeProgressStrengthWeak},
		{name: "fallback credential tool start", retry: "fallback", entry: LogEntry{Level: "tool_use", Metadata: map[string]any{"tool": "command_execution", "item_id": "review-tool", "status": "in_progress"}}, expectedType: models.RuntimeProgressTypeToolUse, expectedPower: models.RuntimeProgressStrengthWeak, toolActive: true},
		{name: "fallback credential tool completion", retry: "fallback", entry: LogEntry{Level: "output", Metadata: map[string]any{"type": "tool_result", "item_id": "review-tool"}}, priorTool: true, expectedType: models.RuntimeProgressTypeToolResult, expectedPower: models.RuntimeProgressStrengthStrong},
		{name: "fallback then token refresh assistant output", retry: "nested", entry: LogEntry{Level: "output", Message: "Review complete"}, expectedType: models.RuntimeProgressTypeAssistantOutput, expectedPower: models.RuntimeProgressStrengthWeak},
		{name: "fallback then token refresh tool start", retry: "nested", entry: LogEntry{Level: "tool_use", Metadata: map[string]any{"tool": "command_execution", "item_id": "review-tool", "status": "in_progress"}}, expectedType: models.RuntimeProgressTypeToolUse, expectedPower: models.RuntimeProgressStrengthWeak, toolActive: true},
		{name: "fallback then token refresh tool completion", retry: "nested", entry: LogEntry{Level: "output", Metadata: map[string]any{"type": "tool_result", "item_id": "review-tool"}}, priorTool: true, expectedType: models.RuntimeProgressTypeToolResult, expectedPower: models.RuntimeProgressStrengthStrong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID, sessionID := uuid.New(), uuid.New()
			observedAt := time.Now().UTC()
			startedAt := observedAt.Add(-20 * time.Minute)
			tracker := newRuntimeProgressTracker(startedAt)
			if tt.priorTool {
				tracker.Record(models.RuntimeProgressTypeToolUse, models.RuntimeProgressStrengthWeak, startedAt, "review-tool")
			}
			provider := &envSandboxProvider{}
			logger := zerolog.Nop()
			env := NewAgentEnv(AgentEnvDeps{
				Credentials: &envCredentialProvider{creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderOpenAI: {Provider: models.ProviderOpenAI, Config: models.OpenAIConfig{APIKey: "synthetic-api-key"}},
				}},
				CodexAuth: &envCodexAuthProvider{token: &models.OpenAISubscriptionConfig{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: observedAt.Add(time.Hour)}},
				Provider:  provider,
				Logger:    logger,
			})
			logs := &testInternalSessionLogStore{}
			orchestrator := &Orchestrator{env: env, provider: provider, agentRunLogs: logs, logger: logger}
			session := &models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeCodex}
			sandbox := &Sandbox{ID: "retry-progress-sandbox", HomeDir: "/home/test", WorkDir: "/workspace"}
			prompt := &AgentPrompt{}
			expectedResult := &AgentResult{Summary: "Review complete"}
			executeCalls := 0
			adapter := retryProgressAdapter{execute: func(logCh chan<- LogEntry) (*AgentResult, error) {
				executeCalls++
				if tt.retry == "nested" && executeCalls == 1 {
					return &AgentResult{Error: "token_expired", ExitCode: 1}, nil
				}
				entry := tt.entry
				entry.Timestamp = observedAt
				logCh <- entry
				return expectedResult, nil
			}}
			// The runtime controller keeps its original deadlines while the same
			// tracker records provider retries and their final output.
			controller := newRuntimeController(runtimeConfig{NoProgressTimeout: 15 * time.Minute}, &runtimeTestSessionStore{}, &runtimeTestJobStore{}, nil, logger, orgID, sessionID, 3, nil, tracker)
			controller.softDeadline = observedAt.Add(time.Minute)
			controller.hardDeadline = observedAt.Add(2 * time.Minute)
			softDeadline, hardDeadline := controller.softDeadline, controller.hardDeadline

			var result *AgentResult
			var err error
			if tt.retry == "token" {
				result, err = orchestrator.retryOnTokenExpired(context.Background(), session.AgentType, orgID, nil, sessionID, TranscriptWriteContext{}, sandbox, adapter, context.Background(), prompt, &AgentResult{Error: "token_expired"}, errors.New("expired authentication"), tracker, logger)
			} else {
				var retried bool
				result, err, retried = orchestrator.retrySessionOnCredentialRateLimit(context.Background(), session, TranscriptWriteContext{}, SandboxConfig{HomeDir: sandbox.HomeDir}, sandbox, adapter, context.Background(), prompt, &AgentResult{Error: "429 too many requests"}, errors.New("rate limited"), false, tracker, logger)
				require.True(t, retried, "a fallback credential should execute another provider attempt")
			}

			require.NoError(t, err, "a successful credential retry should complete normally")
			require.Equal(t, expectedResult, result, "the retry should return the successful provider result")
			expectedCalls := 1
			if tt.retry == "nested" {
				expectedCalls = 2
			}
			require.Equal(t, expectedCalls, executeCalls, "the nested token refresh should preserve the original retry tracker")
			lastProgressAt, lastStrongAt, progressType, strength := tracker.Snapshot()
			require.Equal(t, observedAt, lastProgressAt, "retried output must refresh the existing watchdog's progress timestamp")
			require.Equal(t, tt.expectedType, progressType, "retried logs should retain their exact progress classification")
			require.Equal(t, tt.expectedPower, strength, "retried logs should retain their meaningful-progress strength")
			require.Equal(t, tt.toolActive, tracker.ToolActive(), "retried tool events must maintain the existing watchdog's active-tool state")
			expectedStrongAt := time.Time{}
			if tt.expectedPower == models.RuntimeProgressStrengthStrong {
				expectedStrongAt = observedAt
			}
			require.Equal(t, expectedStrongAt, lastStrongAt, "only meaningful retried output should refresh strong progress")
			controller.tick(context.Background(), observedAt.Add(12*time.Second))
			require.Equal(t, StopReasonNone, controller.stopRequested, "fresh retry output should prevent a false idle stop during result processing")
			require.Equal(t, softDeadline, controller.softDeadline, "a credential retry must not reset the original soft deadline")
			require.Equal(t, hardDeadline, controller.hardDeadline, "a credential retry must not reset the absolute runtime ceiling")
			controller.tick(context.Background(), hardDeadline)
			require.Equal(t, StopReasonAbsoluteCeiling, controller.stopRequested, "retry progress and active tools must still respect the original absolute runtime ceiling")
		})
	}
}
