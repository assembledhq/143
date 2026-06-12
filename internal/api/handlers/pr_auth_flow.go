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

	"github.com/assembledhq/143/internal/api/middleware"
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

// prAuthAction identifies which endpoint signed a resume token. Encoded into
// the signed claims and surfaced back to the frontend as a URL param after
// the OAuth round-trip so the post-auth replay is deterministic — without it
// the frontend would have to guess between "create PR" and "push changes"
// based on current PR state, which can change during the handshake (tab/race).
type prAuthAction string

const (
	prAuthActionCreatePR     prAuthAction = "create_pr"
	prAuthActionCreateBranch prAuthAction = "create_branch"
	prAuthActionPushChanges  prAuthAction = "push_changes"
)

type prAuthResumeClaims struct {
	SessionID      uuid.UUID `json:"session_id"`
	UserID         uuid.UUID `json:"user_id"`
	OrgID          uuid.UUID `json:"org_id"`
	Draft          *bool     `json:"draft,omitempty"`
	MergeWhenReady bool      `json:"merge_when_ready,omitempty"`
	AuthorMode     string    `json:"author_mode"`
	// Action is the originating endpoint ("create_pr" or "push_changes").
	// Empty for tokens signed before this field was added; readers should
	// treat empty as "create_pr" for backward compatibility.
	Action    string `json:"action,omitempty"`
	ExpiresAt int64  `json:"exp"`
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

// prAuthInterceptParams bundles every input requirePRAuthOrIntercept needs.
// SessionID/OrgID/Session/OrgSettings are session context; AuthorMode and
// ResumeToken come from the request body; Action/ActionDescription/
// ResumeExpiredMessage/Draft customize the user-facing copy and resume-claim
// payload. Bundled into one struct because the helper otherwise grew a
// nine-parameter signature that's hard to read at the call sites.
//
//   - Action identifies the calling endpoint and is signed into the resume
//     claims so the OAuth callback can dispatch the correct replay action
//     regardless of any state change during the handshake.
//   - ActionDescription is interpolated into the "Authorize GitHub to
//     <action> as you." message.
//   - ResumeExpiredMessage is shown when the resume token decoded but
//     expired (e.g. user took 10+ minutes to complete OAuth).
//   - Draft is preserved in the resume claims so the original Draft choice
//     survives the GitHub round-trip — leave nil for endpoints that don't
//     expose a draft toggle.
//   - MergeWhenReady is preserved for the combined Create PR + auto-merge
//     publish action so the OAuth round-trip does not downgrade it to a plain
//     Create PR.
type prAuthInterceptParams struct {
	SessionID            uuid.UUID
	OrgID                uuid.UUID
	Session              *models.Session
	OrgSettings          models.OrgSettings
	AuthorMode           prAuthorMode
	ResumeToken          string
	Action               prAuthAction
	ActionDescription    string
	ResumeExpiredMessage string
	Draft                *bool
	MergeWhenReady       bool
}

// requirePRAuthOrIntercept centralizes the GitHub user-auth interception that
// CreatePR and PushChangesToPR share. Returns true if the caller should
// proceed (auth not required, or the user is already authed); returns false if
// a response was already written (auth required → 409 GITHUB_PR_AUTHORSHIP_REQUIRED,
// resume token expired → 409 PR_RESUME_EXPIRED, app-user-auth not configured →
// 503, or an internal error). Single-source-of-truth for the auth flow so a
// fix to the credential check or token-signing path lands in both endpoints.
func (h *SessionHandler) requirePRAuthOrIntercept(w http.ResponseWriter, r *http.Request, p prAuthInterceptParams) bool {
	user := middleware.UserFromContext(r.Context())
	if !shouldPromptForPRAuth(p.AuthorMode, p.OrgSettings.PRAuthorship) ||
		p.Session.TriggeredByUserID == nil ||
		user == nil ||
		user.ID != *p.Session.TriggeredByUserID {
		return true
	}

	if p.ResumeToken != "" {
		if len(h.prAuthSigningKey) == 0 {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "PR auth flow is not configured")
			return false
		}
		claims, tokenErr := parsePRAuthResumeToken(h.prAuthSigningKey, p.ResumeToken, time.Now())
		if tokenErr != nil || claims.SessionID != p.SessionID || claims.UserID != user.ID || claims.OrgID != p.OrgID {
			writeJSON(w, http.StatusConflict, models.ErrorResponse{
				Error: models.ErrorDetail{
					Code:    "PR_RESUME_EXPIRED",
					Message: p.ResumeExpiredMessage,
				},
			})
			return false
		}
	}

	hasCredential := false
	authUnavailable := h.prAuthChecker == nil
	if h.prAuthChecker != nil {
		var checkErr error
		hasCredential, checkErr = h.prAuthChecker.HasValidCredential(r.Context(), p.OrgID, user.ID)
		if checkErr != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to verify GitHub PR authorization", checkErr)
			return false
		}
	}
	if authUnavailable && !hasCredential {
		if p.OrgSettings.PRAuthorship == models.PRAuthorshipUserRequired || p.AuthorMode == prAuthorModeUser {
			writeError(w, r, http.StatusServiceUnavailable, "GITHUB_APP_USER_AUTH_NOT_CONFIGURED", "github app user auth is not configured")
			return false
		}
		// App-token fallback is allowed; let the caller proceed.
		return true
	}
	if hasCredential {
		return true
	}

	if len(h.prAuthSigningKey) == 0 {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "PR auth flow is not configured")
		return false
	}
	signedToken, signErr := signPRAuthResumeToken(h.prAuthSigningKey, prAuthResumeClaims{
		SessionID:      p.SessionID,
		UserID:         user.ID,
		OrgID:          p.OrgID,
		Draft:          p.Draft,
		MergeWhenReady: p.MergeWhenReady,
		AuthorMode:     string(prAuthorModeUser),
		Action:         string(p.Action),
		ExpiresAt:      time.Now().Add(prAuthResumeTokenTTL).Unix(),
	})
	if signErr != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to prepare GitHub PR authorization", signErr)
		return false
	}
	writeJSON(w, http.StatusConflict, models.ErrorResponse{
		Error: models.ErrorDetail{
			Code:    "GITHUB_PR_AUTHORSHIP_REQUIRED",
			Message: fmt.Sprintf("Authorize GitHub to %s as you.", p.ActionDescription),
			Details: map[string]any{
				"session_id":            p.SessionID.String(),
				"connect_url":           "/api/v1/users/me/github/connect?flow=pr_authorship",
				"resume_token":          signedToken,
				"merge_when_ready":      p.MergeWhenReady,
				"can_fallback_to_app":   p.OrgSettings.PRAuthorship != models.PRAuthorshipUserRequired,
				"suggested_author_mode": string(prAuthorModeUser),
			},
		},
	})
	return false
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
