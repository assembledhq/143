package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
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
