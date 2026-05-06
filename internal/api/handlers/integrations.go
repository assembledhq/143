package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	githubOAuthStateCookie            = "github_oauth_state"
	googleOAuthStateCookie            = "google_oauth_state"
	linearIntegrationOAuthStateCookie = "linear_integration_oauth_state"
	sentryIntegrationOAuthStateCookie = "sentry_integration_oauth_state"
	githubIntegrationOAuthStateCookie = "github_integration_oauth_state"
	slackIntegrationOAuthStateCookie  = "slack_integration_oauth_state"
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
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	// ExpiresIn is the access token TTL in seconds. Legacy installs created
	// before Linear's refresh-token rollout can have credential rows with a
	// zero ExpiresAt, which the runtime treats as "no known expiry".
	ExpiresIn int `json:"expires_in"`
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
	AccessToken string        `json:"access_token"`
	TokenType   string        `json:"token_type"`
	Scope       string        `json:"scope"`
	Team        slackTeamInfo `json:"team"`
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

	// PM context auto-trigger (nil-safe: disabled if not configured)
	pmAutoTriggerJobs   pmAutoTriggerJobStore
	pmAutoTriggerDocs   pmAutoTriggerDocStore
	pmAutoTriggerLogger zerolog.Logger

	// Linear post-install hooks (nil-safe).
	linearJobStore                *db.JobStore
	linearTeamKeyRefresher        func(ctx context.Context, orgID uuid.UUID) error
	linearTeamKeyCacheInvalidator func(orgID uuid.UUID)
}

// SetLinearJobStore wires a JobStore so the OAuth callback can enqueue an
// initial refresh_linear_team_keys job. Without this hook, the team-key
// allowlist stays empty until the next 24h cron, which means bare-identifier
// detection won't work right after install.
func (h *IntegrationHandler) SetLinearJobStore(jobs *db.JobStore) {
	h.linearJobStore = jobs
}

// SetLinearTeamKeyRefresher wires an inline refresh hook (typically
// linear.Service.RefreshTeamKeys) the OAuth callback runs under a short
// budget before falling back to the worker enqueue. The inline path is
// best-effort — if it fails or times out, the enqueue still fires so the
// allowlist eventually populates; if both fail, the 24h cron is the last
// line of defense.
func (h *IntegrationHandler) SetLinearTeamKeyRefresher(fn func(ctx context.Context, orgID uuid.UUID) error) {
	h.linearTeamKeyRefresher = fn
}

// SetLinearTeamKeyCacheInvalidator wires the cache-invalidation hook
// (typically linear.Service.InvalidateTeamKeyCache) so DisconnectIntegration
// can drop the in-process team-key allowlist entry for the org as soon as
// the integration goes inactive. Nil-safe: when unset, the cache TTL
// (60s) bounds the worst-case staleness window on its own.
func (h *IntegrationHandler) SetLinearTeamKeyCacheInvalidator(fn func(orgID uuid.UUID)) {
	h.linearTeamKeyCacheInvalidator = fn
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

// pmAutoTriggerJobStore is the minimal job enqueue interface for PM context auto-triggers.
type pmAutoTriggerJobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

// pmAutoTriggerDocStore is the minimal PM doc interface for checking if bootstrap is needed.
type pmAutoTriggerDocStore interface {
	GetByOrgAndSourceType(ctx context.Context, orgID uuid.UUID, sourceType string) (models.PMDocument, error)
}

// WithPMContextAutoTrigger enables automatic bootstrap/refresh when new integrations are connected.
func WithPMContextAutoTrigger(jobs pmAutoTriggerJobStore, docs pmAutoTriggerDocStore, logger zerolog.Logger) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.pmAutoTriggerJobs = jobs
		h.pmAutoTriggerDocs = docs
		h.pmAutoTriggerLogger = logger
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
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list integrations", err)
		return
	}
	if integrations == nil {
		integrations = []models.Integration{}
	}
	for i := range integrations {
		h.deriveIntegrationStatus(r.Context(), &integrations[i])
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.Integration]{Data: integrations})
}

func (h *IntegrationHandler) deriveIntegrationStatus(ctx context.Context, integration *models.Integration) {
	if integration == nil {
		return
	}

	// Auth-error surfacing is provider-agnostic: the linear service stamps
	// the markers, but the UI can render the same banner for any provider
	// that adopts the convention later. Read-only — never echo the rest of
	// config (which holds tokens).
	if authErr := readAuthErrorFromConfig(integration.Config); authErr != nil {
		integration.AuthError = authErr
	}

	if integration.Provider != models.IntegrationProviderGitHub {
		return
	}

	var cfg struct {
		InstallationID int64 `json:"installation_id"`
	}
	installed := false
	if len(integration.Config) > 0 && json.Unmarshal(integration.Config, &cfg) == nil && cfg.InstallationID > 0 {
		installed = true
	}
	if !installed && h.repoStore != nil {
		if installationID, err := h.repoStore.GetAnyInstallationIDByOrg(ctx, integration.OrgID); err == nil && installationID > 0 {
			installed = true
		}
	}
	integration.GitHubAppInstalled = &installed
}

// readAuthErrorFromConfig extracts the auth-error pair the linear service
// stamps when it sees a 401. Returns nil when either key is missing or the
// timestamp doesn't parse — partial markers are treated as absent so a
// malformed jsonb doesn't render an empty banner.
func readAuthErrorFromConfig(raw json.RawMessage) *models.IntegrationAuthError {
	if len(raw) == 0 {
		return nil
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	reason, _ := cfg[models.IntegrationConfigAuthErrorKey].(string)
	atStr, _ := cfg[models.IntegrationConfigAuthErrorAtKey].(string)
	if reason == "" || atStr == "" {
		return nil
	}
	at, err := time.Parse(time.RFC3339, atStr)
	if err != nil {
		return nil
	}
	return &models.IntegrationAuthError{Reason: reason, At: at}
}

// DisconnectIntegration sets the integration status to inactive for a given provider.
// The provider is extracted from the URL path: /api/v1/integrations/{provider}/disconnect.
func (h *IntegrationHandler) DisconnectIntegration(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	// Extract provider from the URL path. We use explicit routes per provider
	// (rather than chi's {provider} param) to avoid routing conflicts with
	// other static integration routes like /integrations/github/sync.
	providerStr := chi.URLParam(r, "provider")
	if providerStr == "" {
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// Expected: api / v1 / integrations / {provider} / disconnect
		if len(segments) >= 5 {
			providerStr = segments[3]
		}
	}

	provider := models.IntegrationProvider(providerStr)
	if err := provider.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROVIDER", "invalid integration provider")
		return
	}

	activeIntegrations, err := h.integrationStore.ListByOrgAndProvider(r.Context(), orgID, string(provider))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to look up integration", err)
		return
	}
	if len(activeIntegrations) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	for _, integration := range activeIntegrations {
		if err := h.integrationStore.UpdateStatus(r.Context(), orgID, integration.ID, string(models.IntegrationStatusInactive)); err != nil {
			writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to disconnect integration", err)
			return
		}
		if provider == models.IntegrationProviderGitHub && h.repoStore != nil {
			if err := h.repoStore.DisconnectByIntegration(r.Context(), orgID, integration.ID); err != nil {
				writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to disconnect github repositories", err)
				return
			}
		}
	}

	// Drop the Linear team-key allowlist cache for this org so a session
	// created right after disconnect doesn't admit bare-identifier matches
	// against a workspace whose integration just went inactive. The DB-side
	// linear_team_keys.ListByOrg already filters on `integrations.status =
	// 'active'`, so this only addresses the in-process cache. Best-effort
	// and nil-safe.
	if provider == models.IntegrationProviderLinear && h.linearTeamKeyCacheInvalidator != nil {
		h.linearTeamKeyCacheInvalidator(orgID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// ──────────────────────────────────────────────────────────────────────────────
// Linear OAuth
// ──────────────────────────────────────────────────────────────────────────────

func (h *IntegrationHandler) StartLinearOAuth(w http.ResponseWriter, r *http.Request) {
	if h.linearClientID == "" || h.linearSecret == "" {
		writeError(w, r, http.StatusServiceUnavailable, "LINEAR_OAUTH_NOT_CONFIGURED", "linear oauth is not configured")
		return
	}

	state, err := setOAuthState(w, linearIntegrationOAuthStateCookie)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state", err)
		return
	}

	// offline_access is the documented Linear scope that triggers refresh_token
	// + expires_in in the token response. Without it, Linear treats the token
	// as long-lived but the refresh path here is unrecoverable on revocation —
	// the user has to manually reconnect after every revocation event. The
	// rest of the refresh machinery in internal/services/linear/refresh.go is
	// a no-op until this scope is granted.
	params := url.Values{
		"client_id":     {h.linearClientID},
		"redirect_uri":  {h.linearRedirectURL()},
		"response_type": {"code"},
		"scope":         {"read,write,offline_access"},
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
		writeError(w, r, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code", err)
		return
	}

	workspaceID, workspaceName, err := h.fetchLinearWorkspace(r.Context(), token.AccessToken)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LINEAR_API_FAILED", "failed to fetch linear workspace", err)
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
		return
	}

	linearConfig := models.LinearConfig{
		AccessToken:   token.AccessToken,
		RefreshToken:  token.RefreshToken,
		TokenType:     token.TokenType,
		Scope:         token.Scope,
		WorkspaceID:   workspaceID,
		WorkspaceName: workspaceName,
	}
	if token.ExpiresIn > 0 {
		linearConfig.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, linearConfig); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store linear credential", err)
		return
	}

	if _, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderLinear); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to connect linear integration", err)
		return
	}

	// Trigger an initial team-key refresh so detection's bare-identifier
	// branch works on the very next session. Three-tier strategy:
	//  1. Detached-context background refresh kicked off here so the OAuth
	//     redirect returns immediately — under typical load Linear's teams
	//     query lands well inside the cache TTL window before the user
	//     creates their next session.
	//  2. Worker enqueue is fired in parallel as the durable fallback so
	//     a slow/failed inline path still has a guaranteed retry path.
	//     Both paths dedupe on the same job key, so we don't over-charge
	//     the worker queue.
	//  3. The 24h scheduler pass (Scheduler.scheduleLinearTeamKeyRefresh)
	//     is the last line of defense after that.
	// All paths are nil-safe so test harnesses without these hooks still
	// complete the OAuth flow normally.
	logger := zerolog.Ctx(r.Context())
	if h.linearTeamKeyRefresher != nil {
		// context.WithoutCancel: a cancelled request context (the user
		// closing the browser tab post-redirect) must not abort the
		// refresh. The 3s cap here is just defense against a wedged
		// Linear API hold of an internal goroutine.
		bgCtx, bgCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
		go func() {
			defer bgCancel()
			if err := h.linearTeamKeyRefresher(bgCtx, orgID); err != nil {
				logger.Warn().Err(err).
					Str("org_id", orgID.String()).
					Msg("background refresh_linear_team_keys after install failed; worker fallback or 24h cron will retry")
			}
		}()
	}
	if h.linearJobStore != nil {
		dedupe := "refresh_linear_team_keys:" + orgID.String()
		if _, err := h.linearJobStore.Enqueue(r.Context(), orgID, "linear", "refresh_linear_team_keys", map[string]any{
			"org_id": orgID.String(),
		}, 5, &dedupe); err != nil {
			logger.Warn().Err(err).Msg("failed to enqueue refresh_linear_team_keys after install")
		}
	}

	http.Redirect(w, r, h.frontendURL+"/integrations?linear=connected", http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) ConnectLinear(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderLinear)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to connect linear integration", err)
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
		writeError(w, r, http.StatusServiceUnavailable, "SENTRY_OAUTH_NOT_CONFIGURED", "sentry oauth is not configured")
		return
	}

	state, err := setOAuthState(w, sentryIntegrationOAuthStateCookie)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state", err)
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
		writeError(w, r, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code", err)
		return
	}

	orgSlug, orgName, err := h.fetchSentryOrganization(r.Context(), token.AccessToken)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SENTRY_API_FAILED", "failed to fetch sentry organization", err)
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
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
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store sentry credential", err)
		return
	}

	if _, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderSentry); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_SENTRY_FAILED", "failed to connect sentry integration", err)
		return
	}

	http.Redirect(w, r, h.frontendURL+"/integrations?sentry=connected", http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) ConnectSentry(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderSentry)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_SENTRY_FAILED", "failed to connect sentry integration", err)
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
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state", err)
			return
		}

		installURL := fmt.Sprintf("https://github.com/apps/%s/installations/new?state=%s", h.githubAppSlug, url.QueryEscape(state))
		http.Redirect(w, r, installURL, http.StatusTemporaryRedirect)
		return
	}

	if h.githubClientID == "" || h.githubSecret == "" {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_OAUTH_NOT_CONFIGURED", "github integration oauth is not configured")
		return
	}

	state, err := setOAuthState(w, githubIntegrationOAuthStateCookie)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state", err)
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
	// When a GitHub App has "Request user authorization during installation"
	// enabled, GitHub redirects here with installation_id and setup_action
	// instead of the Setup URL. Delegate to the App installation handler
	// which stores the installation_id and syncs repos.
	if r.URL.Query().Get("installation_id") != "" && r.URL.Query().Get("setup_action") == "install" {
		h.HandleGitHubAppInstalled(w, r)
		return
	}

	code, ok := validateOAuthCallback(w, r, githubIntegrationOAuthStateCookie)
	if !ok {
		return
	}

	token, err := h.exchangeGitHubCode(r.Context(), code)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code", err)
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
		return
	}

	ghConfig := models.GitHubOAuthConfig{
		AccessToken: token.AccessToken,
		TokenType:   token.TokenType,
		Scope:       token.Scope,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, ghConfig); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store github credential", err)
		return
	}

	if _, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderGitHub); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_GITHUB_FAILED", "failed to connect github integration", err)
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
		writeError(w, r, http.StatusInternalServerError, "CONNECT_GITHUB_FAILED", "failed to connect github integration", err)
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

// SyncGitHubRepos re-syncs repositories from a GitHub App installation.
// This is a recovery mechanism for when the initial webhook-based sync fails.
func (h *IntegrationHandler) SyncGitHubRepos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := middleware.OrgIDFromContext(ctx)

	if h.githubService == nil || h.repoStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_APP_NOT_CONFIGURED", "github app is not configured")
		return
	}

	integrations, err := h.integrationStore.ListByOrgAndProvider(ctx, orgID, string(models.IntegrationProviderGitHub))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_INTEGRATIONS_FAILED", "failed to list integrations", err)
		return
	}
	if len(integrations) == 0 {
		writeError(w, r, http.StatusNotFound, "NO_GITHUB_INTEGRATION", "no active github integration found")
		return
	}

	integration := integrations[0]

	// Extract installation_id from integration config.
	var config struct {
		InstallationID int64 `json:"installation_id"`
	}
	if integration.Config != nil {
		if err := json.Unmarshal(integration.Config, &config); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", "failed to parse integration config")
			return
		}
	}
	if config.InstallationID == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_INSTALLATION_ID", "integration config missing installation_id")
		return
	}

	token, err := h.githubService.GetInstallationToken(ctx, config.InstallationID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to get installation token for sync")
		writeError(w, r, http.StatusBadGateway, "TOKEN_FAILED", "failed to get github installation token")
		return
	}

	repos, err := h.listInstallationRepos(ctx, token)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to list installation repos for sync")
		writeError(w, r, http.StatusBadGateway, "LIST_REPOS_FAILED", "failed to list github repositories")
		return
	}

	synced := 0
	syncErrors := 0
	for _, ghRepo := range repos {
		repo := &models.Repository{
			OrgID:          orgID,
			IntegrationID:  integration.ID,
			GitHubID:       ghRepo.ID,
			FullName:       ghRepo.FullName,
			DefaultBranch:  ghRepo.DefaultBranch,
			Private:        ghRepo.Private,
			CloneURL:       ghRepo.CloneURL,
			InstallationID: config.InstallationID,
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
			zerolog.Ctx(ctx).Warn().Err(err).Str("repo", ghRepo.FullName).Msg("failed to upsert repo during sync")
			syncErrors++
			continue
		}
		synced++
	}

	if err := h.integrationStore.UpdateLastSyncedAt(ctx, orgID, integration.ID, time.Now()); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to update last_synced_at after sync")
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"repos_synced": synced,
			"errors":       syncErrors,
		},
	})
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
		writeError(w, r, http.StatusInternalServerError, "CONNECT_GITHUB_FAILED", "failed to connect github integration", err)
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
		writeError(w, r, http.StatusServiceUnavailable, "SLACK_OAUTH_NOT_CONFIGURED", "slack oauth is not configured")
		return
	}

	state, err := setOAuthState(w, slackIntegrationOAuthStateCookie)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state", err)
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
		writeError(w, r, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code", err)
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
		return
	}

	slackConfig := models.SlackConfig{
		AccessToken: token.AccessToken,
		TeamID:      token.Team.ID,
		TeamName:    token.Team.Name,
		Scope:       token.Scope,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, slackConfig); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store slack credential", err)
		return
	}

	if _, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderSlack); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_SLACK_FAILED", "failed to connect slack integration", err)
		return
	}

	http.Redirect(w, r, h.frontendURL+"/integrations?slack=connected", http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) ConnectSlack(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderSlack)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_SLACK_FAILED", "failed to connect slack integration", err)
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
		writeError(w, r, http.StatusBadRequest, "SLACK_NOT_CONNECTED", "slack integration not connected")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		slackAPIURL+"/conversations.list?types=public_channel&exclude_archived=true&limit=200", nil)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create request", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)

	resp, err := h.client.Do(req)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "SLACK_API_FAILED", "failed to fetch channels")
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
		writeError(w, r, http.StatusInternalServerError, "DECODE_FAILED", "failed to decode slack response", err)
		return
	}
	if !slackResp.OK {
		writeError(w, r, http.StatusBadGateway, "SLACK_API_ERROR", slackResp.Error)
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
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	cred, err := h.getSlackCredential(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "SLACK_NOT_CONNECTED", "slack integration not connected")
		return
	}

	cred.ChannelIDs = body.ChannelIDs
	if err := h.credentialStore.Upsert(r.Context(), orgID, *cred); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to update slack channels", err)
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
		writeError(w, r, http.StatusBadRequest, "INVALID_STATE", "OAuth state mismatch")
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
		writeError(w, r, http.StatusBadRequest, "MISSING_CODE", "missing authorization code")
		return "", false
	}

	return code, true
}

// ──────────────────────────────────────────────────────────────────────────────
// Shared helpers
// ──────────────────────────────────────────────────────────────────────────────

// maybeEnqueuePMContext checks if a PM context bootstrap or refresh should be
// auto-triggered after a new integration is connected. If no autogenerated doc
// exists, it enqueues a bootstrap. If one exists, it enqueues a refresh so the
// new integration's data gets incorporated.
func (h *IntegrationHandler) maybeEnqueuePMContext(ctx context.Context, orgID uuid.UUID) {
	if h.pmAutoTriggerJobs == nil || h.pmAutoTriggerDocs == nil {
		return
	}

	_, err := h.pmAutoTriggerDocs.GetByOrgAndSourceType(ctx, orgID, models.PMDocSourceAutogenerated)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// Transient DB error — log and bail rather than incorrectly triggering bootstrap.
			h.pmAutoTriggerLogger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to check autogenerated doc; skipping PM context auto-trigger")
			return
		}
		// No autogenerated doc exists — enqueue a bootstrap.
		dedupeKey := fmt.Sprintf("pm_bootstrap:%s", orgID.String())
		payload := map[string]string{"org_id": orgID.String()}
		if _, err := h.pmAutoTriggerJobs.Enqueue(ctx, orgID, "default", models.JobTypePMBootstrap, payload, 3, &dedupeKey); err != nil {
			h.pmAutoTriggerLogger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to auto-enqueue pm_bootstrap after integration connect")
		}
		return
	}

	// Autogenerated doc exists — enqueue a refresh to pick up the new integration.
	dedupeKey := fmt.Sprintf("pm_context_refresh:%s", orgID.String())
	payload := map[string]string{"org_id": orgID.String()}
	if _, err := h.pmAutoTriggerJobs.Enqueue(ctx, orgID, "default", models.JobTypePMContextRefresh, payload, 3, &dedupeKey); err != nil {
		h.pmAutoTriggerLogger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to auto-enqueue pm_context_refresh after integration connect")
	}
}

func (h *IntegrationHandler) ensureIntegration(ctx context.Context, orgID uuid.UUID, provider models.IntegrationProvider) (models.Integration, bool, error) {
	// One round trip: returns active rows first, then errored rows. Lets a
	// reconnect after a 401-flip reuse the original row instead of leaving
	// a stale errored row plus a fresh duplicate. Active-first ORDER BY in
	// SQL keeps the existing "prefer active" precedence.
	reusableIntegrations, err := h.integrationStore.ListReusableForReconnect(ctx, orgID, string(provider))
	if err != nil {
		return models.Integration{}, false, err
	}

	if len(reusableIntegrations) > 0 {
		integration := reusableIntegrations[0]
		// A reconnect flow lands here: if the row is errored (most often
		// from a prior 401 the worker stamped) restore it to active and
		// strip the auth-error markers so the settings UI's Reconnect CTA
		// goes away immediately. Best-effort — failures here don't prevent
		// the OAuth flow from completing because the credential write has
		// already succeeded; the next successful Linear API call will
		// invoke ClearIntegrationUnauthorized and converge state anyway.
		// When both fields need to change we write atomically so the row
		// can never be observed as "active with stale auth_error markers"
		// (which would render no Reconnect CTA but also no Connect CTA).
		clearedConfig, configChanged := stripAuthErrorMarkers(integration.Config)
		statusErrored := integration.Status == models.IntegrationStatusError
		switch {
		case configChanged && statusErrored:
			if err := h.integrationStore.UpdateStatusAndConfig(ctx, orgID, integration.ID, string(models.IntegrationStatusActive), clearedConfig); err == nil {
				integration.Config = clearedConfig
				integration.Status = models.IntegrationStatusActive
			}
		case configChanged:
			if err := h.integrationStore.UpdateConfig(ctx, orgID, integration.ID, clearedConfig); err == nil {
				integration.Config = clearedConfig
			}
		case statusErrored:
			if err := h.integrationStore.UpdateStatus(ctx, orgID, integration.ID, string(models.IntegrationStatusActive)); err == nil {
				integration.Status = models.IntegrationStatusActive
			}
		}
		return integration, false, nil
	}

	integration := &models.Integration{
		OrgID:    orgID,
		Provider: provider,
		Status:   models.IntegrationStatusActive,
	}
	if err := h.integrationStore.Create(ctx, integration); err != nil {
		return models.Integration{}, false, err
	}

	// Auto-trigger PM context bootstrap or refresh for the new integration.
	h.maybeEnqueuePMContext(ctx, orgID)

	return *integration, true, nil
}

// stripAuthErrorMarkers removes the auth-error keys the linear service
// stamps into integrations.config when it observes a 401. Returns the
// (possibly unchanged) jsonb and a flag indicating whether any keys were
// dropped — the caller skips the UPDATE when nothing changed to avoid
// pointless updated_at churn on the integrations row.
//
// Lives in the integrations handler so the OAuth reconnect path doesn't
// need to import internal/services/linear; the key names are shared via
// constants in the models package so writer (linear service) and readers
// (this handler) can't drift.
func stripAuthErrorMarkers(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return raw, false
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return raw, false
	}
	_, hadErr := cfg[models.IntegrationConfigAuthErrorKey]
	_, hadAt := cfg[models.IntegrationConfigAuthErrorAtKey]
	if !hadErr && !hadAt {
		return raw, false
	}
	delete(cfg, models.IntegrationConfigAuthErrorKey)
	delete(cfg, models.IntegrationConfigAuthErrorAtKey)
	out, err := json.Marshal(cfg)
	if err != nil {
		return raw, false
	}
	return out, true
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
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {h.linearRedirectURL()},
		"client_id":     {h.linearClientID},
		"client_secret": {h.linearSecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearTokenURL, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create linear oauth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

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

// ──────────────────────────────────────────────────────────────────────────────
// Notion (token-based, no OAuth)
// ──────────────────────────────────────────────────────────────────────────────

// ConnectNotion accepts an API token, validates it against the Notion API,
// stores the credential, and creates an active integration record.
// Unlike other integrations, Notion uses a simple internal integration token
// rather than an OAuth flow.
func (h *IntegrationHandler) ConnectNotion(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var req struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if req.AccessToken == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_TOKEN", "access_token is required")
		return
	}

	// Validate the token by calling the Notion API.
	workspaceName, err := h.validateNotionToken(r.Context(), req.AccessToken)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN", "failed to validate Notion token: "+err.Error())
		return
	}

	// Store the credential.
	cfg := models.NotionConfig{
		AccessToken:   req.AccessToken,
		WorkspaceName: workspaceName,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, cfg); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_SAVE_FAILED", "failed to save Notion credentials", err)
		return
	}

	// Ensure the integration record exists.
	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderNotion)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_NOTION_FAILED", "failed to create Notion integration", err)
		return
	}

	if created {
		writeJSON(w, http.StatusCreated, models.SingleResponse[models.Integration]{Data: integration})
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Integration]{Data: integration})
}

// validateNotionToken calls the Notion /v1/users/me endpoint to verify the
// token is valid and extract the workspace name. Returns the bot's workspace
// name on success, or an error if the token is invalid.
func (h *IntegrationHandler) validateNotionToken(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.notion.com/v1/users/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Notion-Version", "2022-06-28")

	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("invalid or expired token")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("notion API returned %d", resp.StatusCode)
	}

	var result struct {
		Bot struct {
			WorkspaceName string `json:"workspace_name"`
		} `json:"bot"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	return result.Bot.WorkspaceName, nil
}
