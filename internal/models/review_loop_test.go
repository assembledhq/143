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

func TestReviewLoopFixModeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    ReviewLoopFixMode
		wantErr bool
	}{
		{name: "minimal", mode: ReviewLoopFixModeMinimal},
		{name: "exhaustive", mode: ReviewLoopFixModeExhaustive},
		{name: "invalid", mode: ReviewLoopFixMode("bogus"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.mode.Validate()
			if tt.wantErr {
				require.Error(t, err, "invalid review loop fix mode should be rejected")
				return
			}
			require.NoError(t, err, "valid review loop fix mode should be accepted")
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
		{name: "amp", agentType: AgentTypeAmp, expected: true},
		{name: "pi", agentType: AgentTypePi, expected: true},
		{name: "opencode", agentType: AgentTypeOpenCode, expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, AgentSupportsNativeReview(tt.agentType), "review support should match the review-loop agent policy")
		})
	}
}
