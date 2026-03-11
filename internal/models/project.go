package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Project is a persistent, goal-oriented container spanning multiple PM cycles.
type Project struct {
	ID                 uuid.UUID        `db:"id" json:"id"`
	OrgID              uuid.UUID        `db:"org_id" json:"org_id"`
	RepositoryID       uuid.UUID        `db:"repository_id" json:"repository_id"`
	Title              string           `db:"title" json:"title"`
	Goal               string           `db:"goal" json:"goal"`
	Scope              *string          `db:"scope" json:"scope,omitempty"`
	CompletionCriteria *string          `db:"completion_criteria" json:"completion_criteria,omitempty"`
	Status             ProjectStatus    `db:"status" json:"status"`
	Priority           int              `db:"priority" json:"priority"`
	ExecutionMode      ProjectExecMode  `db:"execution_mode" json:"execution_mode"`
	MaxConcurrent      int              `db:"max_concurrent" json:"max_concurrent"`
	AutoMerge          bool             `db:"auto_merge" json:"auto_merge"`
	BaseBranch         string           `db:"base_branch" json:"base_branch"`
	CurrentPhase       *string          `db:"current_phase" json:"current_phase,omitempty"`
	LessonsLearned     []string         `json:"lessons_learned,omitempty"`
	ApproachHistory    []ApproachRecord `json:"approach_history,omitempty"`
	TotalTasks         int              `db:"total_tasks" json:"total_tasks"`
	CompletedTasks     int              `db:"completed_tasks" json:"completed_tasks"`
	FailedTasks        int              `db:"failed_tasks" json:"failed_tasks"`
	ProposedByPM       bool             `db:"proposed_by_pm" json:"proposed_by_pm"`
	SourceIssueIDs     []uuid.UUID      `json:"source_issue_ids,omitempty"`
	ProposalReasoning  *string          `db:"proposal_reasoning" json:"proposal_reasoning,omitempty"`
	AgentType          *string          `db:"agent_type" json:"agent_type,omitempty"`
	ModelOverride      *string          `db:"model_override" json:"model_override,omitempty"`
	CreatedBy          *uuid.UUID       `db:"created_by" json:"created_by,omitempty"`
	CreatedAt          time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time        `db:"updated_at" json:"updated_at"`
	CompletedAt        *time.Time       `db:"completed_at" json:"completed_at,omitempty"`

	// Raw JSONB for store-layer scanning.
	LessonsLearnedRaw  json.RawMessage `db:"lessons_learned" json:"-"`
	ApproachHistoryRaw json.RawMessage `db:"approach_history" json:"-"`
}

// ProjectTask is a single work item within a project, created by the PM each cycle.
type ProjectTask struct {
	ID            uuid.UUID         `db:"id" json:"id"`
	ProjectID     uuid.UUID         `db:"project_id" json:"project_id"`
	OrgID         uuid.UUID         `db:"org_id" json:"org_id"`
	Title         string            `db:"title" json:"title"`
	Description   *string           `db:"description" json:"description,omitempty"`
	Approach      *string           `db:"approach" json:"approach,omitempty"`
	Reasoning     *string           `db:"reasoning" json:"reasoning,omitempty"`
	SortOrder     int               `db:"sort_order" json:"sort_order"`
	DependsOn     []uuid.UUID       `json:"depends_on,omitempty"`
	BatchNumber   int               `db:"batch_number" json:"batch_number"`
	Status        ProjectTaskStatus `db:"status" json:"status"`
	Complexity    *string           `db:"complexity" json:"complexity,omitempty"`
	Confidence    *string           `db:"confidence" json:"confidence,omitempty"`
	AgentRunID    *uuid.UUID        `db:"agent_run_id" json:"agent_run_id,omitempty"`
	IssueID       *uuid.UUID        `db:"issue_id" json:"issue_id,omitempty"`
	BranchName    *string           `db:"branch_name" json:"branch_name,omitempty"`
	PRURL         *string           `db:"pr_url" json:"pr_url,omitempty"`
	OutcomeNotes  *string           `db:"outcome_notes" json:"outcome_notes,omitempty"`
	RetryCount    int               `db:"retry_count" json:"retry_count"`
	MaxRetries    int               `db:"max_retries" json:"max_retries"`
	CreatedAt     time.Time         `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time         `db:"updated_at" json:"updated_at"`
	CompletedAt   *time.Time        `db:"completed_at" json:"completed_at,omitempty"`
}

// ProjectCycle records a PM planning cycle for a project.
type ProjectCycle struct {
	ID                      uuid.UUID       `db:"id" json:"id"`
	ProjectID               uuid.UUID       `db:"project_id" json:"project_id"`
	OrgID                   uuid.UUID       `db:"org_id" json:"org_id"`
	PMPlanID                *uuid.UUID      `db:"pm_plan_id" json:"pm_plan_id,omitempty"`
	CycleNumber             int             `db:"cycle_number" json:"cycle_number"`
	Analysis                string          `db:"analysis" json:"analysis"`
	Decisions               json.RawMessage `db:"decisions" json:"decisions"`
	ProgressPct             *int            `db:"progress_pct" json:"progress_pct,omitempty"`
	TasksCompletedThisCycle int             `db:"tasks_completed_this_cycle" json:"tasks_completed_this_cycle"`
	TasksFailedThisCycle    int             `db:"tasks_failed_this_cycle" json:"tasks_failed_this_cycle"`
	TasksCreatedThisCycle   int             `db:"tasks_created_this_cycle" json:"tasks_created_this_cycle"`
	CreatedAt               time.Time       `db:"created_at" json:"created_at"`
}

// ApproachRecord tracks what approaches were tried and their outcomes.
type ApproachRecord struct {
	TaskTitle     string `json:"task_title"`
	Approach      string `json:"approach"`
	Outcome       string `json:"outcome"`
	LessonLearned string `json:"lesson,omitempty"`
}

// ProjectAttachment is a screenshot, design file, or mockup linked to a project.
type ProjectAttachment struct {
	ID           uuid.UUID  `db:"id" json:"id"`
	ProjectID    uuid.UUID  `db:"project_id" json:"project_id"`
	OrgID        uuid.UUID  `db:"org_id" json:"org_id"`
	FileName     string     `db:"file_name" json:"file_name"`
	FileURL      string     `db:"file_url" json:"file_url"`
	FileType     string     `db:"file_type" json:"file_type"`
	ThumbnailURL *string    `db:"thumbnail_url" json:"thumbnail_url,omitempty"`
	FileSize     *int       `db:"file_size" json:"file_size,omitempty"`
	Category     string     `db:"category" json:"category"`
	Caption      *string    `db:"caption" json:"caption,omitempty"`
	SortOrder    int        `db:"sort_order" json:"sort_order"`
	UploadedBy   *uuid.UUID `db:"uploaded_by" json:"uploaded_by,omitempty"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at" json:"updated_at"`
}

// ProjectSpec is a product requirements document (markdown) linked to a project.
type ProjectSpec struct {
	ID        uuid.UUID  `db:"id" json:"id"`
	ProjectID uuid.UUID  `db:"project_id" json:"project_id"`
	OrgID     uuid.UUID  `db:"org_id" json:"org_id"`
	Title     string     `db:"title" json:"title"`
	Content   string     `db:"content" json:"content"`
	SpecType  string     `db:"spec_type" json:"spec_type"`
	SortOrder int        `db:"sort_order" json:"sort_order"`
	Version   int        `db:"version" json:"version"`
	CreatedBy *uuid.UUID `db:"created_by" json:"created_by,omitempty"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt time.Time  `db:"updated_at" json:"updated_at"`
}
