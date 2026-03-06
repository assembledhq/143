package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    ProjectStatus
		expectErr bool
	}{
		{name: "proposed is valid", status: ProjectStatusProposed, expectErr: false},
		{name: "draft is valid", status: ProjectStatusDraft, expectErr: false},
		{name: "planning is valid", status: ProjectStatusPlanning, expectErr: false},
		{name: "active is valid", status: ProjectStatusActive, expectErr: false},
		{name: "paused is valid", status: ProjectStatusPaused, expectErr: false},
		{name: "completed is valid", status: ProjectStatusCompleted, expectErr: false},
		{name: "cancelled is valid", status: ProjectStatusCancelled, expectErr: false},
		{name: "empty string is invalid", status: "", expectErr: true},
		{name: "unknown value is invalid", status: "unknown", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should return an error for invalid status")
			} else {
				require.NoError(t, err, "Validate should not return an error for valid status")
			}
		})
	}
}

func TestProjectExecMode_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mode      ProjectExecMode
		expectErr bool
	}{
		{name: "sequential is valid", mode: ProjectExecModeSequential, expectErr: false},
		{name: "parallel is valid", mode: ProjectExecModeParallel, expectErr: false},
		{name: "dependency_graph is valid", mode: ProjectExecModeDependencyGraph, expectErr: false},
		{name: "empty string is invalid", mode: "", expectErr: true},
		{name: "unknown value is invalid", mode: "fifo", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.mode.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should return an error for invalid mode")
			} else {
				require.NoError(t, err, "Validate should not return an error for valid mode")
			}
		})
	}
}

func TestProjectTaskStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    ProjectTaskStatus
		expectErr bool
	}{
		{name: "pending is valid", status: ProjectTaskStatusPending, expectErr: false},
		{name: "blocked is valid", status: ProjectTaskStatusBlocked, expectErr: false},
		{name: "delegated is valid", status: ProjectTaskStatusDelegated, expectErr: false},
		{name: "running is valid", status: ProjectTaskStatusRunning, expectErr: false},
		{name: "completed is valid", status: ProjectTaskStatusCompleted, expectErr: false},
		{name: "failed is valid", status: ProjectTaskStatusFailed, expectErr: false},
		{name: "skipped is valid", status: ProjectTaskStatusSkipped, expectErr: false},
		{name: "cancelled is valid", status: ProjectTaskStatusCancelled, expectErr: false},
		{name: "empty string is invalid", status: "", expectErr: true},
		{name: "unknown value is invalid", status: "paused", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should return an error for invalid task status")
			} else {
				require.NoError(t, err, "Validate should not return an error for valid task status")
			}
		})
	}
}
