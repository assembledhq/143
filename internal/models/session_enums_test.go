package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentSessionType_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   AgentSessionType
		wantErr bool
	}{
		{name: "plan is valid", value: AgentSessionTypePlan, wantErr: false},
		{name: "manual is valid", value: AgentSessionTypeManual, wantErr: false},
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

func TestAgentSessionStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   AgentSessionStatus
		wantErr bool
	}{
		{name: "active is valid", value: AgentSessionStatusActive, wantErr: false},
		{name: "completed is valid", value: AgentSessionStatusCompleted, wantErr: false},
		{name: "failed is valid", value: AgentSessionStatusFailed, wantErr: false},
		{name: "empty is invalid", value: "", wantErr: true},
		{name: "unknown is invalid", value: "running", wantErr: true},
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

func TestAgentSessionTriggeredBy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   AgentSessionTriggeredBy
		wantErr bool
	}{
		{name: "scheduled is valid", value: AgentSessionTriggeredByScheduled, wantErr: false},
		{name: "manual is valid", value: AgentSessionTriggeredByManual, wantErr: false},
		{name: "fix_this is valid", value: AgentSessionTriggeredByFixThis, wantErr: false},
		{name: "empty is invalid", value: "", wantErr: true},
		{name: "unknown is invalid", value: "cron", wantErr: true},
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
