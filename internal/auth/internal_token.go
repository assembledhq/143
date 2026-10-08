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

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// InternalTokenClaims are the claims embedded in an internal API token.
type InternalTokenClaims struct {
	AutomationRunID        *uuid.UUID `json:"automation_run_id,omitempty"`
	AutomationJobID        *uuid.UUID `json:"automation_job_id,omitempty"`
	AutomationAttemptToken *uuid.UUID `json:"automation_attempt_token,omitempty"`
	OrgID                  uuid.UUID  `json:"org_id"`
	RepoID                 uuid.UUID  `json:"repo_id"`
	SessionID              *uuid.UUID `json:"session_id,omitempty"`
	ThreadID               *uuid.UUID `json:"thread_id,omitempty"`
	AllowedToolScopes      []string   `json:"allowed_tool_scopes,omitempty"`
	SessionOrigin          string     `json:"session_origin,omitempty"`
	EvalBootstrapRunID     *uuid.UUID `json:"eval_bootstrap_run_id,omitempty"`
	ExpiresAt              time.Time  `json:"exp"`
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
	return GenerateSessionThreadToken(secret, orgID, repoID, sessionID, nil, ttl)
}

// GenerateSessionThreadToken creates a short-lived HMAC token scoped to an
// org, repo, session, and optionally the source thread currently running.
func GenerateSessionThreadToken(secret string, orgID uuid.UUID, repoID uuid.UUID, sessionID uuid.UUID, threadID *uuid.UUID, ttl time.Duration) (string, error) {
	return GenerateSessionThreadTokenWithClaims(secret, orgID, repoID, sessionID, threadID, nil, "", nil, ttl)
}

// GenerateSessionThreadTokenWithClaims creates a session/thread token with
// optional tool-scope claims used by sandbox-only internal APIs.
func GenerateSessionThreadTokenWithClaims(secret string, orgID uuid.UUID, repoID uuid.UUID, sessionID uuid.UUID, threadID *uuid.UUID, allowedToolScopes []string, sessionOrigin string, evalBootstrapRunID *uuid.UUID, ttl time.Duration) (string, error) {
	claims := InternalTokenClaims{
		OrgID:              orgID,
		RepoID:             repoID,
		SessionID:          &sessionID,
		ThreadID:           threadID,
		AllowedToolScopes:  allowedToolScopes,
		SessionOrigin:      sessionOrigin,
		EvalBootstrapRunID: evalBootstrapRunID,
		ExpiresAt:          time.Now().Add(ttl),
	}
	return signInternalToken(secret, claims)
}

// GenerateAutomationActionToken binds a write tool to one executing automation attempt.
func GenerateAutomationActionToken(secret string, actor models.AutomationActionActor, scopes []string, origin string, ttl time.Duration) (string, error) {
	if actor.OrgID == uuid.Nil || actor.RepositoryID == uuid.Nil || actor.RunID == uuid.Nil || actor.AttemptToken == uuid.Nil || actor.JobID == uuid.Nil || actor.SessionID == uuid.Nil || actor.ThreadID == uuid.Nil {
		return "", fmt.Errorf("automation action token requires a run, attempt, session, and thread")
	}
	return signInternalToken(secret, InternalTokenClaims{OrgID: actor.OrgID, RepoID: actor.RepositoryID, SessionID: &actor.SessionID, ThreadID: &actor.ThreadID, AllowedToolScopes: scopes, SessionOrigin: origin, AutomationRunID: &actor.RunID, AutomationJobID: &actor.JobID, AutomationAttemptToken: &actor.AttemptToken, ExpiresAt: time.Now().Add(ttl)})
}

func signInternalToken(secret string, claims InternalTokenClaims) (string, error) {
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
