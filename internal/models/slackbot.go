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
	SlackInboundEventStatusIgnored   SlackInboundEventStatus = "ignored"
)

func (s SlackInboundEventStatus) Validate() error {
	switch s {
	case SlackInboundEventStatusReceived, SlackInboundEventStatusEnqueued, SlackInboundEventStatusProcessed, SlackInboundEventStatusFailed, SlackInboundEventStatusIgnored:
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

type SlackOrgSelection struct {
	ID                  uuid.UUID `db:"id" json:"id"`
	OrgID               uuid.UUID `db:"org_id" json:"org_id"`
	SlackInstallationID uuid.UUID `db:"slack_installation_id" json:"slack_installation_id"`
	SlackTeamID         string    `db:"slack_team_id" json:"slack_team_id"`
	APIAppID            string    `db:"api_app_id" json:"api_app_id"`
	SlackUserID         string    `db:"slack_user_id" json:"slack_user_id"`
	SelectedAt          time.Time `db:"selected_at" json:"selected_at"`
	CreatedAt           time.Time `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time `db:"updated_at" json:"updated_at"`
}

type SlackInstallationHealth struct {
	Installation           SlackInstallation      `json:"installation"`
	RequiredScopes         []string               `json:"required_scopes"`
	MissingScopes          []string               `json:"missing_scopes"`
	LastEventAt            *time.Time             `json:"last_event_at,omitempty"`
	LastAuthCheckAt        *time.Time             `json:"last_auth_check_at,omitempty"`
	AuthOK                 bool                   `json:"auth_ok"`
	AuthError              *IntegrationAuthError  `json:"auth_error,omitempty"`
	Symptoms               []string               `json:"symptoms,omitempty"`
	RecentCallbackFailures []SlackCallbackFailure `json:"recent_callback_failures,omitempty"`
}

type SlackCallbackFailure struct {
	ID         uuid.UUID `json:"id"`
	DeliveryID *string   `json:"delivery_id,omitempty"`
	EventType  string    `json:"event_type"`
	Error      *string   `json:"error,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
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

type LinearUserLinkSource string

const (
	LinearUserLinkSourceObserved    LinearUserLinkSource = "observed"
	LinearUserLinkSourceEmailMatch  LinearUserLinkSource = "email_match"
	LinearUserLinkSourceSelfLinked  LinearUserLinkSource = "self_linked"
	LinearUserLinkSourceAdminLinked LinearUserLinkSource = "admin_linked"
)

func (s LinearUserLinkSource) Validate() error {
	switch s {
	case LinearUserLinkSourceObserved, LinearUserLinkSourceEmailMatch, LinearUserLinkSourceSelfLinked, LinearUserLinkSourceAdminLinked:
		return nil
	default:
		return fmt.Errorf("invalid LinearUserLinkSource: %q", s)
	}
}

type LinearUserLink struct {
	ID                 uuid.UUID            `db:"id" json:"id"`
	OrgID              uuid.UUID            `db:"org_id" json:"org_id"`
	IntegrationID      uuid.UUID            `db:"integration_id" json:"integration_id"`
	UserID             *uuid.UUID           `db:"user_id" json:"user_id,omitempty"`
	LinearWorkspaceKey string               `db:"linear_workspace_key" json:"linear_workspace_key"`
	LinearUserID       string               `db:"linear_user_id" json:"linear_user_id"`
	LinearEmail        *string              `db:"linear_email" json:"linear_email,omitempty"`
	LinearDisplayName  string               `db:"linear_display_name" json:"linear_display_name"`
	Source             LinearUserLinkSource `db:"source" json:"source"`
	LinkedAt           *time.Time           `db:"linked_at" json:"linked_at,omitempty"`
	CreatedAt          time.Time            `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time            `db:"updated_at" json:"updated_at"`
}

type SlackChannelSettings struct {
	ID                        uuid.UUID                `db:"id" json:"id"`
	OrgID                     uuid.UUID                `db:"org_id" json:"org_id"`
	SlackInstallationID       uuid.UUID                `db:"slack_installation_id" json:"slack_installation_id"`
	SlackTeamID               string                   `db:"slack_team_id" json:"slack_team_id"`
	SlackChannelID            string                   `db:"slack_channel_id" json:"slack_channel_id"`
	SlackChannelName          string                   `db:"slack_channel_name" json:"slack_channel_name"`
	ChannelType               string                   `db:"channel_type" json:"channel_type"`
	DefaultRepositoryID       *uuid.UUID               `db:"default_repository_id" json:"default_repository_id,omitempty"`
	DefaultBranch             *string                  `db:"default_branch" json:"default_branch,omitempty"`
	RoutingMode               *SlackRoutingMode        `db:"routing_mode" json:"routing_mode,omitempty"`
	ResponseVisibility        *SlackResponseVisibility `db:"response_visibility" json:"response_visibility,omitempty"`
	AllowedActions            []string                 `db:"allowed_actions" json:"allowed_actions,omitempty"`
	NotificationPreset        *SlackNotificationPreset `db:"notification_preset" json:"notification_preset,omitempty"`
	NotificationSubscriptions json.RawMessage          `db:"notification_subscriptions" json:"notification_subscriptions,omitempty"`
	Active                    bool                     `db:"active" json:"active"`
	CreatedAt                 time.Time                `db:"created_at" json:"created_at"`
	UpdatedAt                 time.Time                `db:"updated_at" json:"updated_at"`
}

type SlackResponseVisibility string

const (
	SlackResponseVisibilityThread SlackResponseVisibility = "thread"
	SlackResponseVisibilityDM     SlackResponseVisibility = "dm"
)

func (s SlackResponseVisibility) Validate() error {
	switch s {
	case SlackResponseVisibilityThread, SlackResponseVisibilityDM:
		return nil
	default:
		return fmt.Errorf("invalid SlackResponseVisibility: %q", s)
	}
}

type SlackChannelAction string

const (
	SlackChannelActionSession    SlackChannelAction = "session"
	SlackChannelActionPreview    SlackChannelAction = "preview"
	SlackChannelActionPRRequest  SlackChannelAction = "pr_request"
	SlackChannelActionHumanInput SlackChannelAction = "human_input"
)

func (s SlackChannelAction) Validate() error {
	switch s {
	case SlackChannelActionSession, SlackChannelActionPreview, SlackChannelActionPRRequest, SlackChannelActionHumanInput:
		return nil
	default:
		return fmt.Errorf("invalid SlackChannelAction: %q", s)
	}
}

type SlackRoutingMode string

const (
	SlackRoutingModeAuto       SlackRoutingMode = "auto"
	SlackRoutingModeAnswerOnly SlackRoutingMode = "answer_only"
	SlackRoutingModeStartWork  SlackRoutingMode = "start_work"
)

func (s SlackRoutingMode) Validate() error {
	switch s {
	case SlackRoutingModeAuto, SlackRoutingModeAnswerOnly, SlackRoutingModeStartWork:
		return nil
	default:
		return fmt.Errorf("invalid SlackRoutingMode: %q", s)
	}
}

type SlackNotificationPreset string

const (
	SlackNotificationPresetQuiet    SlackNotificationPreset = "quiet"
	SlackNotificationPresetBalanced SlackNotificationPreset = "balanced"
	SlackNotificationPresetVerbose  SlackNotificationPreset = "verbose"
	SlackNotificationPresetCustom   SlackNotificationPreset = "custom"
)

func (s SlackNotificationPreset) Validate() error {
	switch s {
	case SlackNotificationPresetQuiet, SlackNotificationPresetBalanced, SlackNotificationPresetVerbose, SlackNotificationPresetCustom:
		return nil
	default:
		return fmt.Errorf("invalid SlackNotificationPreset: %q", s)
	}
}

type SlackNotificationKind string

const (
	SlackNotificationSessionCompleted        SlackNotificationKind = "session.completed"
	SlackNotificationSessionFailed           SlackNotificationKind = "session.failed"
	SlackNotificationAutomationCompleted     SlackNotificationKind = "automation.run.completed"
	SlackNotificationAutomationFailed        SlackNotificationKind = "automation.run.failed"
	SlackNotificationAutomationFailureStreak SlackNotificationKind = "automation.run.failure_streak"
	SlackNotificationPROpened                SlackNotificationKind = "pr.opened"
	SlackNotificationPreviewReady            SlackNotificationKind = "preview.ready"
	SlackNotificationPreviewFailed           SlackNotificationKind = "preview.failed"
	SlackNotificationPreviewStale            SlackNotificationKind = "preview.stale"
	SlackNotificationHumanInputRequested     SlackNotificationKind = "human_input.requested"
)

func (s SlackNotificationKind) Validate() error {
	switch s {
	case SlackNotificationSessionCompleted,
		SlackNotificationSessionFailed,
		SlackNotificationAutomationCompleted,
		SlackNotificationAutomationFailed,
		SlackNotificationAutomationFailureStreak,
		SlackNotificationPROpened,
		SlackNotificationPreviewReady,
		SlackNotificationPreviewFailed,
		SlackNotificationPreviewStale,
		SlackNotificationHumanInputRequested:
		return nil
	default:
		return fmt.Errorf("invalid SlackNotificationKind: %q", s)
	}
}

type SlackBotSettings struct {
	ID                        uuid.UUID               `db:"id" json:"id"`
	OrgID                     uuid.UUID               `db:"org_id" json:"org_id"`
	SlackInstallationID       uuid.UUID               `db:"slack_installation_id" json:"slack_installation_id"`
	DefaultRepositoryID       *uuid.UUID              `db:"default_repository_id" json:"default_repository_id,omitempty"`
	DefaultBranch             *string                 `db:"default_branch" json:"default_branch,omitempty"`
	RoutingMode               SlackRoutingMode        `db:"routing_mode" json:"routing_mode"`
	ResponseVisibility        SlackResponseVisibility `db:"response_visibility" json:"response_visibility"`
	AllowedActions            []string                `db:"allowed_actions" json:"allowed_actions"`
	NotificationPreset        SlackNotificationPreset `db:"notification_preset" json:"notification_preset"`
	NotificationSubscriptions json.RawMessage         `db:"notification_subscriptions" json:"notification_subscriptions,omitempty"`
	Active                    bool                    `db:"active" json:"active"`
	CreatedAt                 time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt                 time.Time               `db:"updated_at" json:"updated_at"`
}

type EffectiveSlackChannelSettings struct {
	OrgID                     uuid.UUID               `db:"org_id" json:"org_id"`
	SlackInstallationID       uuid.UUID               `db:"slack_installation_id" json:"slack_installation_id"`
	SlackTeamID               string                  `db:"slack_team_id" json:"slack_team_id"`
	SlackChannelID            string                  `db:"slack_channel_id" json:"slack_channel_id"`
	DefaultRepositoryID       *uuid.UUID              `db:"default_repository_id" json:"default_repository_id,omitempty"`
	DefaultBranch             *string                 `db:"default_branch" json:"default_branch,omitempty"`
	RoutingMode               SlackRoutingMode        `db:"routing_mode" json:"routing_mode"`
	ResponseVisibility        SlackResponseVisibility `db:"response_visibility" json:"response_visibility"`
	AllowedActions            []string                `db:"allowed_actions" json:"allowed_actions"`
	NotificationPreset        SlackNotificationPreset `db:"notification_preset" json:"notification_preset"`
	NotificationSubscriptions json.RawMessage         `db:"notification_subscriptions" json:"notification_subscriptions,omitempty"`
	HasChannelOverride        bool                    `db:"has_channel_override" json:"has_channel_override"`
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
	LatestProgressKind    *string    `db:"latest_progress_kind" json:"latest_progress_kind,omitempty"`
	FinalMessageTS        *string    `db:"final_message_ts" json:"final_message_ts,omitempty"`
	CreatedAt             time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time  `db:"updated_at" json:"updated_at"`
}

type SlackSessionClaim struct {
	ID                   uuid.UUID `db:"id" json:"id"`
	OrgID                uuid.UUID `db:"org_id" json:"org_id"`
	SlackSessionLinkID   uuid.UUID `db:"slack_session_link_id" json:"slack_session_link_id"`
	ClaimedByUserID      uuid.UUID `db:"claimed_by_user_id" json:"claimed_by_user_id"`
	ClaimedBySlackUserID string    `db:"claimed_by_slack_user_id" json:"claimed_by_slack_user_id"`
	ClaimedAt            time.Time `db:"claimed_at" json:"claimed_at"`
}

type SlackInboundEvent struct {
	ID                  uuid.UUID               `db:"id" json:"id"`
	OrgID               uuid.UUID               `db:"org_id" json:"org_id"`
	WebhookDeliveryID   *uuid.UUID              `db:"webhook_delivery_id" json:"webhook_delivery_id,omitempty"`
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
	OrgID              string `json:"org_id"`
	NotificationID     string `json:"notification_id,omitempty"`
	Kind               string `json:"kind"`
	TeamID             string `json:"team_id"`
	ChannelID          string `json:"channel_id,omitempty"`
	SlackUserID        string `json:"slack_user_id,omitempty"`
	ThreadTS           string `json:"thread_ts,omitempty"`
	Title              string `json:"title"`
	Body               string `json:"body"`
	SessionID          string `json:"session_id,omitempty"`
	AutomationID       string `json:"automation_id,omitempty"`
	AutomationRunID    string `json:"automation_run_id,omitempty"`
	PullRequestID      string `json:"pull_request_id,omitempty"`
	PullRequestURL     string `json:"pull_request_url,omitempty"`
	PreviewID          string `json:"preview_id,omitempty"`
	ActorUserID        string `json:"actor_user_id,omitempty"`
	NotificationPreset string `json:"notification_preset,omitempty"`
}

type SlackPreviewTargetKind string

const (
	SlackPreviewTargetSession     SlackPreviewTargetKind = "session"
	SlackPreviewTargetPullRequest SlackPreviewTargetKind = "pull_request"
	SlackPreviewTargetBranch      SlackPreviewTargetKind = "branch"
	SlackPreviewTargetCommit      SlackPreviewTargetKind = "commit"
	SlackPreviewTargetRepository  SlackPreviewTargetKind = "repository"
)

type SlackPreviewTarget struct {
	Kind          SlackPreviewTargetKind `json:"kind"`
	RepositoryID  uuid.UUID              `json:"repository_id"`
	SessionID     *uuid.UUID             `json:"session_id,omitempty"`
	PullRequestID *uuid.UUID             `json:"pull_request_id,omitempty"`
	Branch        string                 `json:"branch,omitempty"`
	CommitSHA     string                 `json:"commit_sha,omitempty"`
	ConfigName    string                 `json:"config_name,omitempty"`
}

type SlackActor struct {
	UserID      uuid.UUID `json:"user_id"`
	SlackTeamID string    `json:"slack_team_id,omitempty"`
	SlackUserID string    `json:"slack_user_id,omitempty"`
}

type SlackNotificationRenderInput struct {
	Kind            SlackNotificationKind   `json:"kind"`
	Preset          SlackNotificationPreset `json:"preset"`
	Title           string                  `json:"title"`
	Body            string                  `json:"body"`
	SessionID       *uuid.UUID              `json:"session_id,omitempty"`
	AutomationID    *uuid.UUID              `json:"automation_id,omitempty"`
	AutomationRunID *uuid.UUID              `json:"automation_run_id,omitempty"`
	PullRequestID   *uuid.UUID              `json:"pull_request_id,omitempty"`
	PreviewID       *uuid.UUID              `json:"preview_id,omitempty"`
	ActorUserID     *uuid.UUID              `json:"actor_user_id,omitempty"`
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
