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
// the given IANA timezone. The returned time is always >= NextRunTime(from,
// interval, unit) and is expressed in UTC. Timezone is required so that a
// "daily at 9 AM" schedule fires at 9 AM local even as DST shifts the UTC
// offset — doing the arithmetic in the target location is what makes that
// invariant hold. An empty timezone is treated as UTC for backwards
// compatibility with pre-migration-92 interval rows.
func NextRunTimeAt(from time.Time, interval int, unit string, runAt string, timezone string) (time.Time, error) {
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

	minNext := NextRunTime(from, interval, unit).In(loc)

	switch ScheduleUnit(unit) {
	case ScheduleUnitHours:
		candidate := time.Date(minNext.Year(), minNext.Month(), minNext.Day(), minNext.Hour(), parsed.Minute(), 0, 0, loc)
		if candidate.Before(minNext) {
			candidate = candidate.Add(time.Hour)
		}
		return candidate.UTC(), nil
	default:
		candidate := time.Date(minNext.Year(), minNext.Month(), minNext.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
		if candidate.Before(minNext) {
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
