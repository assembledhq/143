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

func TestNextRunTimeAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     time.Time
		interval int
		unit     string
		runAt    string
		tz       string
		expected time.Time
	}{
		{
			name:     "hourly aligns minute",
			from:     time.Date(2026, 3, 10, 8, 50, 0, 0, time.UTC),
			interval: 3,
			unit:     "hours",
			runAt:    "11:15",
			tz:       "UTC",
			expected: time.Date(2026, 3, 10, 12, 15, 0, 0, time.UTC),
		},
		{
			name:     "daily uses exact time",
			from:     time.Date(2026, 3, 10, 21, 50, 0, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "09:35",
			tz:       "UTC",
			expected: time.Date(2026, 3, 12, 9, 35, 0, 0, time.UTC),
		},
		{
			name:     "weekly rolls one more day when base is later",
			from:     time.Date(2026, 3, 10, 10, 10, 0, 0, time.UTC),
			interval: 1,
			unit:     "weeks",
			runAt:    "09:00",
			tz:       "UTC",
			expected: time.Date(2026, 3, 18, 9, 0, 0, 0, time.UTC),
		},
		{
			// Daily 09:00 in America/New_York (EDT = UTC-4 on 2026-03-13) —
			// the stored UTC fire time must be 13:00.
			name:     "daily in non-UTC zone resolves to zone-local wall clock",
			from:     time.Date(2026, 3, 10, 21, 0, 0, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "09:00",
			tz:       "America/New_York",
			expected: time.Date(2026, 3, 12, 13, 0, 0, 0, time.UTC),
		},
		{
			// Empty timezone falls back to UTC for legacy callers that haven't
			// yet migrated to passing the field.
			name:     "empty timezone falls back to UTC",
			from:     time.Date(2026, 3, 10, 21, 50, 0, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "09:35",
			tz:       "",
			expected: time.Date(2026, 3, 12, 9, 35, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NextRunTimeAt(tt.from, tt.interval, tt.unit, tt.runAt, tt.tz)
			require.NoError(t, err, "NextRunTimeAt should parse runAt and compute the next aligned time")
			require.True(t, tt.expected.Equal(got), "NextRunTimeAt should return the expected aligned timestamp; got %s want %s", got, tt.expected)
		})
	}

	t.Run("invalid runAt format returns parse error", func(t *testing.T) {
		t.Parallel()
		_, err := NextRunTimeAt(time.Now().UTC(), 1, "days", "ab:cd", "UTC")
		require.Error(t, err, "NextRunTimeAt should fail when runAt cannot be parsed")
	})

	t.Run("invalid timezone returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NextRunTimeAt(time.Now().UTC(), 1, "days", "09:00", "Not/AZone")
		require.Error(t, err, "NextRunTimeAt should fail when timezone cannot be loaded")
	})
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
