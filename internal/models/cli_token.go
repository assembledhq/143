package models

import (
	"time"

	"github.com/google/uuid"
)

// UserCLIToken is a per-user, per-device CLI credential minted by the
// browser login flow (`143-tools login`). It acts *as the user* — org
// resolution flows through memberships exactly like a session — which is
// why it is a separate table from api_tokens (those belong to org API
// clients with explicit scopes).
//
// TokenHash is the deterministic "sha256:"+hex of the raw "143u_..." token
// and doubles as the lookup key; the raw token is returned exactly once at
// mint time and never stored. Expiry is sliding: authenticated use pushes
// ExpiresAt back out (see middleware), so only genuinely idle devices age
// out. Revocation, not expiry, is the operational control.
type UserCLIToken struct {
	ID          uuid.UUID  `db:"id" json:"id"`
	UserID      uuid.UUID  `db:"user_id" json:"user_id"`
	TokenHash   string     `db:"token_hash" json:"-"`
	TokenPrefix string     `db:"token_prefix" json:"token_prefix"`
	DeviceName  string     `db:"device_name" json:"device_name"`
	LastOrgID   *uuid.UUID `db:"last_org_id" json:"last_org_id,omitempty"`
	ExpiresAt   time.Time  `db:"expires_at" json:"expires_at"`
	LastUsedAt  *time.Time `db:"last_used_at" json:"last_used_at,omitempty"`
	LastUsedIP  *string    `db:"last_used_ip" json:"last_used_ip,omitempty"`
	RevokedAt   *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
}

// CLIAuthCode is the one-time handshake row bridging the browser OAuth
// callback to the CLI's loopback listener. The browser only ever sees the
// raw code (never a credential); the CLI exchanges it — bound to its PKCE
// style verifier via Challenge = SHA-256(verifier) — for a UserCLIToken.
// Rows are single-use and expire 60 seconds after mint.
//
// OrgID is nullable: it is resolved like sessions (last_org_id → oldest
// membership), and a zero-membership user can still complete login.
type CLIAuthCode struct {
	ID         uuid.UUID  `db:"id" json:"id"`
	CodeHash   string     `db:"code_hash" json:"-"`
	Challenge  string     `db:"challenge" json:"-"`
	UserID     uuid.UUID  `db:"user_id" json:"user_id"`
	OrgID      *uuid.UUID `db:"org_id" json:"org_id,omitempty"`
	DeviceName string     `db:"device_name" json:"device_name"`
	ExpiresAt  time.Time  `db:"expires_at" json:"expires_at"`
	ConsumedAt *time.Time `db:"consumed_at" json:"consumed_at,omitempty"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
}

// OrgJoinToken is a multi-use, revocable join link: it grants exactly one
// right — "a GitHub-authenticated person may become a member of this org at
// Role" — and never API access. Every person who redeems one still ends up
// with their own user row and their own CLI token, preserving per-user
// audit and surgical offboarding.
type OrgJoinToken struct {
	ID              uuid.UUID  `db:"id" json:"id"`
	OrgID           uuid.UUID  `db:"org_id" json:"org_id"`
	TokenHash       string     `db:"token_hash" json:"-"`
	TokenPrefix     string     `db:"token_prefix" json:"token_prefix"`
	Role            Role       `db:"role" json:"role"`
	Name            string     `db:"name" json:"name"`
	CreatedByUserID uuid.UUID  `db:"created_by_user_id" json:"created_by_user_id"`
	MaxUses         *int       `db:"max_uses" json:"max_uses,omitempty"`
	UseCount        int        `db:"use_count" json:"use_count"`
	ExpiresAt       *time.Time `db:"expires_at" json:"expires_at,omitempty"`
	RevokedAt       *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
	RevokedByUserID *uuid.UUID `db:"revoked_by_user_id" json:"revoked_by_user_id,omitempty"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
}

// JoinTokenStatus summarizes a join token's lifecycle for list UIs.
type JoinTokenStatus string

const (
	JoinTokenStatusActive    JoinTokenStatus = "active"
	JoinTokenStatusRevoked   JoinTokenStatus = "revoked"
	JoinTokenStatusExpired   JoinTokenStatus = "expired"
	JoinTokenStatusExhausted JoinTokenStatus = "exhausted"
)

// Status derives the display status from the token's lifecycle columns.
// Precedence: revoked > expired > exhausted > active — a revoked token stays
// "revoked" even after its expiry passes, since revocation was the operator
// action that ended it.
func (t OrgJoinToken) Status(now time.Time) JoinTokenStatus {
	switch {
	case t.RevokedAt != nil:
		return JoinTokenStatusRevoked
	case t.ExpiresAt != nil && now.After(*t.ExpiresAt):
		return JoinTokenStatusExpired
	case t.MaxUses != nil && t.UseCount >= *t.MaxUses:
		return JoinTokenStatusExhausted
	default:
		return JoinTokenStatusActive
	}
}
