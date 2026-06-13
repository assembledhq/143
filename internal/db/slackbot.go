package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SlackInstallationStore struct {
	db DBTX
}

func NewSlackInstallationStore(db DBTX) *SlackInstallationStore {
	return &SlackInstallationStore{db: db}
}

func (s *SlackInstallationStore) Upsert(ctx context.Context, installation *models.SlackInstallation) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO slack_installations (
			org_id, integration_id, team_id, team_name, enterprise_id, api_app_id,
			bot_user_id, bot_id, scope, status, installed_by_user_id
		)
		VALUES (
			@org_id, @integration_id, @team_id, @team_name, @enterprise_id, @api_app_id,
			@bot_user_id, @bot_id, @scope, @status, @installed_by_user_id
		)
		ON CONFLICT (org_id, team_id, api_app_id)
		DO UPDATE SET
			integration_id = EXCLUDED.integration_id,
			team_name = EXCLUDED.team_name,
			enterprise_id = EXCLUDED.enterprise_id,
			bot_user_id = EXCLUDED.bot_user_id,
			bot_id = EXCLUDED.bot_id,
			scope = EXCLUDED.scope,
			status = EXCLUDED.status,
			installed_by_user_id = EXCLUDED.installed_by_user_id,
			updated_at = now()
		RETURNING id, org_id, integration_id, team_id, team_name, enterprise_id, api_app_id,
			bot_user_id, bot_id, scope, status, installed_by_user_id, installed_at,
			last_event_at, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":               installation.OrgID,
			"integration_id":       installation.IntegrationID,
			"team_id":              installation.TeamID,
			"team_name":            installation.TeamName,
			"enterprise_id":        installation.EnterpriseID,
			"api_app_id":           installation.APIAppID,
			"bot_user_id":          installation.BotUserID,
			"bot_id":               installation.BotID,
			"scope":                installation.Scope,
			"status":               installation.Status,
			"installed_by_user_id": installation.InstalledByUserID,
		})
	if err != nil {
		return fmt.Errorf("upsert slack installation: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackInstallation])
	if err != nil {
		return fmt.Errorf("scan slack installation: %w", err)
	}
	*installation = updated
	return nil
}

// GetActiveByTeamApp resolves an inbound Slack callback to its active org install.
// lint:allow-no-orgid reason="pre-auth Slack callback resolves org from signed team/app identifiers"
func (s *SlackInstallationStore) GetActiveByTeamApp(ctx context.Context, teamID, apiAppID string) (models.SlackInstallation, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, integration_id, team_id, team_name, enterprise_id, api_app_id,
			bot_user_id, bot_id, scope, status, installed_by_user_id, installed_at,
			last_event_at, created_at, updated_at
		FROM slack_installations
		WHERE team_id = @team_id
		  AND api_app_id = @api_app_id
		  AND status = 'active'
		ORDER BY updated_at DESC
		LIMIT 1`,
		pgx.NamedArgs{"team_id": teamID, "api_app_id": apiAppID})
	if err != nil {
		return models.SlackInstallation{}, fmt.Errorf("query slack installation: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackInstallation])
}

func (s *SlackInstallationStore) MarkLastEvent(ctx context.Context, orgID, installationID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE slack_installations
		SET last_event_at = now(), updated_at = now()
		WHERE org_id = @org_id AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "id": installationID})
	return err
}

func (s *SlackInstallationStore) MarkDisconnected(ctx context.Context, orgID, installationID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE slack_installations
		SET status = 'disconnected', updated_at = now()
		WHERE org_id = @org_id AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "id": installationID})
	return err
}

func (s *SlackInstallationStore) GetActiveByOrg(ctx context.Context, orgID uuid.UUID) (models.SlackInstallation, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, integration_id, team_id, team_name, enterprise_id, api_app_id,
			bot_user_id, bot_id, scope, status, installed_by_user_id, installed_at,
			last_event_at, created_at, updated_at
		FROM slack_installations
		WHERE org_id = @org_id AND status = 'active'
		ORDER BY updated_at DESC
		LIMIT 1`,
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return models.SlackInstallation{}, fmt.Errorf("query active slack installation: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackInstallation])
}

type SlackInboundEventStore struct {
	db DBTX
}

func NewSlackInboundEventStore(db DBTX) *SlackInboundEventStore {
	return &SlackInboundEventStore{db: db}
}

func (s *SlackInboundEventStore) CreateReceived(ctx context.Context, event *models.SlackInboundEvent) (bool, error) {
	if event.Status == "" {
		event.Status = models.SlackInboundEventStatusReceived
	}
	rows, err := s.db.Query(ctx, `
		INSERT INTO slack_inbound_events (
			org_id, slack_installation_id, slack_event_id, slack_team_id, event_type,
			channel_id, user_id, event_ts, payload, status
		)
		VALUES (
			@org_id, @slack_installation_id, @slack_event_id, @slack_team_id, @event_type,
			@channel_id, @user_id, @event_ts, @payload, @status
		)
		ON CONFLICT (org_id, slack_event_id) WHERE slack_event_id IS NOT NULL DO NOTHING
		RETURNING id, org_id, slack_installation_id, slack_event_id, slack_team_id, event_type,
			channel_id, user_id, event_ts, payload, status, job_id, error, received_at, processed_at`,
		pgx.NamedArgs{
			"org_id":                event.OrgID,
			"slack_installation_id": event.SlackInstallationID,
			"slack_event_id":        event.SlackEventID,
			"slack_team_id":         event.SlackTeamID,
			"event_type":            event.EventType,
			"channel_id":            event.ChannelID,
			"user_id":               event.UserID,
			"event_ts":              event.EventTS,
			"payload":               event.Payload,
			"status":                event.Status,
		})
	if err != nil {
		return false, fmt.Errorf("insert slack inbound event: %w", err)
	}
	inserted, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackInboundEvent])
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("scan slack inbound event: %w", err)
	}
	*event = inserted
	return true, nil
}

func (s *SlackInboundEventStore) MarkEnqueued(ctx context.Context, orgID, eventID, jobID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE slack_inbound_events
		SET status = 'enqueued', job_id = @job_id
		WHERE org_id = @org_id AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "id": eventID, "job_id": jobID})
	return err
}

func (s *SlackInboundEventStore) MarkProcessed(ctx context.Context, orgID, eventID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE slack_inbound_events
		SET status = 'processed', processed_at = now()
		WHERE org_id = @org_id AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "id": eventID})
	return err
}

func (s *SlackInboundEventStore) MarkFailed(ctx context.Context, orgID, eventID uuid.UUID, message string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE slack_inbound_events
		SET status = 'failed', error = @error, processed_at = now()
		WHERE org_id = @org_id AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "id": eventID, "error": message})
	return err
}

type SlackUserLinkStore struct {
	db DBTX
}

func NewSlackUserLinkStore(db DBTX) *SlackUserLinkStore {
	return &SlackUserLinkStore{db: db}
}

func (s *SlackUserLinkStore) GetBySlackUser(ctx context.Context, orgID uuid.UUID, teamID, slackUserID string) (models.SlackUserLink, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, slack_installation_id, user_id, slack_team_id, slack_user_id,
			slack_email, slack_display_name, source, linked_at, created_at, updated_at
		FROM slack_user_links
		WHERE org_id = @org_id
		  AND slack_team_id = @slack_team_id
		  AND slack_user_id = @slack_user_id`,
		pgx.NamedArgs{"org_id": orgID, "slack_team_id": teamID, "slack_user_id": slackUserID})
	if err != nil {
		return models.SlackUserLink{}, fmt.Errorf("query slack user link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackUserLink])
}

func (s *SlackUserLinkStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.SlackUserLink, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, slack_installation_id, user_id, slack_team_id, slack_user_id,
			slack_email, slack_display_name, source, linked_at, created_at, updated_at
		FROM slack_user_links
		WHERE org_id = @org_id
		ORDER BY updated_at DESC, created_at DESC`,
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query slack user links: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SlackUserLink])
}

func (s *SlackUserLinkStore) UpsertSelfLink(ctx context.Context, link *models.SlackUserLink) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO slack_user_links (
			org_id, slack_installation_id, user_id, slack_team_id, slack_user_id,
			slack_email, slack_display_name, source, linked_at
		)
		VALUES (
			@org_id, @slack_installation_id, @user_id, @slack_team_id, @slack_user_id,
			@slack_email, @slack_display_name, 'self_linked', now()
		)
		ON CONFLICT (org_id, slack_team_id, slack_user_id)
		DO UPDATE SET
			user_id = EXCLUDED.user_id,
			slack_email = EXCLUDED.slack_email,
			slack_display_name = EXCLUDED.slack_display_name,
			source = 'self_linked',
			linked_at = now(),
			updated_at = now()
		RETURNING id, org_id, slack_installation_id, user_id, slack_team_id, slack_user_id,
			slack_email, slack_display_name, source, linked_at, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":                link.OrgID,
			"slack_installation_id": link.SlackInstallationID,
			"user_id":               link.UserID,
			"slack_team_id":         link.SlackTeamID,
			"slack_user_id":         link.SlackUserID,
			"slack_email":           link.SlackEmail,
			"slack_display_name":    link.SlackDisplayName,
		})
	if err != nil {
		return fmt.Errorf("upsert slack self link: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackUserLink])
	if err != nil {
		return fmt.Errorf("scan slack self link: %w", err)
	}
	*link = updated
	return nil
}

func (s *SlackUserLinkStore) UpsertEmailMatch(ctx context.Context, link *models.SlackUserLink) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO slack_user_links (
			org_id, slack_installation_id, user_id, slack_team_id, slack_user_id,
			slack_email, slack_display_name, source, linked_at
		)
		VALUES (
			@org_id, @slack_installation_id, @user_id, @slack_team_id, @slack_user_id,
			@slack_email, @slack_display_name, 'email_match', now()
		)
		ON CONFLICT (org_id, slack_team_id, slack_user_id)
		DO UPDATE SET
			user_id = COALESCE(slack_user_links.user_id, EXCLUDED.user_id),
			slack_email = EXCLUDED.slack_email,
			slack_display_name = EXCLUDED.slack_display_name,
			source = CASE WHEN slack_user_links.source = 'self_linked' THEN slack_user_links.source ELSE 'email_match' END,
			linked_at = COALESCE(slack_user_links.linked_at, now()),
			updated_at = now()
		RETURNING id, org_id, slack_installation_id, user_id, slack_team_id, slack_user_id,
			slack_email, slack_display_name, source, linked_at, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":                link.OrgID,
			"slack_installation_id": link.SlackInstallationID,
			"user_id":               link.UserID,
			"slack_team_id":         link.SlackTeamID,
			"slack_user_id":         link.SlackUserID,
			"slack_email":           link.SlackEmail,
			"slack_display_name":    link.SlackDisplayName,
		})
	if err != nil {
		return fmt.Errorf("upsert slack email match: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackUserLink])
	if err != nil {
		return fmt.Errorf("scan slack email match: %w", err)
	}
	*link = updated
	return nil
}

func (s *SlackUserLinkStore) UpsertAdminLink(ctx context.Context, link *models.SlackUserLink) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO slack_user_links (
			org_id, slack_installation_id, user_id, slack_team_id, slack_user_id,
			slack_email, slack_display_name, source, linked_at
		)
		VALUES (
			@org_id, @slack_installation_id, @user_id, @slack_team_id, @slack_user_id,
			@slack_email, @slack_display_name, 'admin_linked', now()
		)
		ON CONFLICT (org_id, slack_team_id, slack_user_id)
		DO UPDATE SET
			slack_installation_id = EXCLUDED.slack_installation_id,
			user_id = EXCLUDED.user_id,
			slack_email = EXCLUDED.slack_email,
			slack_display_name = EXCLUDED.slack_display_name,
			source = 'admin_linked',
			linked_at = now(),
			updated_at = now()
		RETURNING id, org_id, slack_installation_id, user_id, slack_team_id, slack_user_id,
			slack_email, slack_display_name, source, linked_at, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":                link.OrgID,
			"slack_installation_id": link.SlackInstallationID,
			"user_id":               link.UserID,
			"slack_team_id":         link.SlackTeamID,
			"slack_user_id":         link.SlackUserID,
			"slack_email":           link.SlackEmail,
			"slack_display_name":    link.SlackDisplayName,
		})
	if err != nil {
		return fmt.Errorf("upsert slack admin link: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackUserLink])
	if err != nil {
		return fmt.Errorf("scan slack admin link: %w", err)
	}
	*link = updated
	return nil
}

func (s *SlackUserLinkStore) DeleteSelfLink(ctx context.Context, orgID, userID uuid.UUID, teamID string) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM slack_user_links
		WHERE org_id = @org_id
		  AND user_id = @user_id
		  AND slack_team_id = @slack_team_id
		  AND source = 'self_linked'`,
		pgx.NamedArgs{"org_id": orgID, "user_id": userID, "slack_team_id": teamID})
	return err
}

func (s *SlackUserLinkStore) DeleteByID(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM slack_user_links
		WHERE org_id = @org_id
		  AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "id": id})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

type SlackChannelSettingsStore struct {
	db DBTX
}

func NewSlackChannelSettingsStore(db DBTX) *SlackChannelSettingsStore {
	return &SlackChannelSettingsStore{db: db}
}

func (s *SlackChannelSettingsStore) GetByChannel(ctx context.Context, orgID uuid.UUID, teamID, channelID string) (models.SlackChannelSettings, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, slack_installation_id, slack_team_id, slack_channel_id, slack_channel_name,
			channel_type, default_repository_id, default_branch, response_visibility, allowed_actions,
			notification_subscriptions, active, created_at, updated_at
		FROM slack_channel_settings
		WHERE org_id = @org_id
		  AND slack_team_id = @slack_team_id
		  AND slack_channel_id = @slack_channel_id
		  AND active = true`,
		pgx.NamedArgs{"org_id": orgID, "slack_team_id": teamID, "slack_channel_id": channelID})
	if err != nil {
		return models.SlackChannelSettings{}, fmt.Errorf("query slack channel settings: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackChannelSettings])
}

func (s *SlackChannelSettingsStore) Upsert(ctx context.Context, settings *models.SlackChannelSettings) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO slack_channel_settings (
			org_id, slack_installation_id, slack_team_id, slack_channel_id, slack_channel_name,
			channel_type, default_repository_id, default_branch, response_visibility, allowed_actions,
			notification_subscriptions, active
		)
		VALUES (
			@org_id, @slack_installation_id, @slack_team_id, @slack_channel_id, @slack_channel_name,
			@channel_type, @default_repository_id, @default_branch, @response_visibility, @allowed_actions,
			@notification_subscriptions, true
		)
		ON CONFLICT (org_id, slack_team_id, slack_channel_id)
		DO UPDATE SET
			slack_installation_id = EXCLUDED.slack_installation_id,
			slack_channel_name = EXCLUDED.slack_channel_name,
			channel_type = EXCLUDED.channel_type,
			default_repository_id = EXCLUDED.default_repository_id,
			default_branch = EXCLUDED.default_branch,
			response_visibility = EXCLUDED.response_visibility,
			allowed_actions = EXCLUDED.allowed_actions,
			notification_subscriptions = EXCLUDED.notification_subscriptions,
			active = true,
			updated_at = now()
		RETURNING id, org_id, slack_installation_id, slack_team_id, slack_channel_id, slack_channel_name,
			channel_type, default_repository_id, default_branch, response_visibility, allowed_actions,
			notification_subscriptions, active, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":                     settings.OrgID,
			"slack_installation_id":      settings.SlackInstallationID,
			"slack_team_id":              settings.SlackTeamID,
			"slack_channel_id":           settings.SlackChannelID,
			"slack_channel_name":         settings.SlackChannelName,
			"channel_type":               settings.ChannelType,
			"default_repository_id":      settings.DefaultRepositoryID,
			"default_branch":             settings.DefaultBranch,
			"response_visibility":        settings.ResponseVisibility,
			"allowed_actions":            settings.AllowedActions,
			"notification_subscriptions": settings.NotificationSubscriptions,
		})
	if err != nil {
		return fmt.Errorf("upsert slack channel settings: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackChannelSettings])
	if err != nil {
		return fmt.Errorf("scan slack channel settings: %w", err)
	}
	*settings = updated
	return nil
}

func (s *SlackChannelSettingsStore) ListNotificationSubscriptions(ctx context.Context, orgID uuid.UUID) ([]models.SlackChannelSettings, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, slack_installation_id, slack_team_id, slack_channel_id, slack_channel_name,
			channel_type, default_repository_id, default_branch, response_visibility, allowed_actions,
			notification_subscriptions, active, created_at, updated_at
		FROM slack_channel_settings
		WHERE org_id = @org_id
		  AND active = true
		  AND notification_subscriptions <> '{}'::jsonb
		ORDER BY updated_at DESC`,
		pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query slack notification subscriptions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SlackChannelSettings])
}

type SlackSessionLinkStore struct {
	db DBTX
}

type SlackHomeSessionSummary struct {
	SessionID uuid.UUID `db:"session_id"`
	Title     *string   `db:"title"`
	Status    string    `db:"status"`
	UpdatedAt time.Time `db:"updated_at"`
}

type SlackHomeHumanInputSummary struct {
	SessionID uuid.UUID `db:"session_id"`
	RequestID uuid.UUID `db:"request_id"`
	Title     string    `db:"title"`
	Body      string    `db:"body"`
	CreatedAt time.Time `db:"created_at"`
}

type SlackHomePreviewSummary struct {
	PreviewID uuid.UUID  `db:"preview_id"`
	Name      string     `db:"name"`
	Status    string     `db:"status"`
	ExpiresAt *time.Time `db:"expires_at"`
	UpdatedAt time.Time  `db:"updated_at"`
}

type SlackHomeAutomationRunSummary struct {
	RunID         uuid.UUID  `db:"run_id"`
	AutomationID  uuid.UUID  `db:"automation_id"`
	GoalSnapshot  string     `db:"goal_snapshot"`
	Status        string     `db:"status"`
	ResultSummary *string    `db:"result_summary"`
	SessionID     *uuid.UUID `db:"session_id"`
	UpdatedAt     time.Time  `db:"updated_at"`
}

func NewSlackSessionLinkStore(db DBTX) *SlackSessionLinkStore {
	return &SlackSessionLinkStore{db: db}
}

func (s *SlackSessionLinkStore) GetByThread(ctx context.Context, orgID uuid.UUID, teamID, channelID, threadTS string) (models.SlackSessionLink, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, session_id, slack_installation_id, slack_team_id, slack_channel_id,
			slack_thread_ts, slack_root_ts, slack_message_permalink, slack_user_id, mapped_user_id,
			team_session, latest_status_message_ts, final_message_ts, created_at, updated_at
		FROM slack_session_links
		WHERE org_id = @org_id
		  AND slack_team_id = @slack_team_id
		  AND slack_channel_id = @slack_channel_id
		  AND slack_thread_ts = @slack_thread_ts`,
		pgx.NamedArgs{"org_id": orgID, "slack_team_id": teamID, "slack_channel_id": channelID, "slack_thread_ts": threadTS})
	if err != nil {
		return models.SlackSessionLink{}, fmt.Errorf("query slack session link: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackSessionLink])
}

func (s *SlackSessionLinkStore) GetBySession(ctx context.Context, orgID, sessionID uuid.UUID) (models.SlackSessionLink, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, session_id, slack_installation_id, slack_team_id, slack_channel_id,
			slack_thread_ts, slack_root_ts, slack_message_permalink, slack_user_id, mapped_user_id,
			team_session, latest_status_message_ts, final_message_ts, created_at, updated_at
		FROM slack_session_links
		WHERE org_id = @org_id
		  AND session_id = @session_id
		ORDER BY updated_at DESC
		LIMIT 1`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return models.SlackSessionLink{}, fmt.Errorf("query slack session link by session: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackSessionLink])
}

func (s *SlackSessionLinkStore) Upsert(ctx context.Context, link *models.SlackSessionLink) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO slack_session_links (
			org_id, session_id, slack_installation_id, slack_team_id, slack_channel_id,
			slack_thread_ts, slack_root_ts, slack_message_permalink, slack_user_id,
			mapped_user_id, team_session
		)
		VALUES (
			@org_id, @session_id, @slack_installation_id, @slack_team_id, @slack_channel_id,
			@slack_thread_ts, @slack_root_ts, @slack_message_permalink, @slack_user_id,
			@mapped_user_id, @team_session
		)
		ON CONFLICT (org_id, slack_team_id, slack_channel_id, slack_thread_ts)
		DO UPDATE SET
			session_id = EXCLUDED.session_id,
			slack_message_permalink = EXCLUDED.slack_message_permalink,
			slack_user_id = EXCLUDED.slack_user_id,
			mapped_user_id = EXCLUDED.mapped_user_id,
			team_session = EXCLUDED.team_session,
			updated_at = now()
		RETURNING id, org_id, session_id, slack_installation_id, slack_team_id, slack_channel_id,
			slack_thread_ts, slack_root_ts, slack_message_permalink, slack_user_id, mapped_user_id,
			team_session, latest_status_message_ts, final_message_ts, created_at, updated_at`,
		pgx.NamedArgs{
			"org_id":                  link.OrgID,
			"session_id":              link.SessionID,
			"slack_installation_id":   link.SlackInstallationID,
			"slack_team_id":           link.SlackTeamID,
			"slack_channel_id":        link.SlackChannelID,
			"slack_thread_ts":         link.SlackThreadTS,
			"slack_root_ts":           link.SlackRootTS,
			"slack_message_permalink": link.SlackMessagePermalink,
			"slack_user_id":           link.SlackUserID,
			"mapped_user_id":          link.MappedUserID,
			"team_session":            link.TeamSession,
		})
	if err != nil {
		return fmt.Errorf("upsert slack session link: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackSessionLink])
	if err != nil {
		return fmt.Errorf("scan slack session link: %w", err)
	}
	*link = updated
	return nil
}

func (s *SlackSessionLinkStore) ListRecentSessionsForSlackUser(ctx context.Context, orgID uuid.UUID, teamID, slackUserID string, limit int) ([]SlackHomeSessionSummary, error) {
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	rows, err := s.db.Query(ctx, `
		SELECT s.id AS session_id, s.title, s.status, s.updated_at
		FROM slack_session_links l
		JOIN sessions s ON s.org_id = l.org_id AND s.id = l.session_id
		WHERE l.org_id = @org_id
		  AND l.slack_team_id = @team_id
		  AND l.slack_user_id = @slack_user_id
		ORDER BY s.updated_at DESC
		LIMIT @limit`,
		pgx.NamedArgs{"org_id": orgID, "team_id": teamID, "slack_user_id": slackUserID, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("query slack home recent sessions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[SlackHomeSessionSummary])
}

func (s *SlackSessionLinkStore) ListPendingHumanInputsForSlackUser(ctx context.Context, orgID uuid.UUID, teamID, slackUserID string, limit int) ([]SlackHomeHumanInputSummary, error) {
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	rows, err := s.db.Query(ctx, `
		SELECT r.session_id, r.id AS request_id, r.title, r.body, r.created_at
		FROM slack_session_links l
		JOIN session_human_input_requests r
		  ON r.org_id = l.org_id AND r.session_id = l.session_id
		WHERE l.org_id = @org_id
		  AND l.slack_team_id = @team_id
		  AND l.slack_user_id = @slack_user_id
		  AND r.status = 'pending'
		ORDER BY r.created_at ASC
		LIMIT @limit`,
		pgx.NamedArgs{"org_id": orgID, "team_id": teamID, "slack_user_id": slackUserID, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("query slack home pending human inputs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[SlackHomeHumanInputSummary])
}

func (s *SlackSessionLinkStore) ListActivePreviewsForSlackUser(ctx context.Context, orgID uuid.UUID, teamID, slackUserID string, limit int) ([]SlackHomePreviewSummary, error) {
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	rows, err := s.db.Query(ctx, `
		WITH matched AS (
			SELECT DISTINCT ON (p.id) p.id AS preview_id, p.name, p.status, p.expires_at, p.updated_at
			FROM slack_user_links l
			LEFT JOIN slack_session_links sl
			  ON sl.org_id = l.org_id
			 AND sl.slack_team_id = l.slack_team_id
			 AND sl.slack_user_id = l.slack_user_id
			JOIN preview_instances p
			  ON p.org_id = l.org_id
			 AND (
			   p.user_id = l.user_id
			   OR (sl.session_id IS NOT NULL AND p.session_id = sl.session_id)
			 )
			WHERE l.org_id = @org_id
			  AND l.slack_team_id = @team_id
			  AND l.slack_user_id = @slack_user_id
			  AND p.status IN ('starting', 'ready', 'partially_ready', 'unhealthy')
			ORDER BY p.id, p.updated_at DESC
		)
		SELECT preview_id, name, status, expires_at, updated_at
		FROM matched
		ORDER BY updated_at DESC
		LIMIT @limit`,
		pgx.NamedArgs{"org_id": orgID, "team_id": teamID, "slack_user_id": slackUserID, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("query slack home active previews: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[SlackHomePreviewSummary])
}

func (s *SlackSessionLinkStore) ListRecentAutomationRunsForSlackUser(ctx context.Context, orgID uuid.UUID, teamID, slackUserID string, limit int) ([]SlackHomeAutomationRunSummary, error) {
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	rows, err := s.db.Query(ctx, `
		WITH matched AS (
			SELECT DISTINCT ON (ar.id) ar.id AS run_id, ar.automation_id, ar.goal_snapshot, ar.status, ar.result_summary,
			       session_match.session_id, ar.updated_at
			FROM slack_user_links l
			JOIN automations a
			  ON a.org_id = l.org_id
			 AND (
			   a.created_by = l.user_id
			   OR EXISTS (
			     SELECT 1
			     FROM slack_channel_settings scs
			     WHERE scs.org_id = l.org_id
			       AND scs.slack_team_id = l.slack_team_id
			       AND scs.active = true
			       AND EXISTS (
			         SELECT 1
			         FROM jsonb_array_elements_text(COALESCE(scs.notification_subscriptions->'automations', '[]'::jsonb)) subscribed_automation(id)
			         WHERE subscribed_automation.id = a.id::text
			       )
			   )
			 )
			JOIN automation_runs ar
			  ON ar.org_id = a.org_id AND ar.automation_id = a.id
			LEFT JOIN LATERAL (
				SELECT s.id AS session_id
				FROM sessions s
				WHERE s.org_id = ar.org_id AND s.automation_run_id = ar.id
				ORDER BY s.updated_at DESC
				LIMIT 1
			) session_match ON true
			WHERE l.org_id = @org_id
			  AND l.slack_team_id = @team_id
			  AND l.slack_user_id = @slack_user_id
			ORDER BY ar.id, ar.updated_at DESC
		)
		SELECT run_id, automation_id, goal_snapshot, status, result_summary, session_id, updated_at
		FROM matched
		ORDER BY updated_at DESC
		LIMIT @limit`,
		pgx.NamedArgs{"org_id": orgID, "team_id": teamID, "slack_user_id": slackUserID, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("query slack home automation runs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[SlackHomeAutomationRunSummary])
}

func (s *SlackSessionLinkStore) SetLatestStatusMessageTS(ctx context.Context, orgID, sessionID uuid.UUID, messageTS string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE slack_session_links
		SET latest_status_message_ts = @message_ts,
		    updated_at = now()
		WHERE org_id = @org_id
		  AND session_id = @session_id`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"session_id": sessionID,
			"message_ts": messageTS,
		},
	)
	if err != nil {
		return fmt.Errorf("update slack session status message timestamp: %w", err)
	}
	return nil
}

func (s *SlackSessionLinkStore) SetFinalMessageTS(ctx context.Context, orgID, sessionID uuid.UUID, messageTS string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE slack_session_links
		SET final_message_ts = @message_ts,
		    updated_at = now()
		WHERE org_id = @org_id
		  AND session_id = @session_id`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"session_id": sessionID,
			"message_ts": messageTS,
		},
	)
	if err != nil {
		return fmt.Errorf("update slack final message timestamp: %w", err)
	}
	return nil
}

type SlackOutboundMessageStore struct {
	db DBTX
}

func NewSlackOutboundMessageStore(db DBTX) *SlackOutboundMessageStore {
	return &SlackOutboundMessageStore{db: db}
}

func (s *SlackOutboundMessageStore) Upsert(ctx context.Context, msg *models.SlackOutboundMessage) error {
	query := `
		INSERT INTO slack_outbound_messages (
			org_id, slack_session_link_id, notification_id, slack_team_id,
			slack_channel_id, slack_message_ts, message_kind, status, last_payload_hash
		)
		VALUES (
			@org_id, @slack_session_link_id, @notification_id, @slack_team_id,
			@slack_channel_id, @slack_message_ts, @message_kind, @status, @last_payload_hash
		)
		ON CONFLICT (org_id, slack_team_id, slack_channel_id, slack_message_ts)
		DO UPDATE SET
			slack_session_link_id = EXCLUDED.slack_session_link_id,
			notification_id = EXCLUDED.notification_id,
			message_kind = EXCLUDED.message_kind,
			status = EXCLUDED.status,
			last_payload_hash = EXCLUDED.last_payload_hash,
			updated_at = now()
		RETURNING
			id, org_id, slack_session_link_id, notification_id, slack_team_id,
			slack_channel_id, slack_message_ts, message_kind, status,
			last_payload_hash, created_at, updated_at`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":                msg.OrgID,
		"slack_session_link_id": msg.SlackSessionLinkID,
		"notification_id":       msg.NotificationID,
		"slack_team_id":         msg.SlackTeamID,
		"slack_channel_id":      msg.SlackChannelID,
		"slack_message_ts":      msg.SlackMessageTS,
		"message_kind":          msg.MessageKind,
		"status":                msg.Status,
		"last_payload_hash":     msg.LastPayloadHash,
	})
	if err != nil {
		return fmt.Errorf("upsert slack outbound message: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SlackOutboundMessage])
	if err != nil {
		return fmt.Errorf("collect slack outbound message: %w", err)
	}
	*msg = updated
	return nil
}

type SessionAttributionStore struct {
	db DBTX
}

func NewSessionAttributionStore(db DBTX) *SessionAttributionStore {
	return &SessionAttributionStore{db: db}
}

func (s *SessionAttributionStore) Create(ctx context.Context, attribution *models.SessionAttribution) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO session_attributions (
			org_id, session_id, source, source_metadata
		)
		VALUES (
			@org_id, @session_id, @source, @source_metadata
		)
		ON CONFLICT (session_id) DO NOTHING
		RETURNING id, org_id, session_id, source, source_metadata, created_at`,
		pgx.NamedArgs{
			"org_id":          attribution.OrgID,
			"session_id":      attribution.SessionID,
			"source":          attribution.Source,
			"source_metadata": attribution.SourceMetadata,
		})
	if err != nil {
		return fmt.Errorf("insert session attribution: %w", err)
	}
	created, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionAttribution])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan session attribution: %w", err)
	}
	*attribution = created
	return nil
}
