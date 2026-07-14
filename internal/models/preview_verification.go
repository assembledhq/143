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
	Index        int              `json:"index"`
	Path         string           `json:"path"`
	Viewport     ViewportSpec     `json:"viewport"`
	Outcome      string           `json:"outcome"`
	Error        string           `json:"error,omitempty"`
	Artifact     *PreviewArtifact `json:"artifact,omitempty"`
	ConsoleCount int              `json:"console_error_count"`
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
	Artifacts         json.RawMessage            `db:"artifacts" json:"artifacts"`
	ConsoleErrorCount int                        `db:"console_error_count" json:"console_error_count"`
	Summary           string                     `db:"summary" json:"summary"`
	FailureReason     string                     `db:"failure_reason" json:"failure_reason,omitempty"`
	SkipReason        string                     `db:"skip_reason" json:"skip_reason,omitempty"`
	StartedAt         time.Time                  `db:"started_at" json:"started_at"`
	CompletedAt       *time.Time                 `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt         time.Time                  `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time                  `db:"updated_at" json:"updated_at"`
}
