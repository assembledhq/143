package models

import (
	"fmt"
	"time"
)

// ProjectStatus represents the lifecycle state of a project.
type ProjectStatus string

const (
	ProjectStatusDraft     ProjectStatus = "draft"
	ProjectStatusActive    ProjectStatus = "active"
	ProjectStatusCompleted ProjectStatus = "completed"
)

func (s ProjectStatus) Validate() error {
	switch s {
	case ProjectStatusDraft, ProjectStatusActive, ProjectStatusCompleted:
		return nil
	default:
		return fmt.Errorf("invalid ProjectStatus: %q", s)
	}
}

// ProjectExecMode controls how tasks within a project are dispatched.
type ProjectExecMode string

const (
	ProjectExecModeSequential      ProjectExecMode = "sequential"
	ProjectExecModeParallel        ProjectExecMode = "parallel"
	ProjectExecModeDependencyGraph ProjectExecMode = "dependency_graph"
)

func (m ProjectExecMode) Validate() error {
	switch m {
	case ProjectExecModeSequential, ProjectExecModeParallel, ProjectExecModeDependencyGraph:
		return nil
	default:
		return fmt.Errorf("invalid ProjectExecMode: %q", m)
	}
}

// ProjectTaskStatus represents the state of a task within a project.
type ProjectTaskStatus string

const (
	ProjectTaskStatusPending   ProjectTaskStatus = "pending"
	ProjectTaskStatusBlocked   ProjectTaskStatus = "blocked"
	ProjectTaskStatusDelegated ProjectTaskStatus = "delegated"
	ProjectTaskStatusRunning   ProjectTaskStatus = "running"
	ProjectTaskStatusCompleted ProjectTaskStatus = "completed"
	ProjectTaskStatusFailed    ProjectTaskStatus = "failed"
	ProjectTaskStatusSkipped   ProjectTaskStatus = "skipped"
	ProjectTaskStatusCancelled ProjectTaskStatus = "cancelled"
)

// ScheduleUnit represents the time unit for project scheduling.
type ScheduleUnit string

const (
	ScheduleUnitHours ScheduleUnit = "hours"
	ScheduleUnitDays  ScheduleUnit = "days"
	ScheduleUnitWeeks ScheduleUnit = "weeks"
)

func (u ScheduleUnit) Validate() error {
	switch u {
	case ScheduleUnitHours, ScheduleUnitDays, ScheduleUnitWeeks:
		return nil
	default:
		return fmt.Errorf("invalid ScheduleUnit: %q (must be hours, days, or weeks)", u)
	}
}

// NextRunTime computes the next scheduled run time from a given start time.
func NextRunTime(from time.Time, interval int, unit string) time.Time {
	switch ScheduleUnit(unit) {
	case ScheduleUnitHours:
		return from.Add(time.Duration(interval) * time.Hour)
	case ScheduleUnitWeeks:
		return from.AddDate(0, 0, interval*7)
	default: // days
		return from.AddDate(0, 0, interval)
	}
}

// NextRunTimeAt computes the next run at a specific HH:MM wall-clock time in
// the given IANA timezone, expressed in UTC. The result is the interval-th
// occurrence of runAt strictly after from, so "every day at 04:00" fires at
// the next 04:00 after from, and a schedule that just fired at 04:00 fires
// again exactly one interval later. The scheduler advances from the fire time
// (always a little after next_run_at) and the API advances from "now" on
// create, update, and resume; requiring a full interval to elapse before the
// first HH:MM made both skip an occurrence (daily ran every other day).
// Timezone is required so that a "daily at 9 AM" schedule fires at 9 AM local
// even as DST shifts the UTC offset — doing the arithmetic in the target
// location is what makes that invariant hold. An empty timezone is treated as
// UTC for backwards compatibility with pre-migration-93 interval rows.
func NextRunTimeAt(from time.Time, interval int, unit string, runAt string, timezone string) (time.Time, error) {
	if interval < 1 {
		return time.Time{}, fmt.Errorf("interval must be at least 1, got %d", interval)
	}
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	parsed, err := time.Parse("15:04", runAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse runAt %q: %w", runAt, err)
	}

	// Advance by all but the last step of the interval, then roll forward to
	// the first runAt occurrence strictly after that point. Weeks are seven
	// daily steps so a weekly schedule lands on the same weekday.
	var floor time.Time
	switch ScheduleUnit(unit) {
	case ScheduleUnitHours:
		floor = from.Add(time.Duration(interval-1) * time.Hour).In(loc)
	case ScheduleUnitWeeks:
		floor = from.AddDate(0, 0, interval*7-1).In(loc)
	default: // days
		floor = from.AddDate(0, 0, interval-1).In(loc)
	}

	switch ScheduleUnit(unit) {
	case ScheduleUnitHours:
		candidate := time.Date(floor.Year(), floor.Month(), floor.Day(), floor.Hour(), parsed.Minute(), 0, 0, loc)
		if !candidate.After(floor) {
			candidate = candidate.Add(time.Hour)
		}
		return candidate.UTC(), nil
	default:
		candidate := time.Date(floor.Year(), floor.Month(), floor.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
		if !candidate.After(floor) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate.UTC(), nil
	}
}

func (s ProjectTaskStatus) Validate() error {
	switch s {
	case ProjectTaskStatusPending, ProjectTaskStatusBlocked, ProjectTaskStatusDelegated,
		ProjectTaskStatusRunning, ProjectTaskStatusCompleted, ProjectTaskStatusFailed,
		ProjectTaskStatusSkipped, ProjectTaskStatusCancelled:
		return nil
	default:
		return fmt.Errorf("invalid ProjectTaskStatus: %q", s)
	}
}
