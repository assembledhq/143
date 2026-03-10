package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AgentSessionTask is a task within a session, enriched with run data when available.
type AgentSessionTask struct {
	Rank               int              `json:"rank"`
	Title              string           `json:"title"`
	IssueIDs           []string         `json:"issue_ids"`
	Complexity         PMTaskComplexity `json:"complexity,omitempty"`
	Confidence         PMTaskConfidence `json:"confidence,omitempty"`
	Reasoning          string           `json:"reasoning,omitempty"`
	Approach           string           `json:"approach,omitempty"`
	Risk               string           `json:"risk,omitempty"`
	Status             PMTaskStatus     `json:"status,omitempty"`
	AgentRunID         *string          `json:"agent_run_id,omitempty"`
	RunStatus          *AgentRunStatus  `json:"run_status,omitempty"`
	RunResultSummary   *string          `json:"run_result_summary,omitempty"`
	RunConfidenceScore *float64         `json:"run_confidence_score,omitempty"`
	RunStartedAt       *string          `json:"run_started_at,omitempty"`
	RunCompletedAt     *string          `json:"run_completed_at,omitempty"`
}

// AgentSessionFileRef represents a file reference parsed from approach text.
type AgentSessionFileRef struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// AgentSession is the API response type that merges PM plans and ad-hoc runs
// into a unified "session" concept. It is NOT a database model.
type AgentSession struct {
	ID                uuid.UUID               `json:"id"`
	Type              AgentSessionType        `json:"type"`
	Status            AgentSessionStatus      `json:"status"`
	TriggeredBy       AgentSessionTriggeredBy `json:"triggered_by"`
	Title             string                  `json:"title"`
	Analysis          *string                 `json:"analysis,omitempty"`
	Tasks             []AgentSessionTask      `json:"tasks"`
	Clusters          json.RawMessage         `json:"clusters,omitempty"`
	SkippedIssues     json.RawMessage         `json:"skipped_issues,omitempty"`
	IssuesReviewed    *int                    `json:"issues_reviewed,omitempty"`
	TaskCount         int                     `json:"task_count"`
	ActiveRunCount    int                     `json:"active_run_count"`
	CompletedRunCount int                     `json:"completed_run_count"`
	FailedRunCount    int                     `json:"failed_run_count"`
	CreatedAt         time.Time               `json:"created_at"`
	CompletedAt       *time.Time              `json:"completed_at,omitempty"`

	// Project grouping (populated when session tasks link to a project).
	ProjectID    *uuid.UUID `json:"project_id,omitempty"`
	ProjectTitle *string    `json:"project_title,omitempty"`

	// Context counts (plan sessions only) showing what the PM considered.
	InFlightRunsChecked   *int `json:"in_flight_runs_checked,omitempty"`
	PastOutcomesReviewed  *int `json:"past_outcomes_reviewed,omitempty"`
	RecentPRsChecked      *int `json:"recent_prs_checked,omitempty"`
	PastDecisionsReviewed *int `json:"past_decisions_reviewed,omitempty"`
	CommitsAnalyzed       *int `json:"commits_analyzed,omitempty"`
}
