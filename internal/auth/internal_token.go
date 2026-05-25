// Package auth provides internal authentication utilities for service-to-service
// communication, such as sandbox-to-server API calls.
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

// InternalTokenClaims are the claims embedded in an internal API token.
type InternalTokenClaims struct {
	OrgID     uuid.UUID  `json:"org_id"`
	RepoID    uuid.UUID  `json:"repo_id"`
	SessionID *uuid.UUID `json:"session_id,omitempty"`
	ExpiresAt time.Time  `json:"exp"`
}

// GenerateInternalToken creates a short-lived HMAC-signed token scoped to an org and repo.
func GenerateInternalToken(secret string, orgID uuid.UUID, repoID uuid.UUID, ttl time.Duration) (string, error) {
	claims := InternalTokenClaims{
		OrgID:     orgID,
		RepoID:    repoID,
		ExpiresAt: time.Now().Add(ttl),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write(payload); err != nil {
		return "", fmt.Errorf("compute HMAC: %w", err)
	}
	sig := mac.Sum(nil)

	// Token format: base64(payload).base64(signature)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
	return token, nil
}

// GenerateSessionToken creates a short-lived HMAC-signed token scoped to an org, repo, and session.
// Use this instead of GenerateInternalToken when the token will be used for session-specific operations
// such as PR creation, so the handler can enforce that the caller is acting on the correct session.
func GenerateSessionToken(secret string, orgID uuid.UUID, repoID uuid.UUID, sessionID uuid.UUID, ttl time.Duration) (string, error) {
	claims := InternalTokenClaims{
		OrgID:     orgID,
		RepoID:    repoID,
		SessionID: &sessionID,
		ExpiresAt: time.Now().Add(ttl),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write(payload); err != nil {
		return "", fmt.Errorf("compute HMAC: %w", err)
	}
	sig := mac.Sum(nil)

	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
	return token, nil
}

// ValidateInternalToken verifies and decodes an internal API token.
func ValidateInternalToken(secret, token string) (*InternalTokenClaims, error) {
	// Split into payload and signature.
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

	// Verify HMAC.
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write(payload); err != nil {
		return nil, fmt.Errorf("compute HMAC: %w", err)
	}
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
