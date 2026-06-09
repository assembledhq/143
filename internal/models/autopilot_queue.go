package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type AutopilotRunState string

const (
	AutopilotRunStateNotStarted    AutopilotRunState = "not_started"
	AutopilotRunStateQueued        AutopilotRunState = "queued"
	AutopilotRunStateRunning       AutopilotRunState = "running"
	AutopilotRunStateAwaitingInput AutopilotRunState = "awaiting_input"
	AutopilotRunStateNeedsReview   AutopilotRunState = "needs_review"
	AutopilotRunStatePROpen        AutopilotRunState = "pr_open"
	AutopilotRunStateMerged        AutopilotRunState = "merged"
	AutopilotRunStateFailed        AutopilotRunState = "failed"
	AutopilotRunStateSkipped       AutopilotRunState = "skipped"
)

func (s AutopilotRunState) Validate() error {
	switch s {
	case AutopilotRunStateNotStarted,
		AutopilotRunStateQueued,
		AutopilotRunStateRunning,
		AutopilotRunStateAwaitingInput,
		AutopilotRunStateNeedsReview,
		AutopilotRunStatePROpen,
		AutopilotRunStateMerged,
		AutopilotRunStateFailed,
		AutopilotRunStateSkipped:
		return nil
	default:
		return fmt.Errorf("invalid AutopilotRunState: %q", s)
	}
}

type AutopilotQueueAction string

const (
	AutopilotQueueActionStartRun AutopilotQueueAction = "start_run"
	AutopilotQueueActionViewRun  AutopilotQueueAction = "view_run"
	AutopilotQueueActionReview   AutopilotQueueAction = "review"
	AutopilotQueueActionOpenPR   AutopilotQueueAction = "open_pr"
	AutopilotQueueActionRetry    AutopilotQueueAction = "retry"
	AutopilotQueueActionBlocked  AutopilotQueueAction = "blocked"
)

func (a AutopilotQueueAction) Validate() error {
	switch a {
	case AutopilotQueueActionStartRun,
		AutopilotQueueActionViewRun,
		AutopilotQueueActionReview,
		AutopilotQueueActionOpenPR,
		AutopilotQueueActionRetry,
		AutopilotQueueActionBlocked:
		return nil
	default:
		return fmt.Errorf("invalid AutopilotQueueAction: %q", a)
	}
}

type AutopilotTriggerMode string

const (
	AutopilotTriggerModeAuto   AutopilotTriggerMode = "auto"
	AutopilotTriggerModeManual AutopilotTriggerMode = "manual"
)

func (m AutopilotTriggerMode) Validate() error {
	switch m {
	case AutopilotTriggerModeAuto, AutopilotTriggerModeManual:
		return nil
	default:
		return fmt.Errorf("invalid AutopilotTriggerMode: %q", m)
	}
}

type AutopilotIssueSource struct {
	Type IssueSource `json:"type"`
	Key  string      `json:"key"`
}

type AutopilotRepoRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type AutopilotCustomerImpact struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type AutopilotLowHangingFruit struct {
	Label       string   `json:"label"`
	Reasons     []string `json:"reasons"`
	ClusterSize int      `json:"cluster_size"`
}

type AutopilotSessionRef struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AutopilotAgentRunRef struct {
	ID          uuid.UUID            `json:"id"`
	Status      SessionStatus        `json:"status"`
	TriggerMode AutopilotTriggerMode `json:"trigger_mode"`
	StartedAt   *time.Time           `json:"started_at,omitempty"`
}

type AutopilotPullRequestRef struct {
	ID       uuid.UUID         `json:"id"`
	Number   int               `json:"number"`
	URL      string            `json:"url"`
	Status   PullRequestStatus `json:"status"`
	MergedAt *time.Time        `json:"merged_at,omitempty"`
}

type AutopilotQueueRow struct {
	ID                   uuid.UUID                `json:"id"`
	Rank                 int                      `json:"rank"`
	Source               AutopilotIssueSource     `json:"source"`
	Title                string                   `json:"title"`
	IssueURL             *string                  `json:"issue_url,omitempty"`
	Repo                 *AutopilotRepoRef        `json:"repo,omitempty"`
	IssueStatus          IssueStatus              `json:"issue_status"`
	CustomerImpact       AutopilotCustomerImpact  `json:"customer_impact"`
	ImplementationEase   string                   `json:"implementation_ease"`
	LowHangingFruit      AutopilotLowHangingFruit `json:"low_hanging_fruit"`
	DisplayRunState      AutopilotRunState        `json:"display_run_state"`
	LatestSession        *AutopilotSessionRef     `json:"latest_session,omitempty"`
	LatestAgentRun       *AutopilotAgentRunRef    `json:"latest_agent_run,omitempty"`
	LatestPR             *AutopilotPullRequestRef `json:"latest_pr,omitempty"`
	AvailableAction      AutopilotQueueAction     `json:"available_action"`
	ActionDisabledReason *string                  `json:"action_disabled_reason"`
}

type AutopilotQueueSummary struct {
	TopIssueID        *uuid.UUID `json:"top_issue_id,omitempty"`
	AutorunnableCount int        `json:"autorunnable_count"`
	NeedsReviewCount  int        `json:"needs_review_count"`
	OpenPRCount       int        `json:"open_pr_count"`
	ActiveRunCount    int        `json:"active_run_count"`
	RankedIssueCount  int        `json:"ranked_issue_count"`
	AnalyzedAt        *time.Time `json:"analyzed_at,omitempty"`
}

type AutopilotQueueMeta struct {
	NextCursor string                `json:"next_cursor,omitempty"`
	Summary    AutopilotQueueSummary `json:"summary"`
}

type AutopilotQueueResponse struct {
	Data []AutopilotQueueRow `json:"data"`
	Meta AutopilotQueueMeta  `json:"meta"`
}
