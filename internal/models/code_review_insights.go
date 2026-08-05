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

type CodeReviewInsightDirectionCount struct {
	Direction CodeReviewDisputeDirection `json:"direction"`
	Count     int64                      `json:"count"`
}

type CodeReviewInsightKindCount struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

type CodeReviewInsightPolicyMix struct {
	PolicyID      uuid.UUID          `json:"policy_id"`
	PolicyVersion int                `json:"policy_version"`
	Decision      CodeReviewDecision `json:"decision"`
	Count         int64              `json:"count"`
}

type CodeReviewInsightReasonCount struct {
	ReasonCode  CodeReviewRiskReasonCode `json:"reason_code"`
	Decisions   int64                    `json:"decisions"`
	Disputes    int64                    `json:"disputes"`
	DisputeRate float64                  `json:"dispute_rate"`
}

type CodeReviewInsightInputChange string

const (
	CodeReviewInsightInputChangeChanged   CodeReviewInsightInputChange = "changed"
	CodeReviewInsightInputChangeUnchanged CodeReviewInsightInputChange = "unchanged"
	CodeReviewInsightInputChangeUnknown   CodeReviewInsightInputChange = "unknown"
)

func (value CodeReviewInsightInputChange) Validate() error {
	switch value {
	case CodeReviewInsightInputChangeChanged, CodeReviewInsightInputChangeUnchanged, CodeReviewInsightInputChangeUnknown:
		return nil
	default:
		return fmt.Errorf("invalid code review insight input change: %q", value)
	}
}

type CodeReviewInsightFlipBucket struct {
	Attempt       int                          `json:"attempt"`
	InputChange   CodeReviewInsightInputChange `json:"input_change"`
	Reassessments int64                        `json:"reassessments"`
	Flips         int64                        `json:"flips"`
}

type CodeReviewInsightActualLimit struct {
	ReasonCode CodeReviewRiskReasonCode `json:"reason_code"`
	Actual     int                      `json:"actual"`
	Limit      int                      `json:"limit"`
	Count      int64                    `json:"count"`
}

type CodeReviewInsights struct {
	Decisions                       int64                             `json:"decisions"`
	Disputes                        int64                             `json:"disputes"`
	ObjectionRate                   float64                           `json:"objection_rate"`
	UpheldDisputes                  int64                             `json:"upheld_disputes"`
	Reassessments                   int64                             `json:"reassessments"`
	ReassessmentFlips               int64                             `json:"reassessment_flips"`
	ReassessmentCostUSD             float64                           `json:"reassessment_cost_usd"`
	PolicyOwnerMinutesPerResolution *float64                          `json:"policy_owner_minutes_per_resolution,omitempty"`
	MedianDecisionSeconds           *float64                          `json:"median_decision_seconds,omitempty"`
	MedianAdjudicationSeconds       *float64                          `json:"median_adjudication_seconds,omitempty"`
	ProjectionFreshThrough          *time.Time                        `json:"projection_fresh_through,omitempty"`
	ProjectionUpdatedAt             *time.Time                        `json:"projection_updated_at,omitempty"`
	RankingEnabled                  bool                              `json:"ranking_enabled"`
	Directions                      []CodeReviewInsightDirectionCount `json:"directions"`
	DisputeKinds                    []CodeReviewInsightKindCount      `json:"dispute_kinds"`
	PolicyDecisionMix               []CodeReviewInsightPolicyMix      `json:"policy_decision_mix"`
	Reasons                         []CodeReviewInsightReasonCount    `json:"reasons"`
	ActualVsLimit                   []CodeReviewInsightActualLimit    `json:"actual_vs_limit"`
	FlipBuckets                     []CodeReviewInsightFlipBucket     `json:"flip_buckets"`
}

type CodeReviewInsightFilters struct {
	RepositoryID *uuid.UUID
	From         *time.Time
	To           *time.Time
	Decision     *CodeReviewDecision
	ReasonCode   *CodeReviewRiskReasonCode
	Direction    *CodeReviewDisputeDirection
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
