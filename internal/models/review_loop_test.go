package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewLoopStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  ReviewLoopStatus
		wantErr bool
	}{
		{name: "running", status: ReviewLoopStatusRunning},
		{name: "clean", status: ReviewLoopStatusClean},
		{name: "needs human decision", status: ReviewLoopStatusNeedsHumanDecision},
		{name: "failed", status: ReviewLoopStatusFailed},
		{name: "cancelled", status: ReviewLoopStatusCancelled},
		{name: "invalid", status: ReviewLoopStatus("bogus"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if tt.wantErr {
				require.Error(t, err, "invalid review loop status should be rejected")
				return
			}
			require.NoError(t, err, "valid review loop status should be accepted")
		})
	}
}

func TestReviewLoopPassStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  ReviewLoopPassStatus
		wantErr bool
	}{
		{name: "reviewing", status: ReviewLoopPassStatusReviewing},
		{name: "deciding", status: ReviewLoopPassStatusDeciding},
		{name: "fixing", status: ReviewLoopPassStatusFixing},
		{name: "clean", status: ReviewLoopPassStatusClean},
		{name: "needs fix", status: ReviewLoopPassStatusNeedsFix},
		{name: "failed", status: ReviewLoopPassStatusFailed},
		{name: "invalid", status: ReviewLoopPassStatus("bogus"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if tt.wantErr {
				require.Error(t, err, "invalid review loop pass status should be rejected")
				return
			}
			require.NoError(t, err, "valid review loop pass status should be accepted")
		})
	}
}

func TestAgentSupportsNativeReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType AgentType
		expected  bool
	}{
		{name: "codex", agentType: AgentTypeCodex, expected: true},
		{name: "claude code", agentType: AgentTypeClaudeCode, expected: true},
		{name: "gemini hidden", agentType: AgentTypeGeminiCLI, expected: false},
		{name: "amp hidden", agentType: AgentTypeAmp, expected: false},
		{name: "pi hidden", agentType: AgentTypePi, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, AgentSupportsNativeReview(tt.agentType), "review support should match the v1 native command policy")
		})
	}
}
