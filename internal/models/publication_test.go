package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionPublicationEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		validate func() error
		wantErr  bool
	}{
		{name: "requested state", validate: SessionPublicationStateRequested.Validate},
		{name: "completed state", validate: SessionPublicationStateCompleted.Validate},
		{name: "invalid state", validate: SessionPublicationState("unknown").Validate, wantErr: true},
		{name: "automation source", validate: SessionPublicationSourceAutomation.Validate},
		{name: "invalid source", validate: SessionPublicationSource("unknown").Validate, wantErr: true},
		{name: "pending review gate", validate: SessionPublicationReviewGatePending.Validate},
		{name: "invalid review gate", validate: SessionPublicationReviewGateState("unknown").Validate, wantErr: true},
		{name: "default job queue", validate: SessionPublicationJobQueueDefault.Validate},
		{name: "agent job queue", validate: SessionPublicationJobQueueAgent.Validate},
		{name: "invalid job queue", validate: SessionPublicationJobQueue("unknown").Validate, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.validate()
			if tt.wantErr {
				require.Error(t, err, "invalid publication enum values should be rejected")
				return
			}
			require.NoError(t, err, "known publication enum values should validate")
		})
	}
}

func TestSessionPublicationStateTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		state    SessionPublicationState
		terminal bool
	}{
		{name: "completed", state: SessionPublicationStateCompleted, terminal: true},
		{name: "completed no-op", state: SessionPublicationStateCompletedNoop, terminal: true},
		{name: "terminal failure", state: SessionPublicationStateTerminalFailed, terminal: true},
		{name: "retryable failure", state: SessionPublicationStateRetryableFailed},
		{name: "branch published", state: SessionPublicationStateBranchPublished},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.terminal, tt.state.Terminal(), "Terminal should classify publication lifecycle states exactly")
		})
	}
}
