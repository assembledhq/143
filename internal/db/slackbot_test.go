package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSlackInboundEventStore_CreateReceivedUsesPartialConflictPredicate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	installationID := uuid.New()
	eventID := "Ev123"
	channelID := "C123"
	userID := "U123"
	eventTS := "1710000001.000000"
	inboundID := uuid.New()
	store := NewSlackInboundEventStore(mock)

	mock.ExpectQuery(`ON CONFLICT \(org_id, slack_event_id\) WHERE slack_event_id IS NOT NULL DO NOTHING`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "slack_installation_id", "slack_event_id", "slack_team_id", "event_type",
			"channel_id", "user_id", "event_ts", "payload", "status", "job_id", "error", "received_at", "processed_at",
		}).AddRow(
			inboundID, orgID, installationID, &eventID, "T123", models.SlackInboundEventTypeAppMention,
			&channelID, &userID, &eventTS, json.RawMessage(`{"type":"event_callback"}`),
			models.SlackInboundEventStatusReceived, nil, nil, time.Now(), nil,
		))

	inserted, err := store.CreateReceived(context.Background(), &models.SlackInboundEvent{
		OrgID:               orgID,
		SlackInstallationID: installationID,
		SlackEventID:        &eventID,
		SlackTeamID:         "T123",
		EventType:           models.SlackInboundEventTypeAppMention,
		Payload:             json.RawMessage(`{"type":"event_callback"}`),
	})

	require.NoError(t, err, "CreateReceived should insert inbound event")
	require.True(t, inserted, "CreateReceived should report inserted event")
	require.NoError(t, mock.ExpectationsWereMet(), "CreateReceived should use the partial unique-index conflict predicate")
}

func TestSlackInboundEventStore_RedactPayloadsOlderThan(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	store := NewSlackInboundEventStore(mock)

	mock.ExpectExec(`(?s)UPDATE slack_inbound_events\s+SET payload = '\{\}'::jsonb\s+WHERE id IN \(`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 25))

	count, err := store.RedactPayloadsOlderThan(context.Background(), orgID, cutoff, 25)

	require.NoError(t, err, "RedactPayloadsOlderThan should clear old payloads")
	require.Equal(t, int64(25), count, "RedactPayloadsOlderThan should report rows affected")
	require.NoError(t, mock.ExpectationsWereMet(), "RedactPayloadsOlderThan should satisfy expected SQL")
}

func TestSlackUserLinkStore_GetByUserScopesByOrgAndTeam(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	installationID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	email := "eng@example.com"
	store := NewSlackUserLinkStore(mock)

	mock.ExpectQuery(`WHERE org_id = @org_id\s+AND user_id = @user_id\s+AND slack_team_id = @slack_team_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "slack_installation_id", "user_id", "slack_team_id", "slack_user_id",
			"slack_email", "slack_display_name", "source", "linked_at", "created_at", "updated_at",
		}).AddRow(
			linkID, orgID, installationID, &userID, "T123", "U123", &email, "Eng User",
			models.SlackUserLinkSourceAdminLinked, &now, now, now,
		))

	link, err := store.GetByUser(context.Background(), orgID, userID, "T123")

	require.NoError(t, err, "GetByUser should return the linked Slack user")
	require.Equal(t, "U123", link.SlackUserID, "GetByUser should return the Slack user for the mapped 143 user")
	require.NoError(t, mock.ExpectationsWereMet(), "GetByUser should satisfy expected SQL")
}

func TestSlackSessionLinkStore_ClaimTeamSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	linkID := uuid.New()
	userID := uuid.New()
	claimID := uuid.New()
	now := time.Now()
	store := NewSlackSessionLinkStore(mock)

	mock.ExpectQuery(`(?s)UPDATE slack_session_links .*INSERT INTO slack_session_claims`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "slack_session_link_id", "claimed_by_user_id", "claimed_by_slack_user_id", "claimed_at",
		}).AddRow(claimID, orgID, linkID, userID, "U123", now))

	claim, err := store.ClaimTeamSession(context.Background(), orgID, linkID, userID, "U123")

	require.NoError(t, err, "ClaimTeamSession should claim a team session")
	require.Equal(t, userID, claim.ClaimedByUserID, "ClaimTeamSession should return the claiming user")
	require.Equal(t, "U123", claim.ClaimedBySlackUserID, "ClaimTeamSession should return the claiming Slack user")
	require.NoError(t, mock.ExpectationsWereMet(), "ClaimTeamSession should satisfy expected SQL")
}

func TestSlackUserLinkStore_UpsertAdminLink(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	installationID := uuid.New()
	userID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	email := "eng@example.com"
	store := NewSlackUserLinkStore(mock)

	mock.ExpectQuery(`ON CONFLICT \(org_id, slack_team_id, slack_user_id\)`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "slack_installation_id", "user_id", "slack_team_id", "slack_user_id",
			"slack_email", "slack_display_name", "source", "linked_at", "created_at", "updated_at",
		}).AddRow(
			linkID, orgID, installationID, &userID, "T123", "U123", &email, "Eng User",
			models.SlackUserLinkSourceAdminLinked, &now, now, now,
		))

	link := &models.SlackUserLink{
		OrgID:               orgID,
		SlackInstallationID: installationID,
		UserID:              &userID,
		SlackTeamID:         "T123",
		SlackUserID:         "U123",
		SlackEmail:          &email,
		SlackDisplayName:    "Eng User",
	}

	err = store.UpsertAdminLink(context.Background(), link)

	require.NoError(t, err, "UpsertAdminLink should persist admin-managed mapping")
	require.Equal(t, models.SlackUserLinkSourceAdminLinked, link.Source, "UpsertAdminLink should mark mapping as admin linked")
	require.Equal(t, linkID, link.ID, "UpsertAdminLink should scan the stored link")
	require.NoError(t, mock.ExpectationsWereMet(), "UpsertAdminLink should satisfy expected SQL")
}

func TestSlackUserLinkStore_DeleteByID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	linkID := uuid.New()
	store := NewSlackUserLinkStore(mock)

	mock.ExpectExec(`DELETE FROM slack_user_links`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.DeleteByID(context.Background(), orgID, linkID)

	require.NoError(t, err, "DeleteByID should delete an org-scoped Slack user link")
	require.NoError(t, mock.ExpectationsWereMet(), "DeleteByID should satisfy expected SQL")
}

func TestSlackUserLinkStore_DeleteByID_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	linkID := uuid.New()
	store := NewSlackUserLinkStore(mock)

	mock.ExpectExec(`DELETE FROM slack_user_links`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	err = store.DeleteByID(context.Background(), orgID, linkID)

	require.ErrorIs(t, err, pgx.ErrNoRows, "DeleteByID should return ErrNoRows when no link is found")
	require.NoError(t, mock.ExpectationsWereMet(), "DeleteByID not-found should satisfy expected SQL")
}

func TestSlackBotSettingsStore_Upsert(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	installationID := uuid.New()
	repoID := uuid.New()
	settingsID := uuid.New()
	now := time.Now()
	branch := "main"
	store := NewSlackBotSettingsStore(mock)

	mock.ExpectQuery(`INSERT INTO slack_bot_settings`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "slack_installation_id", "default_repository_id", "default_branch", "routing_mode",
			"response_visibility", "allowed_actions", "notification_preset", "notification_subscriptions", "active", "created_at", "updated_at",
		}).AddRow(
			settingsID, orgID, installationID, &repoID, &branch, models.SlackRoutingModeAuto,
			models.SlackResponseVisibilityThread, []string{string(models.SlackChannelActionSession)}, models.SlackNotificationPresetBalanced,
			json.RawMessage(`{"events":["session.failed"]}`), true, now, now,
		))

	settings := &models.SlackBotSettings{
		OrgID:                     orgID,
		SlackInstallationID:       installationID,
		DefaultRepositoryID:       &repoID,
		DefaultBranch:             &branch,
		RoutingMode:               models.SlackRoutingModeAuto,
		ResponseVisibility:        models.SlackResponseVisibilityThread,
		AllowedActions:            []string{string(models.SlackChannelActionSession)},
		NotificationPreset:        models.SlackNotificationPresetBalanced,
		NotificationSubscriptions: json.RawMessage(`{"events":["session.failed"]}`),
		Active:                    true,
	}

	err = store.Upsert(context.Background(), settings)

	require.NoError(t, err, "Upsert should persist org-scoped Slackbot defaults")
	require.Equal(t, settingsID, settings.ID, "Upsert should scan the stored defaults")
	require.NoError(t, mock.ExpectationsWereMet(), "Upsert should satisfy expected SQL")
}

func TestSlackChannelSettingsStore_GetEffectiveByChannelFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	installationID := uuid.New()
	repoID := uuid.New()
	branch := "main"
	store := NewSlackChannelSettingsStore(mock)

	mock.ExpectQuery(`FROM slack_installations si`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"org_id", "slack_installation_id", "slack_team_id", "slack_channel_id", "default_repository_id", "default_branch",
			"routing_mode", "response_visibility", "allowed_actions", "notification_preset", "notification_subscriptions", "has_channel_override",
		}).AddRow(
			orgID, installationID, "T123", "C123", &repoID, &branch, models.SlackRoutingModeAuto,
			models.SlackResponseVisibilityThread, []string{string(models.SlackChannelActionSession), string(models.SlackChannelActionPreview)},
			models.SlackNotificationPresetBalanced, json.RawMessage(`{}`), false,
		))

	got, err := store.GetEffectiveByChannel(context.Background(), orgID, "T123", "C123")

	require.NoError(t, err, "GetEffectiveByChannel should resolve inherited defaults")
	require.Equal(t, models.EffectiveSlackChannelSettings{
		OrgID:                     orgID,
		SlackInstallationID:       installationID,
		SlackTeamID:               "T123",
		SlackChannelID:            "C123",
		DefaultRepositoryID:       &repoID,
		DefaultBranch:             &branch,
		RoutingMode:               models.SlackRoutingModeAuto,
		ResponseVisibility:        models.SlackResponseVisibilityThread,
		AllowedActions:            []string{string(models.SlackChannelActionSession), string(models.SlackChannelActionPreview)},
		NotificationPreset:        models.SlackNotificationPresetBalanced,
		NotificationSubscriptions: json.RawMessage(`{}`),
		HasChannelOverride:        false,
	}, got, "GetEffectiveByChannel should return the expected effective settings")
	require.NoError(t, mock.ExpectationsWereMet(), "GetEffectiveByChannel should satisfy expected SQL")
}

func TestSlackSessionLinkStore_SetLatestStatusProgress(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	store := NewSlackSessionLinkStore(mock)

	mock.ExpectExec(`UPDATE slack_session_links`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.SetLatestStatusProgress(context.Background(), orgID, sessionID, "1720000000.0001", "running_tests")

	require.NoError(t, err, "SetLatestStatusProgress should persist the status timestamp and kind")
	require.NoError(t, mock.ExpectationsWereMet(), "SetLatestStatusProgress should satisfy expected SQL")
}

func TestSessionAttributionStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	attributionID := uuid.New()
	now := time.Now()
	metadata := json.RawMessage(`{"slack_team_id":"T123","slack_channel_id":"C123","team_session":true}`)
	store := NewSessionAttributionStore(mock)

	mock.ExpectQuery(`INSERT INTO session_attributions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "source", "source_metadata", "created_at",
		}).AddRow(
			attributionID, orgID, sessionID, models.SessionAttributionSourceSlack, metadata, now,
		))

	attribution := &models.SessionAttribution{
		OrgID:          orgID,
		SessionID:      sessionID,
		Source:         models.SessionAttributionSourceSlack,
		SourceMetadata: metadata,
	}

	err = store.Create(context.Background(), attribution)

	require.NoError(t, err, "Create should persist an org-scoped session attribution")
	require.Equal(t, attributionID, attribution.ID, "Create should scan the stored attribution")
	require.Equal(t, metadata, attribution.SourceMetadata, "Create should preserve sanitized source metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "Create should satisfy expected SQL")
}

func TestSessionAttributionStore_Create_Idempotent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	metadata := json.RawMessage(`{"slack_team_id":"T123"}`)
	store := NewSessionAttributionStore(mock)

	mock.ExpectQuery(`INSERT INTO session_attributions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "source", "source_metadata", "created_at",
		}))

	attribution := &models.SessionAttribution{
		OrgID:          orgID,
		SessionID:      sessionID,
		Source:         models.SessionAttributionSourceSlack,
		SourceMetadata: metadata,
	}

	err = store.Create(context.Background(), attribution)

	require.NoError(t, err, "Create should be idempotent when attribution already exists for the session")
	require.NoError(t, mock.ExpectationsWereMet(), "Create conflict path should satisfy expected SQL")
}

func TestSlackSessionLinkStore_AppHomePreviewQueryIncludesLinkedSessions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	now := time.Now()
	store := NewSlackSessionLinkStore(mock)

	mock.ExpectQuery(`LEFT JOIN slack_session_links sl`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"preview_id", "name", "status", "expires_at", "updated_at"}).
			AddRow(previewID, "web", "ready", nil, now))

	got, err := store.ListActivePreviewsForSlackUser(context.Background(), orgID, "T123", "U123", 5)

	require.NoError(t, err, "App Home preview query should include linked-session previews")
	require.Equal(t, []SlackHomePreviewSummary{{PreviewID: previewID, Name: "web", Status: "ready", UpdatedAt: now}}, got, "App Home preview query should scan summaries")
	require.NoError(t, mock.ExpectationsWereMet(), "preview query should include Slack session links")
}

func TestSlackSessionLinkStore_AppHomeAutomationRunsIncludeSubscriptions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	automationID := uuid.New()
	now := time.Now()
	store := NewSlackSessionLinkStore(mock)

	mock.ExpectQuery(`jsonb_array_elements_text\(COALESCE\(scs.notification_subscriptions->'automations'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"run_id", "automation_id", "goal_snapshot", "status", "result_summary", "session_id", "updated_at"}).
			AddRow(runID, automationID, "ship the thing", "completed", nil, nil, now))

	got, err := store.ListRecentAutomationRunsForSlackUser(context.Background(), orgID, "T123", "U123", 5)

	require.NoError(t, err, "App Home automation query should include subscribed automations")
	require.Equal(t, []SlackHomeAutomationRunSummary{{RunID: runID, AutomationID: automationID, GoalSnapshot: "ship the thing", Status: "completed", UpdatedAt: now}}, got, "App Home automation query should scan summaries")
	require.NoError(t, mock.ExpectationsWereMet(), "automation query should inspect channel automation subscriptions")
}
