package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ReviewLoopStatus string

const (
	ReviewLoopStatusRunning            ReviewLoopStatus = "running"
	ReviewLoopStatusClean              ReviewLoopStatus = "clean"
	ReviewLoopStatusNeedsHumanDecision ReviewLoopStatus = "needs_human_decision"
	ReviewLoopStatusFailed             ReviewLoopStatus = "failed"
	ReviewLoopStatusCancelled          ReviewLoopStatus = "cancelled"
)

func (s ReviewLoopStatus) Validate() error {
	switch s {
	case ReviewLoopStatusRunning,
		ReviewLoopStatusClean,
		ReviewLoopStatusNeedsHumanDecision,
		ReviewLoopStatusFailed,
		ReviewLoopStatusCancelled:
		return nil
	default:
		return fmt.Errorf("invalid ReviewLoopStatus: %q", s)
	}
}

type ReviewLoopSource string

const (
	ReviewLoopSourceManual     ReviewLoopSource = "manual"
	ReviewLoopSourceAutomation ReviewLoopSource = "automation"
)

func (s ReviewLoopSource) Validate() error {
	switch s {
	case ReviewLoopSourceManual, ReviewLoopSourceAutomation:
		return nil
	default:
		return fmt.Errorf("invalid ReviewLoopSource: %q", s)
	}
}

type ReviewLoopFixMode string

const (
	ReviewLoopFixModeMinimal    ReviewLoopFixMode = "minimal"
	ReviewLoopFixModeExhaustive ReviewLoopFixMode = "exhaustive"
)

func (m ReviewLoopFixMode) Validate() error {
	switch m {
	case ReviewLoopFixModeMinimal, ReviewLoopFixModeExhaustive:
		return nil
	default:
		return fmt.Errorf("invalid ReviewLoopFixMode: %q", m)
	}
}

type ReviewLoopPassStatus string

const (
	ReviewLoopPassStatusReviewing ReviewLoopPassStatus = "reviewing"
	ReviewLoopPassStatusDeciding  ReviewLoopPassStatus = "deciding"
	ReviewLoopPassStatusFixing    ReviewLoopPassStatus = "fixing"
	ReviewLoopPassStatusClean     ReviewLoopPassStatus = "clean"
	ReviewLoopPassStatusNeedsFix  ReviewLoopPassStatus = "needs_fix"
	ReviewLoopPassStatusFailed    ReviewLoopPassStatus = "failed"
)

func (s ReviewLoopPassStatus) Validate() error {
	switch s {
	case ReviewLoopPassStatusReviewing,
		ReviewLoopPassStatusDeciding,
		ReviewLoopPassStatusFixing,
		ReviewLoopPassStatusClean,
		ReviewLoopPassStatusNeedsFix,
		ReviewLoopPassStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid ReviewLoopPassStatus: %q", s)
	}
}

type ReviewLoopDecision string

const (
	ReviewLoopDecisionClean    ReviewLoopDecision = "REVIEW_CLEAN"
	ReviewLoopDecisionNeedsFix ReviewLoopDecision = "NEEDS_FIX_PASS"
)

func (d ReviewLoopDecision) Validate() error {
	switch d {
	case ReviewLoopDecisionClean, ReviewLoopDecisionNeedsFix:
		return nil
	default:
		return fmt.Errorf("invalid ReviewLoopDecision: %q", d)
	}
}

type SessionReviewLoop struct {
	ID                     uuid.UUID         `db:"id" json:"id"`
	OrgID                  uuid.UUID         `db:"org_id" json:"org_id"`
	SessionID              uuid.UUID         `db:"session_id" json:"session_id"`
	AutomationRunID        *uuid.UUID        `db:"automation_run_id" json:"automation_run_id,omitempty"`
	ThreadID               *uuid.UUID        `db:"thread_id" json:"thread_id,omitempty"`
	Status                 ReviewLoopStatus  `db:"status" json:"status"`
	Source                 ReviewLoopSource  `db:"source" json:"source"`
	AgentType              AgentType         `db:"agent_type" json:"agent_type"`
	MaxPasses              int               `db:"max_passes" json:"max_passes"`
	FixMode                ReviewLoopFixMode `db:"fix_mode" json:"fix_mode"`
	CompletedPasses        int               `db:"completed_passes" json:"completed_passes"`
	ReviewRequired         bool              `db:"review_required" json:"review_required"`
	BypassedByUserID       *uuid.UUID        `db:"bypassed_by_user_id" json:"bypassed_by_user_id,omitempty"`
	BypassReason           *string           `db:"bypass_reason" json:"bypass_reason,omitempty"`
	LoopStartCheckpointKey *string           `db:"loop_start_checkpoint_key" json:"loop_start_checkpoint_key,omitempty"`
	LatestCheckpointKey    *string           `db:"latest_checkpoint_key" json:"latest_checkpoint_key,omitempty"`
	LatestSummary          *string           `db:"latest_summary" json:"latest_summary,omitempty"`
	StartedByUserID        *uuid.UUID        `db:"started_by_user_id" json:"started_by_user_id,omitempty"`
	StartedAt              time.Time         `db:"started_at" json:"started_at"`
	CompletedAt            *time.Time        `db:"completed_at" json:"completed_at,omitempty"`
}

type SessionReviewLoopPass struct {
	ID                uuid.UUID            `db:"id" json:"id"`
	OrgID             uuid.UUID            `db:"org_id" json:"org_id"`
	LoopID            uuid.UUID            `db:"loop_id" json:"loop_id"`
	SessionID         uuid.UUID            `db:"session_id" json:"session_id"`
	PassIndex         int                  `db:"pass_index" json:"pass_index"`
	ReviewMessageID   *int64               `db:"review_message_id" json:"review_message_id,omitempty"`
	DecisionMessageID *int64               `db:"decision_message_id" json:"decision_message_id,omitempty"`
	FixMessageID      *int64               `db:"fix_message_id" json:"fix_message_id,omitempty"`
	Status            ReviewLoopPassStatus `db:"status" json:"status"`
	AgentDecision     *ReviewLoopDecision  `db:"agent_decision" json:"agent_decision,omitempty"`
	ReviewOutput      *string              `db:"review_output" json:"review_output,omitempty"`
	FixSummary        *string              `db:"fix_summary" json:"fix_summary,omitempty"`
	ReviewStartedAt   *time.Time           `db:"review_started_at" json:"review_started_at,omitempty"`
	ReviewCompletedAt *time.Time           `db:"review_completed_at" json:"review_completed_at,omitempty"`
	FixStartedAt      *time.Time           `db:"fix_started_at" json:"fix_started_at,omitempty"`
	FixCompletedAt    *time.Time           `db:"fix_completed_at" json:"fix_completed_at,omitempty"`
	Summary           *string              `db:"summary" json:"summary,omitempty"`
}

func AgentSupportsNativeReview(agentType AgentType) bool {
	switch agentType {
	case AgentTypeCodex, AgentTypeClaudeCode, AgentTypeAmp, AgentTypePi:
		return true
	default:
		return false
	}
}
