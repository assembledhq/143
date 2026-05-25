package pm

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// Plan is the PM agent's output — a prioritized, reasoned work plan.
type Plan struct {
	ID             uuid.UUID           `json:"id"`
	OrgID          uuid.UUID           `json:"org_id"`
	Status         models.PMPlanStatus `json:"status"`
	Analysis       string              `json:"analysis"`
	Tasks          []Task              `json:"tasks"`
	Clusters       []Cluster           `json:"clusters"`
	SkippedIssues  []SkipEntry         `json:"skipped_issues"`
	ProjectPlans   []ProjectPlan       `json:"project_plans,omitempty"`
	LinearActions  []LinearAction      `json:"linear_actions,omitempty"`
	SlotAllocation *SlotAllocation     `json:"slot_allocation,omitempty"`
	IssuesReviewed int                 `json:"issues_reviewed"`
	TokenUsage     json.RawMessage     `json:"token_usage,omitempty"`
	TriggeredBy    models.PMTrigger    `json:"triggered_by"`
	CreatedAt      time.Time           `json:"created_at"`
	CompletedAt    *time.Time          `json:"completed_at,omitempty"`
}

// Task is a single work item the PM agent wants a coding agent to tackle.
type Task struct {
	Rank       int                     `json:"rank"`
	IssueIDs   []uuid.UUID             `json:"issue_ids"`
	Title      string                  `json:"title"`
	Reasoning  string                  `json:"reasoning"`
	Approach   string                  `json:"approach"`
	Risk       string                  `json:"risk"`
	Complexity models.PMTaskComplexity `json:"complexity"`
	Confidence models.PMTaskConfidence `json:"confidence"`

	SessionID *uuid.UUID          `json:"session_id,omitempty"`
	Status    models.PMTaskStatus `json:"status"`
}

// Cluster groups related issues the PM agent identified as sharing a root cause.
type Cluster struct {
	IssueIDs  []uuid.UUID `json:"issue_ids"`
	RootCause string      `json:"root_cause"`
	Strategy  string      `json:"strategy"`
}

// SkipEntry is an issue the PM agent recommends not working on.
type SkipEntry struct {
	IssueID uuid.UUID           `json:"issue_id"`
	Reason  models.PMSkipReason `json:"reason"`
	Detail  string              `json:"detail"`
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
	ActiveProjects    []ProjectSummary          `json:"active_projects,omitempty"`
	SlackThreads      []SlackThreadContext      `json:"slack_threads,omitempty"`
}

// SlackThreadContext is a lightweight summary of a Slack thread for PM analysis.
type SlackThreadContext struct {
	ChannelName  string   `json:"channel_name"`
	Category     string   `json:"category"`
	Summary      string   `json:"summary"`
	Urgency      string   `json:"urgency"`
	MessageCount int      `json:"message_count"`
	Participants []string `json:"participants"`
	LastActivity string   `json:"last_activity"`
	ThreadFile   string   `json:"thread_file"`
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
	StackTraceSummary     string   `json:"stack_trace_summary,omitempty"`
	LinearState           string   `json:"linear_state,omitempty"`
	LinearTeam            string   `json:"linear_team,omitempty"`
	LinearIdentifier      string   `json:"linear_identifier,omitempty"`
}

type RunSummary struct {
	ID        uuid.UUID  `json:"id"`
	IssueID   *uuid.UUID `json:"issue_id,omitempty"`
	Status    string     `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

type OutcomeSummary struct {
	RunID              uuid.UUID  `json:"run_id"`
	IssueID            *uuid.UUID `json:"issue_id,omitempty"`
	Status             string     `json:"status"`
	FailureCategory    *string    `json:"failure_category,omitempty"`
	FailureExplanation *string    `json:"failure_explanation,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type PRSummary struct {
	ID           uuid.UUID  `json:"id"`
	SessionID    *uuid.UUID `json:"session_id,omitempty"`
	Title        string     `json:"title"`
	Status       string     `json:"status"`
	ReviewStatus string     `json:"review_status"`
	MergedAt     *time.Time `json:"merged_at,omitempty"`
}

type DecisionLogEntrySummary struct {
	ID        uuid.UUID                `json:"id"`
	PlanID    uuid.UUID                `json:"plan_id"`
	IssueID   *uuid.UUID               `json:"issue_id,omitempty"`
	Decision  models.PMDecisionType    `json:"decision"`
	Reasoning string                   `json:"reasoning"`
	Outcome   models.PMDecisionOutcome `json:"outcome,omitempty"`
	CreatedAt time.Time                `json:"created_at"`
}

// ProjectSummary provides the PM with enough context to plan the next batch.
type ProjectSummary struct {
	ID                 string                  `json:"id"`
	Title              string                  `json:"title"`
	Goal               string                  `json:"goal"`
	Scope              string                  `json:"scope,omitempty"`
	CompletionCriteria string                  `json:"completion_criteria,omitempty"`
	Priority           int                     `json:"priority"`
	Status             string                  `json:"status"`
	ExecutionMode      string                  `json:"execution_mode"`
	MaxConcurrent      int                     `json:"max_concurrent"`
	CurrentPhase       string                  `json:"current_phase,omitempty"`
	TotalTasks         int                     `json:"total_tasks"`
	CompletedTasks     int                     `json:"completed_tasks"`
	FailedTasks        int                     `json:"failed_tasks"`
	ProgressPct        int                     `json:"progress_pct,omitempty"`
	RecentCycles       []CycleSummary          `json:"recent_cycles,omitempty"`
	PendingTasks       []TaskSummary           `json:"pending_tasks,omitempty"`
	RunningTasks       []TaskSummary           `json:"running_tasks,omitempty"`
	RecentlyCompleted  []TaskSummary           `json:"recently_completed,omitempty"`
	RecentlyFailed     []TaskSummary           `json:"recently_failed,omitempty"`
	LessonsLearned     []string                `json:"lessons_learned,omitempty"`
	ApproachHistory    []models.ApproachRecord `json:"approach_history,omitempty"`
}

type CycleSummary struct {
	CycleNumber    int    `json:"cycle_number"`
	Analysis       string `json:"analysis"`
	TasksCreated   int    `json:"tasks_created"`
	TasksCompleted int    `json:"tasks_completed"`
	TasksFailed    int    `json:"tasks_failed"`
	CreatedAt      string `json:"created_at"`
}

type TaskSummary struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Approach     string `json:"approach,omitempty"`
	OutcomeNotes string `json:"outcome_notes,omitempty"`
	Complexity   string `json:"complexity,omitempty"`
	Confidence   string `json:"confidence,omitempty"`
	BatchNumber  int    `json:"batch_number"`
}

// ProjectPlan is the PM's plan for a single project in a cycle.
type ProjectPlan struct {
	ProjectID            uuid.UUID          `json:"project_id"`
	CycleAnalysis        string             `json:"cycle_analysis"`
	ProgressPct          int                `json:"progress_pct"`
	CurrentPhase         string             `json:"current_phase"`
	StatusRecommendation string             `json:"status_recommendation"`
	LessonsLearned       []string           `json:"lessons_learned"`
	NewTasks             []ProjectTaskSpec  `json:"new_tasks"`
	SkippedTasks         []SkippedTaskEntry `json:"skipped_tasks"`
}

// ProjectTaskSpec is a task the PM wants to create for a project.
type ProjectTaskSpec struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Approach    string   `json:"approach"`
	Reasoning   string   `json:"reasoning"`
	DependsOn   []string `json:"depends_on"`
	Complexity  string   `json:"complexity"`
	Confidence  string   `json:"confidence"`
}

// SkippedTaskEntry records why a potential task was not created.
type SkippedTaskEntry struct {
	Description string `json:"description"`
	Reason      string `json:"reason"`
}

// slackIntegrationConfig is the shape of the config stored on a Slack integration.
type slackIntegrationConfig struct {
	RecentThreads []slackIntegrationThread `json:"recent_threads"`
}

// slackIntegrationThread is a single thread stored in the Slack integration config.
type slackIntegrationThread struct {
	ChannelName  string                    `json:"channel_name"`
	ThreadTS     string                    `json:"thread_ts"`
	MessageCount int                       `json:"message_count"`
	Participants []string                  `json:"participants"`
	LastActivity string                    `json:"last_activity"`
	Messages     json.RawMessage           `json:"messages"`
	Analysis     *slackIntegrationAnalysis `json:"analysis"`
}

// slackIntegrationAnalysis is the analysis result attached to a thread.
type slackIntegrationAnalysis struct {
	Actionable bool   `json:"actionable"`
	Category   string `json:"category"`
	Summary    string `json:"summary"`
	Urgency    string `json:"urgency"`
}

// SlotAllocation is the PM's recommendation for how to split slots.
type SlotAllocation struct {
	Reactive  int            `json:"reactive"`
	Projects  map[string]int `json:"projects"`
	Reasoning string         `json:"reasoning"`
}

// LinearAction is an action the PM recommends taking on a Linear issue.
type LinearAction struct {
	IssueID    uuid.UUID `json:"issue_id"`
	ExternalID string    `json:"external_id"`
	Action     string    `json:"action"` // "re_prioritize", "re_label", "add_comment", "close"
	Detail     string    `json:"detail"`
	Reasoning  string    `json:"reasoning"`
}
