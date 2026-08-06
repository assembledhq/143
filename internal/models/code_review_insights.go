package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type CodeReviewDecisionOutcome struct {
	OrgID                          uuid.UUID                  `db:"org_id" json:"org_id"`
	SessionID                      uuid.UUID                  `db:"session_id" json:"session_id"`
	PullRequestID                  uuid.UUID                  `db:"pull_request_id" json:"pull_request_id"`
	RepositoryID                   uuid.UUID                  `db:"repository_id" json:"repository_id"`
	PolicyID                       uuid.UUID                  `db:"policy_id" json:"policy_id"`
	Decision                       CodeReviewDecision         `db:"decision" json:"decision"`
	ReasonCodes                    []CodeReviewRiskReasonCode `db:"reason_codes" json:"reason_codes"`
	Merged                         bool                       `db:"merged" json:"merged"`
	MergedAt                       *time.Time                 `db:"merged_at" json:"merged_at,omitempty"`
	IndependentApproverLogin       *string                    `db:"independent_approver_login" json:"independent_approver_login,omitempty"`
	IndependentBlockingReviewLogin *string                    `db:"independent_blocking_review_login" json:"independent_blocking_review_login,omitempty"`
	HumanReviewCommentCount        int                        `db:"human_review_comment_count" json:"human_review_comment_count"`
	Terminal                       bool                       `db:"terminal" json:"terminal"`
	ObservedUntil                  time.Time                  `db:"observed_until" json:"observed_until"`
	ProviderReconcileAttemptedAt   *time.Time                 `db:"provider_reconcile_attempted_at" json:"provider_reconcile_attempted_at,omitempty"`
	ProjectionUpdatedAt            time.Time                  `db:"projection_updated_at" json:"projection_updated_at"`
	CreatedAt                      time.Time                  `db:"created_at" json:"created_at"`
}

type CodeReviewHumanReviewObservation struct {
	GitHubReviewID    int64     `json:"github_review_id"`
	ReviewerLogin     string    `json:"reviewer_login"`
	ReviewerType      string    `json:"reviewer_type"`
	AuthorAssociation string    `json:"author_association"`
	State             string    `json:"state"`
	SubmittedAt       time.Time `json:"submitted_at"`
}

type CodeReviewOutcomeSnapshot struct {
	AuthorLogin string                             `json:"author_login"`
	State       string                             `json:"state"`
	Merged      bool                               `json:"merged"`
	MergedAt    *time.Time                         `json:"merged_at,omitempty"`
	ObservedAt  time.Time                          `json:"observed_at"`
	Reviews     []CodeReviewHumanReviewObservation `json:"reviews"`
}

type CodeReviewOutcomePullRequest struct {
	PullRequestID  uuid.UUID `db:"pull_request_id"`
	RepositoryID   uuid.UUID `db:"repository_id"`
	GitHubPRNumber int       `db:"github_pr_number"`
}

type CodeReviewQueueSignals struct {
	IndependentHumanContradiction bool      `json:"independent_human_contradiction"`
	IndependentHumanLogin         string    `json:"independent_human_login,omitempty"`
	ReassessmentUnchanged         bool      `json:"reassessment_unchanged"`
	ReassessmentFlipped           bool      `json:"reassessment_flipped"`
	FilerIsNotPRAuthor            bool      `json:"filer_is_not_pr_author"`
	RepeatReasonDisputes14Days    int       `json:"repeat_reason_disputes_14_days"`
	Escalated                     bool      `json:"escalated"`
	BasePolicySuperseded          bool      `json:"base_policy_superseded"`
	OutcomeFresh                  bool      `json:"outcome_fresh"`
	RankingEnabled                bool      `json:"ranking_enabled"`
	ComputedAt                    time.Time `json:"computed_at"`
}

type CodeReviewRankUpdate struct {
	ID       uuid.UUID              `json:"id"`
	Signals  CodeReviewQueueSignals `json:"signals"`
	Priority float64                `json:"priority"`
}

type CodeReviewInsightPayload struct {
	OrgID uuid.UUID `json:"org_id"`
}

func (p CodeReviewInsightPayload) Validate() error {
	if p.OrgID == uuid.Nil {
		return fmt.Errorf("org_id is required")
	}
	return nil
}
