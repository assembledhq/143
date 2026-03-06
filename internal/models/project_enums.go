package models

import "fmt"

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
