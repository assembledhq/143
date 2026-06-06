package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type APIIdempotencyRecord struct {
	ID              uuid.UUID       `db:"id"`
	OrgID           uuid.UUID       `db:"org_id"`
	APIClientID     uuid.UUID       `db:"api_client_id"`
	APITokenID      uuid.UUID       `db:"api_token_id"`
	IdempotencyKey  string          `db:"idempotency_key"`
	Method          string          `db:"method"`
	Path            string          `db:"path"`
	RequestBodyHash string          `db:"request_body_hash"`
	ResponseStatus  *int            `db:"response_status"`
	ResponseBody    json.RawMessage `db:"response_body"`
	CreatedAt       time.Time       `db:"created_at"`
	ExpiresAt       time.Time       `db:"expires_at"`
}

type APIIdempotencyStore struct {
	db DBTX
}

func NewAPIIdempotencyStore(db DBTX) *APIIdempotencyStore {
	return &APIIdempotencyStore{db: db}
}

func (s *APIIdempotencyStore) Get(ctx context.Context, orgID, apiClientID uuid.UUID, method, path, key string) (APIIdempotencyRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT id, org_id, api_client_id, api_token_id, idempotency_key,
			method, path, request_body_hash, response_status, response_body, created_at, expires_at
		FROM api_idempotency_keys
		WHERE org_id = @org_id AND api_client_id = @api_client_id AND method = @method AND path = @path
			AND idempotency_key = @idempotency_key AND expires_at > now()`,
		pgx.NamedArgs{"org_id": orgID, "api_client_id": apiClientID, "method": method, "path": path, "idempotency_key": key})
	if err != nil {
		return APIIdempotencyRecord{}, fmt.Errorf("get api idempotency key: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[APIIdempotencyRecord])
}

func (s *APIIdempotencyStore) Create(ctx context.Context, orgID, apiClientID, apiTokenID uuid.UUID, method, path, key, bodyHash string, expiresAt time.Time) (bool, error) {
	tag, err := s.db.Exec(ctx, `INSERT INTO api_idempotency_keys (
			org_id, api_client_id, api_token_id, method, path, idempotency_key, request_body_hash, locked_at, expires_at
		) VALUES (
			@org_id, @api_client_id, @api_token_id, @method, @path, @idempotency_key, @request_body_hash, now(), @expires_at
		) ON CONFLICT (org_id, api_client_id, method, path, idempotency_key) DO NOTHING`,
		pgx.NamedArgs{
			"org_id": orgID, "api_client_id": apiClientID, "api_token_id": apiTokenID,
			"method": method, "path": path, "idempotency_key": key,
			"request_body_hash": bodyHash, "expires_at": expiresAt,
		})
	if err != nil {
		return false, fmt.Errorf("create api idempotency key: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *APIIdempotencyStore) SaveResponse(ctx context.Context, orgID, apiClientID uuid.UUID, method, path, key string, status int, body json.RawMessage) error {
	tag, err := s.db.Exec(ctx, `UPDATE api_idempotency_keys
		SET response_status = @response_status, response_body = @response_body
		WHERE org_id = @org_id AND api_client_id = @api_client_id AND method = @method AND path = @path AND idempotency_key = @idempotency_key`,
		pgx.NamedArgs{
			"org_id": orgID, "api_client_id": apiClientID, "method": method, "path": path,
			"idempotency_key": key, "response_status": status, "response_body": body,
		})
	if err != nil {
		return fmt.Errorf("save api idempotency response: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("save api idempotency response: record not found or already expired")
	}
	return nil
}
