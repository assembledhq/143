package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AgentSessionTask is a task within a session, enriched with run data when available.
type AgentSessionTask struct {
	Rank               int      `json:"rank"`
	Title              string   `json:"title"`
	IssueIDs           []string `json:"issue_ids"`
	Complexity         string   `json:"complexity,omitempty"`
	Confidence         string   `json:"confidence,omitempty"`
	Reasoning          string   `json:"reasoning,omitempty"`
	Approach           string   `json:"approach,omitempty"`
	Risk               string   `json:"risk,omitempty"`
	Status             string   `json:"status,omitempty"`
	AgentRunID         *string  `json:"agent_run_id,omitempty"`
	RunStatus          *string  `json:"run_status,omitempty"`
	RunResultSummary   *string  `json:"run_result_summary,omitempty"`
	RunConfidenceScore *float64 `json:"run_confidence_score,omitempty"`
	RunStartedAt       *string  `json:"run_started_at,omitempty"`
	RunCompletedAt     *string  `json:"run_completed_at,omitempty"`
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
}
