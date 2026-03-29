package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

// githubStatusCredentialStore is the interface for checking GitHub credential status.
type githubStatusCredentialStore interface {
	GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error
}

// githubStatusOrgReader reads org settings to determine PR authorship mode.
type githubStatusOrgReader interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

// GitHubStatusHandler serves the user's GitHub connection status for PR creation.
type GitHubStatusHandler struct {
	credentials githubStatusCredentialStore
	orgs        githubStatusOrgReader
}

// NewGitHubStatusHandler creates a new GitHub status handler.
func NewGitHubStatusHandler(credentials githubStatusCredentialStore, orgs githubStatusOrgReader) *GitHubStatusHandler {
	return &GitHubStatusHandler{credentials: credentials, orgs: orgs}
}

// GitHubStatusResponse is the response for GET /api/v1/users/me/github-status.
type GitHubStatusResponse struct {
	Connected        bool   `json:"connected"`
	HasRepoScope     bool   `json:"has_repo_scope"`
	GitHubLogin      string `json:"github_login,omitempty"`
	PRAuthorshipMode string `json:"pr_authorship_mode"`
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
