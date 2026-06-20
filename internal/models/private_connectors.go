package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PrivateConnectorStatus describes the customer-visible connector group state.
type PrivateConnectorStatus string

const (
	PrivateConnectorStatusWaiting      PrivateConnectorStatus = "waiting"
	PrivateConnectorStatusOnline       PrivateConnectorStatus = "online"
	PrivateConnectorStatusReconnecting PrivateConnectorStatus = "reconnecting"
	PrivateConnectorStatusOffline      PrivateConnectorStatus = "offline"
	PrivateConnectorStatusDisabled     PrivateConnectorStatus = "disabled"
)

func (s PrivateConnectorStatus) Validate() error {
	switch s {
	case PrivateConnectorStatusWaiting, PrivateConnectorStatusOnline,
		PrivateConnectorStatusReconnecting, PrivateConnectorStatusOffline,
		PrivateConnectorStatusDisabled:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorStatus: %q", s)
	}
}

// PrivateConnectorInstanceStatus describes one installed connector daemon.
type PrivateConnectorInstanceStatus string

const (
	PrivateConnectorInstanceStatusOnline       PrivateConnectorInstanceStatus = "online"
	PrivateConnectorInstanceStatusReconnecting PrivateConnectorInstanceStatus = "reconnecting"
	PrivateConnectorInstanceStatusOffline      PrivateConnectorInstanceStatus = "offline"
	PrivateConnectorInstanceStatusRevoked      PrivateConnectorInstanceStatus = "revoked"
)

func (s PrivateConnectorInstanceStatus) Validate() error {
	switch s {
	case PrivateConnectorInstanceStatusOnline, PrivateConnectorInstanceStatusReconnecting,
		PrivateConnectorInstanceStatusOffline, PrivateConnectorInstanceStatusRevoked:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorInstanceStatus: %q", s)
	}
}

// PrivateConnectorTokenPreset names the supported deployment-token policy presets.
type PrivateConnectorTokenPreset string

const (
	PrivateConnectorTokenPresetInteractive PrivateConnectorTokenPreset = "interactive"
	PrivateConnectorTokenPresetAutomation  PrivateConnectorTokenPreset = "automation"
)

func (p PrivateConnectorTokenPreset) Validate() error {
	switch p {
	case PrivateConnectorTokenPresetInteractive, PrivateConnectorTokenPresetAutomation:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorTokenPreset: %q", p)
	}
}

// PrivateConnectorResourceType is the provider family configured under a connector.
type PrivateConnectorResourceType string

const (
	PrivateConnectorResourceTypeVictoriaLogs PrivateConnectorResourceType = "victorialogs"
	PrivateConnectorResourceTypePostgres     PrivateConnectorResourceType = "postgres"
)

func (t PrivateConnectorResourceType) Validate() error {
	switch t {
	case PrivateConnectorResourceTypeVictoriaLogs, PrivateConnectorResourceTypePostgres:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorResourceType: %q", t)
	}
}

// PrivateConnectorResourceMode names the capability class for a resource.
type PrivateConnectorResourceMode string

const (
	PrivateConnectorResourceModeLogs           PrivateConnectorResourceMode = "logs"
	PrivateConnectorResourceModeAgentReadOnly  PrivateConnectorResourceMode = "agent_readonly"
	PrivateConnectorResourceModePreviewRuntime PrivateConnectorResourceMode = "preview_runtime"
)

func (m PrivateConnectorResourceMode) Validate() error {
	switch m {
	case PrivateConnectorResourceModeLogs, PrivateConnectorResourceModeAgentReadOnly,
		PrivateConnectorResourceModePreviewRuntime:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorResourceMode: %q", m)
	}
}

// PrivateConnectorConfigSource records which config authority currently owns a resource.
type PrivateConnectorConfigSource string

const (
	PrivateConnectorConfigSourceFile PrivateConnectorConfigSource = "file"
	PrivateConnectorConfigSourceUI   PrivateConnectorConfigSource = "ui"
)

func (s PrivateConnectorConfigSource) Validate() error {
	switch s {
	case PrivateConnectorConfigSourceFile, PrivateConnectorConfigSourceUI:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorConfigSource: %q", s)
	}
}

// PrivateConnectorProtocol records the daemon-to-gateway transport.
type PrivateConnectorProtocol string

const (
	PrivateConnectorProtocolWebSocket PrivateConnectorProtocol = "websocket"
	PrivateConnectorProtocolGRPC      PrivateConnectorProtocol = "grpc"
)

func (p PrivateConnectorProtocol) Validate() error {
	switch p {
	case PrivateConnectorProtocolWebSocket, PrivateConnectorProtocolGRPC:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorProtocol: %q", p)
	}
}

// PrivateConnectorResourceStatus describes local resource health/config state.
type PrivateConnectorResourceStatus string

const (
	PrivateConnectorResourceStatusConfigured PrivateConnectorResourceStatus = "configured"
	PrivateConnectorResourceStatusReady      PrivateConnectorResourceStatus = "ready"
	PrivateConnectorResourceStatusError      PrivateConnectorResourceStatus = "error"
	PrivateConnectorResourceStatusDisabled   PrivateConnectorResourceStatus = "disabled"
)

func (s PrivateConnectorResourceStatus) Validate() error {
	switch s {
	case PrivateConnectorResourceStatusConfigured, PrivateConnectorResourceStatusReady,
		PrivateConnectorResourceStatusError, PrivateConnectorResourceStatusDisabled:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorResourceStatus: %q", s)
	}
}

// PrivateConnectorActionStatus describes one connector-mediated action request.
type PrivateConnectorActionStatus string

const (
	PrivateConnectorActionStatusPending   PrivateConnectorActionStatus = "pending"
	PrivateConnectorActionStatusRunning   PrivateConnectorActionStatus = "running"
	PrivateConnectorActionStatusSucceeded PrivateConnectorActionStatus = "succeeded"
	PrivateConnectorActionStatusFailed    PrivateConnectorActionStatus = "failed"
	PrivateConnectorActionStatusDenied    PrivateConnectorActionStatus = "denied"
)

func (s PrivateConnectorActionStatus) Validate() error {
	switch s {
	case PrivateConnectorActionStatusPending, PrivateConnectorActionStatusRunning,
		PrivateConnectorActionStatusSucceeded, PrivateConnectorActionStatusFailed,
		PrivateConnectorActionStatusDenied:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorActionStatus: %q", s)
	}
}

// PrivateConnectorRuntimeLeaseStatus describes a preview runtime data-plane grant.
type PrivateConnectorRuntimeLeaseStatus string

const (
	PrivateConnectorRuntimeLeaseStatusActive  PrivateConnectorRuntimeLeaseStatus = "active"
	PrivateConnectorRuntimeLeaseStatusRevoked PrivateConnectorRuntimeLeaseStatus = "revoked"
	PrivateConnectorRuntimeLeaseStatusExpired PrivateConnectorRuntimeLeaseStatus = "expired"
	PrivateConnectorRuntimeLeaseStatusFailed  PrivateConnectorRuntimeLeaseStatus = "failed"
)

func (s PrivateConnectorRuntimeLeaseStatus) Validate() error {
	switch s {
	case PrivateConnectorRuntimeLeaseStatusActive, PrivateConnectorRuntimeLeaseStatusRevoked,
		PrivateConnectorRuntimeLeaseStatusExpired, PrivateConnectorRuntimeLeaseStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorRuntimeLeaseStatus: %q", s)
	}
}

// PrivateConnectorRuntimeAccessMode identifies the scoped protocol exposed to a preview runtime.
type PrivateConnectorRuntimeAccessMode string

const (
	PrivateConnectorRuntimeAccessModePostgresTCP PrivateConnectorRuntimeAccessMode = "postgres_tcp"
)

func (m PrivateConnectorRuntimeAccessMode) Validate() error {
	switch m {
	case PrivateConnectorRuntimeAccessModePostgresTCP:
		return nil
	default:
		return fmt.Errorf("invalid PrivateConnectorRuntimeAccessMode: %q", m)
	}
}

type PrivateConnectorGroup struct {
	ID                       uuid.UUID              `db:"id" json:"id"`
	OrgID                    uuid.UUID              `db:"org_id" json:"org_id"`
	Name                     string                 `db:"name" json:"name"`
	Environment              string                 `db:"environment" json:"environment"`
	GatewayRegion            string                 `db:"gateway_region" json:"gateway_region"`
	Status                   PrivateConnectorStatus `db:"status" json:"status"`
	HealthAlertURL           *string                `db:"health_alert_url" json:"health_alert_url,omitempty"`
	OfflineAlertAfterSeconds int                    `db:"offline_alert_after_seconds" json:"offline_alert_after_seconds"`
	CreatedByUserID          *uuid.UUID             `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	DisabledAt               *time.Time             `db:"disabled_at" json:"disabled_at,omitempty"`
	CreatedAt                time.Time              `db:"created_at" json:"created_at"`
	UpdatedAt                time.Time              `db:"updated_at" json:"updated_at"`
}

type PrivateConnectorDeploymentToken struct {
	ID                   uuid.UUID                   `db:"id" json:"id"`
	OrgID                uuid.UUID                   `db:"org_id" json:"org_id"`
	ConnectorGroupID     uuid.UUID                   `db:"connector_group_id" json:"connector_group_id"`
	Name                 string                      `db:"name" json:"name"`
	TokenHash            string                      `db:"token_hash" json:"-"`
	TokenPrefix          string                      `db:"token_prefix" json:"token_prefix"`
	Preset               PrivateConnectorTokenPreset `db:"preset" json:"preset"`
	MaxRegistrations     *int                        `db:"max_registrations" json:"max_registrations,omitempty"`
	RegistrationCount    int                         `db:"registration_count" json:"registration_count"`
	AllowedSourceCIDRs   []string                    `db:"allowed_source_cidrs" json:"allowed_source_cidrs,omitempty"`
	AllowedGatewayRegion *string                     `db:"allowed_gateway_region" json:"allowed_gateway_region,omitempty"`
	ExpiresAt            *time.Time                  `db:"expires_at" json:"expires_at,omitempty"`
	LastUsedAt           *time.Time                  `db:"last_used_at" json:"last_used_at,omitempty"`
	RevokedAt            *time.Time                  `db:"revoked_at" json:"revoked_at,omitempty"`
	RevokedByUserID      *uuid.UUID                  `db:"revoked_by_user_id" json:"revoked_by_user_id,omitempty"`
	CreatedByUserID      *uuid.UUID                  `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt            time.Time                   `db:"created_at" json:"created_at"`
}

type PrivateConnectorInstance struct {
	ID                       uuid.UUID                      `db:"id" json:"id"`
	OrgID                    uuid.UUID                      `db:"org_id" json:"org_id"`
	ConnectorGroupID         uuid.UUID                      `db:"connector_group_id" json:"connector_group_id"`
	DeploymentTokenID        *uuid.UUID                     `db:"deployment_token_id" json:"deployment_token_id,omitempty"`
	InstanceName             string                         `db:"instance_name" json:"instance_name"`
	PublicKey                string                         `db:"public_key" json:"public_key"`
	Status                   PrivateConnectorInstanceStatus `db:"status" json:"status"`
	Version                  string                         `db:"version" json:"version"`
	Protocol                 PrivateConnectorProtocol       `db:"protocol" json:"protocol"`
	GatewayRegion            string                         `db:"gateway_region" json:"gateway_region"`
	Capabilities             []string                       `db:"capabilities" json:"capabilities"`
	LastHeartbeatAt          *time.Time                     `db:"last_heartbeat_at" json:"last_heartbeat_at,omitempty"`
	HeartbeatIntervalSeconds int                            `db:"heartbeat_interval_seconds" json:"heartbeat_interval_seconds"`
	OnlineAt                 *time.Time                     `db:"online_at" json:"online_at,omitempty"`
	OfflineAt                *time.Time                     `db:"offline_at" json:"offline_at,omitempty"`
	RevokedAt                *time.Time                     `db:"revoked_at" json:"revoked_at,omitempty"`
	RevokedByUserID          *uuid.UUID                     `db:"revoked_by_user_id" json:"revoked_by_user_id,omitempty"`
	CreatedAt                time.Time                      `db:"created_at" json:"created_at"`
	UpdatedAt                time.Time                      `db:"updated_at" json:"updated_at"`
}

type PrivateConnectorResource struct {
	ID                      uuid.UUID                      `db:"id" json:"id"`
	OrgID                   uuid.UUID                      `db:"org_id" json:"org_id"`
	ConnectorGroupID        uuid.UUID                      `db:"connector_group_id" json:"connector_group_id"`
	DisplayName             string                         `db:"display_name" json:"display_name"`
	ResourceType            PrivateConnectorResourceType   `db:"resource_type" json:"resource_type"`
	Mode                    PrivateConnectorResourceMode   `db:"mode" json:"mode"`
	Config                  json.RawMessage                `db:"config" json:"config"`
	ConfigSource            PrivateConnectorConfigSource   `db:"config_source" json:"config_source"`
	ConfigVersion           int64                          `db:"config_version" json:"config_version"`
	Status                  PrivateConnectorResourceStatus `db:"status" json:"status"`
	LastTestStatus          *string                        `db:"last_test_status" json:"last_test_status,omitempty"`
	LastTestError           *string                        `db:"last_test_error" json:"last_test_error,omitempty"`
	LastSuccessfulRequestAt *time.Time                     `db:"last_successful_request_at" json:"last_successful_request_at,omitempty"`
	LastError               *string                        `db:"last_error" json:"last_error,omitempty"`
	CreatedByUserID         *uuid.UUID                     `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt               time.Time                      `db:"created_at" json:"created_at"`
	UpdatedAt               time.Time                      `db:"updated_at" json:"updated_at"`
}

type PrivateConnectorAction struct {
	ID                  uuid.UUID                    `db:"id" json:"id"`
	OrgID               uuid.UUID                    `db:"org_id" json:"org_id"`
	ConnectorGroupID    uuid.UUID                    `db:"connector_group_id" json:"connector_group_id"`
	ConnectorInstanceID *uuid.UUID                   `db:"connector_instance_id" json:"connector_instance_id,omitempty"`
	ResourceID          uuid.UUID                    `db:"resource_id" json:"resource_id"`
	Capability          string                       `db:"capability" json:"capability"`
	ActorType           AuditActorType               `db:"actor_type" json:"actor_type"`
	ActorID             string                       `db:"actor_id" json:"actor_id"`
	RepositoryID        *uuid.UUID                   `db:"repository_id" json:"repository_id,omitempty"`
	SessionID           *uuid.UUID                   `db:"session_id" json:"session_id,omitempty"`
	PreviewID           *uuid.UUID                   `db:"preview_id" json:"preview_id,omitempty"`
	RequestNonce        string                       `db:"request_nonce" json:"request_nonce"`
	RequestFingerprint  string                       `db:"request_fingerprint" json:"request_fingerprint"`
	Status              PrivateConnectorActionStatus `db:"status" json:"status"`
	ErrorCode           *string                      `db:"error_code" json:"error_code,omitempty"`
	ErrorMessage        *string                      `db:"error_message" json:"error_message,omitempty"`
	ResultCount         *int                         `db:"result_count" json:"result_count,omitempty"`
	DurationMs          *int                         `db:"duration_ms" json:"duration_ms,omitempty"`
	CreatedAt           time.Time                    `db:"created_at" json:"created_at"`
	CompletedAt         *time.Time                   `db:"completed_at" json:"completed_at,omitempty"`
}

type PrivateConnectorRuntimeLease struct {
	ID                 uuid.UUID                          `db:"id" json:"id"`
	OrgID              uuid.UUID                          `db:"org_id" json:"org_id"`
	RepositoryID       uuid.UUID                          `db:"repository_id" json:"repository_id"`
	PreviewID          uuid.UUID                          `db:"preview_id" json:"preview_id"`
	PreviewRuntimeID   uuid.UUID                          `db:"preview_runtime_id" json:"preview_runtime_id"`
	ConnectorGroupID   uuid.UUID                          `db:"connector_group_id" json:"connector_group_id"`
	ResourceID         uuid.UUID                          `db:"resource_id" json:"resource_id"`
	Status             PrivateConnectorRuntimeLeaseStatus `db:"status" json:"status"`
	AccessMode         PrivateConnectorRuntimeAccessMode  `db:"access_mode" json:"access_mode"`
	TargetHost         string                             `db:"target_host" json:"target_host"`
	TargetPort         int                                `db:"target_port" json:"target_port"`
	TargetDatabase     string                             `db:"target_database" json:"target_database"`
	LeaseTokenHash     string                             `db:"lease_token_hash" json:"-"`
	LeaseTokenPrefix   string                             `db:"lease_token_prefix" json:"lease_token_prefix"`
	MaxConnections     int                                `db:"max_connections" json:"max_connections"`
	IdleTimeoutSeconds int                                `db:"idle_timeout_seconds" json:"idle_timeout_seconds"`
	ByteLimit          *int64                             `db:"byte_limit" json:"byte_limit,omitempty"`
	ExpiresAt          time.Time                          `db:"expires_at" json:"expires_at"`
	RevokedAt          *time.Time                         `db:"revoked_at" json:"revoked_at,omitempty"`
	CreatedAt          time.Time                          `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time                          `db:"updated_at" json:"updated_at"`
}

type PrivateConnectorHealthTransition struct {
	Group    PrivateConnectorGroup    `json:"group"`
	Instance PrivateConnectorInstance `json:"instance"`
}
