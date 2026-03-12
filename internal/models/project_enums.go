package models

import (
	"fmt"
	"time"
)

// ProjectStatus represents the lifecycle state of a project.
type ProjectStatus string

const (
	ProjectStatusProposed  ProjectStatus = "proposed"
	ProjectStatusDraft     ProjectStatus = "draft"
	ProjectStatusPlanning  ProjectStatus = "planning"
	ProjectStatusActive    ProjectStatus = "active"
	ProjectStatusPaused    ProjectStatus = "paused"
	ProjectStatusCompleted ProjectStatus = "completed"
	ProjectStatusCancelled ProjectStatus = "cancelled"
)

func (s ProjectStatus) Validate() error {
	switch s {
	case ProjectStatusProposed, ProjectStatusDraft, ProjectStatusPlanning,
		ProjectStatusActive, ProjectStatusPaused, ProjectStatusCompleted,
		ProjectStatusCancelled:
		return nil
	default:
		return fmt.Errorf("invalid ProjectStatus: %q", s)
	}
}

// ProjectExecMode controls how tasks within a project are dispatched.
type ProjectExecMode string

const (
	ProjectExecModeSequential     ProjectExecMode = "sequential"
	ProjectExecModeParallel       ProjectExecMode = "parallel"
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
