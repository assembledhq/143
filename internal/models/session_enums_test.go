package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   SessionStatus
		wantErr bool
	}{
		{name: "pending is valid", value: SessionStatusPending, wantErr: false},
		{name: "running is valid", value: SessionStatusRunning, wantErr: false},
		{name: "idle is valid", value: SessionStatusIdle, wantErr: false},
		{name: "awaiting_input is valid", value: SessionStatusAwaitingInput, wantErr: false},
		{name: "needs_human_guidance is valid", value: SessionStatusNeedsHumanGuidance, wantErr: false},
		{name: "completed is valid", value: SessionStatusCompleted, wantErr: false},
		{name: "pr_created is valid", value: SessionStatusPRCreated, wantErr: false},
		{name: "failed is valid", value: SessionStatusFailed, wantErr: false},
		{name: "cancelled is valid", value: SessionStatusCancelled, wantErr: false},
		{name: "skipped is valid", value: SessionStatusSkipped, wantErr: false},
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

func TestSandboxState_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   SandboxState
		wantErr bool
	}{
		{name: "none is valid", value: SandboxStateNone, wantErr: false},
		{name: "running is valid", value: SandboxStateRunning, wantErr: false},
		{name: "snapshotted is valid", value: SandboxStateSnapshotted, wantErr: false},
		{name: "destroyed is valid", value: SandboxStateDestroyed, wantErr: false},
		{name: "empty is invalid", value: "", wantErr: true},
		{name: "bogus is invalid", value: "bogus", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMessageRole_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   MessageRole
		wantErr bool
	}{
		{name: "user is valid", value: MessageRoleUser, wantErr: false},
		{name: "assistant is valid", value: MessageRoleAssistant, wantErr: false},
		{name: "empty is invalid", value: "", wantErr: true},
		{name: "system is invalid", value: "system", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
