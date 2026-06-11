package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

const (
	githubPRConnectStateCookie = "github_pr_connect_state"
)

// githubStatusCredentialStore is the interface for checking GitHub credential status.
type githubStatusCredentialStore interface {
	GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	Upsert(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig) error
	Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error
}

// githubStatusOrgReader reads org settings to determine PR authorship mode.
type githubStatusOrgReader interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

type githubAppUserAuth interface {
	HasValidCredential(ctx context.Context, orgID, userID uuid.UUID) (bool, error)
	ExchangeCode(ctx context.Context, code string) (*models.GitHubAppUserConfig, error)
}

// GitHubStatusHandler serves the user's GitHub connection status for PR creation.
type GitHubStatusHandler struct {
	credentials    githubStatusCredentialStore
	orgs           githubStatusOrgReader
	githubClientID string
	githubSecret   string
	baseURL        string // e.g. "https://app.143.dev"
	frontendURL    string // e.g. "https://app.143.dev"
	signingKey     []byte
	appUserAuth    githubAppUserAuth
}

// NewGitHubStatusHandler creates a new GitHub status handler.
func NewGitHubStatusHandler(
	credentials githubStatusCredentialStore,
	orgs githubStatusOrgReader,
	githubClientID, githubSecret, baseURL, frontendURL string,
) *GitHubStatusHandler {
	return &GitHubStatusHandler{
		credentials:    credentials,
		orgs:           orgs,
		githubClientID: githubClientID,
		githubSecret:   githubSecret,
		baseURL:        baseURL,
		frontendURL:    frontendURL,
	}
}

// SetPRAuthFlow wires the signing key used for signed PR resume tokens.
func (h *GitHubStatusHandler) SetPRAuthFlow(signingKey string) {
	h.signingKey = []byte(signingKey)
}

// SetAppUserAuth wires the refresh-aware GitHub App user auth service used by
// the on-demand Create PR flow.
func (h *GitHubStatusHandler) SetAppUserAuth(auth githubAppUserAuth) {
	h.appUserAuth = auth
}

// GitHubStatusResponse is the response for GET /api/v1/users/me/github-status.
type GitHubStatusResponse struct {
	Connected        bool   `json:"connected"`
	HasRepoScope     bool   `json:"has_repo_scope"`
	GitHubLogin      string `json:"github_login,omitempty"`
	PRAuthorshipMode string `json:"pr_authorship_mode"`
	PRDraftDefault   bool   `json:"pr_draft_default"`
}

// GetStatus returns the user's GitHub connection status and the org's PR authorship mode.
func (h *GitHubStatusHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())

	resp := GitHubStatusResponse{
		PRAuthorshipMode: string(models.PRAuthorshipUserPreferred),
	}

	// Check org settings for PR authorship mode.
	org, err := h.orgs.GetByID(r.Context(), orgID)
	if err == nil {
		settings, parseErr := models.ParseOrgSettings(org.Settings)
		if parseErr == nil {
			resp.PRAuthorshipMode = string(settings.PRAuthorship)
			resp.PRDraftDefault = settings.PRDraftDefault
		}
	}

	if h.appUserAuth != nil {
		ok, authErr := h.appUserAuth.HasValidCredential(r.Context(), orgID, user.ID)
		if authErr != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to determine GitHub PR auth status", authErr)
			return
		}
		if ok {
			resp.Connected = true
			resp.HasRepoScope = true
			if user.GitHubLogin != nil {
				resp.GitHubLogin = *user.GitHubLogin
			}
		}
	} else {
		// Fallback for tests/wiring without the refresh-aware auth service.
		cred, err := h.credentials.GetForUser(r.Context(), orgID, user.ID, models.ProviderGitHubAppUser)
		if err == nil && cred != nil {
			cfg, ok := cred.Config.(models.GitHubAppUserConfig)
			if ok && cfg.AccessToken != "" && !cfg.IsExpired() {
				resp.Connected = true
				resp.HasRepoScope = true
				if user.GitHubLogin != nil {
					resp.GitHubLogin = *user.GitHubLogin
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// StartConnect initiates a GitHub App user authorization flow for PR authorship.
func (h *GitHubStatusHandler) StartConnect(w http.ResponseWriter, r *http.Request) {
	if h.githubClientID == "" || h.githubSecret == "" {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_APP_USER_AUTH_NOT_CONFIGURED", "github app user auth is not configured")
		return
	}
	var resumeCookieName string
	if resumeToken := r.URL.Query().Get("resume_token"); resumeToken != "" {
		user := middleware.UserFromContext(r.Context())
		orgID := middleware.OrgIDFromContext(r.Context())
		if user == nil {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
			return
		}
		if len(h.signingKey) == 0 {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "PR auth flow is not configured")
			return
		}
		claims, err := parsePRAuthResumeToken(h.signingKey, resumeToken, time.Now())
		if err != nil || claims.UserID != user.ID || claims.OrgID != orgID {
			writeJSON(w, http.StatusConflict, models.ErrorResponse{
				Error: models.ErrorDetail{
					Code:    "PR_RESUME_EXPIRED",
					Message: "GitHub authorization completed, but the PR resume request expired. Please click Create PR again.",
				},
			})
			return
		}
	}

	state, err := setOAuthState(w, githubPRConnectStateCookie)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state", err)
		return
	}

	if resumeToken := r.URL.Query().Get("resume_token"); resumeToken != "" {
		resumeCookieName = githubPRResumeCookiePrefix + state
		http.SetCookie(w, &http.Cookie{
			Name:     resumeCookieName,
			Value:    resumeToken,
			Path:     "/",
			MaxAge:   int(prAuthResumeTokenTTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   isSecureRequest(r),
		})
	}

	params := url.Values{
		"client_id":    {h.githubClientID},
		"redirect_uri": {h.baseURL + "/api/v1/users/me/github/callback"},
		"state":        {state},
	}

	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+params.Encode(), http.StatusTemporaryRedirect)
}

// HandleConnectCallback handles the GitHub App user auth callback for PR authorship.
func (h *GitHubStatusHandler) HandleConnectCallback(w http.ResponseWriter, r *http.Request) {
	code, ok := validateOAuthCallback(w, r, githubPRConnectStateCookie)
	if !ok {
		return
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())

	if h.appUserAuth == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_APP_USER_AUTH_NOT_CONFIGURED", "github app user auth is not configured")
		return
	}

	cfg, err := h.appUserAuth.ExchangeCode(r.Context(), code)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code", err)
		return
	}

	if err := h.credentials.Upsert(r.Context(), user.ID, orgID, cfg); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store credential", err)
		return
	}

	redirectURL := h.frontendURL + "/settings?github_pr=connected"
	resumeCookieName := githubPRResumeCookiePrefix + r.URL.Query().Get("state")
	if resumeCookie, err := r.Cookie(resumeCookieName); err == nil && resumeCookie.Value != "" && len(h.signingKey) > 0 {
		if claims, tokenErr := parsePRAuthResumeToken(h.signingKey, resumeCookie.Value, time.Now()); tokenErr == nil {
			// Forward the originating action ("create_pr" or "push_changes")
			// the signed claim recorded so the frontend can dispatch the
			// correct mutation deterministically. Older tokens without an
			// action just omit the param; the frontend falls back to a
			// state-based heuristic in that case.
			redirectURL = fmt.Sprintf("%s/sessions/%s?github_pr=connected&resume_pr=%s", h.frontendURL, claims.SessionID, url.QueryEscape(resumeCookie.Value))
			if claims.Action != "" {
				redirectURL += "&resume_action=" + url.QueryEscape(claims.Action)
			}
		}
	}
	clearCookie(w, r, resumeCookieName)
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

// Disconnect removes the user's stored GitHub App user credential.
func (h *GitHubStatusHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())

	if err := h.credentials.Disable(r.Context(), orgID, user.ID, models.ProviderGitHubAppUser); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DISCONNECT_FAILED", "failed to disconnect GitHub", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
}
