package pm

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// Plan is the PM agent's output — a prioritized, reasoned work plan.
type Plan struct {
	ID             uuid.UUID       `json:"id"`
	OrgID          uuid.UUID       `json:"org_id"`
	Status         models.PMPlanStatus `json:"status"`
	Analysis       string          `json:"analysis"`
	Tasks          []Task          `json:"tasks"`
	Clusters       []Cluster       `json:"clusters"`
	SkippedIssues  []SkipEntry     `json:"skipped_issues"`
	IssuesReviewed int             `json:"issues_reviewed"`
	TokenUsage     json.RawMessage `json:"token_usage,omitempty"`
	TriggeredBy    models.PMTrigger `json:"triggered_by"`
	CreatedAt      time.Time       `json:"created_at"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
}

// Task is a single work item the PM agent wants a coding agent to tackle.
type Task struct {
	Rank       int                  `json:"rank"`
	IssueIDs   []uuid.UUID          `json:"issue_ids"`
	Title      string               `json:"title"`
	Reasoning  string               `json:"reasoning"`
	Approach   string               `json:"approach"`
	Risk       string               `json:"risk"`
	Complexity models.PMTaskComplexity `json:"complexity"`
	Confidence models.PMTaskConfidence `json:"confidence"`

	AgentRunID *uuid.UUID         `json:"agent_run_id,omitempty"`
	Status     models.PMTaskStatus `json:"status"`
}

// Cluster groups related issues the PM agent identified as sharing a root cause.
type Cluster struct {
	IssueIDs  []uuid.UUID `json:"issue_ids"`
	RootCause string      `json:"root_cause"`
	Strategy  string      `json:"strategy"`
}

// SkipEntry is an issue the PM agent recommends not working on.
type SkipEntry struct {
	IssueID uuid.UUID        `json:"issue_id"`
	Reason  models.PMSkipReason `json:"reason"`
	Detail  string           `json:"detail"`
}

// PMContext is the full picture the PM agent reasons over.
type PMContext struct {
	OpenIssues        []IssueSummary            `json:"open_issues"`
	InFlightRuns      []RunSummary              `json:"in_flight_runs"`
	RecentOutcomes    []OutcomeSummary          `json:"recent_outcomes"`
	RecentPRs         []PRSummary               `json:"recent_prs"`
	PreviousDecisions []DecisionLogEntrySummary `json:"previous_decisions"`
	MaxConcurrentRuns int                       `json:"max_concurrent_runs"`
	CurrentRunCount   int                       `json:"current_run_count"`
}

type IssueSummary struct {
	ID                    string   `json:"id"`
	Source                string   `json:"source"`
	Title                 string   `json:"title"`
	Description           string   `json:"description"`
	Severity              string   `json:"severity"`
	OccurrenceCount       int      `json:"occurrences"`
	AffectedCustomerCount int      `json:"affected_customers"`
	FirstSeenAt           string   `json:"first_seen"`
	LastSeenAt            string   `json:"last_seen"`
	Tags                  []string `json:"tags,omitempty"`
	HasStackTrace         bool     `json:"has_stack_trace"`
}

type RunSummary struct {
	ID        uuid.UUID  `json:"id"`
	IssueID   uuid.UUID  `json:"issue_id"`
	Status    string     `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

type OutcomeSummary struct {
	RunID              uuid.UUID  `json:"run_id"`
	IssueID            uuid.UUID  `json:"issue_id"`
	Status             string     `json:"status"`
	ConfidenceScore    *float64   `json:"confidence_score,omitempty"`
	FailureCategory    *string    `json:"failure_category,omitempty"`
	FailureExplanation *string    `json:"failure_explanation,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type PRSummary struct {
	ID           uuid.UUID  `json:"id"`
	AgentRunID   uuid.UUID  `json:"agent_run_id"`
	Title        string     `json:"title"`
	Status       string     `json:"status"`
	ReviewStatus string     `json:"review_status"`
	MergedAt     *time.Time `json:"merged_at,omitempty"`
}

type DecisionLogEntrySummary struct {
	ID        uuid.UUID  `json:"id"`
	PlanID    uuid.UUID  `json:"plan_id"`
	IssueID   *uuid.UUID `json:"issue_id,omitempty"`
	Decision  models.PMDecisionType `json:"decision"`
	Reasoning string     `json:"reasoning"`
	Outcome   models.PMDecisionOutcome `json:"outcome,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}
