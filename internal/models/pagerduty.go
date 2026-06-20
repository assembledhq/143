package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type PagerDutyOAuthMode string

const (
	PagerDutyOAuthModeScoped      PagerDutyOAuthMode = "scoped"
	PagerDutyOAuthModeClassicUser PagerDutyOAuthMode = "classic_user"
)

func (m PagerDutyOAuthMode) Validate() error {
	switch m {
	case PagerDutyOAuthModeScoped, PagerDutyOAuthModeClassicUser:
		return nil
	default:
		return fmt.Errorf("invalid PagerDuty OAuth mode: %q", m)
	}
}

type PagerDutyIntegrationStatus string

const (
	PagerDutyIntegrationStatusActive   PagerDutyIntegrationStatus = "active"
	PagerDutyIntegrationStatusDegraded PagerDutyIntegrationStatus = "degraded"
	PagerDutyIntegrationStatusInactive PagerDutyIntegrationStatus = "inactive"
)

func (s PagerDutyIntegrationStatus) Validate() error {
	switch s {
	case PagerDutyIntegrationStatusActive, PagerDutyIntegrationStatusDegraded, PagerDutyIntegrationStatusInactive:
		return nil
	default:
		return fmt.Errorf("invalid PagerDuty integration status: %q", s)
	}
}

type AutomationEventProvider string

const (
	AutomationEventProviderPagerDuty AutomationEventProvider = "pagerduty"
	AutomationEventProviderGitHub    AutomationEventProvider = "github"
)

func (p AutomationEventProvider) Validate() error {
	switch p {
	case AutomationEventProviderPagerDuty, AutomationEventProviderGitHub:
		return nil
	default:
		return fmt.Errorf("invalid automation event provider: %q", p)
	}
}

type PagerDutyEventType string

const (
	PagerDutyEventIncidentTriggered             PagerDutyEventType = "incident.triggered"
	PagerDutyEventIncidentAcknowledged          PagerDutyEventType = "incident.acknowledged"
	PagerDutyEventIncidentUnacknowledged        PagerDutyEventType = "incident.unacknowledged"
	PagerDutyEventIncidentReassigned            PagerDutyEventType = "incident.reassigned"
	PagerDutyEventIncidentEscalated             PagerDutyEventType = "incident.escalated"
	PagerDutyEventIncidentPriorityUpdated       PagerDutyEventType = "incident.priority_updated"
	PagerDutyEventIncidentAnnotated             PagerDutyEventType = "incident.annotated"
	PagerDutyEventIncidentStatusUpdatePublished PagerDutyEventType = "incident.status_update_published"
	PagerDutyEventIncidentReopened              PagerDutyEventType = "incident.reopened"
	PagerDutyEventIncidentResolved              PagerDutyEventType = "incident.resolved"
)

func (e PagerDutyEventType) Validate() error {
	switch e {
	case PagerDutyEventIncidentTriggered,
		PagerDutyEventIncidentAcknowledged,
		PagerDutyEventIncidentUnacknowledged,
		PagerDutyEventIncidentReassigned,
		PagerDutyEventIncidentEscalated,
		PagerDutyEventIncidentPriorityUpdated,
		PagerDutyEventIncidentAnnotated,
		PagerDutyEventIncidentStatusUpdatePublished,
		PagerDutyEventIncidentReopened,
		PagerDutyEventIncidentResolved:
		return nil
	default:
		return fmt.Errorf("invalid PagerDuty event type: %q", e)
	}
}

type PagerDutyIntegration struct {
	ID                  uuid.UUID                  `db:"id" json:"id"`
	OrgID               uuid.UUID                  `db:"org_id" json:"org_id"`
	IntegrationID       *uuid.UUID                 `db:"integration_id" json:"integration_id,omitempty"`
	AccountSubdomain    *string                    `db:"account_subdomain" json:"account_subdomain,omitempty"`
	ServiceRegion       string                     `db:"service_region" json:"service_region"`
	OAuthMode           PagerDutyOAuthMode         `db:"oauth_mode" json:"oauth_mode"`
	CredentialRef       string                     `db:"credential_ref" json:"-"`
	WebhookSecretRef    *string                    `db:"webhook_secret_ref" json:"-"`
	Status              PagerDutyIntegrationStatus `db:"status" json:"status"`
	Scopes              []string                   `db:"scopes" json:"scopes"`
	LastSyncedAt        *time.Time                 `db:"last_synced_at" json:"last_synced_at,omitempty"`
	LastHealthCheckAt   *time.Time                 `db:"last_health_check_at" json:"last_health_check_at,omitempty"`
	LastError           *string                    `db:"last_error" json:"last_error,omitempty"`
	DefaultRepositoryID *uuid.UUID                 `db:"default_repository_id" json:"default_repository_id,omitempty"`
	WritebackEnabled    bool                       `db:"writeback_enabled" json:"writeback_enabled"`
	AutoCreateWebhook   bool                       `db:"auto_create_webhook" json:"auto_create_webhook"`
	CreatedBy           *uuid.UUID                 `db:"created_by" json:"created_by,omitempty"`
	CreatedAt           time.Time                  `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time                  `db:"updated_at" json:"updated_at"`
	DeletedAt           *time.Time                 `db:"deleted_at" json:"-"`
}

type PagerDutyIntegrationSettings struct {
	ID                  uuid.UUID
	DefaultRepositoryID *uuid.UUID
	WritebackEnabled    bool
	AutoCreateWebhook   bool
	Status              PagerDutyIntegrationStatus
	LastError           *string
}

type PagerDutyServiceRepoMapping struct {
	ID                     uuid.UUID  `db:"id" json:"id"`
	OrgID                  uuid.UUID  `db:"org_id" json:"org_id"`
	PagerDutyIntegrationID uuid.UUID  `db:"pagerduty_integration_id" json:"pagerduty_integration_id"`
	PagerDutyServiceID     string     `db:"pagerduty_service_id" json:"pagerduty_service_id"`
	PagerDutyServiceName   string     `db:"pagerduty_service_name" json:"pagerduty_service_name"`
	PagerDutyTeamID        *string    `db:"pagerduty_team_id" json:"pagerduty_team_id,omitempty"`
	RepositoryID           uuid.UUID  `db:"repository_id" json:"repository_id"`
	BaseBranch             *string    `db:"base_branch" json:"base_branch,omitempty"`
	Enabled                bool       `db:"enabled" json:"enabled"`
	CreatedBy              *uuid.UUID `db:"created_by" json:"created_by,omitempty"`
	CreatedAt              time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt              time.Time  `db:"updated_at" json:"updated_at"`
}

type PagerDutyIncident struct {
	ID                     uuid.UUID       `db:"id" json:"id"`
	OrgID                  uuid.UUID       `db:"org_id" json:"org_id"`
	PagerDutyIntegrationID uuid.UUID       `db:"pagerduty_integration_id" json:"pagerduty_integration_id"`
	IssueID                *uuid.UUID      `db:"issue_id" json:"issue_id,omitempty"`
	IncidentID             string          `db:"incident_id" json:"incident_id"`
	IncidentNumber         *int64          `db:"incident_number" json:"incident_number,omitempty"`
	HTMLURL                *string         `db:"html_url" json:"html_url,omitempty"`
	Title                  string          `db:"title" json:"title"`
	Status                 string          `db:"status" json:"status"`
	Urgency                *string         `db:"urgency" json:"urgency,omitempty"`
	PriorityID             *string         `db:"priority_id" json:"priority_id,omitempty"`
	PriorityName           *string         `db:"priority_name" json:"priority_name,omitempty"`
	ServiceID              *string         `db:"service_id" json:"service_id,omitempty"`
	ServiceName            *string         `db:"service_name" json:"service_name,omitempty"`
	EscalationPolicyID     *string         `db:"escalation_policy_id" json:"escalation_policy_id,omitempty"`
	EscalationPolicyName   *string         `db:"escalation_policy_name" json:"escalation_policy_name,omitempty"`
	IncidentType           *string         `db:"incident_type" json:"incident_type,omitempty"`
	AssignedUserIDs        []string        `db:"assigned_user_ids" json:"assigned_user_ids"`
	TeamIDs                []string        `db:"team_ids" json:"team_ids"`
	LatestNote             *string         `db:"latest_note" json:"latest_note,omitempty"`
	RawData                json.RawMessage `db:"raw_data" json:"raw_data"`
	TriggeredAt            *time.Time      `db:"triggered_at" json:"triggered_at,omitempty"`
	AcknowledgedAt         *time.Time      `db:"acknowledged_at" json:"acknowledged_at,omitempty"`
	ResolvedAt             *time.Time      `db:"resolved_at" json:"resolved_at,omitempty"`
	LastEventAt            *time.Time      `db:"last_event_at" json:"last_event_at,omitempty"`
	CreatedAt              time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt              time.Time       `db:"updated_at" json:"updated_at"`
}

type PagerDutyServiceSummary struct {
	ID               string   `json:"id"`
	Summary          string   `json:"summary"`
	HTMLURL          string   `json:"html_url,omitempty"`
	EscalationPolicy string   `json:"escalation_policy,omitempty"`
	TeamIDs          []string `json:"team_ids"`
}

type PagerDutyHealth struct {
	Integration             PagerDutyIntegration `json:"integration"`
	CredentialConfigured    bool                 `json:"credential_configured"`
	AuthOK                  bool                 `json:"auth_ok"`
	WebhookSecretConfigured bool                 `json:"webhook_secret_configured"`
	RecentWebhookFailures   int                  `json:"recent_webhook_failures"`
	LatestWebhookError      *string              `json:"latest_webhook_error,omitempty"`
	LatestWebhookFailureAt  *time.Time           `json:"latest_webhook_failure_at,omitempty"`
	LastHealthCheckAt       *time.Time           `json:"last_health_check_at,omitempty"`
	LastSyncedAt            *time.Time           `json:"last_synced_at,omitempty"`
	LastError               *string              `json:"last_error,omitempty"`
	WritebackEnabled        bool                 `json:"writeback_enabled"`
	AutoCreateWebhook       bool                 `json:"auto_create_webhook"`
	Symptoms                []string             `json:"symptoms"`
}

type PagerDutyWebhookFailureSummary struct {
	Count           int        `json:"count"`
	LatestError     *string    `json:"latest_error,omitempty"`
	LatestFailureAt *time.Time `json:"latest_failure_at,omitempty"`
}

type PagerDutyWebhookSetup struct {
	PagerDutyIntegrationID  uuid.UUID `json:"pagerduty_integration_id"`
	IntegrationID           uuid.UUID `json:"integration_id"`
	WebhookURL              string    `json:"webhook_url"`
	WebhookSecretConfigured bool      `json:"webhook_secret_configured"`
	WebhookSubscriptionID   *string   `json:"webhook_subscription_id,omitempty"`
	ServiceID               *string   `json:"service_id,omitempty"`
	TeamID                  *string   `json:"team_id,omitempty"`
	Events                  []string  `json:"events,omitempty"`
}

type PagerDutyWebhookSetupRequest struct {
	ServiceID   string               `json:"service_id"`
	TeamID      *string              `json:"team_id,omitempty"`
	Description string               `json:"description"`
	Events      []PagerDutyEventType `json:"events"`
}

type PagerDutyInboundEvent struct {
	ID                     uuid.UUID          `db:"id" json:"id"`
	OrgID                  uuid.UUID          `db:"org_id" json:"org_id"`
	PagerDutyIntegrationID *uuid.UUID         `db:"pagerduty_integration_id" json:"pagerduty_integration_id,omitempty"`
	WebhookDeliveryID      *uuid.UUID         `db:"webhook_delivery_id" json:"webhook_delivery_id,omitempty"`
	ProviderEventID        string             `db:"provider_event_id" json:"provider_event_id"`
	EventType              PagerDutyEventType `db:"event_type" json:"event_type"`
	ResourceType           *string            `db:"resource_type" json:"resource_type,omitempty"`
	IncidentID             *string            `db:"incident_id" json:"incident_id,omitempty"`
	OccurredAt             *time.Time         `db:"occurred_at" json:"occurred_at,omitempty"`
	Payload                json.RawMessage    `db:"payload" json:"payload"`
	Headers                json.RawMessage    `db:"headers" json:"headers"`
	Status                 string             `db:"status" json:"status"`
	ErrorMessage           *string            `db:"error_message" json:"error_message,omitempty"`
	CreatedAt              time.Time          `db:"created_at" json:"created_at"`
	ProcessedAt            *time.Time         `db:"processed_at" json:"processed_at,omitempty"`
}

type AutomationEventTrigger struct {
	ID           uuid.UUID               `db:"id" json:"id"`
	OrgID        uuid.UUID               `db:"org_id" json:"org_id"`
	AutomationID uuid.UUID               `db:"automation_id" json:"automation_id"`
	Provider     AutomationEventProvider `db:"provider" json:"provider"`
	EventTypes   []string                `db:"event_types" json:"event_types"`
	Filter       json.RawMessage         `db:"filter" json:"filter"`
	RepositoryID *uuid.UUID              `db:"repository_id" json:"repository_id,omitempty"`
	Enabled      bool                    `db:"enabled" json:"enabled"`
	CreatedAt    time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time               `db:"updated_at" json:"updated_at"`
}

type PagerDutyConfig struct {
	AccessToken      string    `json:"access_token,omitempty"`  // #nosec G117 -- JSON config field
	RefreshToken     string    `json:"refresh_token,omitempty"` // #nosec G117 -- JSON config field
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
	TokenType        string    `json:"token_type,omitempty"`
	Scope            string    `json:"scope,omitempty"`
	AccountSubdomain string    `json:"account_subdomain,omitempty"`
	ServiceRegion    string    `json:"service_region,omitempty"`
	WebhookSecret    string    `json:"webhook_secret,omitempty"`
}

func (c PagerDutyConfig) Validate() error {
	if c.AccessToken == "" && c.WebhookSecret == "" {
		return fmt.Errorf("access_token or webhook_secret is required")
	}
	return nil
}

func (c PagerDutyConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderPagerDuty,
		Configured: true,
	}
}
