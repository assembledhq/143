package db

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type WebhookDeliveryStore struct {
	db DBTX
}

func NewWebhookDeliveryStore(db DBTX) *WebhookDeliveryStore {
	return &WebhookDeliveryStore{db: db}
}

func (s *WebhookDeliveryStore) Create(ctx context.Context, d *models.WebhookDelivery) error {
	query := `
		INSERT INTO webhook_deliveries (org_id, integration_id, provider, delivery_id, event_type, signature_valid, payload, headers, status)
		VALUES (@org_id, @integration_id, @provider, @delivery_id, @event_type, @signature_valid, @payload, @headers, @status)
		RETURNING id, received_at, created_at`

	args := pgx.NamedArgs{
		"org_id":          d.OrgID,
		"integration_id":  d.IntegrationID,
		"provider":        d.Provider,
		"delivery_id":     d.DeliveryID,
		"event_type":      d.EventType,
		"signature_valid": d.SignatureValid,
		"payload":         d.Payload,
		"headers":         d.Headers,
		"status":          d.Status,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&d.ID, &d.ReceivedAt, &d.CreatedAt)
}

func (s *WebhookDeliveryStore) MarkProcessed(ctx context.Context, d *models.WebhookDelivery, errMsg *string) error {
	status := "processed"
	if errMsg != nil {
		status = "failed"
	}

	query := `
		UPDATE webhook_deliveries
		SET status = @status, processed_at = now(), attempts = attempts + 1, error = @error
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     d.ID,
		"org_id": d.OrgID,
		"status": status,
		"error":  errMsg,
	})
	return err
}

// DeleteExpired removes webhook deliveries older than the given number of days.
// lint:allow-no-orgid reason="cross-org retention cleanup across all orgs"
func (s *WebhookDeliveryStore) DeleteExpired(ctx context.Context, retentionDays int) (int64, error) {
	var deleted int64
	err := s.db.QueryRow(ctx,
		"SELECT delete_expired_webhook_deliveries($1)", retentionDays,
	).Scan(&deleted)
	return deleted, err
}
