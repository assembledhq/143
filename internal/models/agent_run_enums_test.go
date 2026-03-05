package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentRunStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   AgentRunStatus
		wantErr bool
	}{
		{name: "pending is valid", value: AgentRunStatusPending, wantErr: false},
		{name: "running is valid", value: AgentRunStatusRunning, wantErr: false},
		{name: "awaiting_input is valid", value: AgentRunStatusAwaitingInput, wantErr: false},
		{name: "needs_human_guidance is valid", value: AgentRunStatusNeedsHumanGuidance, wantErr: false},
		{name: "completed is valid", value: AgentRunStatusCompleted, wantErr: false},
		{name: "pr_created is valid", value: AgentRunStatusPRCreated, wantErr: false},
		{name: "failed is valid", value: AgentRunStatusFailed, wantErr: false},
		{name: "cancelled is valid", value: AgentRunStatusCancelled, wantErr: false},
		{name: "skipped is valid", value: AgentRunStatusSkipped, wantErr: false},
		{name: "empty is invalid", value: "", wantErr: true},
		{name: "unknown is invalid", value: "unknown", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return an error for %q", tt.value)
			} else {
				require.NoError(t, err, "Validate should not return an error for %q", tt.value)
			}
		})
	}
}
