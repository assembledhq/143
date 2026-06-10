package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// CLIAuthCodeTTL bounds the window between the browser completing OAuth and
// the CLI exchanging the one-time code. 60 seconds covers the loopback
// redirect plus one HTTP round-trip with a wide margin while keeping a
// leaked code (browser history, proxy log) useless almost immediately —
// and the verifier binding makes even a live code unredeemable by anyone
// but the CLI that started the flow.
const CLIAuthCodeTTL = 60 * time.Second

const cliAuthCodeColumns = `id, code_hash, challenge, user_id, org_id,
	device_name, expires_at, consumed_at, created_at`

// GenerateCLIAuthCode returns a fresh one-time code (32 bytes of entropy,
// hex) for the loopback redirect. Only its SHA-256 is persisted.
func GenerateCLIAuthCode() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate cli auth code: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// HashCLIAuthCode is the deterministic lookup hash for one-time codes. Plain
// SHA-256 hex (no prefix): the codes are single-purpose and never co-mingle
// with the "sha256:"-prefixed token hashes.
func HashCLIAuthCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// CLIAuthCodeStore persists the one-time codes bridging the OAuth callback
// to the CLI loopback exchange. The rows must survive across server
// replicas (rolling deploys), which is why this is a table and not memory.
type CLIAuthCodeStore struct {
	db DBTX
}

func NewCLIAuthCodeStore(db DBTX) *CLIAuthCodeStore {
	return &CLIAuthCodeStore{db: db}
}

// Create inserts a handshake row and opportunistically garbage-collects
// long-expired ones. The GC rides on insert because login traffic is the
// only thing that creates garbage here — no background job needed.
//
// lint:allow-no-orgid reason="60-second pre-auth handshake row; user/org are payload columns resolved by the callback, not a tenancy scope"
func (s *CLIAuthCodeStore) Create(ctx context.Context, code *models.CLIAuthCode) error {
	// Best-effort GC; an error here must not block a login.
	_, _ = s.db.Exec(ctx, `DELETE FROM cli_auth_codes WHERE expires_at < now() - interval '1 hour'`)

	query := fmt.Sprintf(`INSERT INTO cli_auth_codes (
		code_hash, challenge, user_id, org_id, device_name, expires_at
	) VALUES (
		@code_hash, @challenge, @user_id, @org_id, @device_name, @expires_at
	) RETURNING %s`, cliAuthCodeColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"code_hash":   code.CodeHash,
		"challenge":   code.Challenge,
		"user_id":     code.UserID,
		"org_id":      code.OrgID,
		"device_name": code.DeviceName,
		"expires_at":  code.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("create cli auth code: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CLIAuthCode])
	if err != nil {
		return fmt.Errorf("scan cli auth code: %w", err)
	}
	*code = row
	return nil
}

// Consume atomically marks an unconsumed, unexpired code as used and returns
// it. pgx.ErrNoRows means the code is unknown, already consumed, or expired —
// the caller cannot distinguish, by design (a single 410 covers all three).
//
// lint:allow-no-orgid reason="single-use redemption by opaque code hash during the pre-auth exchange"
func (s *CLIAuthCodeStore) Consume(ctx context.Context, codeHash string) (models.CLIAuthCode, error) {
	query := fmt.Sprintf(`UPDATE cli_auth_codes SET consumed_at = now()
		WHERE code_hash = @code_hash AND consumed_at IS NULL AND expires_at > now()
		RETURNING %s`, cliAuthCodeColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"code_hash": codeHash})
	if err != nil {
		return models.CLIAuthCode{}, fmt.Errorf("consume cli auth code: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CLIAuthCode])
}
