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

	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err, "test fixture timezone should load")

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
			name:     "hourly aligns minute and counts the interval in occurrences",
			from:     time.Date(2026, 3, 10, 8, 50, 0, 0, time.UTC),
			interval: 3,
			unit:     "hours",
			runAt:    "11:15",
			tz:       "UTC",
			expected: time.Date(2026, 3, 10, 11, 15, 0, 0, time.UTC),
		},
		{
			name:     "hourly schedule that just fired waits one hour, not two",
			from:     time.Date(2026, 3, 10, 8, 15, 3, 0, time.UTC),
			interval: 1,
			unit:     "hours",
			runAt:    "00:15",
			tz:       "UTC",
			expected: time.Date(2026, 3, 10, 9, 15, 0, 0, time.UTC),
		},
		{
			name:     "daily fires at the next wall-clock occurrence",
			from:     time.Date(2026, 3, 10, 21, 50, 0, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "09:35",
			tz:       "UTC",
			expected: time.Date(2026, 3, 11, 9, 35, 0, 0, time.UTC),
		},
		{
			name:     "daily fires again tomorrow when advanced from just after its fire time",
			from:     time.Date(2026, 10, 19, 8, 0, 3, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "04:00",
			tz:       "America/New_York",
			expected: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC),
		},
		{
			name:     "daily advanced from exactly its fire time moves to the next day",
			from:     time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "04:00",
			tz:       "America/New_York",
			expected: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC),
		},
		{
			// Resumed at 19:34 New York on Oct 18: the next 04:00 is Oct 19,
			// not Oct 20.
			name:     "daily resumed in the evening fires the next morning",
			from:     time.Date(2026, 10, 18, 23, 34, 57, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "04:00",
			tz:       "America/New_York",
			expected: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC),
		},
		{
			name:     "every three days lands on the third occurrence",
			from:     time.Date(2026, 3, 10, 21, 50, 0, 0, time.UTC),
			interval: 3,
			unit:     "days",
			runAt:    "09:35",
			tz:       "UTC",
			expected: time.Date(2026, 3, 13, 9, 35, 0, 0, time.UTC),
		},
		{
			name:     "every three days keeps a three day cadence after firing",
			from:     time.Date(2026, 3, 13, 9, 35, 2, 0, time.UTC),
			interval: 3,
			unit:     "days",
			runAt:    "09:35",
			tz:       "UTC",
			expected: time.Date(2026, 3, 16, 9, 35, 0, 0, time.UTC),
		},
		{
			name:     "weekly created after the run time lands on the same weekday next week",
			from:     time.Date(2026, 3, 10, 10, 10, 0, 0, time.UTC),
			interval: 1,
			unit:     "weeks",
			runAt:    "09:00",
			tz:       "UTC",
			expected: time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC),
		},
		{
			name:     "weekly that just fired waits exactly seven days",
			from:     time.Date(2026, 3, 10, 9, 0, 3, 0, time.UTC),
			interval: 1,
			unit:     "weeks",
			runAt:    "09:00",
			tz:       "UTC",
			expected: time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC),
		},
		{
			name:     "biweekly that just fired waits exactly fourteen days",
			from:     time.Date(2026, 3, 10, 9, 0, 3, 0, time.UTC),
			interval: 2,
			unit:     "weeks",
			runAt:    "09:00",
			tz:       "UTC",
			expected: time.Date(2026, 3, 24, 9, 0, 0, 0, time.UTC),
		},
		{
			// Daily 09:00 in America/New_York (EDT = UTC-4 on 2026-03-11) —
			// the stored UTC fire time must be 13:00.
			name:     "daily in non-UTC zone resolves to zone-local wall clock",
			from:     time.Date(2026, 3, 10, 21, 0, 0, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "09:00",
			tz:       "America/New_York",
			expected: time.Date(2026, 3, 12, 13, 0, 0, 0, time.UTC).Add(-24 * time.Hour),
		},
		{
			// Clocks fall back on 2026-11-01: 04:00 EDT (08:00 UTC) on Oct 31
			// is followed by 04:00 EST (09:00 UTC) on Nov 1.
			name:     "daily keeps local wall clock across the DST fall back",
			from:     time.Date(2026, 10, 31, 8, 0, 3, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "04:00",
			tz:       "America/New_York",
			expected: time.Date(2026, 11, 1, 4, 0, 0, 0, newYork),
		},
		{
			// Clocks spring forward on 2026-03-08: 04:00 EST (09:00 UTC) on
			// Mar 7 is followed by 04:00 EDT (08:00 UTC) on Mar 8.
			name:     "daily keeps local wall clock across the DST spring forward",
			from:     time.Date(2026, 3, 7, 9, 0, 3, 0, time.UTC),
			interval: 1,
			unit:     "days",
			runAt:    "04:00",
			tz:       "America/New_York",
			expected: time.Date(2026, 3, 8, 4, 0, 0, 0, newYork),
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
			expected: time.Date(2026, 3, 11, 9, 35, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NextRunTimeAt(tt.from, tt.interval, tt.unit, tt.runAt, tt.tz)
			require.NoError(t, err, "NextRunTimeAt should parse runAt and compute the next aligned time")
			require.True(t, tt.expected.Equal(got), "NextRunTimeAt should return the expected aligned timestamp; got %s want %s", got, tt.expected)
			require.True(t, got.After(tt.from), "NextRunTimeAt should always return a time strictly after from; got %s from %s", got, tt.from)
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

	t.Run("interval below one returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NextRunTimeAt(time.Now().UTC(), 0, "days", "09:00", "UTC")
		require.Error(t, err, "NextRunTimeAt should reject a zero interval instead of scheduling in the past")
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
