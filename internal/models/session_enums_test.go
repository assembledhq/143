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
