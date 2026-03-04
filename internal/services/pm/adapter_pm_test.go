package pm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/stretchr/testify/require"
)

type pmInnerAdapterMock struct {
	executeResult *agent.AgentResult
	executeErr    error
	calledPrompt  *agent.AgentPrompt
}

func (m *pmInnerAdapterMock) Name() string {
	return "inner"
}

func (m *pmInnerAdapterMock) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *pmInnerAdapterMock) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	m.calledPrompt = prompt
	if m.executeErr != nil {
		return nil, m.executeErr
	}
	return m.executeResult, nil
}

func TestPMAdapterPreparePrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         *agent.AgentInput
		expectErr     bool
		expectedUser  string
		expectedToken int
	}{
		{
			name:      "returns error when input is nil",
			input:     nil,
			expectErr: true,
		},
		{
			name:          "builds PM prompt from input context",
			input:         &agent.AgentInput{PMContextJSON: `{"open_issues":[]}`},
			expectedUser:  `{"open_issues":[]}`,
			expectedToken: pmMaxTokens,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			adapter := NewPMAdapter(&pmInnerAdapterMock{}, 3, 7)
			prompt, err := adapter.PreparePrompt(context.Background(), tt.input)
			if tt.expectErr {
				require.Error(t, err, "PreparePrompt should return an error for nil input")
				return
			}

			require.NoError(t, err, "PreparePrompt should not return an error for valid input")
			require.Equal(t, tt.expectedUser, prompt.UserPrompt, "PreparePrompt should pass PM context JSON to user prompt")
			require.Equal(t, tt.expectedToken, prompt.MaxTokens, "PreparePrompt should set PM max token limit")
			require.Contains(t, strings.ToLower(prompt.SystemPrompt), "available agent slots", "PreparePrompt should include available slot guidance in PM system prompt")
		})
	}
}

func TestPMAdapterExecuteAndName(t *testing.T) {
	t.Parallel()

	expected := &agent.AgentResult{Summary: "ok"}
	inner := &pmInnerAdapterMock{executeResult: expected}
	adapter := NewPMAdapter(inner, 1, 2)

	result, err := adapter.Execute(context.Background(), &agent.Sandbox{ID: "sb-1"}, &agent.AgentPrompt{UserPrompt: "ctx"}, make(chan agent.LogEntry, 1))
	require.NoError(t, err, "Execute should delegate to inner adapter without error")
	require.Equal(t, expected, result, "Execute should return inner adapter result")
	require.Equal(t, "ctx", inner.calledPrompt.UserPrompt, "Execute should pass prompt through to inner adapter")
	require.Equal(t, "pm_agent", adapter.Name(), "Name should return pm_agent")
}
