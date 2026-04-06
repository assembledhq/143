package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

const (
	githubPRConnectStateCookie = "github_pr_connect_state"
	githubPRConnectScope       = "repo read:user user:email"
)

// githubStatusCredentialStore is the interface for checking GitHub credential status.
type githubStatusCredentialStore interface {
	GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	Upsert(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig, isTeamDefault bool) error
	Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error
}

// githubStatusOrgReader reads org settings to determine PR authorship mode.
type githubStatusOrgReader interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

// GitHubStatusHandler serves the user's GitHub connection status for PR creation.
type GitHubStatusHandler struct {
	credentials    githubStatusCredentialStore
	orgs           githubStatusOrgReader
	githubClientID string
	githubSecret   string
	baseURL        string // e.g. "https://app.143.dev"
	frontendURL    string // e.g. "https://app.143.dev"
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

	// Check if the user has a GitHub OAuth credential.
	cred, err := h.credentials.GetForUser(r.Context(), orgID, user.ID, models.ProviderGitHubOAuth)
	if err == nil && cred != nil {
		cfg, ok := cred.Config.(models.GitHubOAuthConfig)
		if ok && cfg.AccessToken != "" {
			resp.Connected = true
			resp.HasRepoScope = hasRepoScope(cfg.Scope)
			if user.GitHubLogin != nil {
				resp.GitHubLogin = *user.GitHubLogin
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// StartConnect initiates a GitHub OAuth flow with repo scope for PR authorship.
func (h *GitHubStatusHandler) StartConnect(w http.ResponseWriter, r *http.Request) {
	if h.githubClientID == "" || h.githubSecret == "" {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_OAUTH_NOT_CONFIGURED", "github oauth is not configured")
		return
	}

	state, err := setOAuthState(w, githubPRConnectStateCookie)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state", err)
		return
	}

	params := url.Values{
		"client_id":    {h.githubClientID},
		"redirect_uri": {h.baseURL + "/api/v1/users/me/github/callback"},
		"scope":        {githubPRConnectScope},
		"state":        {state},
	}

	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+params.Encode(), http.StatusTemporaryRedirect)
}

// HandleConnectCallback handles the GitHub OAuth callback for PR authorship.
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

	// Exchange code for token using the shared helper.
	tokenResp, err := exchangeGitHubOAuthCode(h.githubClientID, h.githubSecret, code)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code", err)
		return
	}

	// Store user credential with repo scope.
	cfg := models.GitHubOAuthConfig{
		AccessToken: tokenResp.AccessToken,
		TokenType:   tokenResp.TokenType,
		Scope:       tokenResp.Scope,
	}
	if err := h.credentials.Upsert(r.Context(), user.ID, orgID, cfg, false); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store credential", err)
		return
	}

	http.Redirect(w, r, h.frontendURL+"/settings?github_pr=connected", http.StatusTemporaryRedirect)
}

// hasRepoScope returns true if the comma/space-separated scope string includes "repo".
func hasRepoScope(scope string) bool {
	for _, s := range strings.FieldsFunc(scope, func(r rune) bool { return r == ',' || r == ' ' }) {
		if s == "repo" {
			return true
		}
	}
	return false
}

// Disconnect removes the user's stored GitHub OAuth credential.
func (h *GitHubStatusHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())

	if err := h.credentials.Disable(r.Context(), orgID, user.ID, models.ProviderGitHubOAuth); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DISCONNECT_FAILED", "failed to disconnect GitHub", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
}
