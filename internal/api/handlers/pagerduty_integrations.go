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
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	pagerdutysvc "github.com/assembledhq/143/internal/services/pagerduty"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

const (
	pagerDutyAuthorizeURL                = "https://identity.pagerduty.com/oauth/authorize"
	pagerDutyTokenURL                    = "https://identity.pagerduty.com/oauth/token" // #nosec G101 -- OAuth endpoint URL, not credentials
	pagerDutyIntegrationOAuthStateCookie = "pagerduty_integration_oauth_state"
	pagerDutyLegacyCredentialRef         = "org_credential:pagerduty" // #nosec G101 -- database credential row reference, not a secret
	// pagerDutySigningSecretMinLen is the minimum webhook secret length PagerDuty
	// accepts for delivery-method signing. Secrets at least this long are
	// registered as the signing secret so PagerDuty HMAC-signs each delivery.
	pagerDutySigningSecretMinLen = 16
)

type pagerDutyGenericIntegrationStore interface {
	ListReusableForReconnect(ctx context.Context, orgID uuid.UUID, provider models.IntegrationProvider) ([]models.Integration, error)
	Create(ctx context.Context, integration *models.Integration) error
	UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status models.IntegrationStatus) error
}

type pagerDutyCredentialWriter interface {
	UpsertWithLabel(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error)
}

type pagerDutyProviderIntegrationWriter interface {
	GetByID(ctx context.Context, orgID, id uuid.UUID) (models.PagerDutyIntegration, error)
	GetByIntegrationID(ctx context.Context, orgID, integrationID uuid.UUID) (models.PagerDutyIntegration, error)
	GetByAccount(ctx context.Context, orgID uuid.UUID, accountSubdomain, serviceRegion string) (models.PagerDutyIntegration, error)
	Create(ctx context.Context, integration *models.PagerDutyIntegration) error
	ListManageable(ctx context.Context, orgID uuid.UUID) ([]models.PagerDutyIntegration, error)
	UpdateSettings(ctx context.Context, orgID uuid.UUID, settings models.PagerDutyIntegrationSettings) (models.PagerDutyIntegration, error)
	DeactivateAll(ctx context.Context, orgID uuid.UUID) error
}

type pagerDutyMappingStore interface {
	ListByIntegration(ctx context.Context, orgID, integrationID uuid.UUID) ([]models.PagerDutyServiceRepoMapping, error)
	GetByServiceID(ctx context.Context, orgID, integrationID uuid.UUID, serviceID string) (models.PagerDutyServiceRepoMapping, error)
	Upsert(ctx context.Context, mapping *models.PagerDutyServiceRepoMapping) error
}

type pagerDutyIncidentReader interface {
	List(ctx context.Context, orgID uuid.UUID, filter db.PagerDutyIncidentListFilter) ([]models.PagerDutyIncident, error)
	GetByIncidentID(ctx context.Context, orgID, integrationID uuid.UUID, incidentID string) (models.PagerDutyIncident, error)
	GetLatestByIncidentID(ctx context.Context, orgID uuid.UUID, incidentID string) (models.PagerDutyIncident, error)
}

type pagerDutySessionStarter interface {
	StartSession(ctx context.Context, input pagerdutysvc.StartSessionInput) (models.Session, error)
}

type pagerDutyCredentialReader interface {
	GetByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error)
}

type pagerDutyCredentialDisabler interface {
	Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
	DisableLabeled(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
	DisableByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
}

type pagerDutyWebhookFailureReader interface {
	SummarizeRecentFailuresForIntegration(ctx context.Context, orgID, integrationID uuid.UUID, provider string, since time.Time) (models.PagerDutyWebhookFailureSummary, error)
}

type pagerDutyClient interface {
	TestCredential(ctx context.Context, cfg models.PagerDutyConfig) error
	ListServices(ctx context.Context, cfg models.PagerDutyConfig) ([]models.PagerDutyServiceSummary, error)
	CreateWebhookSubscription(ctx context.Context, cfg models.PagerDutyConfig, req pagerDutyWebhookSubscriptionRequest) (pagerDutyWebhookSubscription, error)
	ExchangeOAuthCode(ctx context.Context, req pagerDutyOAuthExchangeRequest) (pagerDutyOAuthToken, error)
}

type pagerDutyOAuthExchangeRequest struct {
	Code         string
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

type pagerDutyOAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

type pagerDutyWebhookSubscriptionRequest struct {
	WebhookURL  string
	Secret      string
	Description string
	FilterID    string
	FilterType  string
	Events      []models.PagerDutyEventType
}

type pagerDutyWebhookSubscription struct {
	ID string
}

type PagerDutyIntegrationHandlerConfig struct {
	Integrations          pagerDutyGenericIntegrationStore
	Credentials           pagerDutyCredentialWriter
	CredentialReader      pagerDutyCredentialReader
	CredentialDisabler    pagerDutyCredentialDisabler
	PagerDutyIntegrations pagerDutyProviderIntegrationWriter
	Mappings              pagerDutyMappingStore
	Repositories          repoLookup
	Incidents             pagerDutyIncidentReader
	SessionStarter        pagerDutySessionStarter
	WebhookFailures       pagerDutyWebhookFailureReader
	Client                pagerDutyClient
	BaseURL               string
	FrontendURL           string
	Disabled              bool
	OAuthClientID         string
	OAuthClientSecret     string
	Metrics               *metrics.PagerDutyMetrics
	Logger                zerolog.Logger
}

type PagerDutyIntegrationHandler struct {
	integrations          pagerDutyGenericIntegrationStore
	credentials           pagerDutyCredentialWriter
	credentialReader      pagerDutyCredentialReader
	credentialDisabler    pagerDutyCredentialDisabler
	pagerDutyIntegrations pagerDutyProviderIntegrationWriter
	mappings              pagerDutyMappingStore
	repositories          repoLookup
	incidents             pagerDutyIncidentReader
	sessionStarter        pagerDutySessionStarter
	webhookFailures       pagerDutyWebhookFailureReader
	client                pagerDutyClient
	baseURL               string
	frontendURL           string
	disabled              bool
	oauthClientID         string
	oauthClientSecret     string
	audit                 *db.AuditEmitter
	metrics               *metrics.PagerDutyMetrics
	logger                zerolog.Logger
}

func NewPagerDutyIntegrationHandler(cfg PagerDutyIntegrationHandlerConfig) *PagerDutyIntegrationHandler {
	client := cfg.Client
	if client == nil {
		client = pagerDutyRESTClient{httpClient: &http.Client{Timeout: 30 * time.Second}, metrics: cfg.Metrics}
	}
	return &PagerDutyIntegrationHandler{
		integrations:          cfg.Integrations,
		credentials:           cfg.Credentials,
		credentialReader:      cfg.CredentialReader,
		credentialDisabler:    cfg.CredentialDisabler,
		pagerDutyIntegrations: cfg.PagerDutyIntegrations,
		mappings:              cfg.Mappings,
		repositories:          cfg.Repositories,
		incidents:             cfg.Incidents,
		sessionStarter:        cfg.SessionStarter,
		webhookFailures:       cfg.WebhookFailures,
		client:                client,
		baseURL:               strings.TrimRight(cfg.BaseURL, "/"),
		frontendURL:           strings.TrimRight(cfg.FrontendURL, "/"),
		disabled:              cfg.Disabled,
		oauthClientID:         strings.TrimSpace(cfg.OAuthClientID),
		oauthClientSecret:     strings.TrimSpace(cfg.OAuthClientSecret),
		metrics:               cfg.Metrics,
		logger:                cfg.Logger,
	}
}

func (h *PagerDutyIntegrationHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

func (h *PagerDutyIntegrationHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if h == nil || h.pagerDutyIntegrations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty integrations are not configured")
		return
	}
	integrations, err := h.pagerDutyIntegrations.ListManageable(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list PagerDuty integrations", err)
		return
	}
	if integrations == nil {
		integrations = []models.PagerDutyIntegration{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PagerDutyIntegration]{Data: integrations})
}

func (h *PagerDutyIntegrationHandler) Connect(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if h == nil || h.integrations == nil || h.credentials == nil || h.pagerDutyIntegrations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty integrations are not configured")
		return
	}

	var req struct {
		AccessToken         string                    `json:"access_token"`
		RefreshToken        string                    `json:"refresh_token"`
		ExpiresAt           *time.Time                `json:"expires_at"`
		TokenType           string                    `json:"token_type"`
		Scope               string                    `json:"scope"`
		AccountSubdomain    string                    `json:"account_subdomain"`
		ServiceRegion       string                    `json:"service_region"`
		WebhookSecret       string                    `json:"webhook_secret"`
		OAuthMode           models.PagerDutyOAuthMode `json:"oauth_mode"`
		Scopes              []string                  `json:"scopes"`
		DefaultRepositoryID *uuid.UUID                `json:"default_repository_id"`
		WritebackEnabled    *bool                     `json:"writeback_enabled"`
		AutoCreateWebhook   *bool                     `json:"auto_create_webhook"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	cfg := models.PagerDutyConfig{
		AccessToken:      strings.TrimSpace(req.AccessToken),
		RefreshToken:     strings.TrimSpace(req.RefreshToken),
		TokenType:        strings.TrimSpace(req.TokenType),
		Scope:            strings.TrimSpace(req.Scope),
		AccountSubdomain: strings.TrimSpace(req.AccountSubdomain),
		ServiceRegion:    strings.TrimSpace(req.ServiceRegion),
		WebhookSecret:    strings.TrimSpace(req.WebhookSecret),
	}
	if req.ExpiresAt != nil {
		cfg.ExpiresAt = *req.ExpiresAt
	}
	if cfg.ServiceRegion == "" {
		cfg.ServiceRegion = "us"
	}
	if cfg.TokenType == "" {
		cfg.TokenType = "Bearer"
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CREDENTIAL", err.Error())
		return
	}
	oauthMode := req.OAuthMode
	if oauthMode == "" {
		oauthMode = models.PagerDutyOAuthModeScoped
	}
	if err := oauthMode.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_OAUTH_MODE", err.Error())
		return
	}
	if req.DefaultRepositoryID != nil && *req.DefaultRepositoryID != uuid.Nil {
		if !h.validatePagerDutyRepository(w, r, orgID, *req.DefaultRepositoryID, "default_repository_id") {
			return
		}
	}

	var createdBy *uuid.UUID
	if user != nil {
		createdBy = &user.ID
	}
	credentialID, err := h.credentials.UpsertWithLabel(r.Context(), orgID, createdBy, pagerDutyCredentialLabel(cfg), cfg)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_SAVE_FAILED", "failed to save PagerDuty credentials", err)
		return
	}
	generic, _, err := h.ensureGenericIntegration(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_FAILED", "failed to create PagerDuty integration", err)
		return
	}

	existing, err := h.pagerDutyIntegrations.GetByAccount(r.Context(), orgID, cfg.AccountSubdomain, cfg.ServiceRegion)
	if err == nil {
		h.emitAudit(r, models.AuditActionIntegrationConnected, models.AuditResourceIntegration, existing.ID, nil, map[string]any{
			"provider":   string(models.IntegrationProviderPagerDuty),
			"oauth_mode": existing.OAuthMode,
			"existing":   true,
		})
		writeJSON(w, http.StatusOK, models.SingleResponse[models.PagerDutyIntegration]{Data: existing})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_FAILED", "failed to lookup PagerDuty provider integration", err)
		return
	}

	writebackEnabled := true
	if req.WritebackEnabled != nil {
		writebackEnabled = *req.WritebackEnabled
	}
	autoCreateWebhook := false
	if req.AutoCreateWebhook != nil {
		autoCreateWebhook = *req.AutoCreateWebhook
	}
	credentialRef := ""
	if credentialID != nil {
		credentialRef = "org_credential:" + credentialID.String()
	} else {
		credentialRef = pagerDutyLegacyCredentialRef
	}
	install := &models.PagerDutyIntegration{
		OrgID:               orgID,
		IntegrationID:       &generic.ID,
		AccountSubdomain:    stringPtrOrNilPagerDutyIntegration(cfg.AccountSubdomain),
		ServiceRegion:       cfg.ServiceRegion,
		OAuthMode:           oauthMode,
		CredentialRef:       credentialRef,
		Status:              models.PagerDutyIntegrationStatusActive,
		Scopes:              req.Scopes,
		DefaultRepositoryID: req.DefaultRepositoryID,
		WritebackEnabled:    writebackEnabled,
		AutoCreateWebhook:   autoCreateWebhook,
		CreatedBy:           createdBy,
	}
	if len(install.Scopes) == 0 && cfg.Scope != "" {
		install.Scopes = strings.Fields(strings.ReplaceAll(cfg.Scope, ",", " "))
	}
	if err := h.pagerDutyIntegrations.Create(r.Context(), install); err != nil {
		// The connect flow is not a single transaction (the credential,
		// generic-integration, and provider-integration writes go to different
		// stores). If the final write fails we'd otherwise leave an orphaned,
		// active credential. Best-effort disable it so we don't accumulate
		// dangling credentials; a retry re-activates it via UpsertWithLabel.
		if credentialID != nil && h.credentialDisabler != nil {
			if cleanupErr := h.credentialDisabler.DisableByID(r.Context(), orgID, *credentialID); cleanupErr != nil {
				h.logger.Warn().Err(cleanupErr).
					Str("org_id", orgID.String()).
					Str("credential_id", credentialID.String()).
					Msg("failed to clean up orphaned PagerDuty credential after provider-integration create failure")
			}
		}
		writeError(w, r, http.StatusInternalServerError, "CONNECT_FAILED", "failed to create PagerDuty provider integration", err)
		return
	}
	h.emitAudit(r, models.AuditActionIntegrationConnected, models.AuditResourceIntegration, install.ID, nil, map[string]any{
		"provider":   string(models.IntegrationProviderPagerDuty),
		"oauth_mode": install.OAuthMode,
	})

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.PagerDutyIntegration]{Data: *install})
}

func (h *PagerDutyIntegrationHandler) StartOAuth(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	if h == nil || h.oauthClientID == "" || h.oauthClientSecret == "" {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_OAUTH_NOT_CONFIGURED", "PagerDuty OAuth is not configured")
		return
	}
	state, err := setOAuthState(w, pagerDutyIntegrationOAuthStateCookie)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state", err)
		return
	}
	params := url.Values{
		"response_type": {"code"},
		"client_id":     {h.oauthClientID},
		"redirect_uri":  {h.oauthRedirectURL()},
		"state":         {state},
	}
	http.Redirect(w, r, pagerDutyAuthorizeURL+"?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (h *PagerDutyIntegrationHandler) HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	if h == nil || h.credentials == nil || h.integrations == nil || h.pagerDutyIntegrations == nil || h.client == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty integrations are not configured")
		return
	}
	if h.oauthClientID == "" || h.oauthClientSecret == "" {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_OAUTH_NOT_CONFIGURED", "PagerDuty OAuth is not configured")
		return
	}
	code, ok := validateOAuthCallback(w, r, pagerDutyIntegrationOAuthStateCookie)
	if !ok {
		return
	}
	token, err := h.client.ExchangeOAuthCode(r.Context(), pagerDutyOAuthExchangeRequest{
		Code:         code,
		ClientID:     h.oauthClientID,
		ClientSecret: h.oauthClientSecret,
		RedirectURI:  h.oauthRedirectURL(),
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange PagerDuty OAuth code", err)
		return
	}
	cfg := models.PagerDutyConfig{
		AccessToken:   strings.TrimSpace(token.AccessToken),
		RefreshToken:  strings.TrimSpace(token.RefreshToken),
		TokenType:     strings.TrimSpace(token.TokenType),
		Scope:         strings.TrimSpace(token.Scope),
		ServiceRegion: "us",
	}
	if cfg.TokenType == "" {
		cfg.TokenType = "Bearer"
	}
	if token.ExpiresIn > 0 {
		cfg.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, r, http.StatusBadGateway, "INVALID_PAGERDUTY_TOKEN", err.Error())
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	var createdBy *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		createdBy = &user.ID
	}
	credentialID, err := h.credentials.UpsertWithLabel(r.Context(), orgID, createdBy, pagerDutyCredentialLabel(cfg), cfg)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_SAVE_FAILED", "failed to save PagerDuty credentials", err)
		return
	}
	generic, _, err := h.ensureGenericIntegration(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_FAILED", "failed to create PagerDuty integration", err)
		return
	}
	existing, err := h.pagerDutyIntegrations.GetByAccount(r.Context(), orgID, cfg.AccountSubdomain, cfg.ServiceRegion)
	if err == nil {
		h.emitAudit(r, models.AuditActionIntegrationConnected, models.AuditResourceIntegration, existing.ID, nil, map[string]any{
			"provider": string(models.IntegrationProviderPagerDuty),
			"oauth":    true,
			"existing": true,
		})
		http.Redirect(w, r, h.oauthFrontendRedirectURL("pagerduty=connected"), http.StatusTemporaryRedirect)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_FAILED", "failed to lookup PagerDuty provider integration", err)
		return
	}
	credentialRef := pagerDutyLegacyCredentialRef
	if credentialID != nil {
		credentialRef = "org_credential:" + credentialID.String()
	}
	install := &models.PagerDutyIntegration{
		OrgID:             orgID,
		IntegrationID:     &generic.ID,
		ServiceRegion:     cfg.ServiceRegion,
		OAuthMode:         models.PagerDutyOAuthModeScoped,
		CredentialRef:     credentialRef,
		Status:            models.PagerDutyIntegrationStatusActive,
		Scopes:            strings.Fields(strings.ReplaceAll(cfg.Scope, ",", " ")),
		WritebackEnabled:  true,
		AutoCreateWebhook: false,
		CreatedBy:         createdBy,
	}
	if err := h.pagerDutyIntegrations.Create(r.Context(), install); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONNECT_FAILED", "failed to create PagerDuty provider integration", err)
		return
	}
	h.emitAudit(r, models.AuditActionIntegrationConnected, models.AuditResourceIntegration, install.ID, nil, map[string]any{
		"provider":   string(models.IntegrationProviderPagerDuty),
		"oauth":      true,
		"oauth_mode": install.OAuthMode,
	})
	http.Redirect(w, r, h.oauthFrontendRedirectURL("pagerduty=connected"), http.StatusTemporaryRedirect)
}

func (h *PagerDutyIntegrationHandler) Patch(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if h == nil || h.pagerDutyIntegrations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty integrations are not configured")
		return
	}
	integration, ok := h.resolvePagerDutyIntegrationForRequest(w, r, orgID)
	if !ok {
		return
	}
	raw := map[string]json.RawMessage{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	settings := models.PagerDutyIntegrationSettings{
		ID:                  integration.ID,
		DefaultRepositoryID: integration.DefaultRepositoryID,
		WritebackEnabled:    integration.WritebackEnabled,
		AutoCreateWebhook:   integration.AutoCreateWebhook,
		Status:              integration.Status,
		LastError:           integration.LastError,
	}
	if value, exists := raw["default_repository_id"]; exists {
		repoID, parseOK := parseNullableUUIDField(w, r, value, "default_repository_id")
		if !parseOK {
			return
		}
		if repoID != nil && *repoID != uuid.Nil {
			if !h.validatePagerDutyRepository(w, r, orgID, *repoID, "default_repository_id") {
				return
			}
		}
		settings.DefaultRepositoryID = repoID
	}
	if value, exists := raw["writeback_enabled"]; exists {
		v, parseOK := parseBoolField(w, r, value, "writeback_enabled")
		if !parseOK {
			return
		}
		settings.WritebackEnabled = v
	}
	if value, exists := raw["auto_create_webhook"]; exists {
		v, parseOK := parseBoolField(w, r, value, "auto_create_webhook")
		if !parseOK {
			return
		}
		settings.AutoCreateWebhook = v
	}
	shouldAutoCreateWebhook := settings.AutoCreateWebhook && !integration.AutoCreateWebhook
	if value, exists := raw["status"]; exists {
		var status models.PagerDutyIntegrationStatus
		if err := json.Unmarshal(value, &status); err != nil || status == "" {
			writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "status must be a valid PagerDuty integration status")
			return
		}
		if err := status.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", err.Error())
			return
		}
		settings.Status = status
	}
	if shouldAutoCreateWebhook {
		cfg, ok := h.resolveCredentialForPagerDutyIntegration(w, r, orgID, integration)
		if !ok {
			return
		}
		if err := h.createDefaultPagerDutyWebhookSubscription(r.Context(), r, integration, cfg); err != nil {
			writeError(w, r, http.StatusBadGateway, "PAGERDUTY_WEBHOOK_CREATE_FAILED", "failed to auto-create PagerDuty webhook subscription", err)
			return
		}
	}
	updated, err := h.pagerDutyIntegrations.UpdateSettings(r.Context(), orgID, settings)
	if err != nil {
		// UpdateSettings re-validates default_repository_id ownership/active
		// state inside the UPDATE ... RETURNING, so a zero-row result (e.g. the
		// integration or repo changed between validation and write) surfaces as
		// ErrNoRows. That's a client-correctable condition, not a server fault.
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "PagerDuty integration not found or default repository is no longer valid")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update PagerDuty settings", err)
		return
	}
	h.emitAudit(r, models.AuditActionIntegrationUpdated, models.AuditResourceIntegration, updated.ID, nil, map[string]any{
		"provider":              string(models.IntegrationProviderPagerDuty),
		"status":                updated.Status,
		"default_repository_id": updated.DefaultRepositoryID,
		"writeback_enabled":     updated.WritebackEnabled,
		"auto_create_webhook":   updated.AutoCreateWebhook,
	})
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PagerDutyIntegration]{Data: updated})
}

func (h *PagerDutyIntegrationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if h == nil || h.pagerDutyIntegrations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty integrations are not configured")
		return
	}
	if h.integrations != nil {
		integrations, err := h.integrations.ListReusableForReconnect(r.Context(), orgID, models.IntegrationProviderPagerDuty)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list PagerDuty integrations", err)
			return
		}
		for _, integration := range integrations {
			if integration.Status == models.IntegrationStatusInactive {
				continue
			}
			if err := h.integrations.UpdateStatus(r.Context(), orgID, integration.ID, models.IntegrationStatusInactive); err != nil {
				writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to disconnect PagerDuty integration", err)
				return
			}
		}
	}
	if err := h.pagerDutyIntegrations.DeactivateAll(r.Context(), orgID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to deactivate PagerDuty provider integrations", err)
		return
	}
	if h.credentialDisabler != nil {
		if err := h.credentialDisabler.Disable(r.Context(), orgID, models.ProviderPagerDuty); err != nil {
			writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to disable legacy PagerDuty credentials", err)
			return
		}
		if err := h.credentialDisabler.DisableLabeled(r.Context(), orgID, models.ProviderPagerDuty); err != nil {
			writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to disable labeled PagerDuty credentials", err)
			return
		}
	}
	h.emitAudit(r, models.AuditActionIntegrationDisconnected, models.AuditResourceIntegration, uuid.Nil, nil, map[string]any{
		"provider": string(models.IntegrationProviderPagerDuty),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *PagerDutyIntegrationHandler) Test(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, cfg, ok := h.resolveIntegrationCredentialForRequest(w, r, orgID)
	if !ok {
		return
	}
	if err := h.client.TestCredential(r.Context(), cfg); err != nil {
		writeError(w, r, http.StatusBadGateway, "PAGERDUTY_TEST_FAILED", "failed to validate PagerDuty credentials", err)
		return
	}
	webhookSummary, err := h.pagerDutyWebhookFailureSummary(r.Context(), orgID, integration)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PAGERDUTY_WEBHOOK_HEALTH_FAILED", "failed to inspect PagerDuty webhook failures", err)
		return
	}
	symptoms := pagerDutyHealthSymptoms(integration, cfg, true)
	if webhookSummary.Count > 0 {
		symptoms = append(symptoms, "recent_webhook_failures")
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PagerDutyHealth]{Data: models.PagerDutyHealth{
		Integration:             integration,
		CredentialConfigured:    cfg.AccessToken != "",
		AuthOK:                  true,
		WebhookSecretConfigured: cfg.WebhookSecret != "",
		RecentWebhookFailures:   webhookSummary.Count,
		LatestWebhookError:      webhookSummary.LatestError,
		LatestWebhookFailureAt:  webhookSummary.LatestFailureAt,
		LastHealthCheckAt:       integration.LastHealthCheckAt,
		LastSyncedAt:            integration.LastSyncedAt,
		LastError:               integration.LastError,
		WritebackEnabled:        integration.WritebackEnabled,
		AutoCreateWebhook:       integration.AutoCreateWebhook,
		Symptoms:                symptoms,
	}})
}

func (h *PagerDutyIntegrationHandler) pagerDutyWebhookFailureSummary(ctx context.Context, orgID uuid.UUID, integration models.PagerDutyIntegration) (models.PagerDutyWebhookFailureSummary, error) {
	if h == nil || h.webhookFailures == nil || integration.IntegrationID == nil {
		return models.PagerDutyWebhookFailureSummary{}, nil
	}
	return h.webhookFailures.SummarizeRecentFailuresForIntegration(ctx, orgID, *integration.IntegrationID, string(models.IntegrationProviderPagerDuty), time.Now().Add(-24*time.Hour))
}

func pagerDutyHealthSymptoms(integration models.PagerDutyIntegration, cfg models.PagerDutyConfig, authOK bool) []string {
	symptoms := []string{}
	if !authOK {
		symptoms = append(symptoms, "auth_failed")
	}
	if integration.Status == models.PagerDutyIntegrationStatusDegraded {
		symptoms = append(symptoms, "integration_degraded")
	}
	if integration.Status == models.PagerDutyIntegrationStatusInactive {
		symptoms = append(symptoms, "integration_inactive")
	}
	if integration.LastError != nil && strings.TrimSpace(*integration.LastError) != "" {
		symptoms = append(symptoms, "last_error")
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		symptoms = append(symptoms, "webhook_secret_missing")
	}
	if integration.AutoCreateWebhook && strings.TrimSpace(cfg.WebhookSecret) == "" {
		symptoms = append(symptoms, "auto_webhook_secret_missing")
	}
	if integration.WritebackEnabled && strings.TrimSpace(cfg.AccessToken) == "" {
		symptoms = append(symptoms, "writeback_credential_missing")
	}
	return symptoms
}

func (h *PagerDutyIntegrationHandler) ListServices(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	_, cfg, ok := h.resolveIntegrationCredentialForRequest(w, r, orgID)
	if !ok {
		return
	}
	services, err := h.client.ListServices(r.Context(), cfg)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "PAGERDUTY_SERVICES_FAILED", "failed to list PagerDuty services", err)
		return
	}
	if services == nil {
		services = []models.PagerDutyServiceSummary{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PagerDutyServiceSummary]{Data: services})
}

func (h *PagerDutyIntegrationHandler) GetWebhookSetup(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, cfg, ok := h.resolveIntegrationCredentialForRequest(w, r, orgID)
	if !ok {
		return
	}
	if integration.IntegrationID == nil || *integration.IntegrationID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_INTEGRATION_LINK", "PagerDuty integration is not linked to a generic integration")
		return
	}
	webhookURL := h.pagerDutyWebhookURL(r, integration, "/api/v1/webhooks/pagerduty")
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PagerDutyWebhookSetup]{Data: models.PagerDutyWebhookSetup{
		PagerDutyIntegrationID:  integration.ID,
		IntegrationID:           *integration.IntegrationID,
		WebhookURL:              webhookURL,
		WebhookSecretConfigured: cfg.WebhookSecret != "",
	}})
}

func (h *PagerDutyIntegrationHandler) CreateWebhookSetup(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, cfg, ok := h.resolveIntegrationCredentialForRequest(w, r, orgID)
	if !ok {
		return
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		writeError(w, r, http.StatusBadRequest, "WEBHOOK_SECRET_REQUIRED", "configure a PagerDuty webhook secret before creating webhook subscriptions")
		return
	}
	if integration.IntegrationID == nil || *integration.IntegrationID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_INTEGRATION_LINK", "PagerDuty integration is not linked to a generic integration")
		return
	}

	var req models.PagerDutyWebhookSetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	serviceID := strings.TrimSpace(req.ServiceID)
	teamID := ""
	if req.TeamID != nil {
		teamID = strings.TrimSpace(*req.TeamID)
	}
	if serviceID == "" && teamID == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FILTER", "service_id or team_id is required")
		return
	}
	if serviceID != "" && teamID != "" {
		writeError(w, r, http.StatusBadRequest, "AMBIGUOUS_FILTER", "provide only one of service_id or team_id")
		return
	}
	events, ok := pagerDutyWebhookEventsForRequest(w, r, req.Events)
	if !ok {
		return
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = "143 incident automation"
	}
	filterID := serviceID
	filterType := "service_reference"
	if teamID != "" {
		filterID = teamID
		filterType = "team_reference"
	}
	webhookURL := h.pagerDutyWebhookURL(r, integration, "/api/v1/webhooks/pagerduty")
	subscription, err := h.client.CreateWebhookSubscription(r.Context(), cfg, pagerDutyWebhookSubscriptionRequest{
		WebhookURL:  webhookURL,
		Secret:      cfg.WebhookSecret,
		Description: description,
		FilterID:    filterID,
		FilterType:  filterType,
		Events:      events,
	})
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "PAGERDUTY_WEBHOOK_CREATE_FAILED", "failed to create PagerDuty webhook subscription", err)
		return
	}
	eventNames := make([]string, 0, len(events))
	for _, event := range events {
		eventNames = append(eventNames, string(event))
	}
	h.emitAudit(r, models.AuditActionIntegrationUpdated, models.AuditResourceIntegration, integration.ID, nil, map[string]any{
		"provider":                    string(models.IntegrationProviderPagerDuty),
		"webhook_subscription_id":     subscription.ID,
		"webhook_subscription_filter": filterType,
		"webhook_subscription_events": eventNames,
	})
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.PagerDutyWebhookSetup]{Data: models.PagerDutyWebhookSetup{
		PagerDutyIntegrationID:  integration.ID,
		IntegrationID:           *integration.IntegrationID,
		WebhookURL:              webhookURL,
		WebhookSecretConfigured: true,
		WebhookSubscriptionID:   stringPtrOrNilPagerDutyIntegration(subscription.ID),
		ServiceID:               stringPtrOrNilPagerDutyIntegration(serviceID),
		TeamID:                  stringPtrOrNilPagerDutyIntegration(teamID),
		Events:                  eventNames,
	}})
}

func (h *PagerDutyIntegrationHandler) ListIncidents(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if h == nil || h.incidents == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty incidents are not configured")
		return
	}
	var integrationID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("integration_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid integration_id")
			return
		}
		integrationID = &id
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	incidents, err := h.incidents.List(r.Context(), orgID, db.PagerDutyIncidentListFilter{
		IntegrationID: integrationID,
		Status:        strings.TrimSpace(r.URL.Query().Get("status")),
		ServiceID:     strings.TrimSpace(r.URL.Query().Get("service_id")),
		Limit:         limit,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list PagerDuty incidents", err)
		return
	}
	if incidents == nil {
		incidents = []models.PagerDutyIncident{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PagerDutyIncident]{Data: incidents})
}

func (h *PagerDutyIntegrationHandler) GetIncident(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if h == nil || h.incidents == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty incidents are not configured")
		return
	}
	incidentID := strings.TrimSpace(chi.URLParam(r, "incident_id"))
	if incidentID == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_INCIDENT", "incident_id is required")
		return
	}
	integrationID, ok := parseOptionalPagerDutyIntegrationID(w, r, r.URL.Query().Get("pagerduty_integration_id"), r.URL.Query().Get("integration_id"))
	if !ok {
		return
	}
	incident, err := h.lookupPagerDutyIncident(r.Context(), orgID, integrationID, incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "PagerDuty incident not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to load PagerDuty incident", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PagerDutyIncident]{Data: incident})
}

func (h *PagerDutyIntegrationHandler) StartIncidentSession(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if h == nil || h.incidents == nil || h.sessionStarter == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty incident session start is not configured")
		return
	}
	incidentID := strings.TrimSpace(chi.URLParam(r, "incident_id"))
	if incidentID == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_INCIDENT", "incident_id is required")
		return
	}
	var req struct {
		PagerDutyIntegrationID *uuid.UUID `json:"pagerduty_integration_id"`
		RepositoryID           *uuid.UUID `json:"repository_id"`
		BaseBranch             *string    `json:"base_branch"`
		Message                string     `json:"message"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
			return
		}
	}
	integrationID := req.PagerDutyIntegrationID
	if integrationID == nil {
		parsed, ok := parseOptionalPagerDutyIntegrationID(w, r, r.URL.Query().Get("pagerduty_integration_id"), r.URL.Query().Get("integration_id"))
		if !ok {
			return
		}
		integrationID = parsed
	}
	incident, err := h.lookupPagerDutyIncident(r.Context(), orgID, integrationID, incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "PagerDuty incident not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to load PagerDuty incident", err)
		return
	}
	repositoryID, baseBranch, ok := h.resolveIncidentSessionRepository(w, r, orgID, incident, req.RepositoryID, req.BaseBranch)
	if !ok {
		return
	}
	var userID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		userID = &user.ID
	}
	session, err := h.sessionStarter.StartSession(r.Context(), pagerdutysvc.StartSessionInput{
		OrgID:        orgID,
		Incident:     incident,
		RepositoryID: repositoryID,
		BaseBranch:   baseBranch,
		UserID:       userID,
		Message:      strings.TrimSpace(req.Message),
	})
	if err != nil {
		if errors.Is(err, pagerdutysvc.ErrPagerDutySessionAlreadyRunning) {
			writeError(w, r, http.StatusConflict, "PAGERDUTY_SESSION_ALREADY_RUNNING", "a session is already running for this PagerDuty incident")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "SESSION_START_FAILED", "failed to start PagerDuty incident session", err)
		return
	}
	h.emitAudit(r, models.AuditActionSessionCreated, models.AuditResourceSession, session.ID, &session.ID, map[string]any{
		"provider":      string(models.IntegrationProviderPagerDuty),
		"incident_id":   incident.IncidentID,
		"repository_id": repositoryID,
	})
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Session]{Data: session})
}

func (h *PagerDutyIntegrationHandler) ListMappings(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if h == nil || h.mappings == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty mappings are not configured")
		return
	}
	integrationID, ok := parsePagerDutyIntegrationIDQuery(w, r)
	if !ok {
		return
	}
	mappings, err := h.mappings.ListByIntegration(r.Context(), orgID, integrationID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list PagerDuty service mappings", err)
		return
	}
	if mappings == nil {
		mappings = []models.PagerDutyServiceRepoMapping{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PagerDutyServiceRepoMapping]{Data: mappings})
}

func (h *PagerDutyIntegrationHandler) UpsertMapping(w http.ResponseWriter, r *http.Request) {
	if h.pagerDutyDisabled(w, r) {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if h == nil || h.mappings == nil || h.pagerDutyIntegrations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty mappings are not configured")
		return
	}
	var req struct {
		PagerDutyIntegrationID uuid.UUID `json:"pagerduty_integration_id"`
		ServiceID              string    `json:"pagerduty_service_id"`
		ServiceName            string    `json:"pagerduty_service_name"`
		TeamID                 *string   `json:"pagerduty_team_id"`
		RepositoryID           uuid.UUID `json:"repository_id"`
		BaseBranch             *string   `json:"base_branch"`
		Enabled                *bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	serviceID := strings.TrimSpace(req.ServiceID)
	serviceName := strings.TrimSpace(req.ServiceName)
	if req.PagerDutyIntegrationID == uuid.Nil || serviceID == "" || serviceName == "" || req.RepositoryID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "pagerduty_integration_id, pagerduty_service_id, pagerduty_service_name, and repository_id are required")
		return
	}
	if _, err := h.pagerDutyIntegrations.GetByID(r.Context(), orgID, req.PagerDutyIntegrationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusBadRequest, "INVALID_PAGERDUTY_INTEGRATION_ID", "pagerduty_integration_id was not found in this org")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTEGRATION_LOOKUP_FAILED", "failed to validate PagerDuty integration", err)
		return
	}
	if !h.validatePagerDutyRepository(w, r, orgID, req.RepositoryID, "repository_id") {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	var createdBy *uuid.UUID
	if user != nil {
		createdBy = &user.ID
	}
	mapping := &models.PagerDutyServiceRepoMapping{
		OrgID:                  orgID,
		PagerDutyIntegrationID: req.PagerDutyIntegrationID,
		PagerDutyServiceID:     serviceID,
		PagerDutyServiceName:   serviceName,
		PagerDutyTeamID:        req.TeamID,
		RepositoryID:           req.RepositoryID,
		BaseBranch:             req.BaseBranch,
		Enabled:                enabled,
		CreatedBy:              createdBy,
	}
	if err := h.mappings.Upsert(r.Context(), mapping); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPSERT_FAILED", "failed to save PagerDuty service mapping", err)
		return
	}
	h.emitAudit(r, models.AuditActionIntegrationUpdated, models.AuditResourceIntegration, mapping.PagerDutyIntegrationID, nil, map[string]any{
		"provider":                 string(models.IntegrationProviderPagerDuty),
		"pagerduty_service_id":     mapping.PagerDutyServiceID,
		"pagerduty_service_name":   mapping.PagerDutyServiceName,
		"pagerduty_team_id":        mapping.PagerDutyTeamID,
		"repository_id":            mapping.RepositoryID,
		"base_branch":              mapping.BaseBranch,
		"enabled":                  mapping.Enabled,
		"pagerduty_integration_id": mapping.PagerDutyIntegrationID,
	})
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PagerDutyServiceRepoMapping]{Data: *mapping})
}

func (h *PagerDutyIntegrationHandler) ensureGenericIntegration(ctx context.Context, orgID uuid.UUID) (models.Integration, bool, error) {
	reusable, err := h.integrations.ListReusableForReconnect(ctx, orgID, models.IntegrationProviderPagerDuty)
	if err != nil {
		return models.Integration{}, false, err
	}
	if len(reusable) > 0 {
		integration := reusable[0]
		if integration.Status != models.IntegrationStatusActive {
			if err := h.integrations.UpdateStatus(ctx, orgID, integration.ID, models.IntegrationStatusActive); err != nil {
				return models.Integration{}, false, err
			}
			integration.Status = models.IntegrationStatusActive
		}
		return integration, false, nil
	}
	integration := &models.Integration{
		OrgID:    orgID,
		Provider: models.IntegrationProviderPagerDuty,
		Status:   models.IntegrationStatusActive,
	}
	if err := h.integrations.Create(ctx, integration); err != nil {
		return models.Integration{}, false, err
	}
	return *integration, true, nil
}

func parsePagerDutyIntegrationIDQuery(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("integration_id"))
	if raw == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_INTEGRATION", "integration_id query parameter required")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid integration_id")
		return uuid.Nil, false
	}
	return id, true
}

func (h *PagerDutyIntegrationHandler) pagerDutyDisabled(w http.ResponseWriter, r *http.Request) bool {
	if h != nil && h.disabled {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_DISABLED", "PagerDuty integration is disabled")
		return true
	}
	return false
}

func (h *PagerDutyIntegrationHandler) resolveIncidentSessionRepository(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, incident models.PagerDutyIncident, requestedRepoID *uuid.UUID, requestedBaseBranch *string) (uuid.UUID, *string, bool) {
	if requestedRepoID != nil && *requestedRepoID != uuid.Nil {
		if !h.validatePagerDutyRepository(w, r, orgID, *requestedRepoID, "repository_id") {
			return uuid.Nil, nil, false
		}
		baseBranch := trimOptionalStringPointer(requestedBaseBranch)
		if !validatePagerDutyBaseBranch(w, r, baseBranch) {
			return uuid.Nil, nil, false
		}
		return *requestedRepoID, baseBranch, true
	}
	if h.mappings != nil && incident.ServiceID != nil && strings.TrimSpace(*incident.ServiceID) != "" {
		mapping, err := h.mappings.GetByServiceID(r.Context(), orgID, incident.PagerDutyIntegrationID, strings.TrimSpace(*incident.ServiceID))
		if err == nil {
			baseBranch := trimOptionalStringPointer(requestedBaseBranch)
			if baseBranch == nil {
				baseBranch = trimOptionalStringPointer(mapping.BaseBranch)
			}
			if !validatePagerDutyBaseBranch(w, r, baseBranch) {
				return uuid.Nil, nil, false
			}
			return mapping.RepositoryID, baseBranch, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "MAPPING_LOOKUP_FAILED", "failed to resolve PagerDuty service mapping", err)
			return uuid.Nil, nil, false
		}
	}
	if h.pagerDutyIntegrations != nil {
		integration, err := h.pagerDutyIntegrations.GetByID(r.Context(), orgID, incident.PagerDutyIntegrationID)
		if err == nil && integration.DefaultRepositoryID != nil && *integration.DefaultRepositoryID != uuid.Nil {
			baseBranch := trimOptionalStringPointer(requestedBaseBranch)
			if !validatePagerDutyBaseBranch(w, r, baseBranch) {
				return uuid.Nil, nil, false
			}
			return *integration.DefaultRepositoryID, baseBranch, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "INTEGRATION_LOOKUP_FAILED", "failed to resolve PagerDuty integration defaults", err)
			return uuid.Nil, nil, false
		}
	}
	writeError(w, r, http.StatusBadRequest, "REPOSITORY_UNMAPPED", "PagerDuty incident service is not mapped to a repository")
	return uuid.Nil, nil, false
}

// validatePagerDutyBaseBranch rejects a resolved base branch that isn't a safe
// git ref before it becomes a session TargetBranch (and reaches `git fetch
// origin <branch>`). A nil branch is allowed. On rejection it writes a 400 and
// returns false. Both the request-supplied branch and a stored service-mapping
// branch flow through here, so neither source can inject a git argument.
func validatePagerDutyBaseBranch(w http.ResponseWriter, r *http.Request, branch *string) bool {
	if branch == nil {
		return true
	}
	if !isValidGitRef(*branch) {
		writeError(w, r, http.StatusBadRequest, "INVALID_BASE_BRANCH", "base_branch is not a valid git branch name")
		return false
	}
	return true
}

func (h *PagerDutyIntegrationHandler) lookupPagerDutyIncident(ctx context.Context, orgID uuid.UUID, integrationID *uuid.UUID, incidentID string) (models.PagerDutyIncident, error) {
	if integrationID != nil && *integrationID != uuid.Nil {
		return h.incidents.GetByIncidentID(ctx, orgID, *integrationID, incidentID)
	}
	return h.incidents.GetLatestByIncidentID(ctx, orgID, incidentID)
}

func parseOptionalPagerDutyIntegrationID(w http.ResponseWriter, r *http.Request, values ...string) (*uuid.UUID, bool) {
	raw := strings.TrimSpace(firstNonEmptyPagerDutyIntegration(values...))
	if raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PAGERDUTY_INTEGRATION_ID", "pagerduty_integration_id must be a valid uuid")
		return nil, false
	}
	if id == uuid.Nil {
		return nil, true
	}
	return &id, true
}

func trimOptionalStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func (h *PagerDutyIntegrationHandler) resolvePagerDutyIntegrationForRequest(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (models.PagerDutyIntegration, bool) {
	rawID := strings.TrimSpace(firstNonEmptyPagerDutyIntegration(r.URL.Query().Get("id"), r.URL.Query().Get("pagerduty_integration_id")))
	if rawID != "" {
		id, err := uuid.Parse(rawID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid PagerDuty integration id")
			return models.PagerDutyIntegration{}, false
		}
		integration, err := h.pagerDutyIntegrations.GetByID(r.Context(), orgID, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "NOT_FOUND", "PagerDuty integration not found")
				return models.PagerDutyIntegration{}, false
			}
			writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to load PagerDuty integration", err)
			return models.PagerDutyIntegration{}, false
		}
		return integration, true
	}
	integrations, err := h.pagerDutyIntegrations.ListManageable(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to load PagerDuty integration", err)
		return models.PagerDutyIntegration{}, false
	}
	if len(integrations) == 0 || integrations[0].ID == uuid.Nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "PagerDuty integration not found")
		return models.PagerDutyIntegration{}, false
	}
	return integrations[0], true
}

func (h *PagerDutyIntegrationHandler) resolveIntegrationCredentialForRequest(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (models.PagerDutyIntegration, models.PagerDutyConfig, bool) {
	if h == nil || h.pagerDutyIntegrations == nil || h.credentialReader == nil || h.client == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty integrations are not configured")
		return models.PagerDutyIntegration{}, models.PagerDutyConfig{}, false
	}
	integration, ok := h.resolvePagerDutyIntegrationForRequest(w, r, orgID)
	if !ok {
		return models.PagerDutyIntegration{}, models.PagerDutyConfig{}, false
	}
	cfg, ok := h.resolveCredentialForPagerDutyIntegration(w, r, orgID, integration)
	if !ok {
		return models.PagerDutyIntegration{}, models.PagerDutyConfig{}, false
	}
	return integration, cfg, true
}

func (h *PagerDutyIntegrationHandler) resolveCredentialForPagerDutyIntegration(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, integration models.PagerDutyIntegration) (models.PagerDutyConfig, bool) {
	if h == nil || h.credentialReader == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_UNAVAILABLE", "PagerDuty credentials are not configured")
		return models.PagerDutyConfig{}, false
	}
	credentialID, err := pagerDutyCredentialIDFromRef(integration.CredentialRef)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CREDENTIAL_REF", "PagerDuty credential reference is invalid", err)
		return models.PagerDutyConfig{}, false
	}
	credential, err := h.credentialReader.GetByID(r.Context(), orgID, credentialID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "CREDENTIAL_NOT_FOUND", "PagerDuty credential not found")
			return models.PagerDutyConfig{}, false
		}
		writeError(w, r, http.StatusInternalServerError, "CREDENTIAL_LOOKUP_FAILED", "failed to load PagerDuty credentials", err)
		return models.PagerDutyConfig{}, false
	}
	cfg, ok := credential.Config.(models.PagerDutyConfig)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "INVALID_CREDENTIAL", "stored credential is not a PagerDuty credential")
		return models.PagerDutyConfig{}, false
	}
	return cfg, true
}

func pagerDutyCredentialLabel(cfg models.PagerDutyConfig) string {
	region := strings.ToLower(strings.TrimSpace(cfg.ServiceRegion))
	if region == "" {
		region = "us"
	}
	account := strings.ToLower(strings.TrimSpace(cfg.AccountSubdomain))
	if account == "" {
		account = "default"
	}
	return "pagerduty:" + region + ":" + account
}

func pagerDutyCredentialIDFromRef(ref string) (uuid.UUID, error) {
	value, ok := strings.CutPrefix(strings.TrimSpace(ref), "org_credential:")
	if !ok {
		return uuid.Nil, fmt.Errorf("unsupported credential ref %q", ref)
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (h *PagerDutyIntegrationHandler) validatePagerDutyRepository(w http.ResponseWriter, r *http.Request, orgID, repoID uuid.UUID, field string) bool {
	if _, err := requireActiveRepo(r.Context(), h.repositories, orgID, repoID); err != nil {
		switch {
		case errors.Is(err, errRepoDisconnected):
			writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", field+" is disconnected")
		case errors.Is(err, errRepoStoreUnconfigured):
			writeError(w, r, http.StatusServiceUnavailable, "REPOSITORY_LOOKUP_UNAVAILABLE", "repository validation is not configured")
		default:
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", field+" was not found in this org")
		}
		return false
	}
	return true
}

func parseNullableUUIDField(w http.ResponseWriter, r *http.Request, raw json.RawMessage, field string) (*uuid.UUID, bool) {
	if string(raw) == "null" {
		return nil, true
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_FIELD", field+" must be a uuid string or null")
		return nil, false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, true
	}
	id, err := uuid.Parse(value)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_FIELD", field+" must be a valid uuid")
		return nil, false
	}
	return &id, true
}

func parseBoolField(w http.ResponseWriter, r *http.Request, raw json.RawMessage, field string) (bool, bool) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_FIELD", field+" must be a boolean")
		return false, false
	}
	return value, true
}

func firstNonEmptyPagerDutyIntegration(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func pagerDutyWebhookEventsForRequest(w http.ResponseWriter, r *http.Request, requested []models.PagerDutyEventType) ([]models.PagerDutyEventType, bool) {
	events := requested
	if len(events) == 0 {
		events = defaultPagerDutyWebhookEvents()
	}
	seen := map[models.PagerDutyEventType]bool{}
	out := make([]models.PagerDutyEventType, 0, len(events))
	for _, event := range events {
		if err := event.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_EVENT", err.Error())
			return nil, false
		}
		if seen[event] {
			continue
		}
		seen[event] = true
		out = append(out, event)
	}
	if len(out) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_EVENT", "at least one PagerDuty event is required")
		return nil, false
	}
	return out, true
}

func defaultPagerDutyWebhookEvents() []models.PagerDutyEventType {
	return []models.PagerDutyEventType{
		models.PagerDutyEventIncidentTriggered,
		models.PagerDutyEventIncidentAcknowledged,
		models.PagerDutyEventIncidentUnacknowledged,
		models.PagerDutyEventIncidentReassigned,
		models.PagerDutyEventIncidentEscalated,
		models.PagerDutyEventIncidentPriorityUpdated,
		models.PagerDutyEventIncidentAnnotated,
		models.PagerDutyEventIncidentStatusUpdatePublished,
		models.PagerDutyEventIncidentReopened,
		models.PagerDutyEventIncidentResolved,
	}
}

func (h *PagerDutyIntegrationHandler) createDefaultPagerDutyWebhookSubscription(ctx context.Context, r *http.Request, integration models.PagerDutyIntegration, cfg models.PagerDutyConfig) error {
	if h == nil || h.client == nil {
		return fmt.Errorf("PagerDuty client is not configured")
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		return fmt.Errorf("PagerDuty webhook secret is required")
	}
	if integration.IntegrationID == nil || *integration.IntegrationID == uuid.Nil {
		return fmt.Errorf("PagerDuty integration is not linked to a generic integration")
	}
	filterID := integration.ID.String()
	if integration.AccountSubdomain != nil && strings.TrimSpace(*integration.AccountSubdomain) != "" {
		filterID = strings.TrimSpace(*integration.AccountSubdomain)
	}
	webhookURL := h.pagerDutyWebhookURL(r, integration, "/api/v1/webhooks/pagerduty")
	_, err := h.client.CreateWebhookSubscription(ctx, cfg, pagerDutyWebhookSubscriptionRequest{
		WebhookURL:  webhookURL,
		Secret:      cfg.WebhookSecret,
		Description: "143 incident automation",
		FilterID:    filterID,
		FilterType:  "account_reference",
		Events:      defaultPagerDutyWebhookEvents(),
	})
	return err
}

func (h *PagerDutyIntegrationHandler) pagerDutyWebhookURL(r *http.Request, integration models.PagerDutyIntegration, path string) string {
	query := url.Values{}
	if integration.IntegrationID != nil && *integration.IntegrationID != uuid.Nil {
		query.Set("integration_id", integration.IntegrationID.String())
	}
	if integration.ID != uuid.Nil {
		query.Set("pagerduty_integration_id", integration.ID.String())
	}
	return h.publicBaseURL(r) + path + "?" + query.Encode()
}

func (h *PagerDutyIntegrationHandler) publicBaseURL(r *http.Request) string {
	if h.baseURL != "" {
		return h.baseURL
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *PagerDutyIntegrationHandler) oauthRedirectURL() string {
	return strings.TrimRight(h.baseURL, "/") + "/api/v1/integrations/pagerduty/callback"
}

func (h *PagerDutyIntegrationHandler) oauthFrontendRedirectURL(query string) string {
	base := h.frontendURL
	if base == "" {
		base = h.baseURL
	}
	if base == "" {
		base = "/"
	}
	return strings.TrimRight(base, "/") + "/integrations?" + query
}

func (h *PagerDutyIntegrationHandler) emitAudit(r *http.Request, action models.AuditAction, resourceType models.AuditResourceType, resourceID uuid.UUID, sessionID *uuid.UUID, details map[string]any) {
	if h == nil || h.audit == nil {
		return
	}
	var resourceIDStr *string
	if resourceID != uuid.Nil {
		value := resourceID.String()
		resourceIDStr = &value
	}
	emitUserAuditWithSession(h.audit, r, action, resourceType, resourceIDStr, sessionID, nil, marshalAuditDetails(h.logger, details))
}

func stringPtrOrNilPagerDutyIntegration(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

type pagerDutyRESTClient struct {
	httpClient *http.Client
	metrics    *metrics.PagerDutyMetrics
}

func (c pagerDutyRESTClient) TestCredential(ctx context.Context, cfg models.PagerDutyConfig) error {
	var out struct {
		User map[string]any `json:"user"`
	}
	err := c.do(ctx, cfg, http.MethodGet, "/users/me", nil, &out)
	c.recordAPIRequest(ctx, "test_credential", err)
	return err
}

func (c pagerDutyRESTClient) ListServices(ctx context.Context, cfg models.PagerDutyConfig) ([]models.PagerDutyServiceSummary, error) {
	// Page through the full service list rather than capping at one page, so
	// orgs with more than a page of services get complete repo-mapping options.
	// A page cap bounds the worst case (PagerDuty's classic REST limit is 100).
	const pageLimit = 100
	const maxPages = 50
	services := make([]models.PagerDutyServiceSummary, 0, pageLimit)
	offset := 0
	for page := 0; page < maxPages; page++ {
		var out struct {
			Services []struct {
				ID               string `json:"id"`
				Summary          string `json:"summary"`
				HTMLURL          string `json:"html_url"`
				EscalationPolicy struct {
					Summary string `json:"summary"`
				} `json:"escalation_policy"`
				Teams []struct {
					ID string `json:"id"`
				} `json:"teams"`
			} `json:"services"`
			More bool `json:"more"`
		}
		path := fmt.Sprintf("/services?limit=%d&offset=%d", pageLimit, offset)
		if err := c.do(ctx, cfg, http.MethodGet, path, nil, &out); err != nil {
			c.recordAPIRequest(ctx, "list_services", err)
			return nil, err
		}
		for _, service := range out.Services {
			teamIDs := make([]string, 0, len(service.Teams))
			for _, team := range service.Teams {
				if strings.TrimSpace(team.ID) != "" {
					teamIDs = append(teamIDs, team.ID)
				}
			}
			services = append(services, models.PagerDutyServiceSummary{
				ID:               service.ID,
				Summary:          service.Summary,
				HTMLURL:          service.HTMLURL,
				EscalationPolicy: service.EscalationPolicy.Summary,
				TeamIDs:          teamIDs,
			})
		}
		if !out.More || len(out.Services) == 0 {
			break
		}
		offset += len(out.Services)
	}
	c.recordAPIRequest(ctx, "list_services", nil)
	return services, nil
}

func (c pagerDutyRESTClient) CreateWebhookSubscription(ctx context.Context, cfg models.PagerDutyConfig, req pagerDutyWebhookSubscriptionRequest) (pagerDutyWebhookSubscription, error) {
	events := make([]string, 0, len(req.Events))
	for _, event := range req.Events {
		events = append(events, string(event))
	}
	deliveryMethod := map[string]any{
		"type": "http_delivery_method",
		"url":  req.WebhookURL,
		"custom_headers": []map[string]string{{
			"name":  "X-143-PagerDuty-Secret",
			"value": req.Secret,
		}},
	}
	// When the secret is long enough for PagerDuty to sign with, register it as
	// the delivery-method signing secret so PagerDuty stamps each delivery with
	// an `X-PagerDuty-Signature: v1=...` HMAC over the raw body. The webhook
	// handler prefers that body-bound signature over the shared-secret header.
	// PagerDuty requires the signing secret to be at least pagerDutySigningSecretMinLen.
	if len(req.Secret) >= pagerDutySigningSecretMinLen {
		deliveryMethod["secret"] = req.Secret
	}
	body := map[string]any{
		"webhook_subscription": map[string]any{
			"type":            "webhook_subscription",
			"active":          true,
			"description":     req.Description,
			"events":          events,
			"delivery_method": deliveryMethod,
			"filter": map[string]string{
				"id":   req.FilterID,
				"type": req.FilterType,
			},
		},
	}
	var out struct {
		WebhookSubscription struct {
			ID string `json:"id"`
		} `json:"webhook_subscription"`
	}
	if err := c.do(ctx, cfg, http.MethodPost, "/webhook_subscriptions", bodyReaderPagerDutyIntegration(body), &out); err != nil {
		c.recordAPIRequest(ctx, "create_webhook_subscription", err)
		return pagerDutyWebhookSubscription{}, err
	}
	c.recordAPIRequest(ctx, "create_webhook_subscription", nil)
	return pagerDutyWebhookSubscription{ID: out.WebhookSubscription.ID}, nil
}

func (c pagerDutyRESTClient) ExchangeOAuthCode(ctx context.Context, req pagerDutyOAuthExchangeRequest) (pagerDutyOAuthToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {req.ClientID},
		"client_secret": {req.ClientSecret},
		"code":          {req.Code},
		"redirect_uri":  {req.RedirectURI},
	}
	httpClient := c.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, pagerDutyTokenURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return pagerDutyOAuthToken{}, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		c.recordAPIRequest(ctx, "oauth_token", err)
		return pagerDutyOAuthToken{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(raw) > 0 {
			err := fmt.Errorf("PagerDuty OAuth token exchange returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
			c.recordAPIRequest(ctx, "oauth_token", err)
			return pagerDutyOAuthToken{}, err
		}
		err := fmt.Errorf("PagerDuty OAuth token exchange returned %d", resp.StatusCode)
		c.recordAPIRequest(ctx, "oauth_token", err)
		return pagerDutyOAuthToken{}, err
	}
	var token pagerDutyOAuthToken
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		c.recordAPIRequest(ctx, "oauth_token", err)
		return pagerDutyOAuthToken{}, err
	}
	c.recordAPIRequest(ctx, "oauth_token", nil)
	return token, nil
}

func (c pagerDutyRESTClient) do(ctx context.Context, cfg models.PagerDutyConfig, method, path string, body io.Reader, out any) error {
	token := strings.TrimSpace(cfg.AccessToken)
	if token == "" {
		return fmt.Errorf("PagerDuty access token is required")
	}
	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, method, pagerDutyAPIBaseURL(cfg)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.pagerduty+json;version=2")
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(raw) > 0 {
			return fmt.Errorf("PagerDuty API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return fmt.Errorf("PagerDuty API returned %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func bodyReaderPagerDutyIntegration(body any) io.Reader {
	raw, err := json.Marshal(body)
	if err != nil {
		return strings.NewReader("{}")
	}
	return bytes.NewReader(raw)
}

func (c pagerDutyRESTClient) recordAPIRequest(ctx context.Context, endpoint string, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	c.metrics.RecordAPIRequest(ctx, endpoint, result)
}

func pagerDutyAPIBaseURL(cfg models.PagerDutyConfig) string {
	return cfg.APIBaseURL()
}
