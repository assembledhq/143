package db

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	appcrypto "github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

// OrgJoinTokenPrefix is the distinctive prefix for join tokens. Like the
// "143u_" CLI-token prefix it exists partly so leaked tokens are
// machine-findable by secret scanners.
const OrgJoinTokenPrefix = "143j_"

// joinTokenRandomLength is the length of the random part. The alphabet is
// strictly alphanumeric (no base64 '-'/'_') because the token travels as a
// path segment in /install/{join_token}, whose syntactic gate is
// ^143j_[A-Za-z0-9]{12,64}$. 24 chars over a 62-symbol alphabet ≈ 143 bits.
const joinTokenRandomLength = 24

const joinTokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

var ErrOrgJoinTokenNotRecoverable = errors.New("org join token raw value is not recoverable")

const orgJoinTokenColumns = `id, org_id, token_hash, token_prefix, role, name,
	raw_token_encrypted, created_by_user_id, max_uses, use_count, expires_at, revoked_at,
	revoked_by_user_id, created_at` // #nosec G101 -- SQL column list, not a credential

// GenerateOrgJoinToken returns a fresh "143j_..." join token whose random
// part is strictly alphanumeric so it survives URL-path templating and zsh
// word-splitting without quoting.
func GenerateOrgJoinToken() (string, error) {
	out := make([]byte, joinTokenRandomLength)
	max := big.NewInt(int64(len(joinTokenAlphabet)))
	for i := range out {
		n, err := crand.Int(crand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate org join token: %w", err)
		}
		out[i] = joinTokenAlphabet[n.Int64()]
	}
	return OrgJoinTokenPrefix + string(out), nil
}

// OrgJoinTokenDisplayPrefix returns "143j_" plus the first 8 random chars
// for UI display.
func OrgJoinTokenDisplayPrefix(token string) string {
	if len(token) <= len(OrgJoinTokenPrefix)+8 {
		return token
	}
	return token[:len(OrgJoinTokenPrefix)+8]
}

// OrgJoinTokenStore persists multi-use org join links.
type OrgJoinTokenStore struct {
	db     DBTX
	crypto *appcrypto.Service
}

func NewOrgJoinTokenStore(db DBTX, cryptoSvc ...*appcrypto.Service) *OrgJoinTokenStore {
	var svc *appcrypto.Service
	if len(cryptoSvc) > 0 {
		svc = cryptoSvc[0]
	}
	return &OrgJoinTokenStore{db: db, crypto: svc}
}

func (s *OrgJoinTokenStore) Create(ctx context.Context, token *models.OrgJoinToken, rawToken string) error {
	if rawToken == "" {
		return ErrOrgJoinTokenNotRecoverable
	}
	encrypted, err := s.encryptRawToken(rawToken)
	if err != nil {
		return fmt.Errorf("encrypt org join token: %w", err)
	}
	query := fmt.Sprintf(`INSERT INTO org_join_tokens (
		org_id, token_hash, token_prefix, raw_token_encrypted, role, name, created_by_user_id, max_uses, expires_at
	) VALUES (
		@org_id, @token_hash, @token_prefix, @raw_token_encrypted, @role, @name, @created_by_user_id, @max_uses, @expires_at
	) RETURNING %s`, orgJoinTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":              token.OrgID,
		"token_hash":          token.TokenHash,
		"token_prefix":        token.TokenPrefix,
		"raw_token_encrypted": encrypted,
		"role":                token.Role,
		"name":                token.Name,
		"created_by_user_id":  token.CreatedByUserID,
		"max_uses":            token.MaxUses,
		"expires_at":          token.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("create org join token: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgJoinToken])
	if err != nil {
		return fmt.Errorf("scan org join token: %w", err)
	}
	*token = row
	return nil
}

func (s *OrgJoinTokenStore) GetActiveRecoverableToken(ctx context.Context, orgID, id uuid.UUID) (models.OrgJoinToken, string, error) {
	query := fmt.Sprintf(`SELECT %s FROM org_join_tokens
		WHERE id = @id
		  AND org_id = @org_id
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		  AND (max_uses IS NULL OR use_count < max_uses)`, orgJoinTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return models.OrgJoinToken{}, "", fmt.Errorf("get recoverable org join token: %w", err)
	}
	token, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgJoinToken])
	if err != nil {
		return models.OrgJoinToken{}, "", err
	}
	rawToken, err := s.decryptRawToken(token.RawTokenEncrypted)
	if err != nil {
		return models.OrgJoinToken{}, "", err
	}
	return token, rawToken, nil
}

// List returns the org's join tokens for the admin UI, excluding revoked
// ones. Revoked links are kept in the table for the audit trail but are
// filtered out here so the settings list only shows links an admin can still
// act on (expired/exhausted ones stay visible until revoked).
func (s *OrgJoinTokenStore) List(ctx context.Context, orgID uuid.UUID) ([]models.OrgJoinToken, error) {
	query := fmt.Sprintf(`SELECT %s FROM org_join_tokens
		WHERE org_id = @org_id AND revoked_at IS NULL
		ORDER BY created_at DESC, id DESC`, orgJoinTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list org join tokens: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.OrgJoinToken])
}

// Revoke marks a join token revoked. Returns pgx.ErrNoRows when the token
// doesn't exist in this org or is already revoked.
func (s *OrgJoinTokenStore) Revoke(ctx context.Context, orgID, id, revokedBy uuid.UUID) (models.OrgJoinToken, error) {
	query := fmt.Sprintf(`UPDATE org_join_tokens
		SET revoked_at = now(), revoked_by_user_id = @revoked_by
		WHERE id = @id AND org_id = @org_id AND revoked_at IS NULL
		RETURNING %s`, orgJoinTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID, "revoked_by": revokedBy})
	if err != nil {
		return models.OrgJoinToken{}, fmt.Errorf("revoke org join token: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgJoinToken])
}

func (s *OrgJoinTokenStore) encryptRawToken(rawToken string) ([]byte, error) {
	if s.crypto != nil {
		return s.crypto.Encrypt([]byte(rawToken))
	}
	return appcrypto.DevEncrypt([]byte(rawToken)), nil
}

func (s *OrgJoinTokenStore) decryptRawToken(encrypted []byte) (string, error) {
	if len(encrypted) == 0 {
		return "", ErrOrgJoinTokenNotRecoverable
	}
	if s.crypto != nil {
		plaintext, err := s.crypto.Decrypt(encrypted)
		if err != nil {
			return "", fmt.Errorf("decrypt org join token: %w", err)
		}
		return string(plaintext), nil
	}
	plaintext, err := appcrypto.DevDecrypt(encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt org join token: %w", err)
	}
	return string(plaintext), nil
}

// ConsumeByToken atomically validates a raw join token and increments its
// use counter in one statement: not revoked, not expired, and under
// max_uses. Zero rows updated means the token is invalid or just raced out
// of uses — the caller treats both as JOIN_TOKEN_INVALID. Run inside the
// same transaction that creates the membership so an aborted join never
// burns a use.
//
// lint:allow-no-orgid reason="pre-auth redemption by opaque token hash; the returned row carries org_id and the membership write is scoped to it"
func (s *OrgJoinTokenStore) ConsumeByToken(ctx context.Context, rawToken string) (models.OrgJoinToken, error) {
	query := fmt.Sprintf(`UPDATE org_join_tokens
		SET use_count = use_count + 1
		WHERE token_hash = @token_hash
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		  AND (max_uses IS NULL OR use_count < max_uses)
		RETURNING %s`, orgJoinTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token_hash": HashAPIToken(rawToken)})
	if err != nil {
		return models.OrgJoinToken{}, fmt.Errorf("consume org join token: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgJoinToken])
}

// GetActiveByToken resolves a raw join token to its row when currently
// valid, without consuming a use. Used for the existing-member no-op check:
// someone already in the org shouldn't burn a use just by logging in with a
// join link.
//
// lint:allow-no-orgid reason="pre-auth lookup by opaque token hash; org scoping is carried by the returned row"
func (s *OrgJoinTokenStore) GetActiveByToken(ctx context.Context, rawToken string) (models.OrgJoinToken, error) {
	query := fmt.Sprintf(`SELECT %s FROM org_join_tokens
		WHERE token_hash = @token_hash
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		  AND (max_uses IS NULL OR use_count < max_uses)`, orgJoinTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token_hash": HashAPIToken(rawToken)})
	if err != nil {
		return models.OrgJoinToken{}, fmt.Errorf("get org join token: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgJoinToken])
}
