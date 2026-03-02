package models

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// PMPlanStatus captures the lifecycle of a PM plan.
type PMPlanStatus string

const (
	PMPlanStatusExecuting PMPlanStatus = "executing"
	PMPlanStatusCompleted PMPlanStatus = "completed"
	PMPlanStatusFailed    PMPlanStatus = "failed"
)

func (s PMPlanStatus) Validate() error {
	switch s {
	case PMPlanStatusExecuting, PMPlanStatusCompleted, PMPlanStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid PMPlanStatus: %q", s)
	}
}

func (s PMPlanStatus) TextValue() (pgtype.Text, error) {
	return pgtype.Text{String: string(s), Valid: true}, nil
}

func (s *PMPlanStatus) ScanText(v pgtype.Text) error {
	if !v.Valid {
		*s = ""
		return nil
	}
	*s = PMPlanStatus(v.String)
	return nil
}

// PMTaskStatus indicates how a PM task was handled.
type PMTaskStatus string

const (
	PMTaskStatusPending         PMTaskStatus = "pending"
	PMTaskStatusDelegated       PMTaskStatus = "delegated"
	PMTaskStatusSkippedCapacity PMTaskStatus = "skipped_capacity"
)

func (s PMTaskStatus) Validate() error {
	switch s {
	case PMTaskStatusPending, PMTaskStatusDelegated, PMTaskStatusSkippedCapacity:
		return nil
	default:
		return fmt.Errorf("invalid PMTaskStatus: %q", s)
	}
}

// PMTaskComplexity is the PM agent's complexity label.
type PMTaskComplexity string

const (
	PMTaskComplexityTrivial  PMTaskComplexity = "trivial"
	PMTaskComplexitySimple   PMTaskComplexity = "simple"
	PMTaskComplexityModerate PMTaskComplexity = "moderate"
	PMTaskComplexityComplex  PMTaskComplexity = "complex"
)

func (s PMTaskComplexity) Validate() error {
	switch s {
	case PMTaskComplexityTrivial, PMTaskComplexitySimple, PMTaskComplexityModerate, PMTaskComplexityComplex:
		return nil
	default:
		return fmt.Errorf("invalid PMTaskComplexity: %q", s)
	}
}

// PMTaskConfidence captures the PM agent's confidence in the task.
type PMTaskConfidence string

const (
	PMTaskConfidenceHigh   PMTaskConfidence = "high"
	PMTaskConfidenceMedium PMTaskConfidence = "medium"
	PMTaskConfidenceLow    PMTaskConfidence = "low"
)

func (s PMTaskConfidence) Validate() error {
	switch s {
	case PMTaskConfidenceHigh, PMTaskConfidenceMedium, PMTaskConfidenceLow:
		return nil
	default:
		return fmt.Errorf("invalid PMTaskConfidence: %q", s)
	}
}

// PMSkipReason is the reason a PM task was skipped.
type PMSkipReason string

const (
	PMSkipReasonDuplicate          PMSkipReason = "duplicate"
	PMSkipReasonNeedsHumanDecision PMSkipReason = "needs_human_decision"
	PMSkipReasonTooComplex         PMSkipReason = "too_complex"
	PMSkipReasonMisaligned         PMSkipReason = "misaligned"
	PMSkipReasonInAvoidArea        PMSkipReason = "in_avoid_area"
	PMSkipReasonAlreadyInFlight    PMSkipReason = "already_in_flight"
)

func (s PMSkipReason) Validate() error {
	switch s {
	case PMSkipReasonDuplicate,
		PMSkipReasonNeedsHumanDecision,
		PMSkipReasonTooComplex,
		PMSkipReasonMisaligned,
		PMSkipReasonInAvoidArea,
		PMSkipReasonAlreadyInFlight:
		return nil
	default:
		return fmt.Errorf("invalid PMSkipReason: %q", s)
	}
}

// PMDecisionType records the PM agent's decision.
type PMDecisionType string

const (
	PMDecisionTypeDelegate PMDecisionType = "delegate"
	PMDecisionTypeSkip     PMDecisionType = "skip"
	PMDecisionTypeCluster  PMDecisionType = "cluster"
)

func (s PMDecisionType) Validate() error {
	switch s {
	case PMDecisionTypeDelegate, PMDecisionTypeSkip, PMDecisionTypeCluster:
		return nil
	default:
		return fmt.Errorf("invalid PMDecisionType: %q", s)
	}
}

func (s PMDecisionType) TextValue() (pgtype.Text, error) {
	return pgtype.Text{String: string(s), Valid: true}, nil
}

func (s *PMDecisionType) ScanText(v pgtype.Text) error {
	if !v.Valid {
		*s = ""
		return nil
	}
	*s = PMDecisionType(v.String)
	return nil
}

// PMDecisionOutcome records the eventual outcome for a PM decision.
type PMDecisionOutcome string

const (
	PMDecisionOutcomeSucceeded PMDecisionOutcome = "succeeded"
	PMDecisionOutcomeFailed    PMDecisionOutcome = "failed"
	PMDecisionOutcomeStillOpen PMDecisionOutcome = "still_open"
)

func (s PMDecisionOutcome) Validate() error {
	switch s {
	case PMDecisionOutcomeSucceeded, PMDecisionOutcomeFailed, PMDecisionOutcomeStillOpen:
		return nil
	default:
		return fmt.Errorf("invalid PMDecisionOutcome: %q", s)
	}
}

func (s PMDecisionOutcome) TextValue() (pgtype.Text, error) {
	if s == "" {
		return pgtype.Text{Valid: false}, nil
	}
	return pgtype.Text{String: string(s), Valid: true}, nil
}

func (s *PMDecisionOutcome) ScanText(v pgtype.Text) error {
	if !v.Valid {
		*s = ""
		return nil
	}
	*s = PMDecisionOutcome(v.String)
	return nil
}

// PMTrigger tracks how a PM analysis was initiated.
type PMTrigger string

const (
	PMTriggerCron   PMTrigger = "cron"
	PMTriggerManual PMTrigger = "manual"
)

func (s PMTrigger) Validate() error {
	switch s {
	case PMTriggerCron, PMTriggerManual:
		return nil
	default:
		return fmt.Errorf("invalid PMTrigger: %q", s)
	}
}

func (s PMTrigger) TextValue() (pgtype.Text, error) {
	return pgtype.Text{String: string(s), Valid: true}, nil
}

func (s *PMTrigger) ScanText(v pgtype.Text) error {
	if !v.Valid {
		*s = ""
		return nil
	}
	*s = PMTrigger(v.String)
	return nil
}
