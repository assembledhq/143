package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type SlackInstallationStatus string

const (
	SlackInstallationStatusActive       SlackInstallationStatus = "active"
	SlackInstallationStatusDisconnected SlackInstallationStatus = "disconnected"
)

func (s SlackInstallationStatus) Validate() error {
	switch s {
	case SlackInstallationStatusActive, SlackInstallationStatusDisconnected:
		return nil
	default:
		return fmt.Errorf("invalid SlackInstallationStatus: %q", s)
	}
}

type SlackUserLinkSource string

const (
	SlackUserLinkSourceObserved    SlackUserLinkSource = "observed"
	SlackUserLinkSourceEmailMatch  SlackUserLinkSource = "email_match"
	SlackUserLinkSourceSelfLinked  SlackUserLinkSource = "self_linked"
	SlackUserLinkSourceAdminLinked SlackUserLinkSource = "admin_linked"
)

func (s SlackUserLinkSource) Validate() error {
	switch s {
	case SlackUserLinkSourceObserved, SlackUserLinkSourceEmailMatch, SlackUserLinkSourceSelfLinked, SlackUserLinkSourceAdminLinked:
		return nil
	default:
		return fmt.Errorf("invalid SlackUserLinkSource: %q", s)
	}
}

type SlackInboundEventStatus string

const (
	SlackInboundEventStatusReceived  SlackInboundEventStatus = "received"
	SlackInboundEventStatusEnqueued  SlackInboundEventStatus = "enqueued"
	SlackInboundEventStatusProcessed SlackInboundEventStatus = "processed"
	SlackInboundEventStatusFailed    SlackInboundEventStatus = "failed"
)

func (s SlackInboundEventStatus) Validate() error {
	switch s {
	case SlackInboundEventStatusReceived, SlackInboundEventStatusEnqueued, SlackInboundEventStatusProcessed, SlackInboundEventStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid SlackInboundEventStatus: %q", s)
	}
}

type SlackInboundEventType string

const (
	SlackInboundEventTypeAppMention         SlackInboundEventType = "app_mention"
	SlackInboundEventTypeMessageIM          SlackInboundEventType = "message.im"
	SlackInboundEventTypeAppHomeOpened      SlackInboundEventType = "app_home_opened"
	SlackInboundEventTypeAppUninstalled     SlackInboundEventType = "app_uninstalled"
	SlackInboundEventTypeAppUninstalledTeam SlackInboundEventType = "app_uninstalled_team"
	SlackInboundEventTypeAppRateLimited     SlackInboundEventType = "app_rate_limited"
	SlackInboundEventTypeMemberJoined       SlackInboundEventType = "member_joined_channel"
	SlackInboundEventTypeSlashCommand       SlackInboundEventType = "slash_command"
	SlackInboundEventTypeInteraction        SlackInboundEventType = "interaction"
)

type SlackOutboundMessageKind string

const (
	SlackOutboundMessageKindAck          SlackOutboundMessageKind = "ack"
	SlackOutboundMessageKindProgress     SlackOutboundMessageKind = "progress"
	SlackOutboundMessageKindFinal        SlackOutboundMessageKind = "final"
	SlackOutboundMessageKindNotification SlackOutboundMessageKind = "notification"
	SlackOutboundMessageKindHumanInput   SlackOutboundMessageKind = "human_input"
)

type SlackInstallation struct {
	ID                uuid.UUID               `db:"id" json:"id"`
	OrgID             uuid.UUID               `db:"org_id" json:"org_id"`
	IntegrationID     uuid.UUID               `db:"integration_id" json:"integration_id"`
	TeamID            string                  `db:"team_id" json:"team_id"`
	TeamName          string                  `db:"team_name" json:"team_name"`
	EnterpriseID      *string                 `db:"enterprise_id" json:"enterprise_id,omitempty"`
	APIAppID          string                  `db:"api_app_id" json:"api_app_id"`
	BotUserID         string                  `db:"bot_user_id" json:"bot_user_id"`
	BotID             string                  `db:"bot_id" json:"bot_id"`
	Scope             []string                `db:"scope" json:"scope"`
	Status            SlackInstallationStatus `db:"status" json:"status"`
	InstalledByUserID *uuid.UUID              `db:"installed_by_user_id" json:"installed_by_user_id,omitempty"`
	InstalledAt       time.Time               `db:"installed_at" json:"installed_at"`
	LastEventAt       *time.Time              `db:"last_event_at" json:"last_event_at,omitempty"`
	CreatedAt         time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time               `db:"updated_at" json:"updated_at"`
}

type SlackUserLink struct {
	ID                  uuid.UUID           `db:"id" json:"id"`
	OrgID               uuid.UUID           `db:"org_id" json:"org_id"`
	SlackInstallationID uuid.UUID           `db:"slack_installation_id" json:"slack_installation_id"`
	UserID              *uuid.UUID          `db:"user_id" json:"user_id,omitempty"`
	SlackTeamID         string              `db:"slack_team_id" json:"slack_team_id"`
	SlackUserID         string              `db:"slack_user_id" json:"slack_user_id"`
	SlackEmail          *string             `db:"slack_email" json:"slack_email,omitempty"`
	SlackDisplayName    string              `db:"slack_display_name" json:"slack_display_name"`
	Source              SlackUserLinkSource `db:"source" json:"source"`
	LinkedAt            *time.Time          `db:"linked_at" json:"linked_at,omitempty"`
	CreatedAt           time.Time           `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time           `db:"updated_at" json:"updated_at"`
}

type SlackChannelSettings struct {
	ID                        uuid.UUID       `db:"id" json:"id"`
	OrgID                     uuid.UUID       `db:"org_id" json:"org_id"`
	SlackInstallationID       uuid.UUID       `db:"slack_installation_id" json:"slack_installation_id"`
	SlackTeamID               string          `db:"slack_team_id" json:"slack_team_id"`
	SlackChannelID            string          `db:"slack_channel_id" json:"slack_channel_id"`
	SlackChannelName          string          `db:"slack_channel_name" json:"slack_channel_name"`
	ChannelType               string          `db:"channel_type" json:"channel_type"`
	DefaultRepositoryID       *uuid.UUID      `db:"default_repository_id" json:"default_repository_id,omitempty"`
	DefaultBranch             *string         `db:"default_branch" json:"default_branch,omitempty"`
	ResponseVisibility        string          `db:"response_visibility" json:"response_visibility"`
	AllowedActions            []string        `db:"allowed_actions" json:"allowed_actions"`
	NotificationSubscriptions json.RawMessage `db:"notification_subscriptions" json:"notification_subscriptions,omitempty"`
	Active                    bool            `db:"active" json:"active"`
	CreatedAt                 time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt                 time.Time       `db:"updated_at" json:"updated_at"`
}

type SlackSessionLink struct {
	ID                    uuid.UUID  `db:"id" json:"id"`
	OrgID                 uuid.UUID  `db:"org_id" json:"org_id"`
	SessionID             uuid.UUID  `db:"session_id" json:"session_id"`
	SlackInstallationID   uuid.UUID  `db:"slack_installation_id" json:"slack_installation_id"`
	SlackTeamID           string     `db:"slack_team_id" json:"slack_team_id"`
	SlackChannelID        string     `db:"slack_channel_id" json:"slack_channel_id"`
	SlackThreadTS         string     `db:"slack_thread_ts" json:"slack_thread_ts"`
	SlackRootTS           string     `db:"slack_root_ts" json:"slack_root_ts"`
	SlackMessagePermalink string     `db:"slack_message_permalink" json:"slack_message_permalink"`
	SlackUserID           string     `db:"slack_user_id" json:"slack_user_id"`
	MappedUserID          *uuid.UUID `db:"mapped_user_id" json:"mapped_user_id,omitempty"`
	TeamSession           bool       `db:"team_session" json:"team_session"`
	LatestStatusMessageTS *string    `db:"latest_status_message_ts" json:"latest_status_message_ts,omitempty"`
	FinalMessageTS        *string    `db:"final_message_ts" json:"final_message_ts,omitempty"`
	CreatedAt             time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time  `db:"updated_at" json:"updated_at"`
}

type SlackInboundEvent struct {
	ID                  uuid.UUID               `db:"id" json:"id"`
	OrgID               uuid.UUID               `db:"org_id" json:"org_id"`
	SlackInstallationID uuid.UUID               `db:"slack_installation_id" json:"slack_installation_id"`
	SlackEventID        *string                 `db:"slack_event_id" json:"slack_event_id,omitempty"`
	SlackTeamID         string                  `db:"slack_team_id" json:"slack_team_id"`
	EventType           SlackInboundEventType   `db:"event_type" json:"event_type"`
	ChannelID           *string                 `db:"channel_id" json:"channel_id,omitempty"`
	UserID              *string                 `db:"user_id" json:"user_id,omitempty"`
	EventTS             *string                 `db:"event_ts" json:"event_ts,omitempty"`
	Payload             json.RawMessage         `db:"payload" json:"payload"`
	Status              SlackInboundEventStatus `db:"status" json:"status"`
	JobID               *uuid.UUID              `db:"job_id" json:"job_id,omitempty"`
	Error               *string                 `db:"error" json:"error,omitempty"`
	ReceivedAt          time.Time               `db:"received_at" json:"received_at"`
	ProcessedAt         *time.Time              `db:"processed_at" json:"processed_at,omitempty"`
}

type SlackOutboundMessage struct {
	ID                 uuid.UUID                `db:"id" json:"id"`
	OrgID              uuid.UUID                `db:"org_id" json:"org_id"`
	SlackSessionLinkID *uuid.UUID               `db:"slack_session_link_id" json:"slack_session_link_id,omitempty"`
	NotificationID     *uuid.UUID               `db:"notification_id" json:"notification_id,omitempty"`
	SlackTeamID        string                   `db:"slack_team_id" json:"slack_team_id"`
	SlackChannelID     string                   `db:"slack_channel_id" json:"slack_channel_id"`
	SlackMessageTS     string                   `db:"slack_message_ts" json:"slack_message_ts"`
	MessageKind        SlackOutboundMessageKind `db:"message_kind" json:"message_kind"`
	Status             string                   `db:"status" json:"status"`
	LastPayloadHash    string                   `db:"last_payload_hash" json:"last_payload_hash"`
	CreatedAt          time.Time                `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time                `db:"updated_at" json:"updated_at"`
}

type SessionAttributionSource string

const (
	SessionAttributionSourceSlack       SessionAttributionSource = "slack"
	SessionAttributionSourceExternalAPI SessionAttributionSource = "external_api"
)

func (s SessionAttributionSource) Validate() error {
	switch s {
	case SessionAttributionSourceSlack, SessionAttributionSourceExternalAPI:
		return nil
	default:
		return fmt.Errorf("invalid SessionAttributionSource: %q", s)
	}
}

type SessionAttribution struct {
	ID             uuid.UUID                `db:"id" json:"id"`
	OrgID          uuid.UUID                `db:"org_id" json:"org_id"`
	SessionID      uuid.UUID                `db:"session_id" json:"session_id"`
	Source         SessionAttributionSource `db:"source" json:"source"`
	SourceMetadata json.RawMessage          `db:"source_metadata" json:"source_metadata,omitempty"`
	CreatedAt      time.Time                `db:"created_at" json:"created_at"`
}

type SlackStartSessionJobPayload struct {
	OrgID               string   `json:"org_id"`
	SlackInboundEventID string   `json:"slack_inbound_event_id"`
	SlackInstallationID string   `json:"slack_installation_id"`
	TeamID              string   `json:"team_id"`
	ChannelID           string   `json:"channel_id"`
	ThreadTS            string   `json:"thread_ts"`
	MessageTS           string   `json:"message_ts"`
	SlackUserID         string   `json:"slack_user_id"`
	Text                string   `json:"text"`
	Permalink           string   `json:"permalink"`
	Source              string   `json:"source"`
	FileIDs             []string `json:"file_ids,omitempty"`
}

type SlackPostFinalResponseJobPayload struct {
	OrgID              string `json:"org_id"`
	SessionID          string `json:"session_id"`
	SlackSessionLinkID string `json:"slack_session_link_id"`
	FinalMessageID     int64  `json:"final_message_id"`
}

type SlackPostRunUpdateJobPayload struct {
	OrgID              string `json:"org_id"`
	SessionID          string `json:"session_id"`
	SlackSessionLinkID string `json:"slack_session_link_id"`
	UpdateKind         string `json:"update_kind"`
	Title              string `json:"title"`
	Summary            string `json:"summary"`
	Terminal           bool   `json:"terminal"`
}

type SlackDeliverHumanInputJobPayload struct {
	OrgID       string `json:"org_id"`
	SessionID   string `json:"session_id"`
	RequestID   string `json:"request_id"`
	SlackUserID string `json:"slack_user_id,omitempty"`
}

type SlackSendNotificationJobPayload struct {
	OrgID          string `json:"org_id"`
	NotificationID string `json:"notification_id,omitempty"`
	Kind           string `json:"kind"`
	TeamID         string `json:"team_id"`
	ChannelID      string `json:"channel_id,omitempty"`
	SlackUserID    string `json:"slack_user_id,omitempty"`
	ThreadTS       string `json:"thread_ts,omitempty"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	SessionID      string `json:"session_id,omitempty"`
	PreviewID      string `json:"preview_id,omitempty"`
}

type SlackInteractionJobPayload struct {
	OrgID               string          `json:"org_id"`
	SlackInboundEventID string          `json:"slack_inbound_event_id"`
	SlackInstallationID string          `json:"slack_installation_id"`
	TeamID              string          `json:"team_id"`
	ChannelID           string          `json:"channel_id"`
	UserID              string          `json:"user_id"`
	ActionID            string          `json:"action_id"`
	CallbackID          string          `json:"callback_id"`
	Value               string          `json:"value"`
	TriggerID           string          `json:"trigger_id,omitempty"`
	ViewID              string          `json:"view_id,omitempty"`
	MessageTS           string          `json:"message_ts"`
	RawPayload          json.RawMessage `json:"raw_payload"`
}
