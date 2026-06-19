package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const apiClientColumns = `id, org_id, name, description, status,
	created_by_user_id, disabled_by_user_id, disabled_at, created_at, updated_at`

const apiTokenColumns = `id, org_id, api_client_id, name, token_hash, token_prefix,
	scopes, repository_ids, allowed_ip_cidrs, expires_at, last_used_at, last_used_ip, last_used_user_agent,
	revoked_by_user_id, revoked_at, created_by_user_id, created_at` // #nosec G101 -- SQL column list, not a credential

type APIClientStore struct {
	db DBTX
}

func NewAPIClientStore(db DBTX) *APIClientStore {
	return &APIClientStore{db: db}
}

func GenerateAPIToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate api token: %w", err)
	}
	return "143_sk_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func HashAPIToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func APITokenPrefix(token string) string {
	if len(token) <= len("143_sk_")+4 {
		return token
	}
	return token[:len("143_sk_")+4]
}

func (s *APIClientStore) Create(ctx context.Context, client *models.APIClient) error {
	query := fmt.Sprintf(`INSERT INTO api_clients (
		org_id, name, description, status, created_by_user_id
	) VALUES (
		@org_id, @name, @description, @status, @created_by_user_id
	) RETURNING %s`, apiClientColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":             client.OrgID,
		"name":               client.Name,
		"description":        client.Description,
		"status":             client.Status,
		"created_by_user_id": client.CreatedByUserID,
	})
	if err != nil {
		return fmt.Errorf("create api client: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.APIClient])
	if err != nil {
		return fmt.Errorf("scan api client: %w", err)
	}
	*client = row
	return nil
}

func (s *APIClientStore) List(ctx context.Context, orgID uuid.UUID) ([]models.APIClient, error) {
	query := fmt.Sprintf(`SELECT %s FROM api_clients WHERE org_id = @org_id ORDER BY created_at DESC, id DESC`, apiClientColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list api clients: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.APIClient])
}

func (s *APIClientStore) Get(ctx context.Context, orgID, clientID uuid.UUID) (models.APIClient, error) {
	query := fmt.Sprintf(`SELECT %s FROM api_clients WHERE org_id = @org_id AND id = @id`, apiClientColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": clientID})
	if err != nil {
		return models.APIClient{}, fmt.Errorf("get api client: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.APIClient])
}

func (s *APIClientStore) Update(ctx context.Context, client *models.APIClient) error {
	query := fmt.Sprintf(`UPDATE api_clients
		SET name = @name, description = @description, status = @status,
			disabled_by_user_id = @disabled_by_user_id, disabled_at = @disabled_at, updated_at = now()
		WHERE org_id = @org_id AND id = @id
		RETURNING %s`, apiClientColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":                  client.ID,
		"org_id":              client.OrgID,
		"name":                client.Name,
		"description":         client.Description,
		"status":              client.Status,
		"disabled_by_user_id": client.DisabledByUserID,
		"disabled_at":         client.DisabledAt,
	})
	if err != nil {
		return fmt.Errorf("update api client: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.APIClient])
	if err != nil {
		return fmt.Errorf("scan api client update: %w", err)
	}
	*client = row
	return nil
}

func (s *APIClientStore) Disable(ctx context.Context, orgID, clientID, disabledBy uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE api_clients
		SET status = 'disabled', disabled_by_user_id = @disabled_by_user_id, disabled_at = now(), updated_at = now()
		WHERE org_id = @org_id AND id = @id AND status <> 'disabled'`,
		pgx.NamedArgs{"org_id": orgID, "id": clientID, "disabled_by_user_id": disabledBy})
	if err != nil {
		return fmt.Errorf("disable api client: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

type APITokenStore struct {
	db DBTX
}

func NewAPITokenStore(db DBTX) *APITokenStore {
	return &APITokenStore{db: db}
}

func (s *APITokenStore) Create(ctx context.Context, token *models.APIToken) error {
	query := fmt.Sprintf(`INSERT INTO api_tokens (
		org_id, api_client_id, name, token_hash, token_prefix, scopes,
		repository_ids, allowed_ip_cidrs, expires_at, created_by_user_id
	) VALUES (
		@org_id, @api_client_id, @name, @token_hash, @token_prefix, @scopes,
		@repository_ids, @allowed_ip_cidrs, @expires_at, @created_by_user_id
	) RETURNING %s`, apiTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":             token.OrgID,
		"api_client_id":      token.APIClientID,
		"name":               token.Name,
		"token_hash":         token.TokenHash,
		"token_prefix":       token.TokenPrefix,
		"scopes":             token.Scopes,
		"repository_ids":     token.RepositoryIDs,
		"allowed_ip_cidrs":   token.AllowedIPCidrs,
		"expires_at":         token.ExpiresAt,
		"created_by_user_id": token.CreatedByUserID,
	})
	if err != nil {
		return fmt.Errorf("create api token: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.APIToken])
	if err != nil {
		return fmt.Errorf("scan api token: %w", err)
	}
	*token = row
	return nil
}

// GetByToken resolves a plaintext bearer token before an org is known.
//
// lint:allow-no-orgid reason="pre-auth bearer token lookup resolves org_id from token hash"
func (s *APITokenStore) GetByToken(ctx context.Context, rawToken, ip, userAgent string) (models.AuthenticatedAPIToken, error) {
	hash := HashAPIToken(rawToken)
	query := `UPDATE api_tokens t
		SET last_used_at = now(), last_used_ip = @last_used_ip, last_used_user_agent = @last_used_user_agent
		FROM api_clients c
		WHERE t.api_client_id = c.id
			AND t.token_hash = @token_hash
			AND t.revoked_at IS NULL
			AND (t.expires_at IS NULL OR t.expires_at > now())
		RETURNING
			t.id AS token_id, t.org_id, t.api_client_id, t.name AS token_name, t.token_hash, t.token_prefix,
			t.scopes, t.repository_ids, t.allowed_ip_cidrs, t.expires_at, t.revoked_at, t.created_by_user_id,
			c.status AS client_status, c.name AS client_name`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"token_hash":           hash,
		"last_used_ip":         strings.TrimSpace(ip),
		"last_used_user_agent": strings.TrimSpace(userAgent),
	})
	if err != nil {
		return models.AuthenticatedAPIToken{}, fmt.Errorf("query api token: %w", err)
	}
	type tokenRow struct {
		TokenID         uuid.UUID              `db:"token_id"`
		OrgID           uuid.UUID              `db:"org_id"`
		APIClientID     uuid.UUID              `db:"api_client_id"`
		TokenName       string                 `db:"token_name"`
		TokenHash       string                 `db:"token_hash"`
		TokenPrefix     string                 `db:"token_prefix"`
		Scopes          []string               `db:"scopes"`
		RepositoryIDs   []uuid.UUID            `db:"repository_ids"`
		AllowedIPCidrs  []string               `db:"allowed_ip_cidrs"`
		ExpiresAt       *time.Time             `db:"expires_at"`
		RevokedAt       *time.Time             `db:"revoked_at"`
		CreatedByUserID *uuid.UUID             `db:"created_by_user_id"`
		ClientStatus    models.APIClientStatus `db:"client_status"`
		ClientName      string                 `db:"client_name"`
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[tokenRow])
	if err != nil {
		return models.AuthenticatedAPIToken{}, fmt.Errorf("get api token: %w", err)
	}
	return models.AuthenticatedAPIToken{
		Client: models.APIClient{
			ID:     row.APIClientID,
			OrgID:  row.OrgID,
			Name:   row.ClientName,
			Status: row.ClientStatus,
		},
		Token: models.APIToken{
			ID:              row.TokenID,
			OrgID:           row.OrgID,
			APIClientID:     row.APIClientID,
			Name:            row.TokenName,
			TokenHash:       row.TokenHash,
			TokenPrefix:     row.TokenPrefix,
			Scopes:          row.Scopes,
			RepositoryIDs:   row.RepositoryIDs,
			AllowedIPCidrs:  row.AllowedIPCidrs,
			ExpiresAt:       row.ExpiresAt,
			RevokedAt:       row.RevokedAt,
			CreatedByUserID: row.CreatedByUserID,
		},
	}, nil
}

func (s *APITokenStore) List(ctx context.Context, orgID, clientID uuid.UUID) ([]models.APIToken, error) {
	query := fmt.Sprintf(`SELECT %s FROM api_tokens
		WHERE org_id = @org_id AND api_client_id = @api_client_id AND revoked_at IS NULL
		ORDER BY created_at DESC, id DESC`, apiTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "api_client_id": clientID})
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.APIToken])
}

func (s *APITokenStore) Revoke(ctx context.Context, orgID, clientID, tokenID, revokedBy uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE api_tokens
		SET revoked_at = now(), revoked_by_user_id = @revoked_by_user_id
		WHERE org_id = @org_id AND api_client_id = @api_client_id AND id = @id AND revoked_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "api_client_id": clientID, "id": tokenID, "revoked_by_user_id": revokedBy})
	if err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
