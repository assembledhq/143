package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	// Linear OAuth endpoints
	linearAuthorizeURL = "https://linear.app/oauth/authorize"
	linearTokenURL     = "https://api.linear.app/oauth/token" // #nosec G101 -- OAuth endpoint URL, not credentials
	linearGraphQLURL   = "https://api.linear.app/graphql"

	// Sentry OAuth endpoints
	sentryAuthorizeURL = "https://sentry.io/oauth/authorize/"
	sentryTokenURL     = "https://sentry.io/oauth/token/" // #nosec G101 -- OAuth endpoint URL, not credentials
	sentryAPIURL       = "https://sentry.io/api/0"

	// GitHub OAuth endpoints (for integration, not user auth)
	githubAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubTokenURL     = "https://github.com/login/oauth/access_token" // #nosec G101 -- OAuth endpoint URL, not credentials
	githubAPIURL       = "https://api.github.com"

	// Slack OAuth endpoints
	slackAuthorizeURL = "https://slack.com/oauth/v2/authorize"
	slackTokenURL     = "https://slack.com/api/oauth.v2.access" // #nosec G101 -- OAuth endpoint URL, not credentials
	slackAPIURL       = "https://slack.com/api"

	// OAuth state cookie names — each flow gets its own cookie so concurrent
	// flows cannot collide. Integration cookies use the _integration_ infix to
	// distinguish them from the user-auth cookies in auth.go.
	githubOAuthStateCookie              = "github_oauth_state"
	googleOAuthStateCookie              = "google_oauth_state"
	linearIntegrationOAuthStateCookie   = "linear_integration_oauth_state"
	sentryIntegrationOAuthStateCookie   = "sentry_integration_oauth_state"
	githubIntegrationOAuthStateCookie   = "github_integration_oauth_state"
	slackIntegrationOAuthStateCookie    = "slack_integration_oauth_state"
)

type integrationCredentialStore interface {
	Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error
}

// githubAppService provides GitHub App installation tokens for fetching repos.
type githubAppService interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
}

// --- Linear types ---

type linearTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

type linearOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type linearViewer struct {
	Organization linearOrganization `json:"organization"`
}

type linearViewerData struct {
	Viewer linearViewer `json:"viewer"`
}

type linearViewerResponse struct {
	Data linearViewerData `json:"data"`
}

// --- Slack types ---

type slackTokenResponse struct {
	AccessToken string          `json:"access_token"`
	TokenType   string          `json:"token_type"`
	Scope       string          `json:"scope"`
	Team        slackTeamInfo   `json:"team"`
}

type slackTeamInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// --- Sentry types ---

type sentryTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

type sentryOrganization struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// --- GitHub types ---

type githubIntegrationTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

// IntegrationHandler manages OAuth flows for Linear, Sentry, and GitHub integrations.
type IntegrationHandler struct {
	integrationStore *db.IntegrationStore
	credentialStore  integrationCredentialStore
	baseURL          string
	frontendURL      string
	client           *http.Client

	// Linear OAuth
	linearClientID string
	linearSecret   string

	// Sentry OAuth
	sentryClientID string
	sentrySecret   string

	// GitHub integration OAuth
	githubClientID string
	githubSecret   string

	// GitHub App slug (for installation flow)
	githubAppSlug string

	// GitHub App service and repo store (for fetching repos on install)
	githubService githubAppService
	repoStore     *db.RepositoryStore

	// Slack OAuth
	slackClientID string
	slackSecret   string
}

// IntegrationOAuthConfig holds all integration OAuth credentials.
type IntegrationOAuthConfig struct {
	LinearClientID string
	LinearSecret   string
	SentryClientID string
	SentrySecret   string
	GitHubClientID string
	GitHubSecret   string
}

func NewIntegrationHandler(
	integrationStore *db.IntegrationStore,
	credentialStore integrationCredentialStore,
	linearClientID, linearSecret, baseURL, frontendURL string,
	opts ...IntegrationHandlerOption,
) *IntegrationHandler {
	h := &IntegrationHandler{
		integrationStore: integrationStore,
		credentialStore:  credentialStore,
		linearClientID:   linearClientID,
		linearSecret:     linearSecret,
		baseURL:          baseURL,
		frontendURL:      frontendURL,
		client:           http.DefaultClient,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// IntegrationHandlerOption configures optional IntegrationHandler fields.
type IntegrationHandlerOption func(*IntegrationHandler)

// WithSentryOAuth configures Sentry OAuth credentials.
func WithSentryOAuth(clientID, secret string) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.sentryClientID = clientID
		h.sentrySecret = secret
	}
}

// WithGitHubIntegrationOAuth configures GitHub integration OAuth credentials.
func WithGitHubIntegrationOAuth(clientID, secret string) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.githubClientID = clientID
		h.githubSecret = secret
	}
}

// WithGitHubAppSlug configures the GitHub App slug for the installation flow.
// When set, StartGitHubOAuth redirects to the App installation page instead of OAuth.
func WithGitHubAppSlug(slug string) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.githubAppSlug = slug
	}
}

// WithGitHubApp injects the GitHub App service and repo store so that
// HandleGitHubAppInstalled can fetch repos from the GitHub API.
func WithGitHubApp(svc githubAppService, repoStore *db.RepositoryStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.githubService = svc
		h.repoStore = repoStore
	}
}

// WithSlackOAuth configures Slack OAuth credentials.
func WithSlackOAuth(clientID, secret string) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.slackClientID = clientID
		h.slackSecret = secret
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// List integrations
// ──────────────────────────────────────────────────────────────────────────────

func (h *IntegrationHandler) ListIntegrations(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integrations, err := h.integrationStore.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list integrations")
		return
	}
	if integrations == nil {
		integrations = []models.Integration{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.Integration]{Data: integrations})
}

// ──────────────────────────────────────────────────────────────────────────────
// Linear OAuth
// ──────────────────────────────────────────────────────────────────────────────

func (h *IntegrationHandler) StartLinearOAuth(w http.ResponseWriter, r *http.Request) {
	if h.linearClientID == "" || h.linearSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "LINEAR_OAUTH_NOT_CONFIGURED", "linear oauth is not configured")
		return
	}

	state, err := setOAuthState(w, linearIntegrationOAuthStateCookie)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state")
		return
	}

	params := url.Values{
		"client_id":     {h.linearClientID},
		"redirect_uri":  {h.linearRedirectURL()},
		"response_type": {"code"},
		"scope":         {"read"},
		"state":         {state},
	}

	http.Redirect(w, r, linearAuthorizeURL+"?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) HandleLinearOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code, ok := validateOAuthCallback(w, r, linearIntegrationOAuthStateCookie)
	if !ok {
		return
	}

	token, err := h.exchangeLinearCode(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code")
		return
	}

	workspaceID, workspaceName, err := h.fetchLinearWorkspace(r.Context(), token.AccessToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LINEAR_API_FAILED", "failed to fetch linear workspace")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
		return
	}

	linearConfig := models.LinearConfig{
		AccessToken:   token.AccessToken,
		TokenType:     token.TokenType,
		Scope:         token.Scope,
		WorkspaceID:   workspaceID,
		WorkspaceName: workspaceName,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, linearConfig); err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store linear credential")
		return
	}

	if _, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderLinear); err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to connect linear integration")
		return
	}

	http.Redirect(w, r, h.frontendURL+"/integrations?linear=connected", http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) ConnectLinear(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderLinear)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to connect linear integration")
		return
	}
	if created {
		writeJSON(w, http.StatusCreated, models.SingleResponse[models.Integration]{Data: integration})
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Integration]{Data: integration})
}

// ──────────────────────────────────────────────────────────────────────────────
// Sentry OAuth
// ──────────────────────────────────────────────────────────────────────────────

func (h *IntegrationHandler) StartSentryOAuth(w http.ResponseWriter, r *http.Request) {
	if h.sentryClientID == "" || h.sentrySecret == "" {
		writeError(w, http.StatusServiceUnavailable, "SENTRY_OAUTH_NOT_CONFIGURED", "sentry oauth is not configured")
		return
	}

	state, err := setOAuthState(w, sentryIntegrationOAuthStateCookie)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state")
		return
	}

	params := url.Values{
		"client_id":     {h.sentryClientID},
		"redirect_uri":  {h.sentryRedirectURL()},
		"response_type": {"code"},
		"scope":         {"org:read project:read event:read"},
		"state":         {state},
	}

	http.Redirect(w, r, sentryAuthorizeURL+"?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) HandleSentryOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code, ok := validateOAuthCallback(w, r, sentryIntegrationOAuthStateCookie)
	if !ok {
		return
	}

	token, err := h.exchangeSentryCode(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code")
		return
	}

	orgSlug, orgName, err := h.fetchSentryOrganization(r.Context(), token.AccessToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SENTRY_API_FAILED", "failed to fetch sentry organization")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
		return
	}

	sentryConfig := models.SentryConfig{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		OrgSlug:      orgSlug,
		OrgName:      orgName,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, sentryConfig); err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store sentry credential")
		return
	}

	if _, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderSentry); err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_SENTRY_FAILED", "failed to connect sentry integration")
		return
	}

	http.Redirect(w, r, h.frontendURL+"/integrations?sentry=connected", http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) ConnectSentry(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderSentry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_SENTRY_FAILED", "failed to connect sentry integration")
		return
	}
	if created {
		writeJSON(w, http.StatusCreated, models.SingleResponse[models.Integration]{Data: integration})
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Integration]{Data: integration})
}

// ──────────────────────────────────────────────────────────────────────────────
// GitHub Integration OAuth
// ──────────────────────────────────────────────────────────────────────────────

func (h *IntegrationHandler) StartGitHubOAuth(w http.ResponseWriter, r *http.Request) {
	// If a GitHub App slug is configured, redirect to the App installation page
	// instead of the OAuth flow. The App installation triggers a webhook that
	// registers repos automatically. We still set the OAuth state cookie so
	// the callback can validate the redirect when GitHub returns the user with
	// a code (GitHub Apps pass the state parameter through).
	if h.githubAppSlug != "" {
		state, err := setOAuthState(w, githubIntegrationOAuthStateCookie)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state")
			return
		}

		installURL := fmt.Sprintf("https://github.com/apps/%s/installations/new?state=%s", h.githubAppSlug, url.QueryEscape(state))
		http.Redirect(w, r, installURL, http.StatusTemporaryRedirect)
		return
	}

	if h.githubClientID == "" || h.githubSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "GITHUB_OAUTH_NOT_CONFIGURED", "github integration oauth is not configured")
		return
	}

	state, err := setOAuthState(w, githubIntegrationOAuthStateCookie)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state")
		return
	}

	params := url.Values{
		"client_id":    {h.githubClientID},
		"redirect_uri": {h.githubRedirectURL()},
		"scope":        {"repo read:org"},
		"state":        {state},
	}

	http.Redirect(w, r, githubAuthorizeURL+"?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) HandleGitHubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code, ok := validateOAuthCallback(w, r, githubIntegrationOAuthStateCookie)
	if !ok {
		return
	}

	token, err := h.exchangeGitHubCode(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
		return
	}

	ghConfig := models.GitHubOAuthConfig{
		AccessToken: token.AccessToken,
		TokenType:   token.TokenType,
		Scope:       token.Scope,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, ghConfig); err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store github credential")
		return
	}

	if _, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderGitHub); err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_GITHUB_FAILED", "failed to connect github integration")
		return
	}

	http.Redirect(w, r, h.frontendURL+"/integrations?github=connected", http.StatusTemporaryRedirect)
}

// HandleGitHubAppInstalled is called after a user installs the GitHub App.
// It creates the integration record (with installation_id in config),
// fetches the repos for that installation from the GitHub API, and
// redirects to the frontend integrations page.
func (h *IntegrationHandler) HandleGitHubAppInstalled(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	ctx := r.Context()

	integration, _, err := h.ensureIntegration(ctx, orgID, models.IntegrationProviderGitHub)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_GITHUB_FAILED", "failed to connect github integration")
		return
	}

	// GitHub redirects here with ?installation_id=<id>&setup_action=install.
	// Store the installation_id in the integration config so webhooks can
	// resolve the integration later, and fetch repos via the API so the user
	// doesn't have to wait for the webhook.
	if installIDStr := r.URL.Query().Get("installation_id"); installIDStr != "" {
		installationID, parseErr := strconv.ParseInt(installIDStr, 10, 64)
		if parseErr == nil {
			configJSON, marshalErr := json.Marshal(map[string]any{
				"installation_id": installationID,
			})
			if marshalErr != nil {
				zerolog.Ctx(r.Context()).Warn().Err(marshalErr).Msg("failed to marshal installation config")
				http.Redirect(w, r, h.frontendURL+"/integrations?github=connected", http.StatusTemporaryRedirect)
				return
			}
			if err := h.integrationStore.UpdateConfig(ctx, orgID, integration.ID, configJSON); err != nil {
				zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to update integration config")
			}

			// Fetch and upsert repos from the GitHub API.
			if h.githubService != nil && h.repoStore != nil {
				h.syncInstallationRepos(ctx, orgID, integration.ID, installationID)
			}
		}
	}

	http.Redirect(w, r, h.frontendURL+"/integrations?github=connected", http.StatusTemporaryRedirect)
}

// syncInstallationRepos fetches repos for a GitHub App installation and
// upserts them into the database. Errors are logged but not surfaced to
// the caller — the webhook provides a fallback if this fails.
func (h *IntegrationHandler) syncInstallationRepos(ctx context.Context, orgID uuid.UUID, integrationID uuid.UUID, installationID int64) {
	token, err := h.githubService.GetInstallationToken(ctx, installationID)
	if err != nil {
		return
	}

	repos, err := h.listInstallationRepos(ctx, token)
	if err != nil {
		return
	}

	for _, ghRepo := range repos {
		repo := &models.Repository{
			OrgID:          orgID,
			IntegrationID:  integrationID,
			GitHubID:       ghRepo.ID,
			FullName:       ghRepo.FullName,
			DefaultBranch:  ghRepo.DefaultBranch,
			Private:        ghRepo.Private,
			CloneURL:       ghRepo.CloneURL,
			InstallationID: installationID,
			Status:         "active",
			Settings:       json.RawMessage(`{}`),
		}
		if repo.DefaultBranch == "" {
			repo.DefaultBranch = "main"
		}
		if repo.CloneURL == "" {
			repo.CloneURL = "https://github.com/" + ghRepo.FullName + ".git"
		}
		if err := h.repoStore.UpsertFromGitHub(ctx, repo); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("repo", ghRepo.FullName).Msg("failed to upsert repo from GitHub")
			continue
		}
	}
}

// githubInstallationRepo is the subset of fields returned by
// GET /installation/repositories.
type githubInstallationRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
}

// listInstallationRepos calls the GitHub API to list all repos accessible
// to the given installation token.
func (h *IntegrationHandler) listInstallationRepos(ctx context.Context, token string) ([]githubInstallationRepo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL+"/installation/repositories?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API error %d", resp.StatusCode)
	}

	var result struct {
		Repositories []githubInstallationRepo `json:"repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Repositories, nil
}

func (h *IntegrationHandler) ConnectGitHub(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderGitHub)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_GITHUB_FAILED", "failed to connect github integration")
		return
	}
	if created {
		writeJSON(w, http.StatusCreated, models.SingleResponse[models.Integration]{Data: integration})
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Integration]{Data: integration})
}

// ──────────────────────────────────────────────────────────────────────────────
// Slack OAuth
// ──────────────────────────────────────────────────────────────────────────────

func (h *IntegrationHandler) StartSlackOAuth(w http.ResponseWriter, r *http.Request) {
	if h.slackClientID == "" || h.slackSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "SLACK_OAUTH_NOT_CONFIGURED", "slack oauth is not configured")
		return
	}

	state, err := setOAuthState(w, slackIntegrationOAuthStateCookie)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state")
		return
	}

	params := url.Values{
		"client_id":    {h.slackClientID},
		"redirect_uri": {h.slackRedirectURL()},
		"scope":        {"channels:history,channels:read,groups:read,groups:history,users:read"},
		"state":        {state},
	}

	http.Redirect(w, r, slackAuthorizeURL+"?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) HandleSlackOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code, ok := validateOAuthCallback(w, r, slackIntegrationOAuthStateCookie)
	if !ok {
		return
	}

	token, err := h.exchangeSlackCode(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
		return
	}

	slackConfig := models.SlackConfig{
		AccessToken: token.AccessToken,
		TeamID:      token.Team.ID,
		TeamName:    token.Team.Name,
		Scope:       token.Scope,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, slackConfig); err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store slack credential")
		return
	}

	if _, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderSlack); err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_SLACK_FAILED", "failed to connect slack integration")
		return
	}

	http.Redirect(w, r, h.frontendURL+"/integrations?slack=connected", http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) ConnectSlack(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderSlack)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_SLACK_FAILED", "failed to connect slack integration")
		return
	}
	if created {
		writeJSON(w, http.StatusCreated, models.SingleResponse[models.Integration]{Data: integration})
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Integration]{Data: integration})
}

// ListSlackChannels fetches available channels from the Slack API.
func (h *IntegrationHandler) ListSlackChannels(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	cred, err := h.getSlackCredential(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "SLACK_NOT_CONNECTED", "slack integration not connected")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		slackAPIURL+"/conversations.list?types=public_channel&exclude_archived=true&limit=200", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)

	resp, err := h.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "SLACK_API_FAILED", "failed to fetch channels")
		return
	}
	defer resp.Body.Close()

	var slackResp struct {
		OK       bool `json:"ok"`
		Channels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"channels"`
		Error string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&slackResp); err != nil {
		writeError(w, http.StatusInternalServerError, "DECODE_FAILED", "failed to decode slack response")
		return
	}
	if !slackResp.OK {
		writeError(w, http.StatusBadGateway, "SLACK_API_ERROR", slackResp.Error)
		return
	}

	type channelEntry struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Selected bool   `json:"selected"`
	}
	channels := make([]channelEntry, 0, len(slackResp.Channels))
	for _, ch := range slackResp.Channels {
		selected := false
		for _, id := range cred.ChannelIDs {
			if id == ch.ID {
				selected = true
				break
			}
		}
		channels = append(channels, channelEntry{ID: ch.ID, Name: ch.Name, Selected: selected})
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": channels})
}

// UpdateSlackChannels saves the selected channel IDs to the credential config.
func (h *IntegrationHandler) UpdateSlackChannels(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var body struct {
		ChannelIDs []string `json:"channel_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	cred, err := h.getSlackCredential(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "SLACK_NOT_CONNECTED", "slack integration not connected")
		return
	}

	cred.ChannelIDs = body.ChannelIDs
	if err := h.credentialStore.Upsert(r.Context(), orgID, *cred); err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to update slack channels")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"channel_ids": body.ChannelIDs}})
}

// ──────────────────────────────────────────────────────────────────────────────
// OAuth state helpers
// ──────────────────────────────────────────────────────────────────────────────

// setOAuthState generates a random state string and sets it as a cookie.
// Returns the state value for use in the redirect URL.
func setOAuthState(w http.ResponseWriter, cookieName string) (string, error) {
	state, err := generateRandomString(32)
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return state, nil
}

// validateOAuthCallback validates the state parameter against the cookie,
// clears the state cookie, and extracts the authorization code.
// Returns the authorization code on success. Writes an error response and
// returns ("", false) if validation fails.
func validateOAuthCallback(w http.ResponseWriter, r *http.Request, cookieName string) (code string, ok bool) {
	stateCookie, err := r.Cookie(cookieName)
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		writeError(w, http.StatusBadRequest, "INVALID_STATE", "OAuth state mismatch")
		return "", false
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	code = r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "MISSING_CODE", "missing authorization code")
		return "", false
	}

	return code, true
}

// ──────────────────────────────────────────────────────────────────────────────
// Shared helpers
// ──────────────────────────────────────────────────────────────────────────────

func (h *IntegrationHandler) ensureIntegration(ctx context.Context, orgID uuid.UUID, provider models.IntegrationProvider) (models.Integration, bool, error) {
	activeIntegrations, err := h.integrationStore.ListByOrgAndProvider(ctx, orgID, string(provider))
	if err != nil {
		return models.Integration{}, false, err
	}

	if len(activeIntegrations) > 0 {
		return activeIntegrations[0], false, nil
	}

	integration := &models.Integration{
		OrgID:    orgID,
		Provider: provider,
		Status:   models.IntegrationStatusActive,
	}
	if err := h.integrationStore.Create(ctx, integration); err != nil {
		return models.Integration{}, false, err
	}

	return *integration, true, nil
}

// --- Redirect URLs ---

func (h *IntegrationHandler) linearRedirectURL() string {
	return h.baseURL + "/api/v1/integrations/linear/callback"
}

func (h *IntegrationHandler) sentryRedirectURL() string {
	return h.baseURL + "/api/v1/integrations/sentry/callback"
}

func (h *IntegrationHandler) githubRedirectURL() string {
	return h.baseURL + "/api/v1/integrations/github/callback"
}

func (h *IntegrationHandler) slackRedirectURL() string {
	return h.baseURL + "/api/v1/integrations/slack/callback"
}

// --- Linear token exchange ---

func (h *IntegrationHandler) exchangeLinearCode(ctx context.Context, code string) (*linearTokenResponse, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  h.linearRedirectURL(),
		"client_id":     h.linearClientID,
		"client_secret": h.linearSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal linear oauth request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearTokenURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("create linear oauth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("linear oauth token request failed: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var token linearTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode linear token response: %w", err)
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("linear token response missing access_token")
	}

	return &token, nil
}

func (h *IntegrationHandler) fetchLinearWorkspace(ctx context.Context, accessToken string) (string, string, error) {
	queryBody := map[string]string{"query": "query ViewerOrg { viewer { organization { id name } } }"}
	body, err := json.Marshal(queryBody)
	if err != nil {
		return "", "", fmt.Errorf("marshal linear viewer query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearGraphQLURL, bytes.NewBuffer(body))
	if err != nil {
		return "", "", fmt.Errorf("create linear viewer request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := h.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("linear viewer request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("linear viewer request failed: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var viewer linearViewerResponse
	if err := json.NewDecoder(resp.Body).Decode(&viewer); err != nil {
		return "", "", fmt.Errorf("decode linear viewer response: %w", err)
	}

	workspaceID := viewer.Data.Viewer.Organization.ID
	workspaceName := viewer.Data.Viewer.Organization.Name
	if workspaceID == "" {
		return "", "", fmt.Errorf("linear viewer response missing organization id")
	}

	return workspaceID, workspaceName, nil
}

// --- Sentry token exchange ---

func (h *IntegrationHandler) exchangeSentryCode(ctx context.Context, code string) (*sentryTokenResponse, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {h.sentryRedirectURL()},
		"client_id":     {h.sentryClientID},
		"client_secret": {h.sentrySecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sentryTokenURL, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create sentry oauth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sentry oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("sentry oauth token request failed: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var token sentryTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode sentry token response: %w", err)
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("sentry token response missing access_token")
	}

	return &token, nil
}

func (h *IntegrationHandler) fetchSentryOrganization(ctx context.Context, accessToken string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sentryAPIURL+"/organizations/", nil)
	if err != nil {
		return "", "", fmt.Errorf("create sentry org request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := h.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("sentry org request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("sentry org request failed: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var orgs []sentryOrganization
	if err := json.NewDecoder(resp.Body).Decode(&orgs); err != nil {
		return "", "", fmt.Errorf("decode sentry org response: %w", err)
	}
	if len(orgs) == 0 {
		return "", "", fmt.Errorf("sentry org response returned no organizations")
	}

	return orgs[0].Slug, orgs[0].Name, nil
}

// --- GitHub token exchange ---

func (h *IntegrationHandler) exchangeGitHubCode(ctx context.Context, code string) (*githubIntegrationTokenResponse, error) {
	body, err := json.Marshal(map[string]string{
		"client_id":     h.githubClientID,
		"client_secret": h.githubSecret,
		"code":          code,
		"redirect_uri":  h.githubRedirectURL(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal github oauth request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("create github oauth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("github oauth token request failed: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var token githubIntegrationTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode github token response: %w", err)
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("github token response missing access_token")
	}

	return &token, nil
}

// --- Slack token exchange ---

func (h *IntegrationHandler) exchangeSlackCode(ctx context.Context, code string) (*slackTokenResponse, error) {
	data := url.Values{
		"code":          {code},
		"redirect_uri":  {h.slackRedirectURL()},
		"client_id":     {h.slackClientID},
		"client_secret": {h.slackSecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackTokenURL, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create slack oauth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("slack oauth token request failed: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var token slackTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode slack token response: %w", err)
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("slack token response missing access_token")
	}

	return &token, nil
}

// getSlackCredential reads the decrypted Slack config for an org.
func (h *IntegrationHandler) getSlackCredential(ctx context.Context, orgID uuid.UUID) (*models.SlackConfig, error) {
	type credentialGetter interface {
		Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
	}

	getter, ok := h.credentialStore.(credentialGetter)
	if !ok {
		return nil, fmt.Errorf("credential store does not support Get")
	}

	decrypted, err := getter.Get(ctx, orgID, models.ProviderSlack)
	if err != nil {
		return nil, fmt.Errorf("get slack credential: %w", err)
	}

	cfg, ok := decrypted.Config.(models.SlackConfig)
	if !ok {
		return nil, fmt.Errorf("unexpected slack config type")
	}

	return &cfg, nil
}
