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

	"sync"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/externalidentity"
	ghapp "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/ingestion"
	linearservice "github.com/assembledhq/143/internal/services/linear"
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

var requiredSlackBotScopes = []string{
	"app_mentions:read",
	"channels:history",
	"channels:read",
	"chat:write",
	"commands",
	"files:read",
	"groups:history",
	"groups:read",
	"im:history",
	"im:read",
	"im:write",
	"users:read",
	"users:read.email",
}

type integrationCredentialStore interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
	Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error
	// Disable marks the singleton (label="") credential for a provider as
	// disabled so it stops being injected into sandboxes. Disconnect calls this
	// so revoking an integration in the UI actually revokes runtime access, not
	// just the integration row's status. Re-connecting re-activates it via Upsert.
	Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
}

// githubAppService provides GitHub App installation tokens for fetching repos.
type githubAppService interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
}

type githubOrgAutoJoinService interface {
	GetInstallationDetails(ctx context.Context, installationID int64) (ghapp.InstallationDetails, error)
	ListOrgMembers(ctx context.Context, installationID int64, orgLogin string) ([]ghapp.OrgMember, error)
}

type githubAppUserCredentialProvider interface {
	GetValidCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error)
}

type githubMembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
}

type slackUserInfoClient interface {
	FetchUserInfo(ctx context.Context, accessToken, userID string) (ingestion.SlackUser, error)
}

type slackAuthTestClient interface {
	AuthTest(ctx context.Context, accessToken string) (ingestion.SlackAuthInfo, error)
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
	// Organization is the Linear workspace this token belongs to.
	Organization linearOrganization `json:"organization"`
	// ID and Name describe the *agent user* Linear provisioned at install
	// time when the OAuth flow used actor=app. For a non-agent install
	// these fields describe the human installer and we ignore them.
	// Distinguishing the two cases happens upstream: HandleLinearOAuthCallback
	// only stores AppUserID when AgentScopesGranted is true.
	ID   string `json:"id"`
	Name string `json:"name"`
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
	AppID       string        `json:"app_id"`
	BotUserID   string        `json:"bot_user_id"`
	BotID       string        `json:"bot_id"`
	Enterprise  *struct {
		ID string `json:"id"`
	} `json:"enterprise,omitempty"`
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

	// untrustedURLClient fetches user-supplied URLs (e.g. a custom Mezmo
	// base_url). It blocks private/loopback/metadata addresses at dial time;
	// never use h.client for caller-controlled hosts. See ssrf_guard.go.
	untrustedURLClient *http.Client

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
	githubOrgAutoJoin   githubOrgAutoJoinService
	repoStore           *db.RepositoryStore
	githubInstallations *db.GitHubInstallationStore
	githubAppUserAuth   githubAppUserCredentialProvider
	memberships         githubMembershipStore
	setupSigningKey     string
	audit               *db.AuditEmitter

	// Slack OAuth
	slackClientID          string
	slackSecret            string
	slackInstallationStore *db.SlackInstallationStore
	slackBotSettingsStore  *db.SlackBotSettingsStore
	slackUserLinkStore     *db.SlackUserLinkStore
	slackChannelStore      *db.SlackChannelSettingsStore
	slackUserInfoClient    slackUserInfoClient
	slackAuthTestClient    slackAuthTestClient
	slackbotMetrics        *metrics.SlackbotMetrics
	webhookDeliveryStore   *db.WebhookDeliveryStore
	externalUserLinks      *db.ExternalUserLinkStore
	externalSuggestions    *db.ExternalUserLinkSuggestionStore

	// PM context auto-trigger (nil-safe: disabled if not configured)
	pmAutoTriggerJobs   pmAutoTriggerJobStore
	pmAutoTriggerDocs   pmAutoTriggerDocStore
	pmAutoTriggerLogger zerolog.Logger

	// Linear post-install hooks (nil-safe).
	linearJobStore                *db.JobStore
	githubRosterJobStore          *db.JobStore
	linearTeamKeyRefresher        func(ctx context.Context, orgID uuid.UUID) error
	linearTeamKeyCacheInvalidator func(orgID uuid.UUID)
	linearAgentBootstrapper       func(ctx context.Context, orgID uuid.UUID) error
}

// SetLinearJobStore wires a JobStore so the OAuth callback can enqueue an
// initial refresh_linear_team_keys job. Without this hook, the team-key
// allowlist stays empty until the next 24h cron, which means bare-identifier
// detection won't work right after install.
func (h *IntegrationHandler) SetLinearJobStore(jobs *db.JobStore) {
	h.linearJobStore = jobs
}

func (h *IntegrationHandler) SetGitHubRosterJobStore(jobs *db.JobStore) {
	h.githubRosterJobStore = jobs
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

// SetLinearAgentBootstrapper wires the hook that runs after a successful
// Linear OAuth callback that granted agent scopes. The hook flips the
// per-org agent toggle to enabled when the admin hasn't expressed an
// opinion yet and picks an org-default repo when the org has exactly one
// connected GitHub repo — both are convenience defaults that remove the
// "I re-authorized but nothing happens" cliff without overriding any
// explicit user choice. Nil-safe.
func (h *IntegrationHandler) SetLinearAgentBootstrapper(fn func(ctx context.Context, orgID uuid.UUID) error) {
	h.linearAgentBootstrapper = fn
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
		integrationStore:   integrationStore,
		credentialStore:    credentialStore,
		linearClientID:     linearClientID,
		linearSecret:       linearSecret,
		baseURL:            baseURL,
		frontendURL:        frontendURL,
		client:             http.DefaultClient,
		untrustedURLClient: newSSRFSafeHTTPClient(30 * time.Second),
		slackUserInfoClient: ingestion.NewSlackAPIClient(
			zerolog.Nop(),
		),
		slackAuthTestClient: ingestion.NewSlackAPIClient(zerolog.Nop()),
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
		if autoJoin, ok := svc.(githubOrgAutoJoinService); ok {
			h.githubOrgAutoJoin = autoJoin
		} else {
			zerolog.Ctx(context.Background()).Warn().Msg("github app service does not implement githubOrgAutoJoinService; org auto-join permission checks disabled — use WithGitHubOrgAutoJoinService to wire it explicitly")
		}
		h.repoStore = repoStore
	}
}

func WithGitHubOrgAutoJoinService(svc githubOrgAutoJoinService) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.githubOrgAutoJoin = svc
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

func (h *IntegrationHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
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

func WithSlackInstallationStore(store *db.SlackInstallationStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.slackInstallationStore = store
	}
}

func WithSlackBotSettingsStore(store *db.SlackBotSettingsStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.slackBotSettingsStore = store
	}
}

func WithSlackUserLinkStore(store *db.SlackUserLinkStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.slackUserLinkStore = store
	}
}

func WithExternalUserIdentityStores(links *db.ExternalUserLinkStore, suggestions *db.ExternalUserLinkSuggestionStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.externalUserLinks = links
		h.externalSuggestions = suggestions
	}
}

func WithSlackChannelSettingsStore(store *db.SlackChannelSettingsStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.slackChannelStore = store
	}
}

func WithSlackUserInfoClient(client slackUserInfoClient) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.slackUserInfoClient = client
	}
}

func WithSlackbotMetrics(metrics *metrics.SlackbotMetrics) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.slackbotMetrics = metrics
	}
}

func WithWebhookDeliveryStore(store *db.WebhookDeliveryStore) IntegrationHandlerOption {
	return func(h *IntegrationHandler) {
		h.webhookDeliveryStore = store
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

	h.deriveSafeCredentialMetadata(ctx, integration)

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

func (h *IntegrationHandler) deriveSafeCredentialMetadata(ctx context.Context, integration *models.Integration) {
	if h.credentialStore == nil || integration.Status != models.IntegrationStatusActive {
		return
	}

	switch integration.Provider {
	case models.IntegrationProviderNotion, models.IntegrationProviderCircleCI, models.IntegrationProviderMezmo:
	default:
		return
	}

	credential, err := h.credentialStore.Get(ctx, integration.OrgID, models.ProviderName(integration.Provider))
	if err != nil || credential == nil {
		return
	}

	switch cfg := credential.Config.(type) {
	case models.NotionConfig:
		if cfg.WorkspaceID != "" {
			integration.NotionWorkspaceID = integrationStringPtr(cfg.WorkspaceID)
		}
		if cfg.WorkspaceName != "" {
			integration.NotionWorkspaceName = integrationStringPtr(cfg.WorkspaceName)
		}
	case models.CircleCIConfig:
		if cfg.ProjectSlug != "" {
			integration.CircleCIProjectSlug = integrationStringPtr(cfg.ProjectSlug)
		}
	case models.MezmoConfig:
		if cfg.BaseURL != "" {
			integration.MezmoBaseURL = integrationStringPtr(cfg.BaseURL)
		}
	}
}

func integrationStringPtr(value string) *string {
	return &value
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

	activeIntegrations, err := h.integrationStore.ListByOrgAndProvider(r.Context(), orgID, provider)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to look up integration", err)
		return
	}
	if len(activeIntegrations) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	for _, integration := range activeIntegrations {
		if err := h.integrationStore.UpdateStatus(r.Context(), orgID, integration.ID, models.IntegrationStatusInactive); err != nil {
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

	// Disable the stored org credential so sandboxes stop receiving this
	// provider's secrets. The sandbox env-injection path reads org_credentials
	// (status != 'disabled') independently of integrations.status, so without
	// this a "disconnected" provider would keep being injected and remain
	// usable by agents. Reconnecting re-activates the row via Upsert. No-op for
	// providers whose credentials don't live in org_credentials (e.g. GitHub).
	if h.credentialStore != nil {
		if err := h.credentialStore.Disable(r.Context(), orgID, models.ProviderName(provider)); err != nil {
			writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to disable integration credential", err)
			return
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

	// Single OAuth flow that grants the legacy read/write scopes used by
	// the existing session-linking write-back plus the agent scopes
	// (app:assignable, app:mentionable) used by the inbound agent feature.
	// Linear treats scopes additively, so this flow is a strict superset
	// of the previous one — orgs that only ever use the outbound
	// integration pay nothing for the agent scopes being granted.
	//
	// Refresh tokens are issued automatically by Linear without any
	// special scope. PR #807 attempted offline_access but Linear rejected
	// it as "Invalid scope"; PR #816 dropped it and confirmed refresh
	// works without it.
	//
	// actor=app provisions a dedicated agent user (`@143`) in the workspace
	// and ties subsequent token-authored writes to it. Workspace-admin
	// privileges are required by Linear; the install copy in the frontend
	// must reflect that. See docs/design/implemented/69-linear-agent.md
	// §"OAuth flow — single upgraded install" for the rationale on a single
	// flow vs parallel flows.
	params := url.Values{
		"client_id":     {h.linearClientID},
		"redirect_uri":  {h.linearRedirectURL()},
		"response_type": {"code"},
		"scope":         {strings.Join(models.LinearAgentRequiredScopes, ",")},
		"state":         {state},
		"actor":         {"app"},
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

	viewer, err := h.fetchLinearViewer(r.Context(), token.AccessToken)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LINEAR_API_FAILED", "failed to fetch linear workspace", err)
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
		return
	}

	// Build the credential. AppUser* fields are recorded only when the
	// returned token actually carries the agent scopes — installing through
	// a third-party OAuth tool that strips actor=app will give us a legacy
	// scope set even though the GraphQL viewer call returned *something*.
	// Storing AppUserID in that case would lie to downstream code that
	// uses "AppUserID != \"\"" as the "agent installed" predicate.
	linearConfig := models.LinearConfig{
		AccessToken:   token.AccessToken,
		RefreshToken:  token.RefreshToken,
		TokenType:     token.TokenType,
		Scope:         token.Scope,
		WorkspaceID:   viewer.WorkspaceID,
		WorkspaceName: viewer.WorkspaceName,
	}
	if linearConfig.HasAgentScopes() {
		linearConfig.AppUserID = viewer.AppUserID
		linearConfig.AppUserName = viewer.AppUserName
		linearConfig.AgentScopesGranted = true
	}
	if token.ExpiresIn > 0 {
		linearConfig.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	mergedConfig, err := h.preserveExistingWebhookSecret(r.Context(), orgID, linearConfig)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to preserve linear webhook secret", err)
		return
	}
	linearConfig = mergedConfig.(models.LinearConfig)
	if err := h.credentialStore.Upsert(r.Context(), orgID, linearConfig); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store linear credential", err)
		return
	}

	integration, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderLinear)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to connect linear integration", err)
		return
	}

	// Persist the Linear workspace id on integrations.config so the
	// multi-tenant webhook handler can resolve org-by-workspace at request
	// time. A single Linear OAuth app has one webhook URL across every
	// workspace it's installed in; the payload's `organizationId` field is
	// the only org-identifying signal available pre-HMAC. This is a
	// Required write: if it fails, the shared Linear webhook URL would keep
	// routing to an old org (or to no org), so the OAuth callback must not
	// report a successful connection.
	if viewer.WorkspaceID != "" {
		if err := h.integrationStore.SetLinearWorkspaceID(r.Context(), orgID, integration.ID, viewer.WorkspaceID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to persist linear workspace routing key", err)
			return
		}
	}

	logger.Info().
		Str("org_id", orgID.String()).
		Str("workspace_id", viewer.WorkspaceID).
		Msg("linear oauth callback completed; credentials stored and integration active")

	// Auto-enable the agent and pick an org-default repo when the install
	// just acquired agent scopes. Best-effort: failures here are logged but
	// don't break the OAuth flow — the admin can always toggle/configure
	// manually from Settings → Integrations → Linear → Agent.
	if linearConfig.HasAgentScopes() && h.linearAgentBootstrapper != nil {
		if err := h.linearAgentBootstrapper(r.Context(), orgID); err != nil {
			logger.Warn().Err(err).
				Str("org_id", orgID.String()).
				Msg("linear agent bootstrap (enable + default repo) failed; admin can configure manually")
		}
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
	if h.linearTeamKeyRefresher != nil {
		// context.WithoutCancel: a cancelled request context (the user
		// closing the browser tab post-redirect) must not abort the
		// refresh. The 3s cap here is just defense against a wedged
		// Linear API hold of an internal goroutine.
		bgCtx, bgCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
		go func() {
			defer bgCancel()
			if err := h.refreshLinearTeamKeysAfterInstall(bgCtx, orgID); err != nil {
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

const linearTeamKeyInstallRetryDelay = 100 * time.Millisecond

func (h *IntegrationHandler) refreshLinearTeamKeysAfterInstall(ctx context.Context, orgID uuid.UUID) error {
	if h.linearTeamKeyRefresher == nil {
		return nil
	}
	err := h.linearTeamKeyRefresher(ctx, orgID)
	if err == nil || !isTransientLinearIntegrationLookupMiss(err) {
		return err
	}

	timer := time.NewTimer(linearTeamKeyInstallRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	return h.linearTeamKeyRefresher(ctx, orgID)
}

func isTransientLinearIntegrationLookupMiss(err error) bool {
	return errors.Is(err, linearservice.ErrIntegrationNotFound) ||
		(err != nil && strings.Contains(err.Error(), "linear integration not found"))
}

func (h *IntegrationHandler) preserveExistingWebhookSecret(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) (models.ProviderConfig, error) {
	if h.credentialStore == nil {
		return cfg, nil
	}

	switch next := cfg.(type) {
	case models.LinearConfig:
		if next.WebhookSecret != "" {
			return cfg, nil
		}
		existing, err := h.credentialStore.Get(ctx, orgID, models.ProviderLinear)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return cfg, nil
			}
			return nil, err
		}
		if prior, ok := existing.Config.(models.LinearConfig); ok && prior.WebhookSecret != "" {
			next.WebhookSecret = prior.WebhookSecret
		}
		return next, nil
	case models.SentryConfig:
		if next.WebhookSecret != "" {
			return cfg, nil
		}
		existing, err := h.credentialStore.Get(ctx, orgID, models.ProviderSentry)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return cfg, nil
			}
			return nil, err
		}
		if prior, ok := existing.Config.(models.SentryConfig); ok && prior.WebhookSecret != "" {
			next.WebhookSecret = prior.WebhookSecret
		}
		return next, nil
	default:
		return cfg, nil
	}
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
	mergedConfig, err := h.preserveExistingWebhookSecret(r.Context(), orgID, sentryConfig)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to preserve sentry webhook secret", err)
		return
	}
	sentryConfig = mergedConfig.(models.SentryConfig)
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
		} else {
			h.tryEnableGitHubOrgAutoJoin(ctx, orgID, userID, installationID, accountLogin)
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

type githubOrgAutoJoinResponse struct {
	GitHubOrgs []githubOrgAutoJoinRow `json:"github_orgs"`
}

type githubOrgAutoJoinRow struct {
	InstallationID     int64      `json:"installation_id"`
	AccountLogin       string     `json:"account_login"`
	AccountType        *string    `json:"account_type,omitempty"`
	AutoJoinEnabled    bool       `json:"auto_join_enabled"`
	MembersPermission  string     `json:"members_permission"`
	RosterSyncedAt     *time.Time `json:"roster_synced_at,omitempty"`
	CapturedByOtherOrg bool       `json:"captured_by_other_org"`
	SettingsURL        string     `json:"settings_url,omitempty"`
}

func (h *IntegrationHandler) ListGitHubOrgAutoJoin(w http.ResponseWriter, r *http.Request) {
	if h.githubInstallations == nil {
		writeJSON(w, http.StatusOK, githubOrgAutoJoinResponse{GitHubOrgs: []githubOrgAutoJoinRow{}})
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	summaries, err := h.githubInstallations.ListOrgAutoJoinSummaries(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "GITHUB_ORGS_LOOKUP_FAILED", "failed to list github organizations", err)
		return
	}
	// Fetch installation permission state concurrently — one GitHub API call per
	// installation would otherwise serialize and stall the page load.
	permissions := make([]string, len(summaries))
	for i := range permissions {
		permissions[i] = "missing"
	}
	if h.githubOrgAutoJoin != nil {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i, summary := range summaries {
			wg.Add(1)
			go func(idx int, installationID int64) {
				defer wg.Done()
				if details, err := h.githubOrgAutoJoin.GetInstallationDetails(r.Context(), installationID); err == nil && details.Permissions.Members == "read" {
					mu.Lock()
					permissions[idx] = "granted"
					mu.Unlock()
				}
			}(i, summary.InstallationID)
		}
		wg.Wait()
	}
	rows := make([]githubOrgAutoJoinRow, 0, len(summaries))
	for i, summary := range summaries {
		rows = append(rows, githubOrgAutoJoinRow{
			InstallationID:     summary.InstallationID,
			AccountLogin:       summary.AccountLogin,
			AccountType:        summary.AccountType,
			AutoJoinEnabled:    summary.AutoJoinEnabled,
			MembersPermission:  permissions[i],
			RosterSyncedAt:     summary.RosterSyncedAt,
			CapturedByOtherOrg: summary.CapturedByOtherOrg,
			SettingsURL:        githubInstallationSettingsURL(summary.AccountLogin, summary.InstallationID),
		})
	}
	writeJSON(w, http.StatusOK, githubOrgAutoJoinResponse{GitHubOrgs: rows})
}

func (h *IntegrationHandler) UpdateGitHubOrgAutoJoin(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	installationID, ok := parseInstallationIDParam(w, r)
	if !ok {
		return
	}
	var body struct {
		AutoJoinEnabled bool `json:"auto_join_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	user := middleware.UserFromContext(r.Context())
	actorID := uuid.Nil
	if user != nil {
		actorID = user.ID
	}
	if body.AutoJoinEnabled {
		result := h.tryEnableGitHubOrgAutoJoin(r.Context(), orgID, actorID, installationID, "")
		if result.err != nil {
			writeGitHubOrgAutoJoinError(w, r, result.err, result.settingsURL)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result.link})
		return
	}
	link, err := h.githubInstallations.SetOrgLinkAutoJoin(r.Context(), orgID, installationID, false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "GITHUB_ORG_NOT_FOUND", "github organization link not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GITHUB_ORG_AUTO_JOIN_UPDATE_FAILED", "failed to update github organization auto-join", err)
		return
	}
	if err := h.githubInstallations.ClearRosterForInstallation(r.Context(), installationID); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Int64("installation_id", installationID).Msg("failed to clear github org roster after disabling auto-join")
	}
	h.emitGitHubOrgAutoJoinAudit(r.Context(), orgID, actorID, models.AuditActionTeamGitHubOrgAutoJoinDisabled, link.AccountLogin, installationID, "admin_disabled")
	writeJSON(w, http.StatusOK, map[string]any{"data": link})
}

type githubOrgAutoJoinEnableResult struct {
	link        models.GitHubInstallationOrgLink
	settingsURL string
	err         error
}

var (
	errGitHubOrgMembersPermissionMissing = errors.New("github members permission missing")
	errGitHubAutoJoinNotOrganization     = errors.New("github installation is not an organization")
)

func (h *IntegrationHandler) tryEnableGitHubOrgAutoJoin(ctx context.Context, orgID, actorID uuid.UUID, installationID int64, accountLogin string) githubOrgAutoJoinEnableResult {
	result := githubOrgAutoJoinEnableResult{}
	if h.githubInstallations == nil || h.githubOrgAutoJoin == nil {
		result.err = errors.New("github org auto-join dependencies not configured")
		return result
	}
	link, err := h.githubInstallations.GetOrgLink(ctx, orgID, installationID)
	if err != nil {
		result.err = err
		return result
	}
	inst, err := h.githubInstallations.GetByInstallationID(ctx, installationID)
	if err != nil {
		result.err = err
		return result
	}
	if accountLogin == "" {
		accountLogin = link.AccountLogin
	}
	result.settingsURL = githubInstallationSettingsURL(accountLogin, installationID)
	details, err := h.githubOrgAutoJoin.GetInstallationDetails(ctx, installationID)
	if err != nil {
		result.err = err
		return result
	}
	accountType := ""
	if inst.AccountType != nil {
		accountType = *inst.AccountType
	}
	if accountType == "" {
		accountType = details.Account.Type
	}
	if accountType != "Organization" {
		result.err = errGitHubAutoJoinNotOrganization
		return result
	}
	if accountLogin == "" || accountLogin == "unknown" {
		accountLogin = details.Account.Login
	}
	result.settingsURL = githubInstallationSettingsURL(accountLogin, installationID)
	if details.Permissions.Members != "read" {
		result.err = errGitHubOrgMembersPermissionMissing
		return result
	}
	enabled, err := h.githubInstallations.SetOrgLinkAutoJoin(ctx, orgID, installationID, true)
	if err != nil {
		result.err = err
		return result
	}
	result.link = enabled
	h.emitGitHubOrgAutoJoinAudit(ctx, orgID, actorID, models.AuditActionTeamGitHubOrgAutoJoinEnabled, enabled.AccountLogin, installationID, "admin_enabled")
	if err := h.enqueueGitHubOrgRosterSync(ctx, orgID, installationID, enabled.AccountLogin); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Int64("installation_id", installationID).Msg("failed to enqueue github org roster sync after enabling auto-join")
	}
	return result
}

func (h *IntegrationHandler) enqueueGitHubOrgRosterSync(ctx context.Context, orgID uuid.UUID, installationID int64, accountLogin string) error {
	if h.githubRosterJobStore == nil {
		return fmt.Errorf("github roster job store not configured")
	}
	dedupe := fmt.Sprintf("sync_github_org_roster:%d", installationID)
	_, err := h.githubRosterJobStore.Enqueue(ctx, orgID, "github", models.JobTypeSyncGitHubOrgRoster, map[string]any{
		"org_id":          orgID.String(),
		"installation_id": installationID,
		"account_login":   accountLogin,
	}, 5, &dedupe)
	return err
}

func parseInstallationIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "installation_id")
	installationID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || installationID <= 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_INSTALLATION_ID", "invalid github installation id")
		return 0, false
	}
	return installationID, true
}

func writeGitHubOrgAutoJoinError(w http.ResponseWriter, r *http.Request, err error, settingsURL string) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, http.StatusNotFound, "GITHUB_ORG_NOT_FOUND", "github organization link not found")
	case errors.Is(err, db.ErrGitHubOrgAlreadyCaptured):
		writeError(w, r, http.StatusConflict, "GITHUB_ORG_ALREADY_CAPTURED", "this GitHub organization is already connected to another workspace")
	case errors.Is(err, errGitHubOrgMembersPermissionMissing):
		zerolog.Ctx(r.Context()).Info().Str("settings_url", settingsURL).Msg("github members permission missing")
		writeJSON(w, http.StatusPreconditionFailed, models.ErrorResponse{
			Error: models.ErrorDetail{
				Code:    "MEMBERS_PERMISSION_MISSING",
				Message: "an owner must approve updated GitHub App permissions",
				Details: map[string]any{"settings_url": settingsURL},
			},
		})
	case errors.Is(err, errGitHubAutoJoinNotOrganization):
		writeError(w, r, http.StatusUnprocessableEntity, "NOT_AN_ORGANIZATION", "auto-join requires a GitHub organization installation")
	default:
		writeError(w, r, http.StatusInternalServerError, "GITHUB_ORG_AUTO_JOIN_UPDATE_FAILED", "failed to update github organization auto-join", err)
	}
}

func githubInstallationSettingsURL(accountLogin string, installationID int64) string {
	if accountLogin == "" || accountLogin == "unknown" {
		return ""
	}
	return fmt.Sprintf("https://github.com/organizations/%s/settings/installations/%d", url.PathEscape(accountLogin), installationID)
}

func (h *IntegrationHandler) emitGitHubOrgAutoJoinAudit(ctx context.Context, orgID, actorID uuid.UUID, action models.AuditAction, accountLogin string, installationID int64, reason string) {
	resourceID := fmt.Sprintf("%d", installationID)
	details := marshalAuditDetails(zerolog.Ctx(ctx).With().Logger(), map[string]any{
		"account_login":   accountLogin,
		"installation_id": installationID,
		"reason":          reason,
	})
	if h.audit != nil {
		if actorID != uuid.Nil {
			h.audit.EmitUserAction(ctx, db.UserActionParams{
				OrgID:        orgID,
				UserID:       actorID,
				Action:       action,
				ResourceType: models.AuditResourceIntegration,
				ResourceID:   &resourceID,
				Details:      details,
			})
		} else {
			h.audit.EmitSystemAction(ctx, db.SystemActionParams{
				OrgID:        orgID,
				ActorID:      "github_org_auto_join",
				Action:       action,
				ResourceType: models.AuditResourceIntegration,
				ResourceID:   &resourceID,
				Details:      details,
			})
		}
	}
	zerolog.Ctx(ctx).Info().
		Str("org_id", orgID.String()).
		Str("actor_user_id", actorID.String()).
		Str("action", string(action)).
		Str("account_login", accountLogin).
		Int64("installation_id", installationID).
		Str("reason", reason).
		Msg("github org auto-join policy changed")
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

	integrations, err := h.integrationStore.ListByOrgAndProvider(ctx, orgID, models.IntegrationProviderGitHub)
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
		writeError(w, r, http.StatusBadRequest, "GITHUB_USER_AUTH_REQUIRED", "Connect your GitHub account before claiming repositories")
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
		writeError(w, r, http.StatusBadRequest, "GITHUB_USER_AUTH_REQUIRED", "Connect your GitHub account before claiming repositories")
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
			Status:         models.RepositoryStatusActive,
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

	integrations, err := h.integrationStore.ListByOrgAndProvider(ctx, orgID, models.IntegrationProviderGitHub)
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
		"scope":        {strings.Join(requiredSlackBotScopes, ",")},
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
		BotUserID:   token.BotUserID,
		BotID:       token.BotID,
		Scope:       token.Scope,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, slackConfig); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store slack credential", err)
		return
	}

	integration, _, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderSlack)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_SLACK_FAILED", "failed to connect slack integration", err)
		return
	}
	if h.slackInstallationStore != nil {
		var enterpriseID *string
		if token.Enterprise != nil && token.Enterprise.ID != "" {
			enterpriseID = &token.Enterprise.ID
		}
		var installedBy *uuid.UUID
		if user := middleware.UserFromContext(r.Context()); user != nil {
			installedBy = &user.ID
		}
		installation := &models.SlackInstallation{
			OrgID:             orgID,
			IntegrationID:     integration.ID,
			TeamID:            token.Team.ID,
			TeamName:          token.Team.Name,
			EnterpriseID:      enterpriseID,
			APIAppID:          token.AppID,
			BotUserID:         token.BotUserID,
			BotID:             token.BotID,
			Scope:             strings.FieldsFunc(token.Scope, func(r rune) bool { return r == ',' || r == ' ' }),
			Status:            models.SlackInstallationStatusActive,
			InstalledByUserID: installedBy,
		}
		if err := h.slackInstallationStore.Upsert(r.Context(), installation); err != nil {
			writeError(w, r, http.StatusInternalServerError, "CONNECT_SLACK_FAILED", "failed to store slack bot installation", err)
			return
		}
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
		slackAPIURL+"/conversations.list?types=public_channel,private_channel&exclude_archived=true&limit=200", nil)
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
			ID        string `json:"id"`
			Name      string `json:"name"`
			IsChannel bool   `json:"is_channel"`
			IsGroup   bool   `json:"is_group"`
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
		ID                string                                `json:"id"`
		Name              string                                `json:"name"`
		Type              string                                `json:"type"`
		Selected          bool                                  `json:"selected"`
		MonitoringEnabled bool                                  `json:"monitoring_enabled"`
		BotConfigured     bool                                  `json:"bot_configured"`
		Settings          *models.SlackChannelSettings          `json:"settings,omitempty"`
		EffectiveSettings *models.EffectiveSlackChannelSettings `json:"effective_settings,omitempty"`
	}
	channels := make([]channelEntry, 0, len(slackResp.Channels))
	var installation *models.SlackInstallation
	if h.slackInstallationStore != nil {
		if active, lookupErr := h.slackInstallationStore.GetActiveByOrg(r.Context(), orgID); lookupErr == nil {
			installation = &active
		}
	}
	for _, ch := range slackResp.Channels {
		selected := false
		for _, id := range cred.ChannelIDs {
			if id == ch.ID {
				selected = true
				break
			}
		}
		channelType := "channel"
		if ch.IsGroup {
			channelType = "private_channel"
		}
		entry := channelEntry{
			ID:                ch.ID,
			Name:              ch.Name,
			Type:              channelType,
			Selected:          selected,
			MonitoringEnabled: selected,
		}
		if installation != nil && h.slackChannelStore != nil {
			settings, settingsErr := h.slackChannelStore.GetByChannel(r.Context(), orgID, installation.TeamID, ch.ID)
			if settingsErr == nil {
				entry.Settings = &settings
				entry.BotConfigured = true
			} else if !errors.Is(settingsErr, pgx.ErrNoRows) {
				writeError(w, r, http.StatusInternalServerError, "SLACK_CHANNEL_SETTINGS_FAILED", "failed to load slack channel settings", settingsErr)
				return
			}
			effective, effectiveErr := h.slackChannelStore.GetEffectiveByChannel(r.Context(), orgID, installation.TeamID, ch.ID)
			if effectiveErr == nil {
				entry.EffectiveSettings = &effective
				entry.BotConfigured = entry.BotConfigured || effective.HasChannelOverride
			} else if !errors.Is(effectiveErr, pgx.ErrNoRows) {
				writeError(w, r, http.StatusInternalServerError, "SLACK_CHANNEL_SETTINGS_FAILED", "failed to load effective slack channel settings", effectiveErr)
				return
			}
		}
		channels = append(channels, entry)
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

func (h *IntegrationHandler) GetSlackBot(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if h.slackInstallationStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}
	installation, err := h.slackInstallationStore.GetActiveByOrg(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "SLACK_NOT_CONNECTED", "slack bot is not connected")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "SLACK_LOOKUP_FAILED", "failed to load slack bot installation", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SlackInstallation]{Data: installation})
}

func (h *IntegrationHandler) GetSlackHealth(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if h.slackInstallationStore == nil || h.credentialStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}
	installation, err := h.slackInstallationStore.GetActiveByOrg(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "SLACK_NOT_CONNECTED", "slack bot is not connected")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "SLACK_LOOKUP_FAILED", "failed to load slack bot installation", err)
		return
	}
	health := models.SlackInstallationHealth{
		Installation:   installation,
		RequiredScopes: append([]string(nil), requiredSlackBotScopes...),
		MissingScopes:  missingSlackScopes(installation.Scope, requiredSlackBotScopes),
		LastEventAt:    installation.LastEventAt,
		AuthOK:         true,
	}
	if installation.LastEventAt == nil {
		health.Symptoms = append(health.Symptoms, "no_events_observed_check_event_subscriptions_and_signing_secret")
	}
	if h.webhookDeliveryStore != nil {
		failures, lookupErr := h.webhookDeliveryStore.ListRecentFailures(r.Context(), orgID, "slack", time.Now().UTC().Add(-24*time.Hour), 5)
		if lookupErr != nil {
			writeError(w, r, http.StatusInternalServerError, "SLACK_HEALTH_FAILED", "failed to load slack callback health", lookupErr)
			return
		}
		for _, failure := range failures {
			health.RecentCallbackFailures = append(health.RecentCallbackFailures, models.SlackCallbackFailure{
				ID:         failure.ID,
				DeliveryID: failure.DeliveryID,
				EventType:  failure.EventType,
				Error:      failure.Error,
				ReceivedAt: failure.ReceivedAt,
			})
		}
		if len(health.RecentCallbackFailures) > 0 {
			health.Symptoms = append(health.Symptoms, "recent_callback_failures")
		}
	}
	if h.integrationStore != nil {
		if integration, lookupErr := h.integrationStore.GetByOrgAndProvider(r.Context(), orgID, models.IntegrationProviderSlack); lookupErr == nil {
			if authErr := readAuthErrorFromConfig(integration.Config); authErr != nil {
				health.AuthOK = false
				health.AuthError = authErr
				health.LastAuthCheckAt = &authErr.At
			}
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "SLACK_HEALTH_FAILED", "failed to load slack integration health", lookupErr)
			return
		}
	}
	cred, credErr := h.getSlackCredential(r.Context(), orgID)
	if credErr != nil || cred.AccessToken == "" {
		health.AuthOK = false
		reason := "missing_slack_credential"
		if credErr != nil {
			reason = "slack_credential_unavailable"
		}
		authErr := models.IntegrationAuthError{Reason: reason, At: time.Now().UTC()}
		health.AuthError = &authErr
		health.LastAuthCheckAt = &authErr.At
	} else if h.slackAuthTestClient != nil {
		checkedAt := time.Now().UTC()
		health.LastAuthCheckAt = &checkedAt
		if _, authErr := h.slackAuthTestClient.AuthTest(r.Context(), cred.AccessToken); authErr != nil {
			health.AuthOK = false
			errValue := models.IntegrationAuthError{Reason: "slack_auth_failed", At: checkedAt}
			health.AuthError = &errValue
		}
	}
	if len(health.MissingScopes) > 0 {
		health.AuthOK = false
		for _, scope := range health.MissingScopes {
			h.slackbotMetrics.RecordMissingScope(r.Context(), scope)
		}
	}
	if health.AuthOK && len(health.RecentCallbackFailures) == 0 {
		h.slackbotMetrics.RecordInstallHealth(r.Context(), "ok")
	} else {
		h.slackbotMetrics.RecordInstallHealth(r.Context(), "unhealthy")
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SlackInstallationHealth]{Data: health})
}

func missingSlackScopes(actual []string, required []string) []string {
	seen := make(map[string]bool, len(actual))
	for _, scope := range actual {
		seen[strings.TrimSpace(scope)] = true
	}
	missing := make([]string, 0)
	for _, scope := range required {
		if !seen[scope] {
			missing = append(missing, scope)
		}
	}
	return missing
}

func (h *IntegrationHandler) ReinstallSlackBot(w http.ResponseWriter, r *http.Request) {
	h.StartSlackOAuth(w, r)
}

func (h *IntegrationHandler) GetSlackSettings(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if h.slackInstallationStore == nil || h.slackBotSettingsStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}
	installation, err := h.slackInstallationStore.GetActiveByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SLACK_NOT_CONNECTED", "slack bot is not connected", err)
		return
	}
	settings, err := h.slackBotSettingsStore.GetByOrg(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			settings = defaultSlackBotSettings(orgID, installation.ID)
			writeJSON(w, http.StatusOK, models.SingleResponse[models.SlackBotSettings]{Data: settings})
			return
		}
		writeError(w, r, http.StatusInternalServerError, "SLACK_SETTINGS_FAILED", "failed to load slack settings", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SlackBotSettings]{Data: settings})
}

func (h *IntegrationHandler) PatchSlackSettings(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if h.slackInstallationStore == nil || h.slackBotSettingsStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}
	var body slackSettingsPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	installation, err := h.slackInstallationStore.GetActiveByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SLACK_NOT_CONNECTED", "slack bot is not connected", err)
		return
	}
	if body.DefaultRepositoryID != nil {
		if h.repoStore == nil {
			writeError(w, r, http.StatusServiceUnavailable, "REPOSITORY_STORE_NOT_CONFIGURED", "repository validation is not configured")
			return
		}
		if _, err := h.repoStore.GetByID(r.Context(), orgID, *body.DefaultRepositoryID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY", "default_repository_id must belong to the current org")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOOKUP_FAILED", "failed to validate default repository", err)
			return
		}
	}
	settings, err := h.slackBotSettingsStore.GetByOrg(r.Context(), orgID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "SLACK_SETTINGS_LOOKUP_FAILED", "failed to load slack settings", err)
			return
		}
		settings = defaultSlackBotSettings(orgID, installation.ID)
	}
	settings.OrgID = orgID
	settings.SlackInstallationID = installation.ID
	settings.Active = true
	if err := body.Apply(&settings); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SLACK_SETTINGS", err.Error())
		return
	}
	if err := h.slackBotSettingsStore.Upsert(r.Context(), &settings); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_SETTINGS_UPDATE_FAILED", "failed to update slack settings", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SlackBotSettings]{Data: settings})
}

func defaultSlackBotSettings(orgID, installationID uuid.UUID) models.SlackBotSettings {
	return models.SlackBotSettings{
		OrgID:                     orgID,
		SlackInstallationID:       installationID,
		RoutingMode:               models.SlackRoutingModeAuto,
		ResponseVisibility:        models.SlackResponseVisibilityThread,
		AllowedActions:            []string{string(models.SlackChannelActionSession), string(models.SlackChannelActionPreview)},
		NotificationPreset:        models.SlackNotificationPresetBalanced,
		NotificationSubscriptions: json.RawMessage(`{}`),
		Active:                    true,
	}
}

func validateSlackBotSettings(settings models.SlackBotSettings) error {
	if err := settings.RoutingMode.Validate(); err != nil {
		return err
	}
	if err := settings.ResponseVisibility.Validate(); err != nil {
		return err
	}
	if err := settings.NotificationPreset.Validate(); err != nil {
		return err
	}
	for _, action := range settings.AllowedActions {
		if err := models.SlackChannelAction(action).Validate(); err != nil {
			return err
		}
	}
	return nil
}

type slackSettingsPatchRequest struct {
	DefaultRepositoryID       *uuid.UUID      `json:"default_repository_id"`
	DefaultBranch             *string         `json:"default_branch"`
	RoutingMode               string          `json:"routing_mode"`
	ResponseVisibility        string          `json:"response_visibility"`
	AllowedActions            []string        `json:"allowed_actions"`
	NotificationPreset        string          `json:"notification_preset"`
	NotificationSubscriptions json.RawMessage `json:"notification_subscriptions"`

	defaultRepositoryIDSet       bool
	defaultBranchSet             bool
	routingModeSet               bool
	responseVisibilitySet        bool
	allowedActionsSet            bool
	notificationPresetSet        bool
	notificationSubscriptionsSet bool
}

func (r *slackSettingsPatchRequest) UnmarshalJSON(data []byte) error {
	type slackSettingsPatchRequestJSON struct {
		DefaultRepositoryID       *uuid.UUID      `json:"default_repository_id"`
		DefaultBranch             *string         `json:"default_branch"`
		RoutingMode               string          `json:"routing_mode"`
		ResponseVisibility        string          `json:"response_visibility"`
		AllowedActions            []string        `json:"allowed_actions"`
		NotificationPreset        string          `json:"notification_preset"`
		NotificationSubscriptions json.RawMessage `json:"notification_subscriptions"`
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var decoded slackSettingsPatchRequestJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	r.DefaultRepositoryID = decoded.DefaultRepositoryID
	r.DefaultBranch = decoded.DefaultBranch
	r.RoutingMode = decoded.RoutingMode
	r.ResponseVisibility = decoded.ResponseVisibility
	r.AllowedActions = decoded.AllowedActions
	r.NotificationPreset = decoded.NotificationPreset
	r.NotificationSubscriptions = decoded.NotificationSubscriptions
	_, r.defaultRepositoryIDSet = raw["default_repository_id"]
	_, r.defaultBranchSet = raw["default_branch"]
	_, r.routingModeSet = raw["routing_mode"]
	_, r.responseVisibilitySet = raw["response_visibility"]
	_, r.allowedActionsSet = raw["allowed_actions"]
	_, r.notificationPresetSet = raw["notification_preset"]
	_, r.notificationSubscriptionsSet = raw["notification_subscriptions"]
	return nil
}

func (r slackSettingsPatchRequest) Apply(settings *models.SlackBotSettings) error {
	if r.defaultRepositoryIDSet || r.DefaultRepositoryID != nil {
		settings.DefaultRepositoryID = r.DefaultRepositoryID
	}
	if r.defaultBranchSet || r.DefaultBranch != nil {
		settings.DefaultBranch = r.DefaultBranch
	}
	if (r.routingModeSet || strings.TrimSpace(r.RoutingMode) != "") && strings.TrimSpace(r.RoutingMode) != "" {
		settings.RoutingMode = models.SlackRoutingMode(strings.TrimSpace(r.RoutingMode))
	}
	if (r.responseVisibilitySet || strings.TrimSpace(r.ResponseVisibility) != "") && strings.TrimSpace(r.ResponseVisibility) != "" {
		settings.ResponseVisibility = models.SlackResponseVisibility(strings.TrimSpace(r.ResponseVisibility))
	}
	if r.allowedActionsSet || len(r.AllowedActions) > 0 {
		settings.AllowedActions = r.AllowedActions
	}
	if (r.notificationPresetSet || strings.TrimSpace(r.NotificationPreset) != "") && strings.TrimSpace(r.NotificationPreset) != "" {
		settings.NotificationPreset = models.SlackNotificationPreset(strings.TrimSpace(r.NotificationPreset))
	}
	if r.notificationSubscriptionsSet || len(r.NotificationSubscriptions) > 0 {
		settings.NotificationSubscriptions = r.NotificationSubscriptions
	}
	return validateSlackBotSettings(*settings)
}

func (h *IntegrationHandler) ListSlackUserLinks(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if h.slackUserLinkStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slack user links are not configured")
		return
	}
	links, err := h.slackUserLinkStore.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_USER_LINKS_FAILED", "failed to list slack user links", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.SlackUserLink]{Data: links, Meta: models.PaginationMeta{}})
}

func (h *IntegrationHandler) UpsertSlackUserLinkAdmin(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if h.slackInstallationStore == nil || h.slackUserLinkStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slack user links are not configured")
		return
	}
	if h.memberships == nil {
		writeError(w, r, http.StatusServiceUnavailable, "MEMBERSHIP_STORE_NOT_CONFIGURED", "membership validation is not configured")
		return
	}
	var body slackUserLinkAdminUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if err := body.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SLACK_USER_LINK", err.Error())
		return
	}
	if _, err := h.memberships.Get(r.Context(), body.UserID, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusBadRequest, "USER_NOT_IN_ORG", "user_id must belong to the current org")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "MEMBERSHIP_LOOKUP_FAILED", "failed to validate user membership", err)
		return
	}
	installation, err := h.slackInstallationStore.GetActiveByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SLACK_NOT_CONNECTED", "slack bot is not connected", err)
		return
	}
	var emailPtr *string
	if body.SlackEmail != "" {
		emailPtr = &body.SlackEmail
	}
	link := &models.SlackUserLink{
		OrgID:               orgID,
		SlackInstallationID: installation.ID,
		UserID:              &body.UserID,
		SlackTeamID:         installation.TeamID,
		SlackUserID:         body.SlackUserID,
		SlackEmail:          emailPtr,
		SlackDisplayName:    body.SlackDisplayName,
	}
	if err := h.slackUserLinkStore.UpsertAdminLink(r.Context(), link); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_USER_LINK_FAILED", "failed to upsert slack user link", err)
		return
	}
	if h.externalUserLinks != nil {
		externalLink, err := h.externalUserLinks.UpsertAdminActive(r.Context(), models.ExternalUserLink{
			OrgID:               orgID,
			Provider:            models.ExternalIdentityProviderSlack,
			ProviderWorkspaceID: installation.TeamID,
			ProviderUserID:      body.SlackUserID,
			UserID:              body.UserID,
			Confidence:          90,
			ExternalEmail:       emailPtr,
			ExternalDisplayName: trimOptional(&body.SlackDisplayName),
		})
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_FAILED", "failed to upsert external user link", err)
			return
		}
		resourceID := externalLink.ID.String()
		emitUserAudit(h.audit, r, models.AuditActionExternalUserLinkCreated, models.AuditResourceExternalUserLink, &resourceID, nil)
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SlackUserLink]{Data: *link})
}

type slackUserLinkAdminUpsertRequest struct {
	UserID           uuid.UUID `json:"user_id"`
	SlackUserID      string    `json:"slack_user_id"`
	SlackEmail       string    `json:"slack_email"`
	SlackDisplayName string    `json:"slack_display_name"`
}

func (r *slackUserLinkAdminUpsertRequest) Validate() error {
	r.SlackUserID = strings.TrimSpace(r.SlackUserID)
	r.SlackEmail = strings.TrimSpace(r.SlackEmail)
	r.SlackDisplayName = strings.TrimSpace(r.SlackDisplayName)
	if r.UserID == uuid.Nil {
		return errors.New("user_id is required")
	}
	if r.SlackUserID == "" {
		return errors.New("slack_user_id is required")
	}
	return nil
}

func (h *IntegrationHandler) DeleteSlackUserLinkAdmin(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if h.slackUserLinkStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slack user links are not configured")
		return
	}
	linkID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_LINK_ID", "id must be a valid UUID")
		return
	}
	link, err := h.slackUserLinkStore.GetByID(r.Context(), orgID, linkID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "SLACK_USER_LINK_NOT_FOUND", "slack user link not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "SLACK_USER_LINK_LOOKUP_FAILED", "failed to load slack user link", err)
		return
	}
	if err := h.slackUserLinkStore.DeleteByID(r.Context(), orgID, linkID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "SLACK_USER_LINK_NOT_FOUND", "slack user link not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "SLACK_USER_LINK_DELETE_FAILED", "failed to delete slack user link", err)
		return
	}
	if h.externalUserLinks != nil {
		if err := h.externalUserLinks.RevokeActiveByExternal(r.Context(), orgID, models.ExternalIdentityProviderSlack, link.SlackTeamID, link.SlackUserID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_REVOKE_FAILED", "failed to revoke external slack user link", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type externalUserLinkRequest struct {
	Provider            models.ExternalIdentityProvider `json:"provider"`
	ProviderWorkspaceID string                          `json:"provider_workspace_id"`
	ProviderUserID      string                          `json:"provider_user_id"`
	UserID              uuid.UUID                       `json:"user_id"`
	ExternalEmail       *string                         `json:"external_email,omitempty"`
	ExternalHandle      *string                         `json:"external_handle,omitempty"`
	ExternalDisplayName *string                         `json:"external_display_name,omitempty"`
}

func (req *externalUserLinkRequest) Validate() error {
	if err := req.Provider.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(req.ProviderWorkspaceID) == "" {
		return errors.New("provider_workspace_id is required")
	}
	if strings.TrimSpace(req.ProviderUserID) == "" {
		return errors.New("provider_user_id is required")
	}
	if req.UserID == uuid.Nil {
		return errors.New("user_id is required")
	}
	return nil
}

func (h *IntegrationHandler) ListExternalUserLinks(w http.ResponseWriter, r *http.Request) {
	if h.externalUserLinks == nil {
		writeError(w, r, http.StatusServiceUnavailable, "EXTERNAL_USER_LINKS_NOT_CONFIGURED", "external user links are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	links, err := h.externalUserLinks.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINKS_FAILED", "failed to list external user links", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.ExternalUserLink]{Data: links, Meta: models.PaginationMeta{}})
}

func (h *IntegrationHandler) CreateExternalUserLink(w http.ResponseWriter, r *http.Request) {
	if h.externalUserLinks == nil {
		writeError(w, r, http.StatusServiceUnavailable, "EXTERNAL_USER_LINKS_NOT_CONFIGURED", "external user links are not configured")
		return
	}
	if h.memberships == nil {
		writeError(w, r, http.StatusServiceUnavailable, "MEMBERSHIP_STORE_NOT_CONFIGURED", "membership validation is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	var req externalUserLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body", err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if _, err := h.memberships.Get(r.Context(), req.UserID, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusBadRequest, "USER_NOT_IN_ORG", "user_id must belong to the current org")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "MEMBERSHIP_LOOKUP_FAILED", "failed to validate user membership", err)
		return
	}
	var linkedBy *uuid.UUID
	if user != nil {
		linkedBy = &user.ID
	}
	link, err := h.externalUserLinks.UpsertAdminActive(r.Context(), models.ExternalUserLink{
		OrgID:               orgID,
		Provider:            req.Provider,
		ProviderWorkspaceID: strings.TrimSpace(req.ProviderWorkspaceID),
		ProviderUserID:      strings.TrimSpace(req.ProviderUserID),
		UserID:              req.UserID,
		Confidence:          90,
		ExternalEmail:       trimOptional(req.ExternalEmail),
		ExternalHandle:      trimOptional(req.ExternalHandle),
		ExternalDisplayName: trimOptional(req.ExternalDisplayName),
		LinkedByUserID:      linkedBy,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_FAILED", "failed to create external user link", err)
		return
	}
	resourceID := link.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionExternalUserLinkCreated, models.AuditResourceExternalUserLink, &resourceID, nil)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.ExternalUserLink]{Data: link})
}

func (h *IntegrationHandler) DeleteExternalUserLink(w http.ResponseWriter, r *http.Request) {
	if h.externalUserLinks == nil {
		writeError(w, r, http.StatusServiceUnavailable, "EXTERNAL_USER_LINKS_NOT_CONFIGURED", "external user links are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	linkID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_LINK_ID", "invalid link id", err)
		return
	}
	if err := h.externalUserLinks.Revoke(r.Context(), orgID, linkID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "EXTERNAL_USER_LINK_NOT_FOUND", "external user link not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_REVOKE_FAILED", "failed to revoke external user link", err)
		return
	}
	resourceID := linkID.String()
	emitUserAudit(h.audit, r, models.AuditActionExternalUserLinkRevoked, models.AuditResourceExternalUserLink, &resourceID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *IntegrationHandler) ListExternalUserLinkSuggestions(w http.ResponseWriter, r *http.Request) {
	if h.externalSuggestions == nil {
		writeError(w, r, http.StatusServiceUnavailable, "EXTERNAL_USER_LINK_SUGGESTIONS_NOT_CONFIGURED", "external user link suggestions are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	suggestions, err := h.externalSuggestions.ListOpenByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_SUGGESTIONS_FAILED", "failed to list external user link suggestions", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.ExternalUserLinkSuggestion]{Data: suggestions, Meta: models.PaginationMeta{}})
}

func (h *IntegrationHandler) ApproveExternalUserLinkSuggestion(w http.ResponseWriter, r *http.Request) {
	if h.externalUserLinks == nil || h.externalSuggestions == nil {
		writeError(w, r, http.StatusServiceUnavailable, "EXTERNAL_USER_LINKS_NOT_CONFIGURED", "external user links are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	suggestionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SUGGESTION_ID", "invalid suggestion id", err)
		return
	}
	user := middleware.UserFromContext(r.Context())
	var linkedBy *uuid.UUID
	if user != nil {
		linkedBy = &user.ID
	}
	link, err := h.externalUserLinks.ApproveSuggestion(r.Context(), orgID, suggestionID, linkedBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "EXTERNAL_USER_LINK_SUGGESTION_NOT_FOUND", "external user link suggestion not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_FAILED", "failed to approve external user link suggestion", err)
		return
	}
	resourceID := link.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionExternalUserLinkSuggestionApproved, models.AuditResourceExternalUserLink, &resourceID, nil)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.ExternalUserLink]{Data: link})
}

func (h *IntegrationHandler) DismissExternalUserLinkSuggestion(w http.ResponseWriter, r *http.Request) {
	if h.externalSuggestions == nil {
		writeError(w, r, http.StatusServiceUnavailable, "EXTERNAL_USER_LINK_SUGGESTIONS_NOT_CONFIGURED", "external user link suggestions are not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	suggestionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SUGGESTION_ID", "invalid suggestion id", err)
		return
	}
	if err := h.externalSuggestions.Dismiss(r.Context(), orgID, suggestionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "EXTERNAL_USER_LINK_SUGGESTION_NOT_FOUND", "external user link suggestion not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_SUGGESTION_DISMISS_FAILED", "failed to dismiss external user link suggestion", err)
		return
	}
	resourceID := suggestionID.String()
	emitUserAudit(h.audit, r, models.AuditActionExternalUserLinkSuggestionDismissed, models.AuditResourceExternalUserLinkSuggestion, &resourceID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *IntegrationHandler) ListMyExternalIdentities(w http.ResponseWriter, r *http.Request) {
	if h.externalUserLinks == nil {
		writeError(w, r, http.StatusServiceUnavailable, "EXTERNAL_USER_LINKS_NOT_CONFIGURED", "external user links are not configured")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	links, err := h.externalUserLinks.ListActiveByUser(r.Context(), orgID, user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_IDENTITIES_FAILED", "failed to list external identities", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.ExternalUserLink]{Data: links, Meta: models.PaginationMeta{}})
}

func (h *IntegrationHandler) DeleteMyExternalIdentity(w http.ResponseWriter, r *http.Request) {
	if h.externalUserLinks == nil {
		writeError(w, r, http.StatusServiceUnavailable, "EXTERNAL_USER_LINKS_NOT_CONFIGURED", "external user links are not configured")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	linkID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_LINK_ID", "invalid link id", err)
		return
	}
	link, err := h.externalUserLinks.GetByID(r.Context(), orgID, linkID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "EXTERNAL_IDENTITY_NOT_FOUND", "external identity not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_IDENTITY_FAILED", "failed to load external identity", err)
		return
	}
	if link.UserID != user.ID {
		writeError(w, r, http.StatusNotFound, "EXTERNAL_IDENTITY_NOT_FOUND", "external identity not found")
		return
	}
	if link.Source == models.ExternalUserLinkSourceAdminLinked {
		writeError(w, r, http.StatusForbidden, "ADMIN_MANAGED_EXTERNAL_IDENTITY", "admin-managed external identities must be removed by an admin")
		return
	}
	if err := h.externalUserLinks.Revoke(r.Context(), orgID, linkID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_IDENTITY_REVOKE_FAILED", "failed to disconnect external identity", err)
		return
	}
	resourceID := linkID.String()
	emitUserAudit(h.audit, r, models.AuditActionExternalUserLinkRevoked, models.AuditResourceExternalUserLink, &resourceID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *IntegrationHandler) ClaimExternalUserLink(w http.ResponseWriter, r *http.Request) {
	if h.externalUserLinks == nil {
		writeError(w, r, http.StatusServiceUnavailable, "EXTERNAL_USER_LINKS_NOT_CONFIGURED", "external user links are not configured")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
		return
	}
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if token == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_CLAIM_TOKEN", "claim token is required")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	link, err := h.externalUserLinks.ClaimSelfLink(r.Context(), orgID, externalidentity.HashClaimToken(token), user.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusGone, "EXTERNAL_USER_LINK_CLAIM_INVALID", "claim link is invalid, expired, already used, or unavailable in this org")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_CLAIM_FAILED", "failed to claim external user link", err)
		return
	}
	resourceID := link.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionExternalUserLinkClaimed, models.AuditResourceExternalUserLink, &resourceID, nil)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.ExternalUserLink]{Data: link})
}

func trimOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func (h *IntegrationHandler) LinkSlackUserMe(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "user is required")
		return
	}
	if h.slackInstallationStore == nil || h.slackUserLinkStore == nil || h.credentialStore == nil || h.slackUserInfoClient == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}
	var body slackUserLinkSelfRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if err := body.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SLACK_USER_LINK", err.Error())
		return
	}
	installation, err := h.slackInstallationStore.GetActiveByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SLACK_NOT_CONNECTED", "slack bot is not connected", err)
		return
	}
	cred, err := h.credentialStore.Get(r.Context(), orgID, models.ProviderSlack)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_CREDENTIAL_FAILED", "failed to load slack credentials", err)
		return
	}
	if cred == nil {
		writeError(w, r, http.StatusNotFound, "SLACK_NOT_CONNECTED", "slack bot credentials are not connected")
		return
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok || strings.TrimSpace(slackCfg.AccessToken) == "" {
		writeError(w, r, http.StatusInternalServerError, "SLACK_CREDENTIAL_INVALID", "slack bot credentials are invalid")
		return
	}
	slackUser, err := h.slackUserInfoClient.FetchUserInfo(r.Context(), slackCfg.AccessToken, body.SlackUserID)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "SLACK_USER_LOOKUP_FAILED", "failed to verify slack user", err)
		return
	}
	slackEmail := strings.TrimSpace(slackUser.Profile.Email)
	if slackEmail == "" || !strings.EqualFold(slackEmail, strings.TrimSpace(user.Email)) {
		writeError(w, r, http.StatusForbidden, "SLACK_USER_EMAIL_MISMATCH", "slack user email does not match the authenticated user")
		return
	}
	displayName := strings.TrimSpace(slackUser.Profile.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(slackUser.RealName)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(slackUser.Name)
	}
	emailPtr := &slackEmail
	link := &models.SlackUserLink{
		OrgID:               orgID,
		SlackInstallationID: installation.ID,
		UserID:              &user.ID,
		SlackTeamID:         installation.TeamID,
		SlackUserID:         body.SlackUserID,
		SlackEmail:          emailPtr,
		SlackDisplayName:    displayName,
	}
	if err := h.slackUserLinkStore.UpsertSelfLink(r.Context(), link); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_USER_LINK_FAILED", "failed to link slack user", err)
		return
	}
	if h.externalUserLinks != nil {
		externalLink, err := h.externalUserLinks.UpsertSelfActive(r.Context(), models.ExternalUserLink{
			OrgID:               orgID,
			Provider:            models.ExternalIdentityProviderSlack,
			ProviderWorkspaceID: installation.TeamID,
			ProviderUserID:      body.SlackUserID,
			UserID:              user.ID,
			Confidence:          100,
			ExternalEmail:       emailPtr,
			ExternalDisplayName: trimOptional(&displayName),
			LinkedByUserID:      &user.ID,
		})
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_FAILED", "failed to link external slack user", err)
			return
		}
		resourceID := externalLink.ID.String()
		emitUserAudit(h.audit, r, models.AuditActionExternalUserLinkCreated, models.AuditResourceExternalUserLink, &resourceID, nil)
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SlackUserLink]{Data: *link})
}

type slackUserLinkSelfRequest struct {
	SlackUserID string `json:"slack_user_id"`
}

func (r *slackUserLinkSelfRequest) Validate() error {
	r.SlackUserID = strings.TrimSpace(r.SlackUserID)
	if r.SlackUserID == "" {
		return errors.New("slack_user_id is required")
	}
	return nil
}

func (h *IntegrationHandler) UnlinkSlackUserMe(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "user is required")
		return
	}
	if h.slackInstallationStore == nil || h.slackUserLinkStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}
	installation, err := h.slackInstallationStore.GetActiveByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SLACK_NOT_CONNECTED", "slack bot is not connected", err)
		return
	}
	if err := h.slackUserLinkStore.DeleteSelfLink(r.Context(), orgID, user.ID, installation.TeamID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_USER_UNLINK_FAILED", "failed to unlink slack user", err)
		return
	}
	if h.externalUserLinks != nil {
		if err := h.externalUserLinks.RevokeSelfLinkedByUser(r.Context(), orgID, user.ID, models.ExternalIdentityProviderSlack, installation.TeamID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "EXTERNAL_USER_LINK_REVOKE_FAILED", "failed to unlink external slack user", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *IntegrationHandler) PatchSlackChannelSettings(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	channelID := chi.URLParam(r, "slack_channel_id")
	if channelID == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_CHANNEL_ID", "slack_channel_id is required")
		return
	}
	if h.slackInstallationStore == nil || h.slackChannelStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}
	var body slackChannelSettingsPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	installation, err := h.slackInstallationStore.GetActiveByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SLACK_NOT_CONNECTED", "slack bot is not connected", err)
		return
	}
	if body.DefaultRepositoryID != nil {
		if h.repoStore == nil {
			writeError(w, r, http.StatusServiceUnavailable, "REPOSITORY_STORE_NOT_CONFIGURED", "repository validation is not configured")
			return
		}
		if _, err := h.repoStore.GetByID(r.Context(), orgID, *body.DefaultRepositoryID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY", "default_repository_id must belong to the current org")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOOKUP_FAILED", "failed to validate default repository", err)
			return
		}
	}
	settings, err := h.slackChannelStore.GetByChannel(r.Context(), orgID, installation.TeamID, channelID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "SLACK_CHANNEL_LOOKUP_FAILED", "failed to load slack channel settings", err)
			return
		}
		settings = models.SlackChannelSettings{
			OrgID:               orgID,
			SlackInstallationID: installation.ID,
			SlackTeamID:         installation.TeamID,
			SlackChannelID:      channelID,
			ChannelType:         "channel",
			Active:              true,
		}
	}
	settings.OrgID = orgID
	settings.SlackInstallationID = installation.ID
	settings.SlackTeamID = installation.TeamID
	settings.SlackChannelID = channelID
	settings.Active = true
	err = body.Apply(&settings)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SLACK_CHANNEL_SETTINGS", err.Error())
		return
	}
	if err := h.slackChannelStore.Upsert(r.Context(), &settings); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_CHANNEL_UPDATE_FAILED", "failed to update slack channel settings", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SlackChannelSettings]{Data: settings})
}

type slackChannelSettingsPatchRequest struct {
	SlackChannelName          string          `json:"slack_channel_name"`
	ChannelType               string          `json:"channel_type"`
	DefaultRepositoryID       *uuid.UUID      `json:"default_repository_id"`
	DefaultBranch             *string         `json:"default_branch"`
	RoutingMode               *string         `json:"routing_mode"`
	ResponseVisibility        *string         `json:"response_visibility"`
	AllowedActions            []string        `json:"allowed_actions"`
	NotificationPreset        *string         `json:"notification_preset"`
	NotificationSubscriptions json.RawMessage `json:"notification_subscriptions"`

	slackChannelNameSet          bool
	channelTypeSet               bool
	defaultRepositoryIDSet       bool
	defaultBranchSet             bool
	routingModeSet               bool
	responseVisibilitySet        bool
	allowedActionsSet            bool
	notificationPresetSet        bool
	notificationSubscriptionsSet bool
}

func (r *slackChannelSettingsPatchRequest) UnmarshalJSON(data []byte) error {
	type slackChannelSettingsPatchRequestJSON struct {
		SlackChannelName          string          `json:"slack_channel_name"`
		ChannelType               string          `json:"channel_type"`
		DefaultRepositoryID       *uuid.UUID      `json:"default_repository_id"`
		DefaultBranch             *string         `json:"default_branch"`
		RoutingMode               *string         `json:"routing_mode"`
		ResponseVisibility        *string         `json:"response_visibility"`
		AllowedActions            []string        `json:"allowed_actions"`
		NotificationPreset        *string         `json:"notification_preset"`
		NotificationSubscriptions json.RawMessage `json:"notification_subscriptions"`
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var decoded slackChannelSettingsPatchRequestJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	r.SlackChannelName = decoded.SlackChannelName
	r.ChannelType = decoded.ChannelType
	r.DefaultRepositoryID = decoded.DefaultRepositoryID
	r.DefaultBranch = decoded.DefaultBranch
	r.RoutingMode = decoded.RoutingMode
	r.ResponseVisibility = decoded.ResponseVisibility
	r.AllowedActions = decoded.AllowedActions
	r.NotificationPreset = decoded.NotificationPreset
	r.NotificationSubscriptions = decoded.NotificationSubscriptions
	_, r.slackChannelNameSet = raw["slack_channel_name"]
	_, r.channelTypeSet = raw["channel_type"]
	_, r.defaultRepositoryIDSet = raw["default_repository_id"]
	_, r.defaultBranchSet = raw["default_branch"]
	_, r.routingModeSet = raw["routing_mode"]
	_, r.responseVisibilitySet = raw["response_visibility"]
	_, r.allowedActionsSet = raw["allowed_actions"]
	_, r.notificationPresetSet = raw["notification_preset"]
	_, r.notificationSubscriptionsSet = raw["notification_subscriptions"]
	return nil
}

func (r slackChannelSettingsPatchRequest) ToSettings(orgID, installationID uuid.UUID, teamID, channelID string) (models.SlackChannelSettings, error) {
	settings := models.SlackChannelSettings{
		OrgID:               orgID,
		SlackInstallationID: installationID,
		SlackTeamID:         teamID,
		SlackChannelID:      channelID,
		ChannelType:         "channel",
		Active:              true,
	}
	if err := r.Apply(&settings); err != nil {
		return models.SlackChannelSettings{}, err
	}
	return settings, nil
}

func (r slackChannelSettingsPatchRequest) Apply(settings *models.SlackChannelSettings) error {
	if r.slackChannelNameSet || strings.TrimSpace(r.SlackChannelName) != "" {
		settings.SlackChannelName = strings.TrimSpace(r.SlackChannelName)
	}
	if r.channelTypeSet || strings.TrimSpace(r.ChannelType) != "" {
		settings.ChannelType = strings.TrimSpace(r.ChannelType)
	}
	if strings.TrimSpace(settings.ChannelType) == "" {
		settings.ChannelType = "channel"
	}
	if r.defaultRepositoryIDSet || r.DefaultRepositoryID != nil {
		settings.DefaultRepositoryID = r.DefaultRepositoryID
	}
	if r.defaultBranchSet || r.DefaultBranch != nil {
		settings.DefaultBranch = r.DefaultBranch
	}
	var routingMode *models.SlackRoutingMode
	if r.RoutingMode != nil && strings.TrimSpace(*r.RoutingMode) != "" {
		value := models.SlackRoutingMode(strings.TrimSpace(*r.RoutingMode))
		routingMode = &value
	}
	if r.routingModeSet || r.RoutingMode != nil {
		settings.RoutingMode = routingMode
	}
	var responseVisibility *models.SlackResponseVisibility
	if r.ResponseVisibility != nil && strings.TrimSpace(*r.ResponseVisibility) != "" {
		value := models.SlackResponseVisibility(strings.TrimSpace(*r.ResponseVisibility))
		responseVisibility = &value
	}
	if r.responseVisibilitySet || r.ResponseVisibility != nil {
		settings.ResponseVisibility = responseVisibility
	}
	var notificationPreset *models.SlackNotificationPreset
	if r.NotificationPreset != nil && strings.TrimSpace(*r.NotificationPreset) != "" {
		value := models.SlackNotificationPreset(strings.TrimSpace(*r.NotificationPreset))
		notificationPreset = &value
	}
	if r.allowedActionsSet || len(r.AllowedActions) > 0 {
		settings.AllowedActions = r.AllowedActions
	}
	if r.notificationPresetSet || r.NotificationPreset != nil {
		settings.NotificationPreset = notificationPreset
	}
	if r.notificationSubscriptionsSet || len(r.NotificationSubscriptions) > 0 {
		settings.NotificationSubscriptions = r.NotificationSubscriptions
	}
	return validateSlackChannelSettings(*settings)
}

func validateSlackChannelSettings(settings models.SlackChannelSettings) error {
	if settings.RoutingMode != nil {
		if err := settings.RoutingMode.Validate(); err != nil {
			return err
		}
	}
	if settings.ResponseVisibility != nil {
		if err := settings.ResponseVisibility.Validate(); err != nil {
			return err
		}
	}
	if settings.NotificationPreset != nil {
		if err := settings.NotificationPreset.Validate(); err != nil {
			return err
		}
	}
	for _, action := range settings.AllowedActions {
		if err := models.SlackChannelAction(action).Validate(); err != nil {
			return err
		}
	}
	return nil
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
	reusableIntegrations, err := h.integrationStore.ListReusableForReconnect(ctx, orgID, provider)
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
		if err := h.integrationStore.UpdateStatusAndConfig(ctx, orgID, integration.ID, models.IntegrationStatusActive, clearedConfig); err != nil {
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
		if err := h.integrationStore.UpdateStatus(ctx, orgID, integration.ID, models.IntegrationStatusActive); err != nil {
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

// linearViewerInfo packages the post-token-exchange viewer GraphQL result.
// Returned by fetchLinearViewer so callers can persist all of workspace +
// agent-user metadata in a single round trip.
type linearViewerInfo struct {
	WorkspaceID   string
	WorkspaceName string
	// AppUserID and AppUserName describe the agent user Linear provisioned
	// when the OAuth completed with actor=app. Empty for legacy (read/write
	// only) installs. The OAuth callback only persists these when the
	// returned scope string includes the agent scopes; otherwise the token
	// is just a regular user token even though the GraphQL `viewer` query
	// happens to return *some* identity.
	AppUserID   string
	AppUserName string
}

func (h *IntegrationHandler) fetchLinearViewer(ctx context.Context, accessToken string) (*linearViewerInfo, error) {
	queryBody := map[string]string{"query": "query ViewerOrg { viewer { id name organization { id name } } }"}
	body, err := json.Marshal(queryBody)
	if err != nil {
		return nil, fmt.Errorf("marshal linear viewer query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearGraphQLURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("create linear viewer request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear viewer request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("linear viewer request failed: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var viewer linearViewerResponse
	if err := json.NewDecoder(resp.Body).Decode(&viewer); err != nil {
		return nil, fmt.Errorf("decode linear viewer response: %w", err)
	}

	if viewer.Data.Viewer.Organization.ID == "" {
		return nil, fmt.Errorf("linear viewer response missing organization id")
	}

	return &linearViewerInfo{
		WorkspaceID:   viewer.Data.Viewer.Organization.ID,
		WorkspaceName: viewer.Data.Viewer.Organization.Name,
		AppUserID:     viewer.Data.Viewer.ID,
		AppUserName:   viewer.Data.Viewer.Name,
	}, nil
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
	if decrypted == nil {
		return nil, fmt.Errorf("slack credential not found")
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
	h.deriveIntegrationStatus(r.Context(), &integration, map[models.IntegrationProvider]bool{
		models.IntegrationProviderNotion: true,
	})

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
	h.deriveIntegrationStatus(r.Context(), &integration, map[models.IntegrationProvider]bool{
		models.IntegrationProviderCircleCI: true,
	})

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

// mezmoDefaultBaseURL is the public Mezmo API host. Kept in sync with the
// runtime provider default in internal/services/integration.NewMezmoProvider.
const mezmoDefaultBaseURL = "https://api.mezmo.com"

// ConnectMezmo accepts a Mezmo access token or legacy service key (plus optional
// base URL), validates it against the export endpoint, stores the
// credential, and creates an active integration record. Mezmo uses a
// paste-the-key flow because its log-query API authenticates with a long-lived
// token rather than OAuth. The stored credential is injected into sandboxes as
// MEZMO_* env vars by the orchestrator (see internal/services/agent/env.go) so
// the agent's 143-tools log commands query this org's Mezmo with its own key.
func (h *IntegrationHandler) ConnectMezmo(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var req struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Dataset string `json:"dataset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	req.Dataset = strings.TrimSpace(req.Dataset)
	if req.APIKey == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_API_KEY", "api_key is required")
		return
	}
	if req.Dataset != "" {
		writeError(w, r, http.StatusBadRequest, "UNSUPPORTED_DATASET", "Mezmo dataset scoping is not supported by the current export API; leave dataset blank")
		return
	}
	if req.BaseURL != "" {
		if err := validatePublicHTTPURL(req.BaseURL); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BASE_URL", "base_url must be a public http(s) URL (e.g. https://api.mezmo.com)")
			return
		}
	}

	cfg := models.MezmoConfig{
		APIKey:  req.APIKey,
		BaseURL: req.BaseURL,
		Dataset: req.Dataset,
	}
	if err := h.validateMezmoToken(r.Context(), cfg); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN", "failed to validate Mezmo key: "+err.Error())
		return
	}

	if err := h.credentialStore.Upsert(r.Context(), orgID, cfg); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_SAVE_FAILED", "failed to save Mezmo credentials", err)
		return
	}

	integration, created, err := h.ensureIntegration(r.Context(), orgID, models.IntegrationProviderMezmo)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_MEZMO_FAILED", "failed to create Mezmo integration", err)
		return
	}
	h.deriveIntegrationStatus(r.Context(), &integration, map[models.IntegrationProvider]bool{
		models.IntegrationProviderMezmo: true,
	})

	if created {
		writeJSON(w, http.StatusCreated, models.SingleResponse[models.Integration]{Data: integration})
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Integration]{Data: integration})
}

// validateMezmoToken issues a minimal export request against the Mezmo API to
// confirm the key is accepted. It mirrors the runtime provider's documented
// GET /v2/export path and prefers the current Authorization token auth, with a
// fallback for legacy service keys.
func (h *IntegrationHandler) validateMezmoToken(ctx context.Context, cfg models.MezmoConfig) error {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = mezmoDefaultBaseURL
	}
	now := time.Now()
	values := url.Values{}
	values.Set("query", "*")
	values.Set("from", strconv.FormatInt(now.Add(-time.Minute).Unix(), 10))
	values.Set("to", strconv.FormatInt(now.Unix(), 10))
	values.Set("size", "1")
	values.Set("prefer", "tail")

	statusCode, err := h.validateMezmoTokenRequest(ctx, baseURL, values, "Authorization", "Token "+cfg.APIKey)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		fallbackStatus, fallbackErr := h.validateMezmoTokenRequest(ctx, baseURL, values, "servicekey", cfg.APIKey)
		if fallbackErr != nil {
			return fmt.Errorf("request failed: %w", fallbackErr)
		}
		statusCode = fallbackStatus
	}

	// We're validating the key, not the query. Only an auth rejection is a
	// definitive "bad key", 404/405 means the configured endpoint is wrong or
	// no longer serves the export API, and a 5xx means Mezmo is unhealthy right
	// now (worth surfacing so the user retries). Other 4xx responses can be
	// query-shape/rate-limit quirks after auth, so do not block a valid key.
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return fmt.Errorf("invalid or expired key")
	case statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed:
		return fmt.Errorf("mezmo API endpoint not found")
	case statusCode >= 500:
		return fmt.Errorf("mezmo API returned %d", statusCode)
	default:
		return nil
	}
}

func (h *IntegrationHandler) validateMezmoTokenRequest(ctx context.Context, baseURL string, values url.Values, authHeader, authValue string) (int, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/v2/export"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set(authHeader, authValue)

	// base_url is user-supplied, so use the SSRF-safe client (private/loopback/
	// metadata addresses are blocked at dial time and across redirects).
	resp, err := h.untrustedURLClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, err
	}
	return resp.StatusCode, nil
}
