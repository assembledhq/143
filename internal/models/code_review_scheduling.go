package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// CodeReviewSchedulingPolicy preserves absent values in historical policies.
// Explicit false and zero are meaningful overrides, not defaults.
type CodeReviewSchedulingPolicy struct {
	AutomaticReReview      *bool `json:"automatic_re_review,omitempty"`
	QuietPeriodSeconds     *int  `json:"quiet_period_seconds,omitempty"`
	MinimumIntervalSeconds *int  `json:"minimum_interval_seconds,omitempty"`
}

type CodeReviewSchedulingSettings struct {
	AutomaticReReview      bool `json:"automatic_re_review"`
	QuietPeriodSeconds     int  `json:"quiet_period_seconds"`
	MinimumIntervalSeconds int  `json:"minimum_interval_seconds"`
}

func (p *CodeReviewSchedulingPolicy) Effective() CodeReviewSchedulingSettings {
	s := CodeReviewSchedulingSettings{AutomaticReReview: true, QuietPeriodSeconds: 60}
	if p == nil {
		return s
	}
	if p.AutomaticReReview != nil {
		s.AutomaticReReview = *p.AutomaticReReview
	}
	if p.QuietPeriodSeconds != nil {
		s.QuietPeriodSeconds = *p.QuietPeriodSeconds
	}
	if p.MinimumIntervalSeconds != nil {
		s.MinimumIntervalSeconds = *p.MinimumIntervalSeconds
	}
	return s
}

func (p *CodeReviewSchedulingPolicy) Validate() error {
	s := p.Effective()
	if s.QuietPeriodSeconds < 0 || s.QuietPeriodSeconds > 3600 {
		return codeReviewPolicyFieldError("scheduling_policy.quiet_period_seconds", "quiet period must be between 0 and 3600 seconds")
	}
	if s.MinimumIntervalSeconds < 0 || s.MinimumIntervalSeconds > 86400 {
		return codeReviewPolicyFieldError("scheduling_policy.minimum_interval_seconds", "minimum interval must be between 0 and 86400 seconds")
	}
	return nil
}

// EligibleAt uses the actual first execution start, never enqueue time.
func (s CodeReviewSchedulingSettings) EligibleAt(changedAt time.Time, lastStart *time.Time) time.Time {
	due := changedAt.Add(time.Duration(s.QuietPeriodSeconds) * time.Second)
	if lastStart != nil {
		cadence := lastStart.Add(time.Duration(s.MinimumIntervalSeconds) * time.Second)
		if cadence.After(due) {
			due = cadence
		}
	}
	return due
}

type CodeReviewScheduleState string

const (
	CodeReviewScheduleIdle    CodeReviewScheduleState = "idle"
	CodeReviewScheduleWaiting CodeReviewScheduleState = "waiting"
	CodeReviewScheduleRunning CodeReviewScheduleState = "running"
	CodeReviewScheduleCovered CodeReviewScheduleState = "covered"
	CodeReviewSchedulePaused  CodeReviewScheduleState = "paused"
	CodeReviewScheduleClosed  CodeReviewScheduleState = "closed"
)

func (s CodeReviewScheduleState) Validate() error {
	switch s {
	case CodeReviewScheduleIdle, CodeReviewScheduleWaiting, CodeReviewScheduleRunning, CodeReviewScheduleCovered, CodeReviewSchedulePaused, CodeReviewScheduleClosed:
		return nil
	}
	return fmt.Errorf("invalid review scheduling state %q", s)
}

type CodeReviewWaitReason string

const (
	CodeReviewWaitNone     CodeReviewWaitReason = ""
	CodeReviewWaitQuiet    CodeReviewWaitReason = "quiet_period"
	CodeReviewWaitInterval CodeReviewWaitReason = "minimum_interval"
	CodeReviewWaitDraft    CodeReviewWaitReason = "draft"
	CodeReviewWaitPaused   CodeReviewWaitReason = "manual_pause"
	CodeReviewWaitPolicy   CodeReviewWaitReason = "policy_disabled"
	CodeReviewWaitApproved CodeReviewWaitReason = "already_approved"
	CodeReviewWaitActive   CodeReviewWaitReason = "active_review"
	CodeReviewWaitContext  CodeReviewWaitReason = "context_unavailable"
)

func (s CodeReviewWaitReason) Validate() error {
	switch s {
	case CodeReviewWaitNone, CodeReviewWaitQuiet, CodeReviewWaitInterval, CodeReviewWaitDraft, CodeReviewWaitPaused, CodeReviewWaitPolicy, CodeReviewWaitApproved, CodeReviewWaitActive, CodeReviewWaitContext:
		return nil
	}
	return fmt.Errorf("invalid review wait reason %q", s)
}

type CodeReviewRequestMode string

const (
	CodeReviewEnsureCurrent CodeReviewRequestMode = "ensure_current"
	CodeReviewReviewNow     CodeReviewRequestMode = "review_now"
)

func (m CodeReviewRequestMode) Validate() error {
	if m == CodeReviewEnsureCurrent || m == CodeReviewReviewNow {
		return nil
	}
	return fmt.Errorf("unsupported review request mode %q", m)
}

type CodeReviewRequestDisposition string

const (
	CodeReviewRequestQueued    CodeReviewRequestDisposition = "queued"
	CodeReviewRequestJoined    CodeReviewRequestDisposition = "joined"
	CodeReviewRequestReused    CodeReviewRequestDisposition = "reused"
	CodeReviewRequestCancelled CodeReviewRequestDisposition = "cancelled"
)

func (d CodeReviewRequestDisposition) Validate() error {
	switch d {
	case CodeReviewRequestQueued, CodeReviewRequestJoined, CodeReviewRequestReused, CodeReviewRequestCancelled:
		return nil
	}
	return fmt.Errorf("invalid review request disposition %q", d)
}

type CodeReviewPRState struct {
	ID                   uuid.UUID               `db:"id" json:"id"`
	OrgID                uuid.UUID               `db:"org_id" json:"-"`
	RepositoryID         uuid.UUID               `db:"repository_id" json:"repository_id"`
	PullRequestID        uuid.UUID               `db:"pull_request_id" json:"pull_request_id"`
	Generation           int64                   `db:"generation" json:"generation"`
	AutomaticPaused      bool                    `db:"automatic_paused" json:"automatic_paused"`
	HeadSHA              string                  `db:"head_sha" json:"head_sha"`
	BaseSHA              string                  `db:"base_sha" json:"base_sha"`
	BaseRef              string                  `db:"base_ref" json:"base_ref"`
	IsDraft              bool                    `db:"is_draft" json:"is_draft"`
	SnapshotObservedAt   *time.Time              `db:"snapshot_observed_at" json:"snapshot_observed_at"`
	LastMaterialChangeAt *time.Time              `db:"last_material_change_at" json:"last_material_change_at"`
	FirstPendingAt       *time.Time              `db:"first_pending_at" json:"first_pending_at"`
	LastAgentStartAt     *time.Time              `db:"last_agent_start_at" json:"last_agent_start_at"`
	EligibleAt           *time.Time              `db:"eligible_at" json:"eligible_at"`
	RetryAt              *time.Time              `db:"retry_at" json:"retry_at"`
	ActiveSessionID      *uuid.UUID              `db:"active_session_id" json:"active_session_id"`
	PendingRequestID     *uuid.UUID              `db:"pending_request_id" json:"pending_request_id"`
	PendingInput         json.RawMessage         `db:"pending_input" json:"-"`
	State                CodeReviewScheduleState `db:"state" json:"state"`
	WaitReason           CodeReviewWaitReason    `db:"wait_reason" json:"wait_reason"`
	CreatedAt            time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time               `db:"updated_at" json:"updated_at"`
}

const JobTypeReconcileCodeReviewSchedule = "reconcile_code_review_schedule"

type CodeReviewScheduleWake struct {
	OrgID         uuid.UUID `json:"org_id"`
	PullRequestID uuid.UUID `json:"pull_request_id"`
}

type CodeReviewScheduledTarget struct {
	Schedule       CodeReviewPRState `db:"schedule" json:"schedule"`
	Title          string            `db:"title" json:"title"`
	GitHubPRURL    string            `db:"github_pr_url" json:"github_pr_url"`
	GitHubPRNumber int               `db:"github_pr_number" json:"github_pr_number"`
	GitHubRepo     string            `db:"github_repo" json:"github_repo"`
}
