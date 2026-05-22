package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

const previewAPITokenColumns = `id, org_id, name, token_hash, scopes, repository_ids,
	created_by_user_id, last_used_at, revoked_at, created_at`

type PreviewAPITokenStore struct {
	db DBTX
}

func NewPreviewAPITokenStore(db DBTX) *PreviewAPITokenStore {
	return &PreviewAPITokenStore{db: db}
}

func GeneratePreviewAPIToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate preview api token: %w", err)
	}
	return "143_prev_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func HashPreviewAPIToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *PreviewAPITokenStore) Create(ctx context.Context, token *models.PreviewAPIToken) error {
	query := fmt.Sprintf(`INSERT INTO preview_api_tokens (
		org_id, name, token_hash, scopes, repository_ids, created_by_user_id
	) VALUES (
		@org_id, @name, @token_hash, @scopes, @repository_ids, @created_by_user_id
	) RETURNING %s`, previewAPITokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":             token.OrgID,
		"name":               token.Name,
		"token_hash":         token.TokenHash,
		"scopes":             token.Scopes,
		"repository_ids":     token.RepositoryIDs,
		"created_by_user_id": token.CreatedByUserID,
	})
	if err != nil {
		return fmt.Errorf("create preview api token: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewAPIToken])
	if err != nil {
		return fmt.Errorf("scan preview api token: %w", err)
	}
	*token = row
	return nil
}

// lint:allow-no-orgid reason="pre-auth bearer token lookup resolves the org from the token hash"
func (s *PreviewAPITokenStore) GetByToken(ctx context.Context, token string) (models.PreviewAPIToken, error) {
	hash := HashPreviewAPIToken(token)
	query := fmt.Sprintf(`UPDATE preview_api_tokens
		SET last_used_at = now()
		WHERE token_hash = @token_hash AND revoked_at IS NULL
		RETURNING %s`, previewAPITokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token_hash": hash})
	if err != nil {
		return models.PreviewAPIToken{}, fmt.Errorf("query preview api token: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewAPIToken])
	if err != nil {
		return models.PreviewAPIToken{}, fmt.Errorf("get preview api token: %w", err)
	}
	return row, nil
}

func (s *PreviewAPITokenStore) List(ctx context.Context, orgID uuid.UUID) ([]models.PreviewAPIToken, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_api_tokens
		WHERE org_id = @org_id AND revoked_at IS NULL
		ORDER BY created_at DESC`, previewAPITokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list preview api tokens: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewAPIToken])
}

func (s *PreviewAPITokenStore) Revoke(ctx context.Context, orgID, tokenID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE preview_api_tokens
		SET revoked_at = now()
		WHERE id = @id AND org_id = @org_id AND revoked_at IS NULL`,
		pgx.NamedArgs{"id": tokenID, "org_id": orgID})
	if err != nil {
		return fmt.Errorf("revoke preview api token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview api token not found")
	}
	return nil
}
