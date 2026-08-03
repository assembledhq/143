package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type PreviewVerificationStatus string

const (
	PreviewVerificationStatusRunning                   PreviewVerificationStatus = "running"
	PreviewVerificationStatusPassed                    PreviewVerificationStatus = "passed"
	PreviewVerificationStatusFailed                    PreviewVerificationStatus = "failed"
	PreviewVerificationStatusSkipped                   PreviewVerificationStatus = "skipped"
	PreviewVerificationStatusHumanInterventionRequired PreviewVerificationStatus = "human_intervention_required"
)

func (s PreviewVerificationStatus) Validate() error {
	switch s {
	case PreviewVerificationStatusRunning, PreviewVerificationStatusPassed,
		PreviewVerificationStatusFailed, PreviewVerificationStatusSkipped,
		PreviewVerificationStatusHumanInterventionRequired:
		return nil
	default:
		return fmt.Errorf("invalid PreviewVerificationStatus: %q", s)
	}
}

type PreviewVerificationTrigger string

const (
	PreviewVerificationTriggerAutomatic PreviewVerificationTrigger = "automatic"
	PreviewVerificationTriggerRequested PreviewVerificationTrigger = "requested"
)

func (t PreviewVerificationTrigger) Validate() error {
	switch t {
	case PreviewVerificationTriggerAutomatic, PreviewVerificationTriggerRequested:
		return nil
	default:
		return fmt.Errorf("invalid PreviewVerificationTrigger: %q", t)
	}
}

type PreviewVerificationPlanStep struct {
	Path     string       `json:"path"`
	Viewport ViewportSpec `json:"viewport"`
}

type PreviewVerificationStep struct {
	Index        int             `json:"index"`
	Attempt      int             `json:"attempt"`
	Path         string          `json:"path"`
	Viewport     ViewportSpec    `json:"viewport"`
	Outcome      string          `json:"outcome"`
	Error        string          `json:"error,omitempty"`
	Capture      *PreviewCapture `json:"capture,omitempty"`
	ConsoleCount int             `json:"console_error_count"`
}

// MarshalJSON preserves the former per-step key for clients that can outlive
// an application rollout.
func (s PreviewVerificationStep) MarshalJSON() ([]byte, error) {
	type previewVerificationStepAlias PreviewVerificationStep
	return json.Marshal(struct {
		previewVerificationStepAlias
		LegacyCapture *PreviewCapture `json:"artifact,omitempty"`
	}{
		previewVerificationStepAlias: previewVerificationStepAlias(s),
		LegacyCapture:                s.Capture,
	})
}

// UnmarshalJSON accepts historical verification steps as well as new ones.
func (s *PreviewVerificationStep) UnmarshalJSON(data []byte) error {
	type previewVerificationStepAlias PreviewVerificationStep
	decoded := struct {
		*previewVerificationStepAlias
		LegacyCapture *PreviewCapture `json:"artifact,omitempty"`
	}{previewVerificationStepAlias: (*previewVerificationStepAlias)(s)}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if s.Capture == nil {
		s.Capture = decoded.LegacyCapture
	}
	return nil
}

type PreviewVerificationRun struct {
	ID                uuid.UUID                  `db:"id" json:"id"`
	OrgID             uuid.UUID                  `db:"org_id" json:"org_id"`
	SessionID         uuid.UUID                  `db:"session_id" json:"session_id"`
	PreviewInstanceID *uuid.UUID                 `db:"preview_instance_id" json:"preview_instance_id,omitempty"`
	WorkspaceRevision int64                      `db:"workspace_revision" json:"workspace_revision"`
	ConfigDigest      string                     `db:"config_digest" json:"config_digest"`
	Trigger           PreviewVerificationTrigger `db:"trigger" json:"trigger"`
	Status            PreviewVerificationStatus  `db:"status" json:"status"`
	Attempt           int                        `db:"attempt" json:"attempt"`
	MaxAttempts       int                        `db:"max_attempts" json:"max_attempts"`
	Plan              json.RawMessage            `db:"plan" json:"plan"`
	Steps             json.RawMessage            `db:"steps" json:"steps"`
	Captures          json.RawMessage            `db:"captures" json:"captures"`
	ConsoleErrorCount int                        `db:"console_error_count" json:"console_error_count"`
	Summary           string                     `db:"summary" json:"summary"`
	FailureReason     string                     `db:"failure_reason" json:"failure_reason,omitempty"`
	SkipReason        string                     `db:"skip_reason" json:"skip_reason,omitempty"`
	StartedAt         time.Time                  `db:"started_at" json:"started_at"`
	CompletedAt       *time.Time                 `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt         time.Time                  `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time                  `db:"updated_at" json:"updated_at"`
}

// MarshalJSON keeps the former top-level screenshot collection available to
// clients that are still inside the supported rollout window.
func (r PreviewVerificationRun) MarshalJSON() ([]byte, error) {
	type previewVerificationRunAlias PreviewVerificationRun
	return json.Marshal(struct {
		previewVerificationRunAlias
		LegacyCaptures json.RawMessage `json:"artifacts"`
	}{
		previewVerificationRunAlias: previewVerificationRunAlias(r),
		LegacyCaptures:              r.Captures,
	})
}

// UnmarshalJSON accepts verification history returned by a draining API
// generation that has not switched its top-level collection key yet.
func (r *PreviewVerificationRun) UnmarshalJSON(data []byte) error {
	type previewVerificationRunAlias PreviewVerificationRun
	decoded := struct {
		*previewVerificationRunAlias
		LegacyCaptures json.RawMessage `json:"artifacts"`
	}{previewVerificationRunAlias: (*previewVerificationRunAlias)(r)}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if len(r.Captures) == 0 || string(r.Captures) == "null" {
		r.Captures = decoded.LegacyCaptures
	}
	return nil
}
