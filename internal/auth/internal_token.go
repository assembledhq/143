// Package auth provides internal authentication utilities for service-to-service
// communication, such as sandbox-to-server API calls.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// InternalTokenClaims are the claims embedded in an internal API token.
type InternalTokenClaims struct {
	OrgID     uuid.UUID `json:"org_id"`
	ExpiresAt time.Time `json:"exp"`
}

// GenerateInternalToken creates a short-lived HMAC-signed token scoped to an org.
func GenerateInternalToken(secret string, orgID uuid.UUID, ttl time.Duration) (string, error) {
	claims := InternalTokenClaims{
		OrgID:     orgID,
		ExpiresAt: time.Now().Add(ttl),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	sig := mac.Sum(nil)

	// Token format: base64(payload).base64(signature)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
	return token, nil
}

// ValidateInternalToken verifies and decodes an internal API token.
func ValidateInternalToken(secret, token string) (*InternalTokenClaims, error) {
	// Split into payload and signature.
	dotIdx := -1
	for i := len(token) - 1; i >= 0; i-- {
		if token[i] == '.' {
			dotIdx = i
			break
		}
	}
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

	// Verify HMAC.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedSig := mac.Sum(nil)
	if !hmac.Equal(sig, expectedSig) {
		return nil, fmt.Errorf("invalid signature")
	}

	var claims InternalTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	if time.Now().After(claims.ExpiresAt) {
		return nil, fmt.Errorf("token expired")
	}

	return &claims, nil
}
