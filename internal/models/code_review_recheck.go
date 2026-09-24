package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type CodeReviewRecheckDispatchStatus string

const (
	CodeReviewRecheckDispatchPending   CodeReviewRecheckDispatchStatus = "pending"
	CodeReviewRecheckDispatchRunning   CodeReviewRecheckDispatchStatus = "running"
	CodeReviewRecheckDispatchCompleted CodeReviewRecheckDispatchStatus = "completed"
	CodeReviewRecheckDispatchFailed    CodeReviewRecheckDispatchStatus = "failed"
	CodeReviewRecheckDispatchCancelled CodeReviewRecheckDispatchStatus = "cancelled"
)

func (s CodeReviewRecheckDispatchStatus) Validate() error {
	switch s {
	case CodeReviewRecheckDispatchPending, CodeReviewRecheckDispatchRunning, CodeReviewRecheckDispatchCompleted, CodeReviewRecheckDispatchFailed, CodeReviewRecheckDispatchCancelled:
		return nil
	default:
		return fmt.Errorf("invalid code review recheck dispatch status %q", s)
	}
}

type SessionCodeReviewOwnedError struct{ PullRequestID uuid.UUID }

func (e *SessionCodeReviewOwnedError) Error() string {
	return fmt.Sprintf("session belongs to code review for pull request %s", e.PullRequestID)
}

// CodeReviewRecheckDispatch is the durable outbox and exact-turn receipt for
// one evidence-only assessment. The assessment remains the decision identity.
type CodeReviewRecheckDispatch struct {
	AssessmentID      uuid.UUID                       `db:"assessment_id" json:"assessment_id"`
	OrgID             uuid.UUID                       `db:"org_id" json:"-"`
	RepositoryID      uuid.UUID                       `db:"repository_id" json:"repository_id"`
	PullRequestID     uuid.UUID                       `db:"pull_request_id" json:"pull_request_id"`
	SessionID         uuid.UUID                       `db:"session_id" json:"session_id"`
	ThreadID          uuid.UUID                       `db:"thread_id" json:"thread_id"`
	ExpectedTurn      int                             `db:"expected_turn" json:"expected_turn"`
	PayloadDigest     string                          `db:"payload_digest" json:"payload_digest"`
	MessageID         int64                           `db:"message_id" json:"message_id"`
	JobID             uuid.UUID                       `db:"job_id" json:"job_id"`
	Status            CodeReviewRecheckDispatchStatus `db:"status" json:"status"`
	AttemptLockToken  *uuid.UUID                      `db:"attempt_lock_token" json:"-"`
	ResultMessageID   *int64                          `db:"result_message_id" json:"result_message_id,omitempty"`
	ProviderSessionID *string                         `db:"provider_session_id" json:"provider_session_id,omitempty"`
	SnapshotKey       *string                         `db:"snapshot_key" json:"snapshot_key,omitempty"`
	NativeContext     *bool                           `db:"native_context" json:"native_context,omitempty"`
	FailureDetail     *string                         `db:"failure_detail" json:"failure_detail,omitempty"`
	AttemptUsage      json.RawMessage                 `db:"attempt_usage" json:"attempt_usage,omitempty"`
	CompletedAt       *time.Time                      `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt         time.Time                       `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time                       `db:"updated_at" json:"updated_at"`
}

type CodeReviewRecheckDispatchInput struct {
	OrgID         uuid.UUID
	RepositoryID  uuid.UUID
	PullRequestID uuid.UUID
	AssessmentID  uuid.UUID
	SessionID     uuid.UUID
	ThreadID      uuid.UUID
	ExpectedTurn  int
	Prompt        string
	ImageURLs     []string
}

type CodeReviewRecheckTurnCompletion struct {
	OrgID                uuid.UUID
	AssessmentID         uuid.UUID
	SessionID            uuid.UUID
	ThreadID             uuid.UUID
	JobID                uuid.UUID
	LockToken            uuid.UUID
	ExpectedTurn         int
	SessionTurn          int
	Summary              string
	Result               *SessionResult
	ProviderSessionID    string
	ParentAgentSessionID string
	SnapshotKey          string
	NativeContext        bool
	TokenUsage           json.RawMessage
}
