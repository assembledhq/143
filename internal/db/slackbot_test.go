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
