package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type AutomationOutcomeDecision string

const (
	AutomationOutcomeDecisionPassed           AutomationOutcomeDecision = "passed"
	AutomationOutcomeDecisionChangesRequested AutomationOutcomeDecision = "changes_requested"
	AutomationOutcomeDecisionAdvisory         AutomationOutcomeDecision = "advisory"
	AutomationOutcomeDecisionNotApplicable    AutomationOutcomeDecision = "not_applicable"
)

func (d AutomationOutcomeDecision) Validate() error {
	switch d {
	case AutomationOutcomeDecisionPassed,
		AutomationOutcomeDecisionChangesRequested,
		AutomationOutcomeDecisionAdvisory,
		AutomationOutcomeDecisionNotApplicable:
		return nil
	default:
		return fmt.Errorf("invalid automation outcome decision: %q", d)
	}
}

type AutomationOutcomeSource string

const (
	AutomationOutcomeSourceAgentReported  AutomationOutcomeSource = "agent_reported"
	AutomationOutcomeSourceLegacyInferred AutomationOutcomeSource = "legacy_inferred"
)

func (s AutomationOutcomeSource) Validate() error {
	switch s {
	case AutomationOutcomeSourceAgentReported, AutomationOutcomeSourceLegacyInferred:
		return nil
	default:
		return fmt.Errorf("invalid automation outcome source: %q", s)
	}
}

type AutomationExternalActionType string

const (
	AutomationExternalActionGitHubReviewChangesRequested AutomationExternalActionType = "github_review_changes_requested"
	AutomationExternalActionGitHubReviewApproved         AutomationExternalActionType = "github_review_approved"
	AutomationExternalActionGitHubComment                AutomationExternalActionType = "github_comment"
)

func (t AutomationExternalActionType) Validate() error {
	switch t {
	case AutomationExternalActionGitHubReviewChangesRequested,
		AutomationExternalActionGitHubReviewApproved,
		AutomationExternalActionGitHubComment:
		return nil
	default:
		return fmt.Errorf("invalid automation external action type: %q", t)
	}
}

type AutomationExternalActionVerificationStatus string

const (
	AutomationExternalActionVerificationReported    AutomationExternalActionVerificationStatus = "reported"
	AutomationExternalActionVerificationVerified    AutomationExternalActionVerificationStatus = "verified"
	AutomationExternalActionVerificationUnavailable AutomationExternalActionVerificationStatus = "unavailable"
)

func (s AutomationExternalActionVerificationStatus) Validate() error {
	switch s {
	case AutomationExternalActionVerificationReported,
		AutomationExternalActionVerificationVerified,
		AutomationExternalActionVerificationUnavailable:
		return nil
	default:
		return fmt.Errorf("invalid automation external action verification status: %q", s)
	}
}

type AutomationRunExternalAction struct {
	ID                 uuid.UUID                                  `db:"id" json:"id"`
	OrgID              uuid.UUID                                  `db:"org_id" json:"org_id"`
	OutcomeID          uuid.UUID                                  `db:"outcome_id" json:"outcome_id"`
	Provider           string                                     `db:"provider" json:"provider"`
	ActionType         AutomationExternalActionType               `db:"action_type" json:"action_type"`
	ExternalID         *string                                    `db:"external_id" json:"external_id,omitempty"`
	URL                string                                     `db:"url" json:"url"`
	VerificationStatus AutomationExternalActionVerificationStatus `db:"verification_status" json:"verification_status"`
	CreatedAt          time.Time                                  `db:"created_at" json:"created_at"`
}

type AutomationRunOutcome struct {
	ID                uuid.UUID                    `db:"id" json:"id"`
	OrgID             uuid.UUID                    `db:"org_id" json:"org_id"`
	AutomationID      uuid.UUID                    `db:"automation_id" json:"automation_id"`
	AutomationRunID   uuid.UUID                    `db:"automation_run_id" json:"automation_run_id"`
	SessionID         uuid.UUID                    `db:"session_id" json:"session_id"`
	Repository        string                       `db:"repository" json:"repository"`
	PullRequestNumber int                          `db:"pull_request_number" json:"pull_request_number"`
	PullRequestURL    string                       `db:"pull_request_url" json:"pull_request_url"`
	PullRequestTitle  *string                      `db:"pull_request_title" json:"pull_request_title,omitempty"`
	HeadSHA           *string                      `db:"head_sha" json:"head_sha,omitempty"`
	Decision          AutomationOutcomeDecision    `db:"decision" json:"decision"`
	Reason            string                       `db:"reason" json:"reason"`
	Source            AutomationOutcomeSource      `db:"source" json:"source"`
	ReportedAt        time.Time                    `db:"reported_at" json:"reported_at"`
	CreatedAt         time.Time                    `db:"created_at" json:"created_at"`
	ExternalAction    *AutomationRunExternalAction `json:"external_action,omitempty"`
}

type AutomationDecisionTarget struct {
	Repository        string  `json:"repository"`
	PullRequestNumber int     `json:"pull_request_number"`
	PullRequestURL    string  `json:"pull_request_url"`
	PullRequestTitle  *string `json:"pull_request_title,omitempty"`
	HeadSHA           *string `json:"head_sha,omitempty"`
}

// AutomationDecision is the latest execution for one PR revision. Execution
// lifecycle and business outcome intentionally remain separate.
type AutomationDecision struct {
	AutomationID    uuid.UUID                `json:"automation_id"`
	RunID           uuid.UUID                `json:"run_id"`
	SessionID       *uuid.UUID               `json:"session_id,omitempty"`
	Target          AutomationDecisionTarget `json:"target"`
	ExecutionStatus AutomationRunStatus      `json:"execution_status"`
	TriggeredAt     time.Time                `json:"triggered_at"`
	CompletedAt     *time.Time               `json:"completed_at,omitempty"`
	AttemptCount    int                      `json:"attempt_count"`
	Outcome         *AutomationRunOutcome    `json:"outcome,omitempty"`
}

type AutomationDecisionStats struct {
	UniquePullRequests int `json:"unique_pull_requests"`
	UniqueRevisions    int `json:"unique_revisions"`
	TotalRuns          int `json:"total_runs"`
	Evaluating         int `json:"evaluating"`
	Passed             int `json:"passed"`
	ChangesRequested   int `json:"changes_requested"`
	Advisory           int `json:"advisory"`
	NotApplicable      int `json:"not_applicable"`
	OutcomeNotReported int `json:"outcome_not_reported"`
	ExecutionFailed    int `json:"execution_failed"`
}
