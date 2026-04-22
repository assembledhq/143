package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

const (
	githubPRResumeCookiePrefix  = "github_pr_resume_"
	prAuthResumeTokenTTL        = 10 * time.Minute
	prAuthResumeTokenSkewWindow = 30 * time.Second
)

type prAuthorMode string

const (
	prAuthorModeAuto prAuthorMode = "auto"
	prAuthorModeUser prAuthorMode = "user"
	prAuthorModeApp  prAuthorMode = "app"
)

type prAuthResumeClaims struct {
	SessionID  uuid.UUID `json:"session_id"`
	UserID     uuid.UUID `json:"user_id"`
	OrgID      uuid.UUID `json:"org_id"`
	Draft      *bool     `json:"draft,omitempty"`
	AuthorMode string    `json:"author_mode"`
	ExpiresAt  int64     `json:"exp"`
}

func parsePRAuthorMode(raw string) (prAuthorMode, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", string(prAuthorModeAuto):
		return prAuthorModeAuto, nil
	case string(prAuthorModeUser):
		return prAuthorModeUser, nil
	case string(prAuthorModeApp):
		return prAuthorModeApp, nil
	default:
		return "", fmt.Errorf("invalid author mode %q", raw)
	}
}

func shouldPromptForPRAuth(mode prAuthorMode, policy models.PRAuthorship) bool {
	if mode == prAuthorModeApp || policy == models.PRAuthorshipAppOnly {
		return false
	}
	if mode == prAuthorModeUser {
		return true
	}
	return policy == models.PRAuthorshipUserPreferred || policy == models.PRAuthorshipUserRequired
}

func signPRAuthResumeToken(key []byte, claims prAuthResumeClaims) (string, error) {
	if len(key) == 0 {
		return "", errors.New("missing signing key")
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func parsePRAuthResumeToken(key []byte, token string, now time.Time) (prAuthResumeClaims, error) {
	var claims prAuthResumeClaims
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return claims, errors.New("invalid token format")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(parts[0]))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expectedSig), []byte(parts[1])) {
		return claims, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, fmt.Errorf("decode payload: %w", err)
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, fmt.Errorf("decode claims: %w", err)
	}
	if claims.ExpiresAt == 0 {
		return claims, errors.New("token missing expiry")
	}
	if now.After(time.Unix(claims.ExpiresAt, 0).Add(prAuthResumeTokenSkewWindow)) {
		return claims, errors.New("token expired")
	}
	return claims, nil
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(r),
	})
}

func isSecureRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.EqualFold(r.URL.Scheme, "https") || r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
