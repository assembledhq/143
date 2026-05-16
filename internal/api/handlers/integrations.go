package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

type githubAppUserCredentialProvider interface {
	GetValidCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error)
}

type githubMembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
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
	githubService       githubAppService
	repoStore           *db.RepositoryStore
	githubInstallations *db.GitHubInstallationStore
	githubAppUserAuth   githubAppUserCredentialProvider
	memberships         githubMembershipStore
	setupSigningKey     string

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

// WithGitHubApp injects the GitHub App service and repo store used by
// repository selection, explicit claims, and legacy sync status endpoints.
func WithGitHubApp(svc githubAppService, repoStore *db.RepositoryStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.githubService = svc
		h.repoStore = repoStore
	}
}

func WithGitHubInstallationStore(store *db.GitHubInstallationStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.githubInstallations = store
	}
}

func WithGitHubAppUserAuth(auth githubAppUserCredentialProvider) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.githubAppUserAuth = auth
	}
}

func WithIntegrationMembershipStore(store githubMembershipStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.memberships = store
	}
}

func WithGitHubSetupSigningKey(key string) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.setupSigningKey = key
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
	activeProviders := activeIntegrationProviders(integrations)
	for i := range integrations {
		h.deriveIntegrationStatus(r.Context(), &integrations[i], activeProviders)
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.Integration]{Data: integrations})
}

func activeIntegrationProviders(integrations []models.Integration) map[models.IntegrationProvider]bool {
	active := make(map[models.IntegrationProvider]bool)
	for _, integration := range integrations {
		if integration.Status == models.IntegrationStatusActive {
			active[integration.Provider] = true
		}
	}
	return active
}

func (h *IntegrationHandler) deriveIntegrationStatus(ctx context.Context, integration *models.Integration, activeProviders map[models.IntegrationProvider]bool) {
	if integration == nil {
		return
	}

	// Auth-error surfacing is provider-agnostic: the linear service stamps
	// the markers, but the UI can render the same banner for any provider
	// that adopts the convention later. Read-only — never echo the rest of
	// config (which holds tokens).
	if authErr := readAuthErrorFromConfig(integration.Config); authErr != nil {
		// Historical reconnect bugs could leave an errored duplicate row
		// beside the active provider row. When an active row exists, the
		// stale duplicate must not keep rendering a reconnect banner.
		if integration.Status == models.IntegrationStatusActive || !activeProviders[integration.Provider] {
			integration.AuthError = authErr
		}
	}

	if integration.Provider != models.IntegrationProviderGitHub {
		return
	}

	var cfg struct {
		InstallationID int64 `json:"installation_id"`
	}
	installed := false
	var installationID int64
	var accountLogin string
	if len(integration.Config) > 0 && json.Unmarshal(integration.Config, &cfg) == nil && cfg.InstallationID > 0 {
		installed = true
		installationID = cfg.InstallationID
	}
	if h.githubInstallations != nil {
		if link, err := h.githubInstallations.FirstOrgLink(ctx, integration.OrgID); err == nil {
			installed = true
			installationID = link.InstallationID
			accountLogin = link.AccountLogin
		}
	}
	if !installed && h.repoStore != nil {
		if fallbackInstallationID, err := h.repoStore.GetAnyInstallationIDByOrg(ctx, integration.OrgID); err == nil && fallbackInstallationID > 0 {
			installed = true
			installationID = fallbackInstallationID
		}
	}
	integration.GitHubAppInstalled = &installed
	if installationID > 0 {
		integration.GitHubInstallationID = &installationID
	}
	if accountLogin != "" {
		integration.GitHubAccountLogin = &accountLogin
	}
	if h.repoStore != nil {
		if count, err := h.repoStore.CountActiveByOrg(ctx, integration.OrgID); err == nil {
			integration.GitHubActiveRepoCount = &count
			required := installed && count == 0
			integration.GitHubRepoSelectionRequired = &required
		}
	}
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
		if provider == models.IntegrationProviderGitHub && h.githubInstallations != nil {
			if err := h.githubInstallations.DeactivateOrgLinksByIntegration(r.Context(), orgID, integration.ID); err != nil {
				writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to disconnect github installation links", err)
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

	params := url.Values{
		"client_id":     {h.linearClientID},
		"redirect_uri":  {h.linearRedirectURL()},
		"response_type": {"code"},
		"scope":         {"read,write"},
		"state":         {state},
	}

	http.Redirect(w, r, linearAuthorizeURL+"?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) HandleLinearOAuthCallback(w http.ResponseWriter, r *http.Request) {
	logger := zerolog.Ctx(r.Context())
	logger.Info().Msg("linear oauth callback received")

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

	logger.Info().
		Str("org_id", orgID.String()).
		Str("workspace_id", workspaceID).
		Msg("linear oauth callback completed; credentials stored and integration active")

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
	// instead of the OAuth flow. Repositories are claimed explicitly after setup;
	// the signed state binds the callback to the intended user and org.
	if h.githubAppSlug != "" {
		state, err := h.setGitHubSetupState(w, r)
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
	// which stores the installation link and redirects to repository selection.
	if r.URL.Query().Get("installation_id") != "" && isGitHubAppSetupAction(r.URL.Query().Get("setup_action")) {
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
	activeOrgID := middleware.OrgIDFromContext(r.Context())
	orgID, userID, ok := h.validateGitHubSetupState(w, r)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "INVALID_GITHUB_SETUP_STATE", "github setup state is missing, expired, or invalid")
		return
	}
	if !h.userIsAdminInOrg(r.Context(), userID, orgID) {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "admin access to the setup organization is required")
		return
	}
	if activeOrgID != uuid.Nil && activeOrgID != orgID {
		zerolog.Ctx(r.Context()).Info().
			Str("active_org_id", activeOrgID.String()).
			Str("setup_org_id", orgID.String()).
			Msg("github setup state org differs from active org")
	}
	ctx := r.Context()

	installIDStr := r.URL.Query().Get("installation_id")
	if installIDStr == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_INSTALLATION_ID", "missing github installation id")
		return
	}
	installationID, parseErr := strconv.ParseInt(installIDStr, 10, 64)
	if parseErr != nil || installationID <= 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_INSTALLATION_ID", "invalid github installation id")
		return
	}

	integration, _, err := h.ensureIntegration(ctx, orgID, models.IntegrationProviderGitHub)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_GITHUB_FAILED", "failed to connect github integration", err)
		return
	}

	// GitHub redirects here with ?installation_id=<id>&setup_action=install.
	// Store the installation_id in the integration config and link it to the
	// target 143 org. Repository rows are activated later through explicit claims.
	accountLogin := r.URL.Query().Get("account_login")
	if accountLogin == "" {
		accountLogin = "unknown"
	}
	if h.githubInstallations != nil {
		inst := &models.GitHubInstallation{
			InstallationID: installationID,
			AccountLogin:   accountLogin,
			Status:         "active",
		}
		if err := h.githubInstallations.UpsertInstallation(ctx, inst); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Int64("installation_id", installationID).Msg("failed to upsert github installation")
		} else if inst.AccountLogin != "" {
			accountLogin = inst.AccountLogin
		}
		var linkedBy *uuid.UUID
		if userID != uuid.Nil {
			linkedBy = &userID
		}
		link := &models.GitHubInstallationOrgLink{
			OrgID:          orgID,
			IntegrationID:  &integration.ID,
			InstallationID: installationID,
			AccountLogin:   accountLogin,
			LinkedByUserID: linkedBy,
			Status:         "active",
		}
		if err := h.githubInstallations.UpsertOrgLink(ctx, link); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Int64("installation_id", installationID).Msg("failed to link github installation to org")
		}
	}
	configJSON, marshalErr := json.Marshal(map[string]any{
		"installation_id": installationID,
		"account_login":   accountLogin,
	})
	if marshalErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(marshalErr).Msg("failed to marshal installation config")
		http.Redirect(w, r, h.frontendURL+"/settings/integrations?github=connected", http.StatusTemporaryRedirect)
		return
	}
	if err := h.integrationStore.UpdateConfig(ctx, orgID, integration.ID, configJSON); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to update integration config")
	}

	http.Redirect(w, r, h.frontendURL+"/settings/integrations?github=connected&select_repos=1", http.StatusTemporaryRedirect)
}

func isGitHubAppSetupAction(action string) bool {
	switch action {
	case "install", "update":
		return true
	default:
		return false
	}
}

// SyncGitHubRepos reports repositories visible to the GitHub App installation.
// It no longer imports repositories; admins claim repositories explicitly.
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

	if err := h.integrationStore.UpdateLastSyncedAt(ctx, orgID, integration.ID, time.Now()); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to update last_synced_at after sync")
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"repos_synced": 0,
			"repos_seen":   len(repos),
			"errors":       0,
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
	nextURL := githubAPIURL + "/installation/repositories?per_page=100"
	var repos []githubInstallationRepo
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "token "+token)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := h.client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			if closeErr := resp.Body.Close(); closeErr != nil {
				return nil, closeErr
			}
			return nil, fmt.Errorf("github API error %d", resp.StatusCode)
		}

		var result struct {
			Repositories []githubInstallationRepo `json:"repositories"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				return nil, closeErr
			}
			return nil, err
		}
		nextURL = githubNextLink(resp.Header.Get("Link"))
		if err := resp.Body.Close(); err != nil {
			return nil, err
		}
		repos = append(repos, result.Repositories...)
	}
	return repos, nil
}

func githubNextLink(linkHeader string) string {
	for _, part := range strings.Split(linkHeader, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		for _, segment := range segments[1:] {
			if strings.TrimSpace(segment) == `rel="next"` {
				return strings.Trim(strings.TrimSpace(segments[0]), "<>")
			}
		}
	}
	return ""
}

func (h *IntegrationHandler) ListGitHubInstallationRepositories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := middleware.OrgIDFromContext(ctx)

	link, ok := h.githubInstallationLinkForRequest(w, r, orgID)
	if !ok {
		return
	}
	if h.githubService == nil || h.repoStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_APP_NOT_CONFIGURED", "github app is not configured")
		return
	}
	token, err := h.githubService.GetInstallationToken(ctx, link.InstallationID)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", "failed to get github installation token", err)
		return
	}
	repos, err := h.listInstallationRepos(ctx, token)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "LIST_REPOS_FAILED", "failed to list github repositories", err)
		return
	}

	user := middleware.UserFromContext(ctx)
	candidates := make([]models.GitHubRepositoryClaimCandidate, 0, len(repos))
	for _, ghRepo := range repos {
		candidate := models.GitHubRepositoryClaimCandidate{
			GitHubID:       ghRepo.ID,
			FullName:       ghRepo.FullName,
			DefaultBranch:  defaultBranchOrMain(ghRepo.DefaultBranch),
			Private:        ghRepo.Private,
			CloneURL:       defaultCloneURL(ghRepo.FullName, ghRepo.CloneURL),
			InstallationID: link.InstallationID,
			Status:         models.GitHubRepositoryClaimStatusUnclaimed,
		}
		if owner, ownerErr := h.repoStore.GetActiveOwnerByGitHubID(ctx, ghRepo.ID); ownerErr == nil {
			candidate.RepositoryID = &owner.RepositoryID
			candidate.OwnerOrgID = &owner.OrgID
			candidate.OwnerOrgName = &owner.OrgName
			if owner.OrgID == orgID {
				candidate.Status = models.GitHubRepositoryClaimStatusOwnedByCurrentOrg
			} else {
				candidate.Status = models.GitHubRepositoryClaimStatusOwnedByOtherOrg
				candidate.CanTransfer = user != nil && h.userIsAdminInOrg(ctx, user.ID, owner.OrgID)
			}
		} else if errors.Is(ownerErr, pgx.ErrNoRows) {
			if existing, existingErr := h.repoStore.GetByOrgAndGitHubIDAnyStatus(ctx, orgID, ghRepo.ID); existingErr == nil {
				candidate.RepositoryID = &existing.ID
				if !existing.IsActive() {
					candidate.Status = models.GitHubRepositoryClaimStatusDisconnectedInCurrentOrg
				}
			}
		} else {
			writeError(w, r, http.StatusInternalServerError, "OWNER_LOOKUP_FAILED", "failed to check repository ownership", ownerErr)
			return
		}
		candidates = append(candidates, candidate)
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.GitHubRepositoryClaimCandidate]{Data: candidates})
}

type githubRepositoryClaimRequest struct {
	InstallationID int64   `json:"installation_id"`
	GitHubIDs      []int64 `json:"github_ids"`
	AllowTransfer  bool    `json:"allow_transfer"`
}

func (h *IntegrationHandler) ClaimGitHubInstallationRepositories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := middleware.OrgIDFromContext(ctx)
	user := middleware.UserFromContext(ctx)
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	if h.githubService == nil || h.repoStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_APP_NOT_CONFIGURED", "github app is not configured")
		return
	}
	if h.githubAppUserAuth == nil {
		writeError(w, r, http.StatusBadRequest, "GITHUB_USER_AUTH_REQUIRED", "connect your GitHub account before claiming repositories")
		return
	}

	var req githubRepositoryClaimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if len(req.GitHubIDs) == 0 {
		writeError(w, r, http.StatusBadRequest, "NO_REPOSITORIES", "select at least one repository")
		return
	}

	link, ok := h.githubInstallationLink(ctx, orgID, req.InstallationID)
	if !ok {
		writeError(w, r, http.StatusNotFound, "INSTALLATION_NOT_LINKED", "github installation is not linked to this organization")
		return
	}
	if link.IntegrationID == nil {
		writeError(w, r, http.StatusBadRequest, "GITHUB_INTEGRATION_NOT_CONNECTED", "github integration is not connected for this organization")
		return
	}

	appToken, err := h.githubService.GetInstallationToken(ctx, link.InstallationID)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", "failed to get github installation token", err)
		return
	}
	installationRepos, err := h.listInstallationRepos(ctx, appToken)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "LIST_REPOS_FAILED", "failed to list github repositories", err)
		return
	}
	byID := make(map[int64]githubInstallationRepo, len(installationRepos))
	for _, repo := range installationRepos {
		byID[repo.ID] = repo
	}

	userCredential, err := h.githubAppUserAuth.GetValidCredential(ctx, orgID, user.ID)
	if err != nil || userCredential == nil || userCredential.AccessToken == "" {
		writeError(w, r, http.StatusBadRequest, "GITHUB_USER_AUTH_REQUIRED", "connect your GitHub account before claiming repositories")
		return
	}

	claimRepos := make([]*models.Repository, 0, len(req.GitHubIDs))
	transferOwners := make(map[int64]uuid.UUID)
	conflicts := make([]models.GitHubRepositoryClaimCandidate, 0)
	for _, githubID := range req.GitHubIDs {
		ghRepo, exists := byID[githubID]
		if !exists {
			writeError(w, r, http.StatusBadRequest, "REPOSITORY_NOT_IN_INSTALLATION", "selected repository is not part of the GitHub installation")
			return
		}
		if ok, accessErr := h.githubUserCanAccessRepo(ctx, userCredential.AccessToken, ghRepo.FullName); accessErr != nil {
			writeError(w, r, http.StatusBadGateway, "GITHUB_ACCESS_CHECK_FAILED", "failed to verify GitHub repository access", accessErr)
			return
		} else if !ok {
			writeError(w, r, http.StatusForbidden, "GITHUB_REPO_ACCESS_DENIED", "your GitHub account cannot access one or more selected repositories")
			return
		}

		if owner, ownerErr := h.repoStore.GetActiveOwnerByGitHubID(ctx, ghRepo.ID); ownerErr == nil && owner.OrgID != orgID {
			conflict := models.GitHubRepositoryClaimCandidate{
				GitHubID:       ghRepo.ID,
				FullName:       ghRepo.FullName,
				DefaultBranch:  defaultBranchOrMain(ghRepo.DefaultBranch),
				Private:        ghRepo.Private,
				CloneURL:       defaultCloneURL(ghRepo.FullName, ghRepo.CloneURL),
				InstallationID: link.InstallationID,
				Status:         models.GitHubRepositoryClaimStatusOwnedByOtherOrg,
				RepositoryID:   &owner.RepositoryID,
				OwnerOrgID:     &owner.OrgID,
				OwnerOrgName:   &owner.OrgName,
			}
			if !req.AllowTransfer {
				conflicts = append(conflicts, conflict)
				continue
			}
			if !h.userIsAdminInOrg(ctx, user.ID, owner.OrgID) {
				conflicts = append(conflicts, conflict)
				continue
			}
			transferOwners[ghRepo.ID] = owner.OrgID
		} else if ownerErr != nil && !errors.Is(ownerErr, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "OWNER_LOOKUP_FAILED", "failed to check repository ownership", ownerErr)
			return
		}

		claimRepos = append(claimRepos, &models.Repository{
			OrgID:          orgID,
			IntegrationID:  *link.IntegrationID,
			GitHubID:       ghRepo.ID,
			FullName:       ghRepo.FullName,
			DefaultBranch:  defaultBranchOrMain(ghRepo.DefaultBranch),
			Private:        ghRepo.Private,
			CloneURL:       defaultCloneURL(ghRepo.FullName, ghRepo.CloneURL),
			InstallationID: link.InstallationID,
			Status:         string(models.RepositoryStatusActive),
			Settings:       json.RawMessage(`{}`),
		})
	}
	if len(conflicts) > 0 {
		writeGitHubRepositoryClaimConflict(w, conflicts)
		return
	}

	if err := h.repoStore.ApplyGitHubClaims(ctx, orgID, claimRepos, transferOwners); err != nil {
		if errors.Is(err, db.ErrActiveGitHubRepositoryOwnershipConflict) {
			writeGitHubRepositoryClaimConflict(w, h.githubClaimConflictCandidates(ctx, orgID, claimRepos))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CLAIM_FAILED", "failed to claim github repositories", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"claimed": len(claimRepos)}})
}

func writeGitHubRepositoryClaimConflict(w http.ResponseWriter, conflicts []models.GitHubRepositoryClaimCandidate) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": map[string]any{
			"code":    "REPOSITORY_OWNERSHIP_CONFLICT",
			"message": "one or more repositories are already owned by another organization",
			"details": map[string]any{"repositories": conflicts},
		},
	})
}

func (h *IntegrationHandler) githubClaimConflictCandidates(ctx context.Context, orgID uuid.UUID, repos []*models.Repository) []models.GitHubRepositoryClaimCandidate {
	conflicts := make([]models.GitHubRepositoryClaimCandidate, 0, len(repos))
	for _, repo := range repos {
		owner, err := h.repoStore.GetActiveOwnerByGitHubID(ctx, repo.GitHubID)
		if err != nil || owner.OrgID == orgID {
			continue
		}
		conflicts = append(conflicts, models.GitHubRepositoryClaimCandidate{
			GitHubID:       repo.GitHubID,
			FullName:       repo.FullName,
			DefaultBranch:  defaultBranchOrMain(repo.DefaultBranch),
			Private:        repo.Private,
			CloneURL:       defaultCloneURL(repo.FullName, repo.CloneURL),
			InstallationID: repo.InstallationID,
			Status:         models.GitHubRepositoryClaimStatusOwnedByOtherOrg,
			RepositoryID:   &owner.RepositoryID,
			OwnerOrgID:     &owner.OrgID,
			OwnerOrgName:   &owner.OrgName,
		})
	}
	return conflicts
}

func (h *IntegrationHandler) githubInstallationLinkForRequest(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (models.GitHubInstallationOrgLink, bool) {
	raw := r.URL.Query().Get("installation_id")
	var installationID int64
	if raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_INSTALLATION_ID", "invalid installation_id")
			return models.GitHubInstallationOrgLink{}, false
		}
		installationID = parsed
	}
	link, ok := h.githubInstallationLink(r.Context(), orgID, installationID)
	if !ok {
		writeError(w, r, http.StatusNotFound, "INSTALLATION_NOT_LINKED", "github installation is not linked to this organization")
		return models.GitHubInstallationOrgLink{}, false
	}
	return link, true
}

func (h *IntegrationHandler) githubInstallationLink(ctx context.Context, orgID uuid.UUID, installationID int64) (models.GitHubInstallationOrgLink, bool) {
	if h.githubInstallations != nil {
		var (
			link models.GitHubInstallationOrgLink
			err  error
		)
		if installationID > 0 {
			link, err = h.githubInstallations.GetOrgLink(ctx, orgID, installationID)
		} else {
			link, err = h.githubInstallations.FirstOrgLink(ctx, orgID)
		}
		if err == nil {
			return link, true
		}
	}

	integrations, err := h.integrationStore.ListByOrgAndProvider(ctx, orgID, string(models.IntegrationProviderGitHub))
	if err != nil || len(integrations) == 0 {
		return models.GitHubInstallationOrgLink{}, false
	}
	for _, integration := range integrations {
		var cfg struct {
			InstallationID int64  `json:"installation_id"`
			AccountLogin   string `json:"account_login"`
		}
		if integration.Config == nil || json.Unmarshal(integration.Config, &cfg) != nil || cfg.InstallationID == 0 {
			continue
		}
		if installationID > 0 && cfg.InstallationID != installationID {
			continue
		}
		if !h.githubInstallationAllowsConfigFallback(ctx, cfg.InstallationID) {
			continue
		}
		if cfg.AccountLogin == "" {
			cfg.AccountLogin = "unknown"
		}
		return models.GitHubInstallationOrgLink{
			OrgID:          orgID,
			IntegrationID:  &integration.ID,
			InstallationID: cfg.InstallationID,
			AccountLogin:   cfg.AccountLogin,
			Status:         "active",
		}, true
	}

	if h.repoStore != nil {
		repoInstallationID, repoErr := h.repoStore.GetAnyInstallationIDByOrg(ctx, orgID)
		if repoErr == nil && repoInstallationID > 0 && (installationID == 0 || repoInstallationID == installationID) {
			accountLogin := "unknown"
			return models.GitHubInstallationOrgLink{
				OrgID:          orgID,
				IntegrationID:  &integrations[0].ID,
				InstallationID: repoInstallationID,
				AccountLogin:   accountLogin,
				Status:         "active",
			}, true
		}
	}

	return models.GitHubInstallationOrgLink{}, false
}

func (h *IntegrationHandler) githubInstallationAllowsConfigFallback(ctx context.Context, installationID int64) bool {
	if h.githubInstallations == nil || installationID == 0 {
		return true
	}
	installation, err := h.githubInstallations.GetByInstallationID(ctx, installationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	if err != nil {
		return false
	}
	return installation.Status == "" || installation.Status == "active"
}

func (h *IntegrationHandler) userIsAdminInOrg(ctx context.Context, userID, orgID uuid.UUID) bool {
	if h.memberships == nil {
		return false
	}
	membership, err := h.memberships.Get(ctx, userID, orgID)
	return err == nil && membership.Role == models.RoleAdmin
}

func (h *IntegrationHandler) githubUserCanAccessRepo(ctx context.Context, token, fullName string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL+"/repos/"+fullName, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := h.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound, http.StatusForbidden:
		return false, nil
	default:
		return false, fmt.Errorf("github API returned %d", resp.StatusCode)
	}
}

func defaultBranchOrMain(branch string) string {
	if branch == "" {
		return "main"
	}
	return branch
}

func defaultCloneURL(fullName, cloneURL string) string {
	if cloneURL != "" {
		return cloneURL
	}
	return "https://github.com/" + fullName + ".git"
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

type githubSetupStatePayload struct {
	Nonce     string    `json:"nonce"`
	UserID    uuid.UUID `json:"user_id"`
	OrgID     uuid.UUID `json:"org_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *IntegrationHandler) setGitHubSetupState(w http.ResponseWriter, r *http.Request) (string, error) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		return "", errors.New("authenticated user required for github setup")
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		return "", errors.New("active organization required for github setup")
	}
	nonce, err := generateRandomString(16)
	if err != nil {
		return "", err
	}
	payload := githubSetupStatePayload{
		Nonce:     nonce,
		UserID:    user.ID,
		OrgID:     orgID,
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}
	encoded, err := h.signGitHubSetupState(payload)
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     githubIntegrationOAuthStateCookie,
		Value:    encoded,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return encoded, nil
}

func (h *IntegrationHandler) validateGitHubSetupState(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(githubIntegrationOAuthStateCookie)
	if err != nil || cookie.Value == "" || state == "" || cookie.Value != state {
		return uuid.Nil, uuid.Nil, false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     githubIntegrationOAuthStateCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	payload, err := h.verifyGitHubSetupState(state)
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("invalid github setup state")
		return uuid.Nil, uuid.Nil, false
	}
	if time.Now().UTC().After(payload.ExpiresAt) {
		zerolog.Ctx(r.Context()).Warn().Msg("expired github setup state")
		return uuid.Nil, uuid.Nil, false
	}
	return payload.OrgID, payload.UserID, true
}

func (h *IntegrationHandler) signGitHubSetupState(payload githubSetupStatePayload) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	bodyEncoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(h.githubSetupSigningKey()))
	mac.Write([]byte(bodyEncoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return bodyEncoded + "." + sig, nil
}

func (h *IntegrationHandler) verifyGitHubSetupState(raw string) (githubSetupStatePayload, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return githubSetupStatePayload{}, errors.New("invalid state format")
	}
	mac := hmac.New(sha256.New, []byte(h.githubSetupSigningKey()))
	mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return githubSetupStatePayload{}, err
	}
	if !hmac.Equal(actual, expected) {
		return githubSetupStatePayload{}, errors.New("state signature mismatch")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return githubSetupStatePayload{}, err
	}
	var payload githubSetupStatePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return githubSetupStatePayload{}, err
	}
	return payload, nil
}

func (h *IntegrationHandler) githubSetupSigningKey() string {
	if h.setupSigningKey != "" {
		return h.setupSigningKey
	}
	return "development-github-setup-state"
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
		integration, err := h.convergeReusableRow(ctx, orgID, reusableIntegrations[0])
		if err != nil {
			return models.Integration{}, false, err
		}
		// Historical duplicates: rows created before ensureIntegration was
		// the only write path can leave an orphan errored row alongside
		// the canonical one. The integrations list endpoint surfaces
		// auth_error from any row (so a worker-flipped active→error row
		// keeps the Reconnect CTA visible), which means a stale errored
		// duplicate continues to render the banner even after a clean
		// reconnect against the canonical row. Converge them all so the
		// next /api/v1/integrations response can't pull auth_error from a
		// row we're not using. We only need the DB side effect here —
		// callers see the canonical row, not the duplicates — so the
		// returned converged value is discarded, but errors still
		// propagate so a partial cleanup surfaces as 500 and the user
		// retries instead of seeing a "successful" reconnect that left
		// the orphan banner in place.
		for i := 1; i < len(reusableIntegrations); i++ {
			if _, err := h.convergeReusableRow(ctx, orgID, reusableIntegrations[i]); err != nil {
				return models.Integration{}, false, err
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

// convergeReusableRow flips an errored reusable row back to active and
// strips any auth-error markers it carries, returning a new Integration
// reflecting the post-converge state. Idempotent: if the row is already
// active with no auth-error markers, no DB write fires and the returned
// value equals the input. DB errors are surfaced to the caller —
// swallowing them is what produced the stale-duplicate-row bug this
// helper exists to fix; if the UPDATE fails, the OAuth flow should fail
// loud (500) so the user retries instead of seeing a "successful"
// reconnect that left the DB in an inconsistent state.
func (h *IntegrationHandler) convergeReusableRow(ctx context.Context, orgID uuid.UUID, integration models.Integration) (models.Integration, error) {
	clearedConfig, configChanged := stripAuthErrorMarkers(integration.Config)
	statusErrored := integration.Status == models.IntegrationStatusError
	switch {
	case configChanged && statusErrored:
		if err := h.integrationStore.UpdateStatusAndConfig(ctx, orgID, integration.ID, string(models.IntegrationStatusActive), clearedConfig); err != nil {
			return integration, fmt.Errorf("converge integration %s: update status and config: %w", integration.ID, err)
		}
		integration.Config = clearedConfig
		integration.Status = models.IntegrationStatusActive
	case configChanged:
		if err := h.integrationStore.UpdateConfig(ctx, orgID, integration.ID, clearedConfig); err != nil {
			return integration, fmt.Errorf("converge integration %s: update config: %w", integration.ID, err)
		}
		integration.Config = clearedConfig
	case statusErrored:
		if err := h.integrationStore.UpdateStatus(ctx, orgID, integration.ID, string(models.IntegrationStatusActive)); err != nil {
			return integration, fmt.Errorf("converge integration %s: update status: %w", integration.ID, err)
		}
		integration.Status = models.IntegrationStatusActive
	}
	return integration, nil
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

// ──────────────────────────────────────────────────────────────────────────────
// CircleCI Integration
// ──────────────────────────────────────────────────────────────────────────────

// ConnectCircleCI accepts a CircleCI personal API token and project slug,
// validates them against /api/v2/me (cheapest authenticated call), stores the
// credential, and creates an active integration record. CircleCI uses a
// paste-the-token flow because the v2 Insights API isn't exposed through
// CircleCI's OAuth scopes.
func (h *IntegrationHandler) ConnectCircleCI(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var req struct {
		AuthToken   string `json:"auth_token"`
		ProjectSlug string `json:"project_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	req.AuthToken = strings.TrimSpace(req.AuthToken)
	req.ProjectSlug = strings.Trim(strings.TrimSpace(req.ProjectSlug), "/")
	if req.AuthToken == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_TOKEN", "auth_token is required")
		return
	}
	if req.ProjectSlug == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_PROJECT_SLUG", "project_slug is required (e.g. gh/org/repo)")
		return
	}
	if strings.Count(req.ProjectSlug, "/") != 2 {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROJECT_SLUG", "project_slug must look like gh/org/repo")
		return
	}

	if err := h.validateCircleCIToken(r.Context(), req.AuthToken); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN", "failed to validate CircleCI token: "+err.Error())
		return
	}

	cfg := models.CircleCIConfig{
		AuthToken:   req.AuthToken,
		ProjectSlug: req.ProjectSlug,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, cfg); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_SAVE_FAILED", "failed to save CircleCI credentials", err)
		return
	}

	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderCircleCI)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_CIRCLECI_FAILED", "failed to create CircleCI integration", err)
		return
	}

	if created {
		writeJSON(w, http.StatusCreated, models.SingleResponse[models.Integration]{Data: integration})
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Integration]{Data: integration})
}

func (h *IntegrationHandler) validateCircleCIToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://circleci.com/api/v2/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Circle-Token", token)
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("invalid or expired token")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("circleci API returned %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
