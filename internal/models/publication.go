package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type SessionPublicationState string

const (
	SessionPublicationStateRequested       SessionPublicationState = "requested"
	SessionPublicationStateReviewPending   SessionPublicationState = "review_pending"
	SessionPublicationStateReadyToPublish  SessionPublicationState = "ready_to_publish"
	SessionPublicationStateBranchPublished SessionPublicationState = "branch_published"
	SessionPublicationStatePRResolved      SessionPublicationState = "pr_resolved"
	SessionPublicationStateRecorded        SessionPublicationState = "recorded"
	SessionPublicationStateCompleted       SessionPublicationState = "completed"
	SessionPublicationStateCompletedNoop   SessionPublicationState = "completed_noop"
	SessionPublicationStateRetryableFailed SessionPublicationState = "retryable_failed"
	SessionPublicationStateTerminalFailed  SessionPublicationState = "terminal_failed"
)

func (s SessionPublicationState) Validate() error {
	switch s {
	case SessionPublicationStateRequested,
		SessionPublicationStateReviewPending,
		SessionPublicationStateReadyToPublish,
		SessionPublicationStateBranchPublished,
		SessionPublicationStatePRResolved,
		SessionPublicationStateRecorded,
		SessionPublicationStateCompleted,
		SessionPublicationStateCompletedNoop,
		SessionPublicationStateRetryableFailed,
		SessionPublicationStateTerminalFailed:
		return nil
	default:
		return fmt.Errorf("invalid SessionPublicationState: %q", s)
	}
}

func (s SessionPublicationState) Terminal() bool {
	return s == SessionPublicationStateCompleted ||
		s == SessionPublicationStateCompletedNoop ||
		s == SessionPublicationStateTerminalFailed
}

type SessionPublicationSource string

const (
	SessionPublicationSourceUser       SessionPublicationSource = "user"
	SessionPublicationSourceAutomation SessionPublicationSource = "automation"
	SessionPublicationSourceAgentTool  SessionPublicationSource = "agent_tool"
	SessionPublicationSourceBackend    SessionPublicationSource = "backend"
	SessionPublicationSourceWebhook    SessionPublicationSource = "webhook"
	SessionPublicationSourceReconciler SessionPublicationSource = "reconciler"
	SessionPublicationSourceBackfill   SessionPublicationSource = "backfill"
)

func (s SessionPublicationSource) Validate() error {
	switch s {
	case SessionPublicationSourceUser,
		SessionPublicationSourceAutomation,
		SessionPublicationSourceAgentTool,
		SessionPublicationSourceBackend,
		SessionPublicationSourceWebhook,
		SessionPublicationSourceReconciler,
		SessionPublicationSourceBackfill:
		return nil
	default:
		return fmt.Errorf("invalid SessionPublicationSource: %q", s)
	}
}

type SessionPublicationReviewGateState string

const (
	SessionPublicationReviewGateNotRequired SessionPublicationReviewGateState = "not_required"
	SessionPublicationReviewGatePending     SessionPublicationReviewGateState = "pending"
	SessionPublicationReviewGatePassed      SessionPublicationReviewGateState = "passed"
	SessionPublicationReviewGateNeedsHuman  SessionPublicationReviewGateState = "needs_human"
	SessionPublicationReviewGateFailed      SessionPublicationReviewGateState = "failed"
)

func (s SessionPublicationReviewGateState) Validate() error {
	switch s {
	case SessionPublicationReviewGateNotRequired,
		SessionPublicationReviewGatePending,
		SessionPublicationReviewGatePassed,
		SessionPublicationReviewGateNeedsHuman,
		SessionPublicationReviewGateFailed:
		return nil
	default:
		return fmt.Errorf("invalid SessionPublicationReviewGateState: %q", s)
	}
}

type SessionPublicationJobQueue string

const (
	SessionPublicationJobQueueDefault SessionPublicationJobQueue = "default"
	SessionPublicationJobQueueAgent   SessionPublicationJobQueue = "agent"
)

func (q SessionPublicationJobQueue) Validate() error {
	switch q {
	case SessionPublicationJobQueueDefault, SessionPublicationJobQueueAgent:
		return nil
	default:
		return fmt.Errorf("invalid SessionPublicationJobQueue: %q", q)
	}
}

type SessionPublicationTriggerKind string

const (
	SessionPublicationTriggerAgentReady     SessionPublicationTriggerKind = "agent_ready"
	SessionPublicationTriggerExplicitAction SessionPublicationTriggerKind = "explicit_action"
	SessionPublicationTriggerPolicy         SessionPublicationTriggerKind = "policy"
)

func (k SessionPublicationTriggerKind) Validate() error {
	switch k {
	case SessionPublicationTriggerAgentReady, SessionPublicationTriggerExplicitAction, SessionPublicationTriggerPolicy:
		return nil
	default:
		return fmt.Errorf("invalid SessionPublicationTriggerKind: %q", k)
	}
}

type PRHandoffMode string

const (
	PRHandoffModePrePublish PRHandoffMode = "pre_publish"
	PRHandoffModeDraftFirst PRHandoffMode = "draft_first"
)

func (m PRHandoffMode) Validate() error {
	switch m {
	case PRHandoffModePrePublish, PRHandoffModeDraftFirst:
		return nil
	default:
		return fmt.Errorf("invalid PRHandoffMode: %q", m)
	}
}

type PublicationPolicySource string

const (
	PublicationPolicySourceProductDefault PublicationPolicySource = "product_default"
	PublicationPolicySourceOrganization   PublicationPolicySource = "organization"
	PublicationPolicySourcePersonal       PublicationPolicySource = "personal"
	PublicationPolicySourceAutomation     PublicationPolicySource = "automation"
	PublicationPolicySourceExplicitAction PublicationPolicySource = "explicit_action"
	PublicationPolicySourceExplicitBypass PublicationPolicySource = "explicit_bypass"
)

func (s PublicationPolicySource) ValidateAutomatic() error {
	switch s {
	case PublicationPolicySourceProductDefault, PublicationPolicySourceOrganization,
		PublicationPolicySourcePersonal, PublicationPolicySourceAutomation,
		PublicationPolicySourceExplicitAction:
		return nil
	default:
		return fmt.Errorf("invalid automatic publication policy source: %q", s)
	}
}

func (s PublicationPolicySource) ValidateReview() error {
	switch s {
	case PublicationPolicySourceProductDefault, PublicationPolicySourceOrganization,
		PublicationPolicySourcePersonal, PublicationPolicySourceAutomation,
		PublicationPolicySourceExplicitBypass:
		return nil
	default:
		return fmt.Errorf("invalid review publication policy source: %q", s)
	}
}

type SessionPublication struct {
	ID                      uuid.UUID                         `db:"id" json:"id"`
	OrgID                   uuid.UUID                         `db:"org_id" json:"org_id"`
	SessionID               uuid.UUID                         `db:"session_id" json:"session_id"`
	ChangesetID             uuid.UUID                         `db:"changeset_id" json:"changeset_id"`
	RepositoryID            uuid.UUID                         `db:"repository_id" json:"repository_id"`
	State                   SessionPublicationState           `db:"state" json:"state"`
	Source                  SessionPublicationSource          `db:"source" json:"source"`
	TriggerKind             SessionPublicationTriggerKind     `db:"trigger_kind" json:"trigger_kind"`
	HandoffMode             PRHandoffMode                     `db:"handoff_mode" json:"handoff_mode"`
	InitiatedByUserID       *uuid.UUID                        `db:"initiated_by_user_id" json:"initiated_by_user_id,omitempty"`
	AutomaticPolicySource   PublicationPolicySource           `db:"automatic_pr_policy_source" json:"automatic_pr_policy_source"`
	ReviewPolicySource      PublicationPolicySource           `db:"review_policy_source" json:"review_policy_source"`
	ReviewMaxPasses         *int                              `db:"review_max_passes" json:"review_max_passes,omitempty"`
	ReviewLoopID            *uuid.UUID                        `db:"review_loop_id" json:"review_loop_id,omitempty"`
	ReviewWorkspaceRevision *int64                            `db:"review_workspace_revision" json:"review_workspace_revision,omitempty"`
	ReviewDesiredHeadSHA    *string                           `db:"review_desired_head_sha" json:"review_desired_head_sha,omitempty"`
	ReviewGateState         SessionPublicationReviewGateState `db:"review_gate_state" json:"review_gate_state"`
	JobQueue                SessionPublicationJobQueue        `db:"job_queue" json:"-"`
	RequestPayload          json.RawMessage                   `db:"request_payload" json:"-"`
	RequestGenerationAt     time.Time                         `db:"request_generation_at" json:"-"`
	BaseBranch              string                            `db:"base_branch" json:"base_branch"`
	HeadBranch              string                            `db:"head_branch" json:"head_branch"`
	DesiredHeadSHA          *string                           `db:"desired_head_sha" json:"desired_head_sha,omitempty"`
	PublishedHeadSHA        *string                           `db:"published_head_sha" json:"published_head_sha,omitempty"`
	GitHubPRNumber          *int                              `db:"github_pr_number" json:"github_pr_number,omitempty"`
	GitHubPRURL             *string                           `db:"github_pr_url" json:"github_pr_url,omitempty"`
	AttemptCount            int                               `db:"attempt_count" json:"attempt_count"`
	LastErrorCode           *string                           `db:"last_error_code" json:"last_error_code,omitempty"`
	LastErrorMessage        *string                           `db:"last_error_message" json:"last_error_message,omitempty"`
	RequestedAt             time.Time                         `db:"requested_at" json:"requested_at"`
	LastAttemptAt           *time.Time                        `db:"last_attempt_at" json:"last_attempt_at,omitempty"`
	BranchPublishedAt       *time.Time                        `db:"branch_published_at" json:"branch_published_at,omitempty"`
	PRResolvedAt            *time.Time                        `db:"pr_resolved_at" json:"pr_resolved_at,omitempty"`
	CompletedAt             *time.Time                        `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt               time.Time                         `db:"created_at" json:"created_at"`
	UpdatedAt               time.Time                         `db:"updated_at" json:"updated_at"`
}
