package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionExecutorStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  SessionExecutorStatus
		wantErr bool
	}{
		{name: "empty is invalid", status: "", wantErr: true},
		{name: "starting", status: SessionExecutorStatusStarting},
		{name: "running", status: SessionExecutorStatusRunning},
		{name: "draining", status: SessionExecutorStatusDraining},
		{name: "requeued", status: SessionExecutorStatusRequeued},
		{name: "completed", status: SessionExecutorStatusCompleted},
		{name: "failed", status: SessionExecutorStatusFailed},
		{name: "lost", status: SessionExecutorStatusLost},
		{name: "unknown", status: SessionExecutorStatus("bad"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.status.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject invalid executor statuses")
				return
			}
			require.NoError(t, err, "Validate should accept valid executor statuses")
		})
	}
}

func TestJobOwnerKind_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kind    JobOwnerKind
		wantErr bool
	}{
		{name: "empty is invalid", kind: "", wantErr: true},
		{name: "worker", kind: JobOwnerKindWorker},
		{name: "session executor", kind: JobOwnerKindSessionExecutor},
		{name: "unknown", kind: JobOwnerKind("bad"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.kind.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject invalid job owner kinds")
				return
			}
			require.NoError(t, err, "Validate should accept valid job owner kinds")
		})
	}
}
