package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PreviewTokenClaims scopes internal preview control-plane calls.
type PreviewTokenClaims struct {
	OrgID        uuid.UUID  `json:"org_id"`
	TargetNodeID string     `json:"target_node_id"`
	RuntimeID    *uuid.UUID `json:"runtime_id,omitempty"`
	RuntimeEpoch int        `json:"runtime_epoch,omitempty"`
	SessionID    *uuid.UUID `json:"session_id,omitempty"`
	PreviewID    *uuid.UUID `json:"preview_id,omitempty"`
	Action       string     `json:"action"`
	ExpiresAt    time.Time  `json:"exp"`
}

// GeneratePreviewToken creates a short-lived HMAC-signed token for internal
// preview RPC between app nodes and workers.
func GeneratePreviewToken(secret string, claims PreviewTokenClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write(payload); err != nil {
		return "", fmt.Errorf("compute HMAC: %w", err)
	}
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ValidatePreviewToken verifies and decodes an internal preview token.
func ValidatePreviewToken(secret, token string) (*PreviewTokenClaims, error) {
	dotIdx := strings.LastIndexByte(token, '.')
	if dotIdx < 0 {
		return nil, fmt.Errorf("invalid token format")
	}
	payloadB64 := token[:dotIdx]
	sigB64 := token[dotIdx+1:]

	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write(payload); err != nil {
		return nil, fmt.Errorf("compute HMAC: %w", err)
	}
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, fmt.Errorf("invalid signature")
	}

	var claims PreviewTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	if time.Now().After(claims.ExpiresAt) {
		return nil, fmt.Errorf("token expired")
	}
	return &claims, nil
}
