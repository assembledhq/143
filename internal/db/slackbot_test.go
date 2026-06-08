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
