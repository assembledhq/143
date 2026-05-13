package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// ErrWebhookDeliveryReplay is returned by Create when a row with the
// same (provider, delivery_id) already exists. The schema enforces this
// via the partial unique index idx_webhook_deliveries_idempotency. Lets
// the ingestion handler short-circuit replay attempts before they reach
// downstream dispatchers (which then can't double-emit bootstrap
// activities or double-enqueue worker jobs).
var ErrWebhookDeliveryReplay = errors.New("webhook delivery already recorded for (provider, delivery_id)")

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
	if err := row.Scan(&d.ID, &d.ReceivedAt, &d.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return s.handleDuplicateDelivery(ctx, d)
		}
		return err
	}
	return nil
}

func (s *WebhookDeliveryStore) handleDuplicateDelivery(ctx context.Context, d *models.WebhookDelivery) error {
	if d.DeliveryID == nil {
		return ErrWebhookDeliveryReplay
	}
	existing, err := s.getByProviderDeliveryID(ctx, d.Provider, d.DeliveryID)
	if err != nil {
		return err
	}
	if existing.OrgID != d.OrgID || existing.IntegrationID != d.IntegrationID {
		return fmt.Errorf("%w: existing delivery belongs to another integration", ErrWebhookDeliveryReplay)
	}
	switch existing.Status {
	case "received", "failed":
		*d = existing
		return nil
	case "processed", "ignored":
		*d = existing
		return ErrWebhookDeliveryReplay
	default:
		return fmt.Errorf("%w: existing delivery has terminal status %q", ErrWebhookDeliveryReplay, existing.Status)
	}
}

func (s *WebhookDeliveryStore) getByProviderDeliveryID(ctx context.Context, provider string, deliveryID *string) (models.WebhookDelivery, error) {
	var d models.WebhookDelivery
	err := s.db.QueryRow(ctx, `
		SELECT id, org_id, integration_id, provider, delivery_id, event_type,
		       signature_valid, received_at, processed_at, status, attempts,
		       error, payload, headers, created_at
		FROM webhook_deliveries
		WHERE provider = @provider
		  AND delivery_id = @delivery_id`,
		pgx.NamedArgs{
			"provider":    provider,
			"delivery_id": deliveryID,
		}).Scan(
		&d.ID,
		&d.OrgID,
		&d.IntegrationID,
		&d.Provider,
		&d.DeliveryID,
		&d.EventType,
		&d.SignatureValid,
		&d.ReceivedAt,
		&d.ProcessedAt,
		&d.Status,
		&d.Attempts,
		&d.Error,
		&d.Payload,
		&d.Headers,
		&d.CreatedAt,
	)
	if err != nil {
		return models.WebhookDelivery{}, fmt.Errorf("lookup existing webhook delivery: %w", err)
	}
	return d, nil
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
