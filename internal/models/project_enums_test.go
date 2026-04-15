package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProjectStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    ProjectStatus
		expectErr bool
	}{
		{name: "draft is valid", status: ProjectStatusDraft, expectErr: false},
		{name: "active is valid", status: ProjectStatusActive, expectErr: false},
		{name: "completed is valid", status: ProjectStatusCompleted, expectErr: false},
		{name: "empty string is invalid", status: "", expectErr: true},
		{name: "unknown value is invalid", status: "unknown", expectErr: true},
		{name: "proposed is invalid", status: "proposed", expectErr: true},
		{name: "paused is invalid", status: "paused", expectErr: true},
		{name: "cancelled is invalid", status: "cancelled", expectErr: true},
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

func TestScheduleUnit_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		unit      ScheduleUnit
		expectErr bool
	}{
		{name: "hours is valid", unit: ScheduleUnitHours, expectErr: false},
		{name: "days is valid", unit: ScheduleUnitDays, expectErr: false},
		{name: "weeks is valid", unit: ScheduleUnitWeeks, expectErr: false},
		{name: "empty string is invalid", unit: "", expectErr: true},
		{name: "months is invalid", unit: "months", expectErr: true},
		{name: "minutes is invalid", unit: "minutes", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.unit.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should return an error for invalid unit")
			} else {
				require.NoError(t, err, "Validate should not return an error for valid unit")
			}
		})
	}
}

func TestNextRunTime(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		interval int
		unit     string
		expected time.Time
	}{
		{name: "1 hour", interval: 1, unit: "hours", expected: base.Add(1 * time.Hour)},
		{name: "6 hours", interval: 6, unit: "hours", expected: base.Add(6 * time.Hour)},
		{name: "1 day", interval: 1, unit: "days", expected: base.AddDate(0, 0, 1)},
		{name: "3 days", interval: 3, unit: "days", expected: base.AddDate(0, 0, 3)},
		{name: "1 week", interval: 1, unit: "weeks", expected: base.AddDate(0, 0, 7)},
		{name: "2 weeks", interval: 2, unit: "weeks", expected: base.AddDate(0, 0, 14)},
		{name: "unknown unit defaults to days", interval: 1, unit: "unknown", expected: base.AddDate(0, 0, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := NextRunTime(base, tt.interval, tt.unit)
			require.Equal(t, tt.expected, result)
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
