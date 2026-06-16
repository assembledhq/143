package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestWebhookDeliveryStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewWebhookDeliveryStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	delivery := &models.WebhookDelivery{
		OrgID:         uuid.New(),
		IntegrationID: uuid.New(),
		Provider:      "github",
		EventType:     "push",
		Payload:       json.RawMessage(`{"action":"push"}`),
		Headers:       json.RawMessage(`{"X-Hub-Signature":"sha256=abc"}`),
		Status:        "pending",
	}

	mock.ExpectQuery("INSERT INTO webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "received_at", "created_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.Create(context.Background(), delivery)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, generatedID, delivery.ID, "should set the generated ID on the delivery")
	require.Equal(t, now, delivery.ReceivedAt, "should set the received_at timestamp on the delivery")
	require.Equal(t, now, delivery.CreatedAt, "should set the created_at timestamp on the delivery")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWebhookDeliveryStore_CreateOrGetReportsInsertStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		duplicateStatus  string
		expectedInserted bool
		expectReplay     bool
	}{
		{
			name:             "reports inserted delivery",
			expectedInserted: true,
		},
		{
			name:             "reports retryable failed duplicate",
			duplicateStatus:  "failed",
			expectedInserted: false,
		},
		{
			name:            "reports terminal processed duplicate as replay",
			duplicateStatus: "processed",
			expectReplay:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create pgx mock")
			defer mock.Close()

			orgID := uuid.New()
			integrationID := uuid.New()
			deliveryID := "slack-event-1"
			now := time.Now()
			delivery := &models.WebhookDelivery{
				OrgID:         orgID,
				IntegrationID: integrationID,
				Provider:      "slack",
				DeliveryID:    &deliveryID,
				EventType:     "app_mention",
				Status:        "received",
				Payload:       json.RawMessage(`{"type":"event_callback"}`),
			}

			insertExpectation := mock.ExpectQuery("INSERT INTO webhook_deliveries").
				WithArgs(
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				)
			if tt.duplicateStatus == "" {
				insertExpectation.WillReturnRows(
					pgxmock.NewRows([]string{"id", "received_at", "created_at"}).
						AddRow(uuid.New(), now, now),
				)
			} else {
				insertExpectation.WillReturnError(&pgconn.PgError{Code: "23505", Message: "duplicate delivery"})
				mock.ExpectQuery("SELECT id, org_id, integration_id").
					WithArgs("slack", &deliveryID).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "integration_id", "provider", "delivery_id", "event_type",
						"signature_valid", "received_at", "processed_at", "status", "attempts",
						"error", "payload", "headers", "created_at",
					}).AddRow(
						uuid.New(), orgID, integrationID, "slack", &deliveryID, "app_mention",
						nil, now, nil, tt.duplicateStatus, 1,
						nil, json.RawMessage(`{"type":"event_callback"}`), nil, now,
					))
			}

			inserted, err := NewWebhookDeliveryStore(mock).CreateOrGet(context.Background(), delivery)
			if tt.expectReplay {
				require.True(t, errors.Is(err, ErrWebhookDeliveryReplay), "terminal duplicate should return replay sentinel")
			} else {
				require.NoError(t, err, "CreateOrGet should not return error for inserted or retryable delivery")
				require.Equal(t, tt.expectedInserted, inserted, "CreateOrGet should report whether a new row was inserted")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWebhookDeliveryStore_CreateDuplicateDeliveryStatusHandling(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	deliveryID := "linear-delivery-1"
	now := time.Now()

	tests := []struct {
		name      string
		status    string
		expectErr error
	}{
		{
			name:   "reuses received delivery so provider retry is processed",
			status: "received",
		},
		{
			name:   "reuses failed delivery so provider retry is processed",
			status: "failed",
		},
		{
			name:      "returns replay for processed delivery",
			status:    "processed",
			expectErr: ErrWebhookDeliveryReplay,
		},
		{
			name:      "returns replay for ignored delivery",
			status:    "ignored",
			expectErr: ErrWebhookDeliveryReplay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create pgx mock")
			defer mock.Close()

			existingID := uuid.New()
			delivery := &models.WebhookDelivery{
				OrgID:         orgID,
				IntegrationID: integrationID,
				Provider:      "linear",
				DeliveryID:    &deliveryID,
				EventType:     "AgentSessionEvent",
				Payload:       json.RawMessage(`{"type":"AgentSessionEvent"}`),
				Status:        "received",
			}

			mock.ExpectQuery("INSERT INTO webhook_deliveries").
				WithArgs(
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				).
				WillReturnError(&pgconn.PgError{Code: "23505", Message: "duplicate delivery"})
			mock.ExpectQuery("SELECT id, org_id, integration_id").
				WithArgs("linear", &deliveryID).
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "org_id", "integration_id", "provider", "delivery_id", "event_type",
					"signature_valid", "received_at", "processed_at", "status", "attempts",
					"error", "payload", "headers", "created_at",
				}).AddRow(
					existingID, orgID, integrationID, "linear", &deliveryID, "AgentSessionEvent",
					nil, now, nil, tt.status, 1,
					nil, json.RawMessage(`{"type":"AgentSessionEvent"}`), nil, now,
				))

			err = NewWebhookDeliveryStore(mock).Create(context.Background(), delivery)
			if tt.expectErr != nil {
				require.True(t, errors.Is(err, tt.expectErr), "terminal duplicate deliveries should return the replay sentinel")
			} else {
				require.NoError(t, err, "non-terminal duplicate deliveries should be retried using the existing row")
				require.Equal(t, existingID, delivery.ID, "retry should hydrate the existing delivery id for MarkProcessed")
				require.Equal(t, tt.status, delivery.Status, "retry should preserve the existing delivery status")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWebhookDeliveryStore_MarkProcessed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		errMsg *string
	}{
		{
			name:   "marks delivery as processed without error",
			errMsg: nil,
		},
		{
			name:   "marks delivery as failed with error message",
			errMsg: strPtr("webhook processing failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewWebhookDeliveryStore(mock)

			delivery := &models.WebhookDelivery{
				ID:    uuid.New(),
				OrgID: uuid.New(),
			}

			mock.ExpectExec("UPDATE webhook_deliveries").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			err = store.MarkProcessed(context.Background(), delivery, tt.errMsg)
			require.NoError(t, err, "MarkProcessed should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWebhookDeliveryStore_MarkIgnored(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewWebhookDeliveryStore(mock)
	delivery := &models.WebhookDelivery{ID: uuid.New(), OrgID: uuid.New()}

	mock.ExpectExec("UPDATE webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.MarkIgnored(context.Background(), delivery)
	require.NoError(t, err, "MarkIgnored should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWebhookDeliveryStore_ListRecentFailures(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	deliveryID := "Ev-failed"
	errorMessage := "insert slack inbound event: boom"
	now := time.Now()
	store := NewWebhookDeliveryStore(mock)

	mock.ExpectQuery(`WHERE org_id = @org_id\s+AND provider = @provider\s+AND status = 'failed'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "provider", "delivery_id", "event_type",
			"signature_valid", "received_at", "processed_at", "status", "attempts",
			"error", "payload", "headers", "created_at",
		}).AddRow(
			uuid.New(), orgID, integrationID, "slack", &deliveryID, "app_mention",
			ptrBool(true), now, &now, "failed", 1,
			&errorMessage, json.RawMessage(`{"type":"event_callback"}`), nil, now,
		))

	failures, err := store.ListRecentFailures(context.Background(), orgID, "slack", now.Add(-24*time.Hour), 5)

	require.NoError(t, err, "ListRecentFailures should not return an error")
	require.Len(t, failures, 1, "ListRecentFailures should return matching failed deliveries")
	require.Equal(t, errorMessage, *failures[0].Error, "ListRecentFailures should include stored error message")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWebhookDeliveryStore_DeleteExpired(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewWebhookDeliveryStore(mock)

	mock.ExpectQuery("SELECT delete_expired_webhook_deliveries").
		WithArgs(90).
		WillReturnRows(pgxmock.NewRows([]string{"delete_expired_webhook_deliveries"}).AddRow(int64(15)))

	deleted, err := store.DeleteExpired(context.Background(), 90)
	require.NoError(t, err, "DeleteExpired should not return an error")
	require.Equal(t, int64(15), deleted, "should return the count of deleted deliveries")
	require.NoError(t, mock.ExpectationsWereMet())
}

func strPtr(s string) *string {
	return &s
}

func ptrBool(v bool) *bool {
	return &v
}
