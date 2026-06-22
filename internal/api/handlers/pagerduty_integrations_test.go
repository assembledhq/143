package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	pagerdutysvc "github.com/assembledhq/143/internal/services/pagerduty"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPagerDutyIntegrationHandler_ConnectStoresCredentialAndInstall(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	genericIntegrationID := uuid.New()
	credentialID := uuid.New()
	providerIntegrationID := uuid.New()

	generic := &pagerDutyGenericIntegrationStoreFake{integrationID: genericIntegrationID}
	credentials := &pagerDutyCredentialWriterFake{credentialID: credentialID}
	provider := &pagerDutyProviderIntegrationWriterFake{providerIntegrationID: providerIntegrationID}
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()
	expectAuditInsert(mock)
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		Integrations:          generic,
		Credentials:           credentials,
		PagerDutyIntegrations: provider,
		Logger:                testLoggerPagerDutyWebhook(),
	})
	handler.SetAuditEmitter(newAuditEmitterForTest(mock))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/pagerduty/connect", strings.NewReader(`{
		"access_token": "access",
		"refresh_token": "refresh",
		"webhook_secret": "secret",
		"account_subdomain": "acme",
		"service_region": "us",
		"scopes": ["incidents.read", "services.read"]
	}`))
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
	rec := httptest.NewRecorder()

	handler.Connect(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "Connect should create the PagerDuty install")
	require.Equal(t, models.ProviderPagerDuty, credentials.cfg.Provider(), "Connect should store PagerDuty credential config")
	cfg := credentials.cfg.(models.PagerDutyConfig)
	require.Equal(t, "access", cfg.AccessToken, "Connect should persist OAuth access token")
	require.Equal(t, "refresh", cfg.RefreshToken, "Connect should persist OAuth refresh token")
	require.Equal(t, "secret", cfg.WebhookSecret, "Connect should persist webhook shared secret")
	require.Equal(t, orgID, provider.integration.OrgID, "provider integration should be org-scoped")
	require.Equal(t, &genericIntegrationID, provider.integration.IntegrationID, "provider integration should link to generic integration")
	require.Equal(t, "org_credential:"+credentialID.String(), provider.integration.CredentialRef, "provider integration should reference the credential row")
	require.Equal(t, "acme", *provider.integration.AccountSubdomain, "provider integration should preserve account subdomain")
	require.Equal(t, models.PagerDutyOAuthModeScoped, provider.integration.OAuthMode, "provider integration should default to scoped OAuth mode")
	require.Equal(t, []string{"incidents.read", "services.read"}, provider.integration.Scopes, "provider integration should preserve OAuth scopes")

	var resp models.SingleResponse[models.PagerDutyIntegration]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "Connect response should be valid JSON")
	require.Equal(t, providerIntegrationID, resp.Data.ID, "Connect response should return provider integration")
	require.NoError(t, mock.ExpectationsWereMet(), "Connect should emit an integration audit row")
}

func TestPagerDutyIntegrationHandler_ConnectCreatesDistinctInstallForSecondAccount(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	genericIntegrationID := uuid.New()
	existingProviderIntegrationID := uuid.New()
	newCredentialID := uuid.New()
	newProviderIntegrationID := uuid.New()

	generic := &pagerDutyGenericIntegrationStoreFake{existing: []models.Integration{{
		ID:       genericIntegrationID,
		OrgID:    orgID,
		Provider: models.IntegrationProviderPagerDuty,
		Status:   models.IntegrationStatusActive,
	}}}
	credentials := &pagerDutyCredentialWriterFake{credentialID: newCredentialID}
	provider := &pagerDutyProviderIntegrationWriterFake{
		providerIntegrationID: newProviderIntegrationID,
		existingByIntegrationID: &models.PagerDutyIntegration{
			ID:               existingProviderIntegrationID,
			OrgID:            orgID,
			IntegrationID:    &genericIntegrationID,
			AccountSubdomain: strPtrPagerDutyIntegrationTest("acme"),
			ServiceRegion:    "us",
			CredentialRef:    "org_credential:" + uuid.New().String(),
			Status:           models.PagerDutyIntegrationStatusActive,
		},
	}
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()
	expectAuditInsert(mock)
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		Integrations:          generic,
		Credentials:           credentials,
		PagerDutyIntegrations: provider,
		Logger:                testLoggerPagerDutyWebhook(),
	})
	handler.SetAuditEmitter(newAuditEmitterForTest(mock))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/pagerduty/connect", strings.NewReader(`{
		"access_token": "access",
		"refresh_token": "refresh",
		"webhook_secret": "secret",
		"account_subdomain": "beta",
		"service_region": "eu"
	}`))
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
	rec := httptest.NewRecorder()

	handler.Connect(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "Connect should create a new provider install for a different PagerDuty account")
	require.Equal(t, "pagerduty:eu:beta", credentials.label, "Connect should store credentials under a deterministic per-account label")
	require.Equal(t, newProviderIntegrationID, provider.integration.ID, "Connect should create a distinct provider integration row")
	require.Equal(t, "beta", *provider.integration.AccountSubdomain, "new provider integration should preserve the second account subdomain")
	require.Equal(t, "eu", provider.integration.ServiceRegion, "new provider integration should preserve the second account region")
	require.Equal(t, "org_credential:"+newCredentialID.String(), provider.integration.CredentialRef, "new provider integration should reference the second account credential")
	require.NotEqual(t, existingProviderIntegrationID, provider.integration.ID, "Connect should not return the existing account's provider integration")
	require.NoError(t, mock.ExpectationsWereMet(), "Connect should emit an audit row for the new provider integration")
}

func TestPagerDutyIntegrationHandler_StartOAuthRedirectsToPagerDuty(t *testing.T) {
	t.Parallel()

	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		OAuthClientID:     "pd-client",
		OAuthClientSecret: "pd-secret",
		BaseURL:           "https://api.143.dev",
		Logger:            testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/pagerduty/login", nil)
	rec := httptest.NewRecorder()

	handler.StartOAuth(rec, req)

	require.Equal(t, http.StatusTemporaryRedirect, rec.Code, "StartOAuth should redirect to PagerDuty authorize")
	location := rec.Header().Get("Location")
	require.NotEmpty(t, location, "StartOAuth should set Location header")
	authorizeURL, err := url.Parse(location)
	require.NoError(t, err, "StartOAuth redirect should be a valid URL")
	require.Equal(t, "identity.pagerduty.com", authorizeURL.Host, "StartOAuth should use PagerDuty identity host")
	require.Equal(t, "/oauth/authorize", authorizeURL.Path, "StartOAuth should use PagerDuty authorize path")
	require.Equal(t, "code", authorizeURL.Query().Get("response_type"), "StartOAuth should request an authorization code")
	require.Equal(t, "pd-client", authorizeURL.Query().Get("client_id"), "StartOAuth should send PagerDuty client id")
	require.Equal(t, "https://api.143.dev/api/v1/integrations/pagerduty/callback", authorizeURL.Query().Get("redirect_uri"), "StartOAuth should send the callback URL")
	require.NotEmpty(t, authorizeURL.Query().Get("state"), "StartOAuth should include OAuth state")
	require.NotEmpty(t, rec.Result().Cookies(), "StartOAuth should set the OAuth state cookie")
}

func TestPagerDutyIntegrationHandler_HandleOAuthCallbackStoresCredentialAndInstall(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	genericIntegrationID := uuid.New()
	credentialID := uuid.New()
	providerIntegrationID := uuid.New()
	generic := &pagerDutyGenericIntegrationStoreFake{integrationID: genericIntegrationID}
	credentials := &pagerDutyCredentialWriterFake{credentialID: credentialID}
	provider := &pagerDutyProviderIntegrationWriterFake{providerIntegrationID: providerIntegrationID}
	client := &pagerDutyClientFake{oauthToken: pagerDutyOAuthToken{
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		Scope:        "incidents.read services.read",
		ExpiresIn:    3600,
	}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		Integrations:          generic,
		Credentials:           credentials,
		PagerDutyIntegrations: provider,
		Client:                client,
		OAuthClientID:         "pd-client",
		OAuthClientSecret:     "pd-secret",
		BaseURL:               "https://api.143.dev",
		FrontendURL:           "https://app.143.dev",
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/pagerduty/callback?code=pd-code&state=state-1", nil)
	req.AddCookie(&http.Cookie{Name: pagerDutyIntegrationOAuthStateCookie, Value: "state-1"})
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
	rec := httptest.NewRecorder()

	handler.HandleOAuthCallback(rec, req)

	require.Equal(t, http.StatusTemporaryRedirect, rec.Code, "HandleOAuthCallback should redirect after successful install")
	require.Equal(t, "https://app.143.dev/integrations?pagerduty=connected", rec.Header().Get("Location"), "HandleOAuthCallback should redirect to the integrations page")
	require.Equal(t, "pd-code", client.oauthRequest.Code, "HandleOAuthCallback should exchange the authorization code")
	require.Equal(t, "pd-client", client.oauthRequest.ClientID, "HandleOAuthCallback should exchange with the PagerDuty client id")
	require.Equal(t, "https://api.143.dev/api/v1/integrations/pagerduty/callback", client.oauthRequest.RedirectURI, "HandleOAuthCallback should exchange with the callback URL")
	cfg := credentials.cfg.(models.PagerDutyConfig)
	require.Equal(t, "access", cfg.AccessToken, "HandleOAuthCallback should persist OAuth access token")
	require.Equal(t, "refresh", cfg.RefreshToken, "HandleOAuthCallback should persist OAuth refresh token")
	require.Equal(t, "incidents.read services.read", cfg.Scope, "HandleOAuthCallback should persist OAuth scopes")
	require.Equal(t, models.PagerDutyOAuthModeScoped, provider.integration.OAuthMode, "HandleOAuthCallback should create scoped OAuth integration")
	require.Equal(t, []string{"incidents.read", "services.read"}, provider.integration.Scopes, "HandleOAuthCallback should store effective scopes")
}

func TestPagerDutyIntegrationHandler_UpsertMappingAcceptsCanonicalServiceFields(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	integrationID := uuid.New()
	repoID := uuid.New()
	mappings := &pagerDutyMappingStoreFake{}
	provider := &pagerDutyProviderIntegrationWriterFake{integration: models.PagerDutyIntegration{
		ID:     integrationID,
		OrgID:  orgID,
		Status: models.PagerDutyIntegrationStatusActive,
	}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		PagerDutyIntegrations: provider,
		Mappings:              mappings,
		Repositories:          &pagerDutyRepositoryStoreFake{repo: models.Repository{ID: repoID, OrgID: orgID, Status: models.RepositoryStatusActive}},
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/pagerduty/mappings", strings.NewReader(`{
		"pagerduty_integration_id": "`+integrationID.String()+`",
		"pagerduty_service_id": "PSVC",
		"pagerduty_service_name": "API",
		"repository_id": "`+repoID.String()+`",
		"base_branch": "main"
	}`))
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
	rec := httptest.NewRecorder()

	handler.UpsertMapping(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "UpsertMapping should accept canonical PagerDuty service fields")
	require.Len(t, mappings.mappings, 1, "UpsertMapping should save one mapping")
	require.Equal(t, "PSVC", mappings.mappings[0].PagerDutyServiceID, "UpsertMapping should preserve service id")
	require.Equal(t, "API", mappings.mappings[0].PagerDutyServiceName, "UpsertMapping should preserve service name")
	require.Equal(t, repoID, mappings.mappings[0].RepositoryID, "UpsertMapping should preserve repository id")
}

func TestPagerDutyIntegrationHandler_UpsertMappingRejectsRepositoryOutsideOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	repoID := uuid.New()
	mappings := &pagerDutyMappingStoreFake{}
	provider := &pagerDutyProviderIntegrationWriterFake{integration: models.PagerDutyIntegration{
		ID:     integrationID,
		OrgID:  orgID,
		Status: models.PagerDutyIntegrationStatusActive,
	}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		PagerDutyIntegrations: provider,
		Mappings:              mappings,
		Repositories:          &pagerDutyRepositoryStoreFake{err: pgx.ErrNoRows},
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/pagerduty/mappings", strings.NewReader(`{
		"pagerduty_integration_id": "`+integrationID.String()+`",
		"pagerduty_service_id": "PSVC",
		"pagerduty_service_name": "API",
		"repository_id": "`+repoID.String()+`"
	}`))
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.UpsertMapping(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "UpsertMapping should reject repository ids that are not visible to the org")
	require.Empty(t, mappings.mappings, "UpsertMapping should not persist mappings with cross-org repositories")
	require.Contains(t, rec.Body.String(), "INVALID_REPOSITORY_ID", "UpsertMapping should return a repository validation error")
}

type pagerDutyGenericIntegrationStoreFake struct {
	integrationID uuid.UUID
	existing      []models.Integration
	updatedStatus models.IntegrationStatus
}

func (s *pagerDutyGenericIntegrationStoreFake) ListReusableForReconnect(_ context.Context, _ uuid.UUID, provider models.IntegrationProvider) ([]models.Integration, error) {
	if provider != models.IntegrationProviderPagerDuty {
		return nil, errPagerDutyWebhookTestUnexpectedLookup
	}
	if s.existing != nil {
		return s.existing, nil
	}
	return nil, nil
}

func (s *pagerDutyGenericIntegrationStoreFake) Create(_ context.Context, integration *models.Integration) error {
	integration.ID = s.integrationID
	integration.Status = models.IntegrationStatusActive
	return nil
}

func (s *pagerDutyGenericIntegrationStoreFake) UpdateStatus(_ context.Context, _ uuid.UUID, _ uuid.UUID, status models.IntegrationStatus) error {
	s.updatedStatus = status
	return nil
}

type pagerDutyCredentialWriterFake struct {
	credentialID uuid.UUID
	cfg          models.ProviderConfig
	label        string
}

func (s *pagerDutyCredentialWriterFake) UpsertWithLabel(_ context.Context, _ uuid.UUID, _ *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	s.label = label
	s.cfg = cfg
	return &s.credentialID, nil
}

type pagerDutyCredentialReaderFake struct {
	credential *models.DecryptedCredential
}

func (s *pagerDutyCredentialReaderFake) GetByID(context.Context, uuid.UUID, uuid.UUID) (*models.DecryptedCredential, error) {
	return s.credential, nil
}

type pagerDutyCredentialDisablerFake struct {
	disabledProvider        models.ProviderName
	disabledLabeledProvider models.ProviderName
	disabledID              uuid.UUID
}

func (s *pagerDutyCredentialDisablerFake) Disable(_ context.Context, _ uuid.UUID, provider models.ProviderName) error {
	s.disabledProvider = provider
	return nil
}

func (s *pagerDutyCredentialDisablerFake) DisableLabeled(_ context.Context, _ uuid.UUID, provider models.ProviderName) error {
	s.disabledLabeledProvider = provider
	return nil
}

func (s *pagerDutyCredentialDisablerFake) DisableByID(_ context.Context, _ uuid.UUID, id uuid.UUID) error {
	s.disabledID = id
	return nil
}

type pagerDutyProviderIntegrationWriterFake struct {
	providerIntegrationID   uuid.UUID
	integration             models.PagerDutyIntegration
	existingByIntegrationID *models.PagerDutyIntegration
	existingByAccount       *models.PagerDutyIntegration
	settings                models.PagerDutyIntegrationSettings
	settingsUpdated         bool
	deactivated             bool
}

func (s *pagerDutyProviderIntegrationWriterFake) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.PagerDutyIntegration, error) {
	return s.integration, nil
}

func (s *pagerDutyProviderIntegrationWriterFake) GetByIntegrationID(context.Context, uuid.UUID, uuid.UUID) (models.PagerDutyIntegration, error) {
	if s.existingByIntegrationID != nil {
		return *s.existingByIntegrationID, nil
	}
	return models.PagerDutyIntegration{}, pgx.ErrNoRows
}

func (s *pagerDutyProviderIntegrationWriterFake) GetByAccount(context.Context, uuid.UUID, string, string) (models.PagerDutyIntegration, error) {
	if s.existingByAccount != nil {
		return *s.existingByAccount, nil
	}
	return models.PagerDutyIntegration{}, pgx.ErrNoRows
}

func (s *pagerDutyProviderIntegrationWriterFake) Create(_ context.Context, integration *models.PagerDutyIntegration) error {
	integration.ID = s.providerIntegrationID
	s.integration = *integration
	return nil
}

func (s *pagerDutyProviderIntegrationWriterFake) ListManageable(context.Context, uuid.UUID) ([]models.PagerDutyIntegration, error) {
	if s.integration.ID != uuid.Nil {
		return []models.PagerDutyIntegration{s.integration}, nil
	}
	return []models.PagerDutyIntegration{s.integration}, nil
}

func (s *pagerDutyProviderIntegrationWriterFake) UpdateSettings(_ context.Context, _ uuid.UUID, settings models.PagerDutyIntegrationSettings) (models.PagerDutyIntegration, error) {
	s.settings = settings
	s.settingsUpdated = true
	s.integration.ID = settings.ID
	s.integration.DefaultRepositoryID = settings.DefaultRepositoryID
	s.integration.WritebackEnabled = settings.WritebackEnabled
	s.integration.AutoCreateWebhook = settings.AutoCreateWebhook
	s.integration.Status = settings.Status
	s.integration.LastError = settings.LastError
	return s.integration, nil
}

func (s *pagerDutyProviderIntegrationWriterFake) DeactivateAll(context.Context, uuid.UUID) error {
	s.deactivated = true
	return nil
}

type pagerDutyWebhookFailureReaderFake struct {
	orgID         uuid.UUID
	integrationID uuid.UUID
	provider      string
	since         time.Time
	summary       models.PagerDutyWebhookFailureSummary
	err           error
}

func (s *pagerDutyWebhookFailureReaderFake) SummarizeRecentFailuresForIntegration(_ context.Context, orgID, integrationID uuid.UUID, provider string, since time.Time) (models.PagerDutyWebhookFailureSummary, error) {
	s.orgID = orgID
	s.integrationID = integrationID
	s.provider = provider
	s.since = since
	if s.err != nil {
		return models.PagerDutyWebhookFailureSummary{}, s.err
	}
	return s.summary, nil
}

type pagerDutyClientFake struct {
	testedConfig          models.PagerDutyConfig
	listedConfig          models.PagerDutyConfig
	services              []models.PagerDutyServiceSummary
	webhookRequest        pagerDutyWebhookSubscriptionRequest
	webhookSubscriptionID string
	webhookErr            error
	oauthToken            pagerDutyOAuthToken
	oauthRequest          pagerDutyOAuthExchangeRequest
}

func (s *pagerDutyClientFake) TestCredential(_ context.Context, cfg models.PagerDutyConfig) error {
	s.testedConfig = cfg
	return nil
}

func (s *pagerDutyClientFake) ListServices(_ context.Context, cfg models.PagerDutyConfig) ([]models.PagerDutyServiceSummary, error) {
	s.listedConfig = cfg
	return s.services, nil
}

func (s *pagerDutyClientFake) CreateWebhookSubscription(_ context.Context, _ models.PagerDutyConfig, req pagerDutyWebhookSubscriptionRequest) (pagerDutyWebhookSubscription, error) {
	s.webhookRequest = req
	if s.webhookErr != nil {
		return pagerDutyWebhookSubscription{}, s.webhookErr
	}
	return pagerDutyWebhookSubscription{ID: s.webhookSubscriptionID}, nil
}

func (s *pagerDutyClientFake) ExchangeOAuthCode(_ context.Context, req pagerDutyOAuthExchangeRequest) (pagerDutyOAuthToken, error) {
	s.oauthRequest = req
	return s.oauthToken, nil
}

func TestPagerDutyIntegrationHandler_ListIncludesDegradedIntegrations(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	provider := &pagerDutyProviderIntegrationWriterFake{integration: models.PagerDutyIntegration{
		ID:     integrationID,
		OrgID:  orgID,
		Status: models.PagerDutyIntegrationStatusDegraded,
	}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		PagerDutyIntegrations: provider,
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/pagerduty", nil)
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.List(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "List should return manageable PagerDuty integrations")
	var resp models.ListResponse[models.PagerDutyIntegration]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "List response should be valid JSON")
	require.Equal(t, []models.PagerDutyIntegration{provider.integration}, resp.Data, "List should include degraded integrations for reauthorization")
}

func TestPagerDutyIntegrationHandler_ListMappingsScopesIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	mappingID := uuid.New()
	repoID := uuid.New()
	mappings := &pagerDutyMappingStoreFake{mappings: []models.PagerDutyServiceRepoMapping{{
		ID:                     mappingID,
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		PagerDutyServiceID:     "PSVC",
		PagerDutyServiceName:   "api",
		RepositoryID:           repoID,
		Enabled:                true,
	}}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		Mappings: mappings,
		Logger:   testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/pagerduty/mappings?integration_id="+integrationID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.ListMappings(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "ListMappings should return mappings for the integration")
	require.Equal(t, orgID, mappings.orgID, "ListMappings should scope by org")
	require.Equal(t, integrationID, mappings.integrationID, "ListMappings should scope by PagerDuty integration")
}

func TestPagerDutyIntegrationHandler_PatchSettingsPreservesUnsetFields(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	genericIntegrationID := uuid.New()
	credentialID := uuid.New()
	existingRepoID := uuid.New()
	provider := &pagerDutyProviderIntegrationWriterFake{
		integration: models.PagerDutyIntegration{
			ID:                  integrationID,
			OrgID:               orgID,
			IntegrationID:       &genericIntegrationID,
			CredentialRef:       "org_credential:" + credentialID.String(),
			Status:              models.PagerDutyIntegrationStatusActive,
			DefaultRepositoryID: &existingRepoID,
			WritebackEnabled:    true,
			AutoCreateWebhook:   false,
		},
	}
	credentials := &pagerDutyCredentialReaderFake{credential: &models.DecryptedCredential{
		ID:       credentialID,
		OrgID:    orgID,
		Provider: models.ProviderPagerDuty,
		Config:   models.PagerDutyConfig{AccessToken: "access", WebhookSecret: "shared-secret"},
	}}
	client := &pagerDutyClientFake{webhookSubscriptionID: "PWEBHOOK"}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		PagerDutyIntegrations: provider,
		CredentialReader:      credentials,
		Client:                client,
		BaseURL:               "https://api.143.dev",
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/integrations/pagerduty", strings.NewReader(`{
		"default_repository_id": null,
		"writeback_enabled": false,
		"auto_create_webhook": true
	}`))
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.Patch(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "Patch should update PagerDuty settings")
	require.True(t, provider.settingsUpdated, "Patch should persist settings through the provider store")
	require.Nil(t, provider.settings.DefaultRepositoryID, "Patch should clear default_repository_id when explicitly null")
	require.False(t, provider.settings.WritebackEnabled, "Patch should update writeback_enabled")
	require.True(t, provider.settings.AutoCreateWebhook, "Patch should update auto_create_webhook")
	require.Equal(t, models.PagerDutyIntegrationStatusActive, provider.settings.Status, "Patch should preserve status when unset")
	require.Equal(t, expectedPagerDutyWebhookURL(genericIntegrationID, integrationID), client.webhookRequest.WebhookURL, "Patch should auto-create webhook subscriptions with the canonical URL")
	require.Equal(t, "shared-secret", client.webhookRequest.Secret, "Patch should use the stored webhook secret when auto-creating subscriptions")
	require.Equal(t, "143 incident automation", client.webhookRequest.Description, "Patch should use the standard webhook description")
	require.Equal(t, defaultPagerDutyWebhookEvents(), client.webhookRequest.Events, "Patch should subscribe to the default incident lifecycle events")
}

func TestPagerDutyIntegrationHandler_PatchDoesNotPersistAutoCreateWhenWebhookCreateFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	genericIntegrationID := uuid.New()
	credentialID := uuid.New()
	provider := &pagerDutyProviderIntegrationWriterFake{
		integration: models.PagerDutyIntegration{
			ID:                integrationID,
			OrgID:             orgID,
			IntegrationID:     &genericIntegrationID,
			CredentialRef:     "org_credential:" + credentialID.String(),
			Status:            models.PagerDutyIntegrationStatusActive,
			WritebackEnabled:  true,
			AutoCreateWebhook: false,
		},
	}
	credentials := &pagerDutyCredentialReaderFake{credential: &models.DecryptedCredential{
		ID:       credentialID,
		OrgID:    orgID,
		Provider: models.ProviderPagerDuty,
		Config:   models.PagerDutyConfig{AccessToken: "access", WebhookSecret: "shared-secret"},
	}}
	client := &pagerDutyClientFake{webhookErr: errPagerDutyWebhookTestUnexpectedLookup}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		PagerDutyIntegrations: provider,
		CredentialReader:      credentials,
		Client:                client,
		BaseURL:               "https://api.143.dev",
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/integrations/pagerduty", strings.NewReader(`{"auto_create_webhook": true}`))
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.Patch(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code, "Patch should fail when PagerDuty webhook creation fails")
	require.False(t, provider.settingsUpdated, "Patch should leave auto_create_webhook unchanged when subscription creation fails")
}

func TestPagerDutyIntegrationHandler_DeleteDeactivatesProviderAndCredentialState(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	generic := &pagerDutyGenericIntegrationStoreFake{existing: []models.Integration{{
		ID:       genericIntegrationID,
		OrgID:    orgID,
		Provider: models.IntegrationProviderPagerDuty,
		Status:   models.IntegrationStatusActive,
	}}}
	credentials := &pagerDutyCredentialDisablerFake{}
	provider := &pagerDutyProviderIntegrationWriterFake{}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		Integrations:          generic,
		CredentialDisabler:    credentials,
		PagerDutyIntegrations: provider,
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/pagerduty", nil)
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.Delete(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code, "Delete should return no content")
	require.Equal(t, models.IntegrationStatusInactive, generic.updatedStatus, "Delete should deactivate the generic PagerDuty integration")
	require.True(t, provider.deactivated, "Delete should deactivate PagerDuty provider integrations")
	require.Equal(t, models.ProviderPagerDuty, credentials.disabledProvider, "Delete should disable legacy unlabeled PagerDuty credentials")
	require.Equal(t, models.ProviderPagerDuty, credentials.disabledLabeledProvider, "Delete should disable labeled PagerDuty credentials")
}

func TestPagerDutyIntegrationHandler_TestUsesStoredCredential(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	credentialID := uuid.New()
	integrationID := uuid.New()
	lastSyncedAt := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	lastErr := "last sync failed"
	provider := &pagerDutyProviderIntegrationWriterFake{
		integration: models.PagerDutyIntegration{
			ID:                integrationID,
			OrgID:             orgID,
			CredentialRef:     "org_credential:" + credentialID.String(),
			Status:            models.PagerDutyIntegrationStatusDegraded,
			LastSyncedAt:      &lastSyncedAt,
			LastError:         &lastErr,
			WritebackEnabled:  true,
			AutoCreateWebhook: true,
		},
	}
	credentials := &pagerDutyCredentialReaderFake{credential: &models.DecryptedCredential{
		ID:       credentialID,
		OrgID:    orgID,
		Provider: models.ProviderPagerDuty,
		Config:   models.PagerDutyConfig{AccessToken: "access", WebhookSecret: "secret"},
	}}
	client := &pagerDutyClientFake{}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		CredentialReader:      credentials,
		PagerDutyIntegrations: provider,
		Client:                client,
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/pagerduty/test", nil)
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.Test(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "Test should return PagerDuty health")
	require.Equal(t, "access", client.testedConfig.AccessToken, "Test should use the stored PagerDuty access token")
	var resp models.SingleResponse[models.PagerDutyHealth]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "Test response should be valid JSON")
	require.True(t, resp.Data.AuthOK, "Test should report successful credential validation")
	require.True(t, resp.Data.WebhookSecretConfigured, "Test should report configured webhook secret")
	require.Equal(t, &lastSyncedAt, resp.Data.LastSyncedAt, "Test should report last sync time")
	require.Equal(t, &lastErr, resp.Data.LastError, "Test should report the provider integration last error")
	require.True(t, resp.Data.WritebackEnabled, "Test should expose writeback setting")
	require.True(t, resp.Data.AutoCreateWebhook, "Test should expose auto-create webhook setting")
	require.Contains(t, resp.Data.Symptoms, "integration_degraded", "Test should surface degraded integration state")
	require.Contains(t, resp.Data.Symptoms, "last_error", "Test should surface last integration error")
}

func TestPagerDutyIntegrationHandler_TestReportsRecentWebhookFailures(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	credentialID := uuid.New()
	genericIntegrationID := uuid.New()
	providerIntegrationID := uuid.New()
	latestAt := time.Date(2026, 6, 20, 10, 30, 0, 0, time.UTC)
	latestErr := "invalid webhook signature"
	provider := &pagerDutyProviderIntegrationWriterFake{
		integration: models.PagerDutyIntegration{
			ID:            providerIntegrationID,
			OrgID:         orgID,
			IntegrationID: uuidPtrPagerDutyIntegrationTest(genericIntegrationID),
			CredentialRef: "org_credential:" + credentialID.String(),
			Status:        models.PagerDutyIntegrationStatusActive,
		},
	}
	credentials := &pagerDutyCredentialReaderFake{credential: &models.DecryptedCredential{
		ID:       credentialID,
		OrgID:    orgID,
		Provider: models.ProviderPagerDuty,
		Config:   models.PagerDutyConfig{AccessToken: "access", WebhookSecret: "secret"},
	}}
	webhooks := &pagerDutyWebhookFailureReaderFake{summary: models.PagerDutyWebhookFailureSummary{
		Count:           2,
		LatestError:     &latestErr,
		LatestFailureAt: &latestAt,
	}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		CredentialReader:      credentials,
		PagerDutyIntegrations: provider,
		WebhookFailures:       webhooks,
		Client:                &pagerDutyClientFake{},
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/pagerduty/test", nil)
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.Test(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "Test should return PagerDuty health")
	var resp models.SingleResponse[models.PagerDutyHealth]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "Test response should decode")
	require.Equal(t, 2, resp.Data.RecentWebhookFailures, "Test should report recent webhook failure count")
	require.Equal(t, &latestErr, resp.Data.LatestWebhookError, "Test should report the latest webhook failure error")
	require.Equal(t, &latestAt, resp.Data.LatestWebhookFailureAt, "Test should report the latest webhook failure time")
	require.Contains(t, resp.Data.Symptoms, "recent_webhook_failures", "Test should surface webhook failures as a health symptom")
	require.Equal(t, orgID, webhooks.orgID, "Test should scope webhook failure summary by org")
	require.Equal(t, genericIntegrationID, webhooks.integrationID, "Test should scope webhook failure summary by generic integration")
	require.Equal(t, "pagerduty", webhooks.provider, "Test should query PagerDuty webhook failures")
	require.WithinDuration(t, time.Now().Add(-24*time.Hour), webhooks.since, 5*time.Second, "Test should use a recent health window")
}

func TestPagerDutyIntegrationHandler_ListServicesUsesStoredCredential(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	credentialID := uuid.New()
	integrationID := uuid.New()
	provider := &pagerDutyProviderIntegrationWriterFake{
		integration: models.PagerDutyIntegration{
			ID:            integrationID,
			OrgID:         orgID,
			CredentialRef: "org_credential:" + credentialID.String(),
			Status:        models.PagerDutyIntegrationStatusActive,
		},
	}
	credentials := &pagerDutyCredentialReaderFake{credential: &models.DecryptedCredential{
		ID:       credentialID,
		OrgID:    orgID,
		Provider: models.ProviderPagerDuty,
		Config:   models.PagerDutyConfig{AccessToken: "access"},
	}}
	client := &pagerDutyClientFake{services: []models.PagerDutyServiceSummary{{
		ID:      "PSVC",
		Summary: "API",
		HTMLURL: "https://acme.pagerduty.com/service-directory/PSVC",
	}}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		CredentialReader:      credentials,
		PagerDutyIntegrations: provider,
		Client:                client,
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/pagerduty/services", nil)
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.ListServices(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "ListServices should return PagerDuty services")
	require.Equal(t, "access", client.listedConfig.AccessToken, "ListServices should use the stored PagerDuty access token")
	var resp models.ListResponse[models.PagerDutyServiceSummary]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "ListServices response should be valid JSON")
	require.Equal(t, client.services, resp.Data, "ListServices should return service summaries from the client")
}

func TestPagerDutyIntegrationHandler_GetWebhookSetupReturnsCanonicalURL(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	credentialID := uuid.New()
	genericIntegrationID := uuid.New()
	providerIntegrationID := uuid.New()
	provider := &pagerDutyProviderIntegrationWriterFake{
		integration: models.PagerDutyIntegration{
			ID:            providerIntegrationID,
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			CredentialRef: "org_credential:" + credentialID.String(),
			Status:        models.PagerDutyIntegrationStatusActive,
		},
	}
	credentials := &pagerDutyCredentialReaderFake{credential: &models.DecryptedCredential{
		ID:       credentialID,
		OrgID:    orgID,
		Provider: models.ProviderPagerDuty,
		Config:   models.PagerDutyConfig{AccessToken: "access", WebhookSecret: "secret"},
	}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		CredentialReader:      credentials,
		PagerDutyIntegrations: provider,
		BaseURL:               "https://api.143.dev",
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/pagerduty/webhook-setup", nil)
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.GetWebhookSetup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "GetWebhookSetup should return webhook setup details")
	var resp models.SingleResponse[models.PagerDutyWebhookSetup]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "GetWebhookSetup response should be valid JSON")
	require.Equal(t, expectedPagerDutyWebhookURL(genericIntegrationID, providerIntegrationID), resp.Data.WebhookURL, "GetWebhookSetup should return the canonical webhook URL")
	require.True(t, resp.Data.WebhookSecretConfigured, "GetWebhookSetup should report configured webhook secret")
}

func TestPagerDutyIntegrationHandler_CreateWebhookSetupCreatesServiceSubscription(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	credentialID := uuid.New()
	genericIntegrationID := uuid.New()
	providerIntegrationID := uuid.New()
	provider := &pagerDutyProviderIntegrationWriterFake{
		integration: models.PagerDutyIntegration{
			ID:            providerIntegrationID,
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			CredentialRef: "org_credential:" + credentialID.String(),
			Status:        models.PagerDutyIntegrationStatusActive,
		},
	}
	credentials := &pagerDutyCredentialReaderFake{credential: &models.DecryptedCredential{
		ID:       credentialID,
		OrgID:    orgID,
		Provider: models.ProviderPagerDuty,
		Config:   models.PagerDutyConfig{AccessToken: "access", WebhookSecret: "shared-secret"},
	}}
	client := &pagerDutyClientFake{webhookSubscriptionID: "PWEBHOOK"}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		CredentialReader:      credentials,
		PagerDutyIntegrations: provider,
		Client:                client,
		BaseURL:               "https://api.143.dev",
		Logger:                testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/pagerduty/webhook-setup", strings.NewReader(`{
		"service_id": "PSVC",
		"description": "143 API incidents",
		"events": ["incident.triggered", "incident.resolved"]
	}`))
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.CreateWebhookSetup(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "CreateWebhookSetup should create a PagerDuty subscription")
	require.Equal(t, expectedPagerDutyWebhookURL(genericIntegrationID, providerIntegrationID), client.webhookRequest.WebhookURL, "CreateWebhookSetup should use the canonical webhook URL")
	require.Equal(t, "shared-secret", client.webhookRequest.Secret, "CreateWebhookSetup should pass the configured shared secret as the custom header value")
	require.Equal(t, "PSVC", client.webhookRequest.FilterID, "CreateWebhookSetup should scope the subscription to the requested service")
	require.Equal(t, "service_reference", client.webhookRequest.FilterType, "CreateWebhookSetup should use a service filter")
	require.Equal(t, []models.PagerDutyEventType{models.PagerDutyEventIncidentTriggered, models.PagerDutyEventIncidentResolved}, client.webhookRequest.Events, "CreateWebhookSetup should preserve requested events")
	var resp models.SingleResponse[models.PagerDutyWebhookSetup]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "CreateWebhookSetup response should be valid JSON")
	require.Equal(t, "PWEBHOOK", *resp.Data.WebhookSubscriptionID, "CreateWebhookSetup should return the subscription id")
	require.Equal(t, "PSVC", *resp.Data.ServiceID, "CreateWebhookSetup should return the service filter")
}

func TestPagerDutyIntegrationHandler_ListIncidentsScopesFilters(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	incidents := &pagerDutyIncidentStoreFake{incidents: []models.PagerDutyIncident{{
		ID:                     uuid.New(),
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PABC123",
		Title:                  "API latency",
		Status:                 "triggered",
	}}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		Incidents: incidents,
		Logger:    testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/pagerduty/incidents?integration_id="+integrationID.String()+"&status=triggered&service_id=PSVC&limit=25", nil)
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	rec := httptest.NewRecorder()

	handler.ListIncidents(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "ListIncidents should return mirrored incidents")
	require.Equal(t, orgID, incidents.listOrgID, "ListIncidents should scope by org")
	require.Equal(t, &integrationID, incidents.listFilter.IntegrationID, "ListIncidents should pass integration filter")
	require.Equal(t, "triggered", incidents.listFilter.Status, "ListIncidents should pass status filter")
	require.Equal(t, "PSVC", incidents.listFilter.ServiceID, "ListIncidents should pass service filter")
	require.Equal(t, 25, incidents.listFilter.Limit, "ListIncidents should pass limit filter")
}

func TestPagerDutyIntegrationHandler_GetIncidentUsesProviderIntegrationScope(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	incident := models.PagerDutyIncident{
		ID:                     uuid.New(),
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PABC123",
		Title:                  "API latency",
		Status:                 "acknowledged",
	}
	incidents := &pagerDutyIncidentStoreFake{incident: incident}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		Incidents: incidents,
		Logger:    testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/pagerduty/incidents/PABC123?pagerduty_integration_id="+integrationID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	req = addPagerDutyIntegrationRouteParam(req, "incident_id", "PABC123")
	rec := httptest.NewRecorder()

	handler.GetIncident(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "GetIncident should return the mirrored incident")
	require.Equal(t, orgID, incidents.getOrgID, "GetIncident should scope by org")
	require.Equal(t, "PABC123", incidents.getIncidentID, "GetIncident should lookup by provider incident id")
	require.Equal(t, integrationID, incidents.getIntegrationID, "GetIncident should scope by PagerDuty provider integration")
}

func TestPagerDutyIntegrationHandler_StartIncidentSessionUsesServiceMapping(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	integrationID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	serviceID := "PSVC"
	incident := models.PagerDutyIncident{
		ID:                     uuid.New(),
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IncidentID:             "PABC123",
		Title:                  "API latency",
		Status:                 "triggered",
		ServiceID:              &serviceID,
	}
	incidents := &pagerDutyIncidentStoreFake{incident: incident}
	mappings := &pagerDutyMappingStoreFake{mapping: models.PagerDutyServiceRepoMapping{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		PagerDutyServiceID:     serviceID,
		PagerDutyServiceName:   "API",
		RepositoryID:           repoID,
		BaseBranch:             strPtrPagerDutyIntegrationTest("main"),
		Enabled:                true,
	}}
	starter := &pagerDutySessionStarterFake{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		Incidents:      incidents,
		Mappings:       mappings,
		SessionStarter: starter,
		Logger:         testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/pagerduty/incidents/PABC123/session", strings.NewReader(`{"pagerduty_integration_id":"`+integrationID.String()+`","message":"Investigate and fix"}`))
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
	req = addPagerDutyIntegrationRouteParam(req, "incident_id", "PABC123")
	rec := httptest.NewRecorder()

	handler.StartIncidentSession(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "StartIncidentSession should create a session")
	require.Equal(t, integrationID, incidents.getIntegrationID, "StartIncidentSession should scope incident lookup by PagerDuty provider integration")
	require.Equal(t, repoID, starter.input.RepositoryID, "StartIncidentSession should use the mapped repository")
	require.Equal(t, "main", *starter.input.BaseBranch, "StartIncidentSession should use the mapped base branch")
	require.Equal(t, userID, *starter.input.UserID, "StartIncidentSession should attribute the user")
	require.Equal(t, "Investigate and fix", starter.input.Message, "StartIncidentSession should pass the responder prompt")
	var resp models.SingleResponse[models.Session]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "StartIncidentSession response should be valid JSON")
	require.Equal(t, sessionID, resp.Data.ID, "StartIncidentSession should return the created session")
}

func TestPagerDutyIntegrationHandler_StartIncidentSessionReturnsConflictForActiveSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	repoID := uuid.New()
	serviceID := "PSVC"
	incident := models.PagerDutyIncident{
		ID:                     uuid.New(),
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		IssueID:                uuidPtrPagerDutyIntegrationTest(uuid.New()),
		IncidentID:             "PABC123",
		Title:                  "API latency",
		Status:                 "triggered",
		ServiceID:              &serviceID,
	}
	incidents := &pagerDutyIncidentStoreFake{incident: incident}
	mappings := &pagerDutyMappingStoreFake{mapping: models.PagerDutyServiceRepoMapping{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integrationID,
		PagerDutyServiceID:     serviceID,
		PagerDutyServiceName:   "API",
		RepositoryID:           repoID,
		Enabled:                true,
	}}
	starter := &pagerDutySessionStarterFake{err: pagerdutysvc.ErrPagerDutySessionAlreadyRunning}
	handler := NewPagerDutyIntegrationHandler(PagerDutyIntegrationHandlerConfig{
		Incidents:      incidents,
		Mappings:       mappings,
		SessionStarter: starter,
		Logger:         testLoggerPagerDutyWebhook(),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/pagerduty/incidents/PABC123/session", strings.NewReader(`{}`))
	req = req.WithContext(middleware.WithOrgID(context.Background(), orgID))
	req = addPagerDutyIntegrationRouteParam(req, "incident_id", "PABC123")
	rec := httptest.NewRecorder()

	handler.StartIncidentSession(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code, "StartIncidentSession should return a conflict when a session is already active")
	require.Contains(t, rec.Body.String(), "PAGERDUTY_SESSION_ALREADY_RUNNING", "StartIncidentSession should return the documented PagerDuty conflict code")
}

type pagerDutyMappingStoreFake struct {
	orgID         uuid.UUID
	integrationID uuid.UUID
	mappings      []models.PagerDutyServiceRepoMapping
	mapping       models.PagerDutyServiceRepoMapping
}

type pagerDutyRepositoryStoreFake struct {
	repo models.Repository
	err  error
}

func (s *pagerDutyRepositoryStoreFake) GetByID(_ context.Context, _ uuid.UUID, repoID uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	repo := s.repo
	if repo.ID == uuid.Nil {
		repo.ID = repoID
	}
	if repo.Status == "" {
		repo.Status = models.RepositoryStatusActive
	}
	return repo, nil
}

func (s *pagerDutyMappingStoreFake) ListByIntegration(_ context.Context, orgID, integrationID uuid.UUID) ([]models.PagerDutyServiceRepoMapping, error) {
	s.orgID = orgID
	s.integrationID = integrationID
	return s.mappings, nil
}

func (s *pagerDutyMappingStoreFake) Upsert(_ context.Context, mapping *models.PagerDutyServiceRepoMapping) error {
	s.mappings = append(s.mappings, *mapping)
	return nil
}

func (s *pagerDutyMappingStoreFake) GetByServiceID(_ context.Context, orgID, integrationID uuid.UUID, serviceID string) (models.PagerDutyServiceRepoMapping, error) {
	s.orgID = orgID
	s.integrationID = integrationID
	if s.mapping.PagerDutyServiceID == serviceID {
		return s.mapping, nil
	}
	return models.PagerDutyServiceRepoMapping{}, pgx.ErrNoRows
}

type pagerDutyIncidentStoreFake struct {
	listOrgID        uuid.UUID
	listFilter       db.PagerDutyIncidentListFilter
	getOrgID         uuid.UUID
	getIntegrationID uuid.UUID
	getIncidentID    string
	getErr           error
	incidents        []models.PagerDutyIncident
	incident         models.PagerDutyIncident
	upsertIncident   models.PagerDutyIncident
}

func (s *pagerDutyIncidentStoreFake) List(_ context.Context, orgID uuid.UUID, filter db.PagerDutyIncidentListFilter) ([]models.PagerDutyIncident, error) {
	s.listOrgID = orgID
	s.listFilter = filter
	return s.incidents, nil
}

func (s *pagerDutyIncidentStoreFake) GetLatestByIncidentID(_ context.Context, orgID uuid.UUID, incidentID string) (models.PagerDutyIncident, error) {
	s.getOrgID = orgID
	s.getIncidentID = incidentID
	if s.getErr != nil {
		return models.PagerDutyIncident{}, s.getErr
	}
	return s.incident, nil
}

func (s *pagerDutyIncidentStoreFake) GetByIncidentID(_ context.Context, orgID, integrationID uuid.UUID, incidentID string) (models.PagerDutyIncident, error) {
	s.getOrgID = orgID
	s.getIntegrationID = integrationID
	s.getIncidentID = incidentID
	if s.getErr != nil {
		return models.PagerDutyIncident{}, s.getErr
	}
	if s.incident.PagerDutyIntegrationID != uuid.Nil && s.incident.PagerDutyIntegrationID != integrationID {
		return models.PagerDutyIncident{}, errPagerDutyWebhookTestUnexpectedLookup
	}
	return s.incident, nil
}

func (s *pagerDutyIncidentStoreFake) Upsert(_ context.Context, incident *models.PagerDutyIncident) error {
	s.upsertIncident = *incident
	s.incident = *incident
	s.getErr = nil
	return nil
}

type pagerDutySessionStarterFake struct {
	input   pagerdutysvc.StartSessionInput
	session models.Session
	err     error
}

func (s *pagerDutySessionStarterFake) StartSession(_ context.Context, input pagerdutysvc.StartSessionInput) (models.Session, error) {
	s.input = input
	if s.err != nil {
		return s.session, s.err
	}
	return s.session, nil
}

func addPagerDutyIntegrationRouteParam(req *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func strPtrPagerDutyIntegrationTest(value string) *string {
	return &value
}

func uuidPtrPagerDutyIntegrationTest(value uuid.UUID) *uuid.UUID {
	return &value
}

func expectedPagerDutyWebhookURL(genericIntegrationID, pagerDutyIntegrationID uuid.UUID) string {
	return "https://api.143.dev/api/v1/webhooks/pagerduty?integration_id=" + genericIntegrationID.String() + "&pagerduty_integration_id=" + pagerDutyIntegrationID.String()
}
