package db

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// UserCLITokenPrefix is the distinctive prefix routing bearer auth to the
// user_cli_tokens table (vs. "143_sk_" org API tokens). It also makes leaked
// tokens machine-findable — register it with secret scanners.
const UserCLITokenPrefix = "143u_"

// UserCLITokenTTL is the sliding-expiry window. Authenticated use extends
// expires_at back out to now()+TTL (piggybacked on the throttled
// last_used_* write), so active devices never get logged out while a laptop
// idle for the full window does.
const UserCLITokenTTL = 90 * 24 * time.Hour

const userCLITokenColumns = `id, user_id, token_hash, token_prefix, device_name,
	last_org_id, expires_at, last_used_at, last_used_ip, revoked_at, created_at` // #nosec G101 -- SQL column list, not a credential

// GenerateUserCLIToken returns a fresh "143u_..." bearer token (32 bytes of
// entropy, URL-safe base64). The raw value is shown to the CLI exactly once;
// only the hash is persisted.
func GenerateUserCLIToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate user cli token: %w", err)
	}
	return UserCLITokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// UserCLITokenDisplayPrefix returns the prefix stored for UI display:
// "143u_" plus the first 8 characters of the random part.
func UserCLITokenDisplayPrefix(token string) string {
	if len(token) <= len(UserCLITokenPrefix)+8 {
		return token
	}
	return token[:len(UserCLITokenPrefix)+8]
}

// UserCLITokenStore persists per-user CLI credentials. All methods are
// user-scoped rather than org-scoped: a user's CLI tokens follow them across
// orgs (like auth_sessions) and active-org resolution happens per-request in
// the auth middleware.
type UserCLITokenStore struct {
	db DBTX
}

func NewUserCLITokenStore(db DBTX) *UserCLITokenStore {
	return &UserCLITokenStore{db: db}
}

// Create inserts a freshly-minted CLI token row.
//
// lint:allow-no-orgid reason="user-scoped credential row; org resolution happens per-request via memberships, mirroring auth_sessions"
func (s *UserCLITokenStore) Create(ctx context.Context, token *models.UserCLIToken) error {
	query := fmt.Sprintf(`INSERT INTO user_cli_tokens (
		user_id, token_hash, token_prefix, device_name, last_org_id, expires_at
	) VALUES (
		@user_id, @token_hash, @token_prefix, @device_name, @last_org_id, @expires_at
	) RETURNING %s`, userCLITokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"user_id":      token.UserID,
		"token_hash":   token.TokenHash,
		"token_prefix": token.TokenPrefix,
		"device_name":  token.DeviceName,
		"last_org_id":  token.LastOrgID,
		"expires_at":   token.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("create user cli token: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.UserCLIToken])
	if err != nil {
		return fmt.Errorf("scan user cli token: %w", err)
	}
	*token = row
	return nil
}

// GetActiveByToken resolves a raw bearer token to its active row. The hash
// is deterministic, so it is the lookup key; revoked and expired rows are
// excluded in SQL so the caller only ever sees a usable credential.
//
// lint:allow-no-orgid reason="pre-auth lookup by opaque bearer token; the row identifies the user, org scoping happens downstream in the middleware"
func (s *UserCLITokenStore) GetActiveByToken(ctx context.Context, rawToken string) (models.UserCLIToken, error) {
	query := fmt.Sprintf(`SELECT %s FROM user_cli_tokens
		WHERE token_hash = @token_hash
		  AND revoked_at IS NULL
		  AND expires_at > now()`, userCLITokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token_hash": HashAPIToken(rawToken)})
	if err != nil {
		return models.UserCLIToken{}, fmt.Errorf("get user cli token: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.UserCLIToken])
}

// TouchUsage stamps last_used_* and slides expires_at forward. Callers
// throttle this (once per minute) so the hot auth path doesn't write a row
// per request.
//
// lint:allow-no-orgid reason="usage stamp on a user-scoped credential identified by primary key during request auth"
func (s *UserCLITokenStore) TouchUsage(ctx context.Context, id uuid.UUID, ip string, expiresAt time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE user_cli_tokens
		SET last_used_at = now(), last_used_ip = @ip, expires_at = @expires_at
		WHERE id = @id AND revoked_at IS NULL`,
		pgx.NamedArgs{"id": id, "ip": ip, "expires_at": expiresAt})
	if err != nil {
		return fmt.Errorf("touch user cli token: %w", err)
	}
	return nil
}

// UpdateLastOrgID persists the active-org hint, mirroring
// auth_sessions.last_org_id semantics.
//
// lint:allow-no-orgid reason="writes the active-org hint itself; row is user-scoped and identified by primary key"
func (s *UserCLITokenStore) UpdateLastOrgID(ctx context.Context, id uuid.UUID, orgID *uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE user_cli_tokens SET last_org_id = @org_id WHERE id = @id`,
		pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return fmt.Errorf("update user cli token last_org_id: %w", err)
	}
	return nil
}

// ListByUser returns the caller's non-revoked CLI tokens, newest first, for
// the self-service "CLI sessions" surface. Expired rows are included so the
// UI can show "expired" rather than having devices silently vanish.
//
// lint:allow-no-orgid reason="self-service listing scoped to the authenticated user's own tokens"
func (s *UserCLITokenStore) ListByUser(ctx context.Context, userID uuid.UUID) ([]models.UserCLIToken, error) {
	query := fmt.Sprintf(`SELECT %s FROM user_cli_tokens
		WHERE user_id = @user_id AND revoked_at IS NULL
		ORDER BY created_at DESC, id DESC`, userCLITokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"user_id": userID})
	if err != nil {
		return nil, fmt.Errorf("list user cli tokens: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.UserCLIToken])
}

// Revoke marks one of the caller's own tokens revoked. The user_id guard
// makes the operation self-service-safe: you can only revoke what you own.
// Returns pgx.ErrNoRows when the row doesn't exist, isn't theirs, or is
// already revoked.
//
// lint:allow-no-orgid reason="self-service revocation scoped to the authenticated user's own token"
func (s *UserCLITokenStore) Revoke(ctx context.Context, userID, id uuid.UUID) (models.UserCLIToken, error) {
	query := fmt.Sprintf(`UPDATE user_cli_tokens SET revoked_at = now()
		WHERE id = @id AND user_id = @user_id AND revoked_at IS NULL
		RETURNING %s`, userCLITokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id, "user_id": userID})
	if err != nil {
		return models.UserCLIToken{}, fmt.Errorf("revoke user cli token: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.UserCLIToken])
}

// RevokeOtherDeviceTokens revokes the user's other tokens carrying the same
// device name, keeping keepID. Called by the CLI after a re-login is
// confirmed working, so the "CLI sessions" list stays one-row-per-device
// instead of accreting duplicates.
//
// lint:allow-no-orgid reason="device dedup across the authenticated user's own tokens"
func (s *UserCLITokenStore) RevokeOtherDeviceTokens(ctx context.Context, userID uuid.UUID, deviceName string, keepID uuid.UUID) (int64, error) {
	if deviceName == "" {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, `UPDATE user_cli_tokens SET revoked_at = now()
		WHERE user_id = @user_id AND device_name = @device_name
		  AND id <> @keep_id AND revoked_at IS NULL`,
		pgx.NamedArgs{"user_id": userID, "device_name": deviceName, "keep_id": keepID})
	if err != nil {
		return 0, fmt.Errorf("revoke other device cli tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RevokeAllForUser revokes every active CLI token a user holds. Called when
// a member removal leaves the user with zero memberships: bearer auth would
// already resolve no org for them, but cutting the credential entirely is
// the belt-and-suspenders the design calls for.
//
// lint:allow-no-orgid reason="offboarding sweep keyed by globally-unique user_id, invoked by the member-removal handler after org checks"
func (s *UserCLITokenStore) RevokeAllForUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE user_cli_tokens SET revoked_at = now()
		WHERE user_id = @user_id AND revoked_at IS NULL`,
		pgx.NamedArgs{"user_id": userID})
	if err != nil {
		return 0, fmt.Errorf("revoke all user cli tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}
