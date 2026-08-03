package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const CodeReviewDisputeBodyMaxRunes = 8000

type CodeReviewRepositoryVisibility string

const (
	CodeReviewRepositoryVisibilityPublic  CodeReviewRepositoryVisibility = "public"
	CodeReviewRepositoryVisibilityPrivate CodeReviewRepositoryVisibility = "private"
	CodeReviewRepositoryVisibilityUnknown CodeReviewRepositoryVisibility = "unknown"
)

func (v CodeReviewRepositoryVisibility) Validate() error {
	switch v {
	case CodeReviewRepositoryVisibilityPublic, CodeReviewRepositoryVisibilityPrivate, CodeReviewRepositoryVisibilityUnknown:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewRepositoryVisibility: %q", v)
	}
}

type CodeReviewDisputeDirection string

const (
	CodeReviewDisputeDirectionShouldHaveApproved    CodeReviewDisputeDirection = "should_have_approved"
	CodeReviewDisputeDirectionShouldNotHaveApproved CodeReviewDisputeDirection = "should_not_have_approved"
)

func (d CodeReviewDisputeDirection) Validate() error {
	switch d {
	case CodeReviewDisputeDirectionShouldHaveApproved, CodeReviewDisputeDirectionShouldNotHaveApproved:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDisputeDirection: %q", d)
	}
}

type CodeReviewDisputeSource string

const (
	CodeReviewDisputeSourceGitHubComment CodeReviewDisputeSource = "github_comment"
	CodeReviewDisputeSourceAppUI         CodeReviewDisputeSource = "app_ui"
	CodeReviewDisputeSourceAPI           CodeReviewDisputeSource = "api"
	CodeReviewDisputeSourceSpotCheck     CodeReviewDisputeSource = "spot_check"
)

func (s CodeReviewDisputeSource) Validate() error {
	switch s {
	case CodeReviewDisputeSourceGitHubComment, CodeReviewDisputeSourceAppUI,
		CodeReviewDisputeSourceAPI, CodeReviewDisputeSourceSpotCheck:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDisputeSource: %q", s)
	}
}

type CodeReviewDisputeRouting string

const (
	CodeReviewDisputeRoutingReassess         CodeReviewDisputeRouting = "reassess"
	CodeReviewDisputeRoutingPolicySignalOnly CodeReviewDisputeRouting = "policy_signal_only"
	CodeReviewDisputeRoutingAnswerOnly       CodeReviewDisputeRouting = "answer_only"
	CodeReviewDisputeRoutingNotADispute      CodeReviewDisputeRouting = "not_a_dispute"
)

func (r CodeReviewDisputeRouting) Validate() error {
	switch r {
	case CodeReviewDisputeRoutingReassess, CodeReviewDisputeRoutingPolicySignalOnly,
		CodeReviewDisputeRoutingAnswerOnly, CodeReviewDisputeRoutingNotADispute:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDisputeRouting: %q", r)
	}
}

type CodeReviewDisputeIntakeStatus string

const (
	CodeReviewDisputeIntakePending   CodeReviewDisputeIntakeStatus = "pending"
	CodeReviewDisputeIntakeTriaged   CodeReviewDisputeIntakeStatus = "triaged"
	CodeReviewDisputeIntakeDiscarded CodeReviewDisputeIntakeStatus = "discarded"
	CodeReviewDisputeIntakeFailed    CodeReviewDisputeIntakeStatus = "failed"
)

func (s CodeReviewDisputeIntakeStatus) Validate() error {
	switch s {
	case CodeReviewDisputeIntakePending, CodeReviewDisputeIntakeTriaged,
		CodeReviewDisputeIntakeDiscarded, CodeReviewDisputeIntakeFailed:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDisputeIntakeStatus: %q", s)
	}
}

type CodeReviewDisputeAdjudicationStatus string

const (
	CodeReviewDisputeAdjudicationPending      CodeReviewDisputeAdjudicationStatus = "pending"
	CodeReviewDisputeAdjudicationUpheld       CodeReviewDisputeAdjudicationStatus = "upheld"
	CodeReviewDisputeAdjudicationRejected     CodeReviewDisputeAdjudicationStatus = "rejected"
	CodeReviewDisputeAdjudicationExpired      CodeReviewDisputeAdjudicationStatus = "expired"
	CodeReviewDisputeAdjudicationNeedsContext CodeReviewDisputeAdjudicationStatus = "needs_context"
)

func (s CodeReviewDisputeAdjudicationStatus) Validate() error {
	switch s {
	case CodeReviewDisputeAdjudicationPending, CodeReviewDisputeAdjudicationUpheld,
		CodeReviewDisputeAdjudicationRejected, CodeReviewDisputeAdjudicationExpired,
		CodeReviewDisputeAdjudicationNeedsContext:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDisputeAdjudicationStatus: %q", s)
	}
}

type CodeReviewDisputeReassessmentStatus string

const (
	CodeReviewDisputeReassessmentNotRequested CodeReviewDisputeReassessmentStatus = "not_requested"
	CodeReviewDisputeReassessmentQueued       CodeReviewDisputeReassessmentStatus = "queued"
	CodeReviewDisputeReassessmentRunning      CodeReviewDisputeReassessmentStatus = "running"
	CodeReviewDisputeReassessmentCompleted    CodeReviewDisputeReassessmentStatus = "completed"
	CodeReviewDisputeReassessmentDeduped      CodeReviewDisputeReassessmentStatus = "deduped"
	CodeReviewDisputeReassessmentHeadChanged  CodeReviewDisputeReassessmentStatus = "head_changed"
	CodeReviewDisputeReassessmentFailed       CodeReviewDisputeReassessmentStatus = "failed"
)

func (s CodeReviewDisputeReassessmentStatus) Validate() error {
	switch s {
	case CodeReviewDisputeReassessmentNotRequested, CodeReviewDisputeReassessmentQueued,
		CodeReviewDisputeReassessmentRunning, CodeReviewDisputeReassessmentCompleted,
		CodeReviewDisputeReassessmentDeduped, CodeReviewDisputeReassessmentHeadChanged,
		CodeReviewDisputeReassessmentFailed:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDisputeReassessmentStatus: %q", s)
	}
}

type CodeReviewDisputeReplyStatus string

const (
	CodeReviewDisputeReplyPending       CodeReviewDisputeReplyStatus = "pending"
	CodeReviewDisputeReplyNotApplicable CodeReviewDisputeReplyStatus = "not_applicable"
	CodeReviewDisputeReplyPublished     CodeReviewDisputeReplyStatus = "published"
	CodeReviewDisputeReplyFailed        CodeReviewDisputeReplyStatus = "failed"
)

func (s CodeReviewDisputeReplyStatus) Validate() error {
	switch s {
	case CodeReviewDisputeReplyPending, CodeReviewDisputeReplyNotApplicable,
		CodeReviewDisputeReplyPublished, CodeReviewDisputeReplyFailed:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDisputeReplyStatus: %q", s)
	}
}

type CodeReviewDispute struct {
	ID                        uuid.UUID                      `db:"id" json:"id"`
	OrgID                     uuid.UUID                      `db:"org_id" json:"org_id"`
	SessionID                 uuid.UUID                      `db:"session_id" json:"session_id"`
	PullRequestID             uuid.UUID                      `db:"pull_request_id" json:"pull_request_id"`
	RepositoryID              uuid.UUID                      `db:"repository_id" json:"repository_id"`
	PolicyID                  uuid.UUID                      `db:"policy_id" json:"policy_id"`
	ReviewedHeadSHA           string                         `db:"reviewed_head_sha" json:"reviewed_head_sha"`
	Decision                  CodeReviewDecision             `db:"decision" json:"decision"`
	Direction                 *CodeReviewDisputeDirection    `db:"direction" json:"direction,omitempty"`
	FiledByUserID             *uuid.UUID                     `db:"filed_by_user_id" json:"filed_by_user_id,omitempty"`
	FiledByLogin              string                         `db:"filed_by_login" json:"filed_by_login"`
	AuthorAssociation         string                         `db:"author_association" json:"author_association"`
	AuthorIsPRAuthor          bool                           `db:"author_is_pr_author" json:"author_is_pr_author"`
	RepositoryVisibility      CodeReviewRepositoryVisibility `db:"repository_visibility" json:"repository_visibility"`
	MembershipEvidence        json.RawMessage                `db:"membership_evidence" json:"-"`
	TrustOverride             *bool                          `db:"trust_override" json:"trust_override,omitempty"`
	Source                    CodeReviewDisputeSource        `db:"source" json:"source"`
	GitHubCommentID           *int64                         `db:"github_comment_id" json:"github_comment_id,omitempty"`
	GitHubThreadRootCommentID *int64                         `db:"github_thread_root_comment_id" json:"github_thread_root_comment_id,omitempty"`
	ReplyCommentID            *int64                         `db:"reply_comment_id" json:"reply_comment_id,omitempty"`
	SourceBodyHash            string                         `db:"source_body_hash" json:"-"`
	SourceVersion             int64                          `db:"source_version" json:"source_version"`
	// SourceUpdatedAt is the provider's last-edited time for the source
	// comment. It orders the disputes filed against one comment, which
	// SourceVersion cannot: that is a content hash.
	SourceUpdatedAt           *time.Time                           `db:"source_updated_at" json:"-"`
	Body                      string                               `db:"body" json:"body"`
	ContestedReasonCodes      []CodeReviewRiskReasonCode           `db:"contested_reason_codes" json:"contested_reason_codes"`
	DisputeKind               *string                              `db:"dispute_kind" json:"dispute_kind,omitempty"`
	AssertsNewInformation     *bool                                `db:"asserts_new_information" json:"asserts_new_information,omitempty"`
	Routing                   *CodeReviewDisputeRouting            `db:"routing" json:"routing,omitempty"`
	IntakeStatus              CodeReviewDisputeIntakeStatus        `db:"intake_status" json:"intake_status"`
	IntakeConfidence          *float64                             `db:"intake_confidence" json:"intake_confidence,omitempty"`
	ReassessmentSessionID     *uuid.UUID                           `db:"reassessment_session_id" json:"reassessment_session_id,omitempty"`
	ReassessmentDecision      *CodeReviewDecision                  `db:"reassessment_decision" json:"reassessment_decision,omitempty"`
	ReassessmentFlipped       *bool                                `db:"reassessment_flipped" json:"reassessment_flipped,omitempty"`
	ReassessmentStatus        CodeReviewDisputeReassessmentStatus  `db:"reassessment_status" json:"reassessment_status"`
	SemanticInputHashAtFiling string                               `db:"semantic_input_hash_at_filing" json:"-"`
	SemanticInputHashAtRerun  *string                              `db:"semantic_input_hash_at_rerun" json:"-"`
	AdjudicationStatus        *CodeReviewDisputeAdjudicationStatus `db:"adjudication_status" json:"adjudication_status,omitempty"`
	AdjudicatedByUserID       *uuid.UUID                           `db:"adjudicated_by_user_id" json:"adjudicated_by_user_id,omitempty"`
	AdjudicatedAt             *time.Time                           `db:"adjudicated_at" json:"adjudicated_at,omitempty"`
	AdjudicationNote          *string                              `db:"adjudication_note" json:"adjudication_note,omitempty"`
	EscalatedAt               *time.Time                           `db:"escalated_at" json:"escalated_at,omitempty"`
	EscalatedByUserID         *uuid.UUID                           `db:"escalated_by_user_id" json:"escalated_by_user_id,omitempty"`
	QueueSignals              json.RawMessage                      `db:"queue_signals" json:"queue_signals"`
	QueuePriority             float64                              `db:"queue_priority" json:"queue_priority"`
	ReplyStatus               CodeReviewDisputeReplyStatus         `db:"reply_status" json:"reply_status"`
	ReplyCycleReserved        bool                                 `db:"reply_cycle_reserved" json:"-"`
	// SupersededByDisputeID is set when a later edit of the same GitHub comment
	// replaced this objection. It is separate from ReplyStatus on purpose:
	// reassessment and triage transitions rewrite ReplyStatus, so a retirement
	// recorded there would be undone and the stale answer republished.
	SupersededByDisputeID      *uuid.UUID `db:"superseded_by_dispute_id" json:"superseded_by_dispute_id,omitempty"`
	StatusDetail               *string    `db:"status_detail" json:"status_detail,omitempty"`
	Version                    int        `db:"version" json:"version"`
	CreatedAt                  time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt                  time.Time  `db:"updated_at" json:"updated_at"`
	Trusted                    bool       `db:"-" json:"trusted"`
	CurrentAuthorizationReason string     `db:"-" json:"current_authorization_reason,omitempty"`
}

func (d CodeReviewDispute) CurrentTrust() (bool, string) {
	if d.TrustOverride != nil {
		if *d.TrustOverride {
			return true, "admin promoted this dispute"
		}
		return false, "admin demoted this dispute"
	}
	if d.RepositoryVisibility == CodeReviewRepositoryVisibilityPrivate {
		return true, "private repository contributor"
	}
	switch strings.ToUpper(strings.TrimSpace(d.AuthorAssociation)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true, "trusted GitHub association"
	default:
		return false, "external contributor"
	}
}

type CodeReviewDisputeAuthorizationAction string

const (
	CodeReviewDisputeAuthorizationRerun          CodeReviewDisputeAuthorizationAction = "rerun"
	CodeReviewDisputeAuthorizationQueueInfluence CodeReviewDisputeAuthorizationAction = "queue_influence"
	CodeReviewDisputeAuthorizationAdminPromotion CodeReviewDisputeAuthorizationAction = "admin_promotion"
)

func (a CodeReviewDisputeAuthorizationAction) Validate() error {
	switch a {
	case CodeReviewDisputeAuthorizationRerun, CodeReviewDisputeAuthorizationQueueInfluence,
		CodeReviewDisputeAuthorizationAdminPromotion:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewDisputeAuthorizationAction: %q", a)
	}
}

type CodeReviewDisputeAuthorization struct {
	ID               uuid.UUID                            `db:"id" json:"id"`
	OrgID            uuid.UUID                            `db:"org_id" json:"org_id"`
	DisputeID        uuid.UUID                            `db:"dispute_id" json:"dispute_id"`
	Action           CodeReviewDisputeAuthorizationAction `db:"action" json:"action"`
	Trusted          bool                                 `db:"trusted" json:"trusted"`
	ObservedInputs   json.RawMessage                      `db:"observed_inputs" json:"observed_inputs"`
	PolicyVersion    *int                                 `db:"policy_version" json:"policy_version,omitempty"`
	EvaluatorVersion string                               `db:"evaluator_version" json:"evaluator_version"`
	OverrideValue    *bool                                `db:"override_value" json:"override_value,omitempty"`
	OverrideByUserID *uuid.UUID                           `db:"override_by_user_id" json:"override_by_user_id,omitempty"`
	DecisionReason   string                               `db:"decision_reason" json:"decision_reason"`
	DecidedAt        time.Time                            `db:"decided_at" json:"decided_at"`
	CreatedAt        time.Time                            `db:"created_at" json:"created_at"`
}

type CodeReviewDisputeTriageResult struct {
	Direction             CodeReviewDisputeDirection `json:"direction"`
	ContestedReasonCodes  []CodeReviewRiskReasonCode `json:"contested_reason_codes"`
	DisputeKind           string                     `json:"dispute_kind"`
	AssertsNewInformation bool                       `json:"asserts_new_information"`
	Routing               CodeReviewDisputeRouting   `json:"routing"`
	Confidence            float64                    `json:"confidence"`
	Reply                 string                     `json:"reply"`
}

func (r CodeReviewDisputeTriageResult) Validate() error {
	if err := r.Direction.Validate(); err != nil {
		return err
	}
	if err := r.Routing.Validate(); err != nil {
		return err
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1")
	}
	for _, code := range r.ContestedReasonCodes {
		if err := code.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type CodeReviewDisputeListFilters struct {
	AdjudicationStatus *CodeReviewDisputeAdjudicationStatus
	RepositoryID       *uuid.UUID
	Direction          *CodeReviewDisputeDirection
	Cursor             *uuid.UUID
	Limit              int
}

type CodeReviewDisputePage struct {
	Items      []CodeReviewDispute
	NextCursor *uuid.UUID
}

type CodeReviewDisputeAdjudicationUpdate struct {
	ExpectedVersion      int
	AdjudicationStatus   *CodeReviewDisputeAdjudicationStatus
	AdjudicationNote     *string
	TrustOverride        *bool
	TrustOverridePresent bool
}
