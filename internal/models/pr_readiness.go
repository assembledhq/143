package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type PRReadinessRunStatus string

const (
	PRReadinessRunStatusQueued   PRReadinessRunStatus = "queued"
	PRReadinessRunStatusRunning  PRReadinessRunStatus = "running"
	PRReadinessRunStatusPassed   PRReadinessRunStatus = "passed"
	PRReadinessRunStatusWarnings PRReadinessRunStatus = "warnings"
	PRReadinessRunStatusBlocked  PRReadinessRunStatus = "blocked"
	PRReadinessRunStatusFailed   PRReadinessRunStatus = "failed"
)

func (s PRReadinessRunStatus) Validate() error {
	switch s {
	case PRReadinessRunStatusQueued, PRReadinessRunStatusRunning, PRReadinessRunStatusPassed,
		PRReadinessRunStatusWarnings, PRReadinessRunStatusBlocked, PRReadinessRunStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid PRReadinessRunStatus: %q", s)
	}
}

type PRReadinessCheckStatus string

const (
	PRReadinessCheckStatusPassed  PRReadinessCheckStatus = "passed"
	PRReadinessCheckStatusWarning PRReadinessCheckStatus = "warning"
	PRReadinessCheckStatusFailed  PRReadinessCheckStatus = "failed"
	PRReadinessCheckStatusSkipped PRReadinessCheckStatus = "skipped"
)

func (s PRReadinessCheckStatus) Validate() error {
	switch s {
	case PRReadinessCheckStatusPassed, PRReadinessCheckStatusWarning, PRReadinessCheckStatusFailed, PRReadinessCheckStatusSkipped:
		return nil
	default:
		return fmt.Errorf("invalid PRReadinessCheckStatus: %q", s)
	}
}

type PRReadinessCheckType string

const (
	PRReadinessCheckTypeFreshness             PRReadinessCheckType = "freshness"
	PRReadinessCheckTypeAgentReviewClean      PRReadinessCheckType = "agent_review_clean"
	PRReadinessCheckTypeDiffCollected         PRReadinessCheckType = "diff_collected"
	PRReadinessCheckTypeTestEvidencePresent   PRReadinessCheckType = "test_evidence_present"
	PRReadinessCheckTypeRiskFlags             PRReadinessCheckType = "risk_flags"
	PRReadinessCheckTypeDependencyConfigRisk  PRReadinessCheckType = "dependency_config_risk"
	PRReadinessCheckTypeGeneratedFileChurn    PRReadinessCheckType = "generated_file_churn"
	PRReadinessCheckTypeContextComplete       PRReadinessCheckType = "context_complete"
	PRReadinessCheckTypeReviewPacketDraftable PRReadinessCheckType = "review_packet_draftable"
)

func (t PRReadinessCheckType) Validate() error {
	switch t {
	case PRReadinessCheckTypeFreshness, PRReadinessCheckTypeAgentReviewClean, PRReadinessCheckTypeDiffCollected,
		PRReadinessCheckTypeTestEvidencePresent, PRReadinessCheckTypeRiskFlags, PRReadinessCheckTypeDependencyConfigRisk,
		PRReadinessCheckTypeGeneratedFileChurn, PRReadinessCheckTypeContextComplete, PRReadinessCheckTypeReviewPacketDraftable:
		return nil
	default:
		return fmt.Errorf("invalid PRReadinessCheckType: %q", t)
	}
}

type PRReadinessEnforcement string

const (
	PRReadinessEnforcementOff      PRReadinessEnforcement = "off"
	PRReadinessEnforcementAdvisory PRReadinessEnforcement = "advisory"
	PRReadinessEnforcementBlocking PRReadinessEnforcement = "blocking"
)

func (e PRReadinessEnforcement) Validate() error {
	switch e {
	case PRReadinessEnforcementOff, PRReadinessEnforcementAdvisory, PRReadinessEnforcementBlocking:
		return nil
	default:
		return fmt.Errorf("invalid PRReadinessEnforcement: %q", e)
	}
}

type PRReadinessPolicy struct {
	Builder  map[PRReadinessCheckType]PRReadinessEnforcement `json:"builder,omitempty"`
	Engineer map[PRReadinessCheckType]PRReadinessEnforcement `json:"engineer,omitempty"`
	Admin    map[PRReadinessCheckType]PRReadinessEnforcement `json:"admin,omitempty"`
}

func DefaultPRReadinessPolicy() PRReadinessPolicy {
	advisory := map[PRReadinessCheckType]PRReadinessEnforcement{
		PRReadinessCheckTypeAgentReviewClean:      PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeDiffCollected:         PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeTestEvidencePresent:   PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeRiskFlags:             PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeDependencyConfigRisk:  PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeGeneratedFileChurn:    PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeContextComplete:       PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeReviewPacketDraftable: PRReadinessEnforcementAdvisory,
		PRReadinessCheckTypeFreshness:             PRReadinessEnforcementAdvisory,
	}
	builder := clonePRReadinessPolicyMap(advisory)
	builder[PRReadinessCheckTypeAgentReviewClean] = PRReadinessEnforcementBlocking
	builder[PRReadinessCheckTypeFreshness] = PRReadinessEnforcementBlocking
	return PRReadinessPolicy{
		Builder:  builder,
		Engineer: advisory,
		Admin:    clonePRReadinessPolicyMap(advisory),
	}
}

func clonePRReadinessPolicyMap(in map[PRReadinessCheckType]PRReadinessEnforcement) map[PRReadinessCheckType]PRReadinessEnforcement {
	out := make(map[PRReadinessCheckType]PRReadinessEnforcement, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (p PRReadinessPolicy) EnforcementFor(role Role, checkType PRReadinessCheckType) PRReadinessEnforcement {
	var values map[PRReadinessCheckType]PRReadinessEnforcement
	switch role {
	case RoleBuilder:
		values = p.Builder
	case RoleMember:
		values = p.Engineer
	case RoleAdmin:
		values = p.Admin
	default:
		return PRReadinessEnforcementOff
	}
	if len(values) == 0 {
		return DefaultPRReadinessPolicy().EnforcementFor(role, checkType)
	}
	if value, ok := values[checkType]; ok {
		return value
	}
	return PRReadinessEnforcementOff
}

type PRReadinessRun struct {
	ID                         uuid.UUID            `db:"id" json:"id"`
	OrgID                      uuid.UUID            `db:"org_id" json:"org_id"`
	SessionID                  uuid.UUID            `db:"session_id" json:"session_id"`
	RepositoryID               *uuid.UUID           `db:"repository_id" json:"repository_id,omitempty"`
	Status                     PRReadinessRunStatus `db:"status" json:"status"`
	EvaluatedWorkspaceRevision int64                `db:"evaluated_workspace_revision" json:"evaluated_workspace_revision"`
	EvaluatedSnapshotKey       *string              `db:"evaluated_snapshot_key" json:"evaluated_snapshot_key,omitempty"`
	Summary                    string               `db:"summary" json:"summary,omitempty"`
	ReviewPacket               json.RawMessage      `db:"review_packet" json:"review_packet,omitempty"`
	TriggeredByUserID          *uuid.UUID           `db:"triggered_by_user_id" json:"triggered_by_user_id,omitempty"`
	StartedAt                  time.Time            `db:"started_at" json:"started_at"`
	CompletedAt                *time.Time           `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt                  time.Time            `db:"created_at" json:"created_at"`
	UpdatedAt                  time.Time            `db:"updated_at" json:"updated_at"`
	Checks                     []PRReadinessCheck   `db:"-" json:"checks,omitempty"`
}

type PRReadinessCheck struct {
	ID          uuid.UUID              `db:"id" json:"id"`
	OrgID       uuid.UUID              `db:"org_id" json:"org_id"`
	RunID       uuid.UUID              `db:"run_id" json:"run_id"`
	SessionID   uuid.UUID              `db:"session_id" json:"session_id"`
	CheckType   PRReadinessCheckType   `db:"check_type" json:"check_type"`
	Status      PRReadinessCheckStatus `db:"status" json:"status"`
	Enforcement PRReadinessEnforcement `db:"enforcement" json:"enforcement"`
	Title       string                 `db:"title" json:"title"`
	Summary     string                 `db:"summary" json:"summary"`
	Details     json.RawMessage        `db:"details" json:"details,omitempty"`
	Action      string                 `db:"action" json:"action,omitempty"`
	CreatedAt   time.Time              `db:"created_at" json:"created_at"`
}

type PRReadinessResponse struct {
	Latest *PRReadinessRun `json:"latest,omitempty"`
}
