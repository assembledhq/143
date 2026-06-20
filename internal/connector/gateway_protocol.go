package connector

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	GatewayMessageHeartbeat      = "heartbeat"
	GatewayMessageActionRequest  = "action_request"
	GatewayMessageActionResponse = "action_response"
	GatewayMessageConfigPush     = "config_push"
	GatewayMessageConfigAck      = "config_ack"

	CapabilityRotateIdentity = "connector.rotate_identity"
	CapabilityReloadConfig   = "connector.reload_config"
	CapabilityTriggerUpdate  = "connector.update"
)

var ConnectorControlResourceID = uuid.MustParse("00000000-0000-0000-0000-000000000143")

type GatewayMessage struct {
	Type      string           `json:"type"`
	RequestID uuid.UUID        `json:"request_id,omitempty"`
	Request   *ActionRequest   `json:"request,omitempty"`
	Signature string           `json:"signature,omitempty"`
	Result    *ActionResult    `json:"result,omitempty"`
	Error     *ActionError     `json:"error,omitempty"`
	Heartbeat *HeartbeatFrame  `json:"heartbeat,omitempty"`
	Config    *ConfigPushFrame `json:"config,omitempty"`
	ConfigAck *ConfigAckFrame  `json:"config_ack,omitempty"`
}

type ActionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type HeartbeatFrame struct {
	Version                  string   `json:"version"`
	Protocol                 string   `json:"protocol"`
	Capabilities             []string `json:"capabilities"`
	HeartbeatIntervalSeconds int      `json:"heartbeat_interval_seconds"`
}

type ConfigPushFrame struct {
	OrgID       uuid.UUID            `json:"org_id"`
	ConnectorID uuid.UUID            `json:"connector_id"`
	Version     int64                `json:"version"`
	IssuedAt    time.Time            `json:"issued_at"`
	ExpiresAt   time.Time            `json:"expires_at"`
	Resources   []ConfigPushResource `json:"resources"`
}

type ConfigPushResource struct {
	ID            uuid.UUID       `json:"id"`
	DisplayName   string          `json:"display_name"`
	ResourceType  string          `json:"resource_type"`
	Mode          string          `json:"mode"`
	ConfigSource  string          `json:"config_source"`
	ConfigVersion int64           `json:"config_version"`
	Config        json.RawMessage `json:"config,omitempty"`
}

type ConfigAckFrame struct {
	Version      int64    `json:"version"`
	Applied      bool     `json:"applied"`
	Capabilities []string `json:"capabilities,omitempty"`
}
