package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// PMPlan stores the persisted output of a PM analysis cycle.
type PMPlan struct {
	ID                     uuid.UUID       `json:"id" db:"id"`
	OrgID                  uuid.UUID       `json:"org_id" db:"org_id"`
	RepositoryID           *uuid.UUID      `json:"repository_id,omitempty" db:"repository_id"`
	Status                 PMPlanStatus    `json:"status" db:"status"`
	Analysis               string          `json:"analysis" db:"analysis"`
	Tasks                  json.RawMessage `json:"tasks" db:"tasks"`
	Clusters               json.RawMessage `json:"clusters" db:"clusters"`
	SkippedIssues          json.RawMessage `json:"skipped_issues" db:"skipped_issues"`
	IssuesReviewed         int             `json:"issues_reviewed" db:"issues_reviewed"`
	InFlightRunsChecked    int             `json:"in_flight_runs_checked" db:"in_flight_runs_checked"`
	PastOutcomesReviewed   int             `json:"past_outcomes_reviewed" db:"past_outcomes_reviewed"`
	RecentPRsChecked       int             `json:"recent_prs_checked" db:"recent_prs_checked"`
	PastDecisionsReviewed  int             `json:"past_decisions_reviewed" db:"past_decisions_reviewed"`
	CommitsAnalyzed        int             `json:"commits_analyzed" db:"commits_analyzed"`
	ProductContextSnapshot json.RawMessage `json:"product_context_snapshot" db:"product_context_snapshot"`
	TokenUsage             json.RawMessage `json:"token_usage,omitempty" db:"token_usage"`
	TriggeredBy            PMTrigger       `json:"triggered_by" db:"triggered_by"`
	CreatedAt              time.Time       `json:"created_at" db:"created_at"`
	CompletedAt            *time.Time      `json:"completed_at,omitempty" db:"completed_at"`
}

// PMDecisionLogEntry captures a single PM decision for institutional memory.
type PMDecisionLogEntry struct {
	ID        uuid.UUID         `json:"id" db:"id"`
	OrgID     uuid.UUID         `json:"org_id" db:"org_id"`
	PlanID    uuid.UUID         `json:"plan_id" db:"plan_id"`
	IssueID   *uuid.UUID        `json:"issue_id,omitempty" db:"issue_id"`
	Decision  PMDecisionType    `json:"decision" db:"decision"`
	Reasoning string            `json:"reasoning" db:"reasoning"`
	Outcome   PMDecisionOutcome `json:"outcome,omitempty" db:"outcome"`
	CreatedAt time.Time         `json:"created_at" db:"created_at"`
}

// PMDecisionView is an enriched view of a PM decision for the API response,
// including issue title and project info from joins.
type PMDecisionView struct {
	ID           uuid.UUID         `json:"id"`
	PlanID       uuid.UUID         `json:"plan_id"`
	IssueID      *uuid.UUID        `json:"issue_id,omitempty"`
	IssueTitle   *string           `json:"issue_title,omitempty"`
	ProjectID    *uuid.UUID        `json:"project_id,omitempty"`
	ProjectTitle *string           `json:"project_title,omitempty"`
	Decision     PMDecisionType    `json:"decision"`
	Reasoning    string            `json:"reasoning"`
	Outcome      PMDecisionOutcome `json:"outcome,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

// PMDecisionSummary is the aggregate stats for the decisions endpoint.
type PMDecisionSummary struct {
	TotalDelegated int `json:"total_delegated"`
	Succeeded      int `json:"succeeded"`
	Failed         int `json:"failed"`
	StillOpen      int `json:"still_open"`
}

// PMCurrentRecommendation is the presentation-friendly response for the
// /api/v1/pm/current endpoint. It combines the latest plan output with
// context stats and decision summary — without exposing raw plan structure.
type PMCurrentRecommendation struct {
	// Analysis summary
	Analysis      string          `json:"analysis"`
	Tasks         json.RawMessage `json:"tasks"`
	Clusters      json.RawMessage `json:"clusters"`
	SkippedIssues json.RawMessage `json:"skipped_issues"`

	// Context stats — how much the PM considered
	ContextStats PMContextStats `json:"context_stats"`

	// Decision summary — historical performance
	DecisionSummary PMDecisionSummary `json:"decision_summary"`

	// Metadata
	AnalyzedAt  time.Time  `json:"analyzed_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Status      string     `json:"status"`
	TriggeredBy string     `json:"triggered_by"`
}

// PMContextStats captures how much context the PM ingested during analysis.
type PMContextStats struct {
	IssuesReviewed        int `json:"issues_reviewed"`
	InFlightRunsChecked   int `json:"in_flight_runs_checked"`
	PastOutcomesReviewed  int `json:"past_outcomes_reviewed"`
	RecentPRsChecked      int `json:"recent_prs_checked"`
	PastDecisionsReviewed int `json:"past_decisions_reviewed"`
	CommitsAnalyzed       int `json:"commits_analyzed"`
}

// PMStatus represents the PM agent's current state for the status banner.
type PMStatus struct {
	IsRunning           bool       `json:"is_running"`
	LastRunAt           *time.Time `json:"last_run_at,omitempty"`
	LastRunStatus       string     `json:"last_run_status,omitempty"`
	IssuesReviewed      int        `json:"issues_reviewed"`
	SuccessRate         float64    `json:"success_rate"`
	SuccessCount        int        `json:"success_count"`
	TotalDelegated      int        `json:"total_delegated"`
	NextRunIn           *string    `json:"next_run_in,omitempty"`
	NextRunAt           *time.Time `json:"next_run_at,omitempty"`
	LastError           *string    `json:"last_error,omitempty"`
	LastFailedAt        *time.Time `json:"last_failed_at,omitempty"`
	LastFailedSessionID *string    `json:"last_failed_session_id,omitempty"`
}
