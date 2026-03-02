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
	Status                 PMPlanStatus    `json:"status" db:"status"`
	Analysis               string          `json:"analysis" db:"analysis"`
	Tasks                  json.RawMessage `json:"tasks" db:"tasks"`
	Clusters               json.RawMessage `json:"clusters" db:"clusters"`
	SkippedIssues          json.RawMessage `json:"skipped_issues" db:"skipped_issues"`
	IssuesReviewed         int             `json:"issues_reviewed" db:"issues_reviewed"`
	ProductContextSnapshot json.RawMessage `json:"product_context_snapshot" db:"product_context_snapshot"`
	TokenUsage             json.RawMessage `json:"token_usage,omitempty" db:"token_usage"`
	TriggeredBy            PMTrigger       `json:"triggered_by" db:"triggered_by"`
	CreatedAt              time.Time       `json:"created_at" db:"created_at"`
	CompletedAt            *time.Time      `json:"completed_at,omitempty" db:"completed_at"`
}

// PMDecisionLogEntry captures a single PM decision for institutional memory.
type PMDecisionLogEntry struct {
	ID        uuid.UUID  `json:"id" db:"id"`
	OrgID     uuid.UUID  `json:"org_id" db:"org_id"`
	PlanID    uuid.UUID  `json:"plan_id" db:"plan_id"`
	IssueID   *uuid.UUID `json:"issue_id,omitempty" db:"issue_id"`
	Decision  PMDecisionType `json:"decision" db:"decision"`
	Reasoning string     `json:"reasoning" db:"reasoning"`
	Outcome   PMDecisionOutcome `json:"outcome,omitempty" db:"outcome"`
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
}
