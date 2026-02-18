package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookDeliveryStore_Create_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Equal(t, generatedID, delivery.ID)
	assert.Equal(t, now, delivery.ReceivedAt)
	assert.Equal(t, now, delivery.CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWebhookDeliveryStore_MarkProcessed_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewWebhookDeliveryStore(mock)

	delivery := &models.WebhookDelivery{
		ID:    uuid.New(),
		OrgID: uuid.New(),
	}

	mock.ExpectExec("UPDATE webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.MarkProcessed(context.Background(), delivery, nil)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWebhookDeliveryStore_MarkProcessed_WithError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewWebhookDeliveryStore(mock)

	delivery := &models.WebhookDelivery{
		ID:    uuid.New(),
		OrgID: uuid.New(),
	}

	errMsg := "webhook processing failed"

	mock.ExpectExec("UPDATE webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.MarkProcessed(context.Background(), delivery, &errMsg)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
