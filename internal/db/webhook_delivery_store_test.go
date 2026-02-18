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

func strPtr(s string) *string {
	return &s
}
