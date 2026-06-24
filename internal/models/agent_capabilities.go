package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type AgentCapabilityID string

const (
	AgentCapabilityRepoContext           AgentCapabilityID = "repo_context"
	AgentCapabilityPRHistory             AgentCapabilityID = "pr_history"
	AgentCapabilitySessionHistory        AgentCapabilityID = "session_history"
	AgentCapabilityReviewFeedback        AgentCapabilityID = "review_feedback"
	AgentCapabilityCIHistory             AgentCapabilityID = "ci_history"
	AgentCapabilityIssueSources          AgentCapabilityID = "issue_sources"
	AgentCapabilityTeamDocs              AgentCapabilityID = "team_docs"
	AgentCapabilityProductionDiagnostics AgentCapabilityID = "production_diagnostics"
	AgentCapabilityExternalComments      AgentCapabilityID = "external_comments"
	AgentCapabilitySlackNotifications    AgentCapabilityID = "slack_notifications"
	AgentCapabilityProjectProposals      AgentCapabilityID = "project_proposals"
	AgentCapabilityEvalAuthoring         AgentCapabilityID = "eval_authoring"
	AgentCapabilityPublishing            AgentCapabilityID = "publishing"
)

func (id AgentCapabilityID) Validate() error {
	switch id {
	case AgentCapabilityRepoContext,
		AgentCapabilityPRHistory,
		AgentCapabilitySessionHistory,
		AgentCapabilityReviewFeedback,
		AgentCapabilityCIHistory,
		AgentCapabilityIssueSources,
		AgentCapabilityTeamDocs,
		AgentCapabilityProductionDiagnostics,
		AgentCapabilityExternalComments,
		AgentCapabilitySlackNotifications,
		AgentCapabilityProjectProposals,
		AgentCapabilityEvalAuthoring,
		AgentCapabilityPublishing:
		return nil
	default:
		return fmt.Errorf("invalid AgentCapabilityID: %q", id)
	}
}

type AgentCapabilityAccessLevel string

const (
	AgentCapabilityAccessRead    AgentCapabilityAccessLevel = "read"
	AgentCapabilityAccessWrite   AgentCapabilityAccessLevel = "write"
	AgentCapabilityAccessPublish AgentCapabilityAccessLevel = "publish"
)

func (a AgentCapabilityAccessLevel) Validate() error {
	switch a {
	case AgentCapabilityAccessRead, AgentCapabilityAccessWrite, AgentCapabilityAccessPublish:
		return nil
	default:
		return fmt.Errorf("invalid AgentCapabilityAccessLevel: %q", a)
	}
}

type AgentCapabilityRisk string

const (
	AgentCapabilityRiskLow    AgentCapabilityRisk = "low"
	AgentCapabilityRiskMedium AgentCapabilityRisk = "medium"
	AgentCapabilityRiskHigh   AgentCapabilityRisk = "high"
)

func (r AgentCapabilityRisk) Validate() error {
	switch r {
	case AgentCapabilityRiskLow, AgentCapabilityRiskMedium, AgentCapabilityRiskHigh:
		return nil
	default:
		return fmt.Errorf("invalid AgentCapabilityRisk: %q", r)
	}
}

type AgentCapabilityScope string

const (
	AgentCapabilityScopeRepository  AgentCapabilityScope = "repository"
	AgentCapabilityScopeOrg         AgentCapabilityScope = "org"
	AgentCapabilityScopeIntegration AgentCapabilityScope = "integration"
)

func (s AgentCapabilityScope) Validate() error {
	switch s {
	case AgentCapabilityScopeRepository, AgentCapabilityScopeOrg, AgentCapabilityScopeIntegration:
		return nil
	default:
		return fmt.Errorf("invalid AgentCapabilityScope: %q", s)
	}
}

type AgentCapabilityPolicyType string

const (
	AgentCapabilityPolicyTypeSessionDefault AgentCapabilityPolicyType = "session_default"
	AgentCapabilityPolicyTypeAutomation     AgentCapabilityPolicyType = "automation"
)

func (t AgentCapabilityPolicyType) Validate() error {
	switch t {
	case AgentCapabilityPolicyTypeSessionDefault, AgentCapabilityPolicyTypeAutomation:
		return nil
	default:
		return fmt.Errorf("invalid AgentCapabilityPolicyType: %q", t)
	}
}

type AgentCapabilityGrantSource string

const (
	AgentCapabilityGrantSourceSessionDefault AgentCapabilityGrantSource = "session_default"
	AgentCapabilityGrantSourceAutomation     AgentCapabilityGrantSource = "automation"
	AgentCapabilityGrantSourceLaunchDefault  AgentCapabilityGrantSource = "launch_default"
	AgentCapabilityGrantSourceUserApproved   AgentCapabilityGrantSource = "user_approved"
)

func (s AgentCapabilityGrantSource) Validate() error {
	switch s {
	case AgentCapabilityGrantSourceSessionDefault,
		AgentCapabilityGrantSourceAutomation,
		AgentCapabilityGrantSourceLaunchDefault,
		AgentCapabilityGrantSourceUserApproved:
		return nil
	default:
		return fmt.Errorf("invalid AgentCapabilityGrantSource: %q", s)
	}
}

type AgentCapabilityDefinition struct {
	ID             AgentCapabilityID            `json:"id"`
	DisplayName    string                       `json:"display_name"`
	Description    string                       `json:"description"`
	Category       string                       `json:"category"`
	MaxAccessLevel AgentCapabilityAccessLevel   `json:"max_access_level"`
	Risk           AgentCapabilityRisk          `json:"risk"`
	Scope          AgentCapabilityScope         `json:"scope"`
	Requirements   []string                     `json:"requirements"`
	DefaultConfig  json.RawMessage              `json:"default_config"`
	Availability   *AgentCapabilityAvailability `json:"availability,omitempty"`
}

type AgentCapabilityAvailability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type AgentCapabilityPolicy struct {
	ID           uuid.UUID                 `db:"id" json:"id"`
	OrgID        uuid.UUID                 `db:"org_id" json:"org_id"`
	PolicyType   AgentCapabilityPolicyType `db:"policy_type" json:"policy_type"`
	AutomationID *uuid.UUID                `db:"automation_id" json:"automation_id,omitempty"`
	Name         string                    `db:"name" json:"name"`
	Active       bool                      `db:"active" json:"active"`
	CreatedBy    *uuid.UUID                `db:"created_by" json:"created_by,omitempty"`
	CreatedAt    time.Time                 `db:"created_at" json:"created_at"`
	Grants       []AgentCapabilityGrant    `db:"-" json:"grants"`
}

type AgentCapabilityGrant struct {
	ID           uuid.UUID                  `db:"id" json:"id"`
	OrgID        uuid.UUID                  `db:"org_id" json:"org_id"`
	PolicyID     uuid.UUID                  `db:"policy_id" json:"policy_id"`
	CapabilityID AgentCapabilityID          `db:"capability_id" json:"capability_id"`
	AccessLevel  AgentCapabilityAccessLevel `db:"access_level" json:"access_level"`
	Enabled      bool                       `db:"enabled" json:"enabled"`
	Config       json.RawMessage            `db:"config" json:"config"`
	CreatedBy    *uuid.UUID                 `db:"created_by" json:"created_by,omitempty"`
	CreatedAt    time.Time                  `db:"created_at" json:"created_at"`
}

type AgentCapabilityPolicyGrantInput struct {
	CapabilityID AgentCapabilityID          `json:"capability_id"`
	AccessLevel  AgentCapabilityAccessLevel `json:"access_level"`
	Enabled      bool                       `json:"enabled"`
	Config       json.RawMessage            `json:"config"`
}

type AgentCapabilitySnapshotItem struct {
	ID                  AgentCapabilityID          `json:"id"`
	DisplayName         string                     `json:"display_name"`
	AccessLevel         AgentCapabilityAccessLevel `json:"access_level"`
	Risk                AgentCapabilityRisk        `json:"risk"`
	Scope               AgentCapabilityScope       `json:"scope"`
	Config              json.RawMessage            `json:"config"`
	Source              AgentCapabilityGrantSource `json:"source"`
	GrantedAt           time.Time                  `json:"granted_at"`
	HumanInputRequestID *uuid.UUID                 `json:"human_input_request_id,omitempty"`
}
