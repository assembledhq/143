package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// ──────────────────────────────────────────────────────────────────────────────
// Constructor
// ──────────────────────────────────────────────────────────────────────────────

func TestNewIntegrationHandler(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	require.NotNil(t, handler, "handler should not be nil")
}

func TestNewIntegrationHandler_WithOptions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "lin-id", "lin-secret", "http://localhost:8080", "http://localhost:3000",
		WithSentryOAuth("sentry-id", "sentry-secret"),
		WithGitHubIntegrationOAuth("gh-id", "gh-secret"),
	)

	require.NotNil(t, handler)
	require.Equal(t, "sentry-id", handler.sentryClientID)
	require.Equal(t, "sentry-secret", handler.sentrySecret)
	require.Equal(t, "gh-id", handler.githubClientID)
	require.Equal(t, "gh-secret", handler.githubSecret)
}

// ──────────────────────────────────────────────────────────────────────────────
// List integrations
// ──────────────────────────────────────────────────────────────────────────────

func TestIntegrationHandler_ListIntegrations_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListIntegrations(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200")
	require.Contains(t, w.Body.String(), `"data":[]`, "should return empty array for no integrations")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ListIntegrations_DBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListIntegrations(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code, "should return 500 on DB error")
	require.Contains(t, w.Body.String(), "LIST_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ──────────────────────────────────────────────────────────────────────────────
// Linear OAuth
// ──────────────────────────────────────────────────────────────────────────────

func TestIntegrationHandler_StartLinearOAuth_RedirectsToLinearAuthorize(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "linear-client-id", "linear-client-secret", "http://localhost:8080", "http://localhost:3000")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/login", nil)
	w := httptest.NewRecorder()

	handler.StartLinearOAuth(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "StartLinearOAuth should redirect to Linear authorize URL")

	redirectURL := w.Header().Get("Location")
	require.NotEmpty(t, redirectURL, "StartLinearOAuth should set redirect location")

	parsed, parseErr := url.Parse(redirectURL)
	require.NoError(t, parseErr, "redirect location should be a valid URL")
	require.Equal(t, "https", parsed.Scheme, "redirect should use https")
	require.Equal(t, "linear.app", parsed.Host, "redirect should target linear app")
	require.Equal(t, "/oauth/authorize", parsed.Path, "redirect path should be Linear authorize endpoint")
	require.Equal(t, "linear-client-id", parsed.Query().Get("client_id"), "redirect should include configured client id")
	require.Equal(t, "code", parsed.Query().Get("response_type"), "redirect should request auth code flow")
	require.Equal(t, "http://localhost:8080/api/v1/integrations/linear/callback", parsed.Query().Get("redirect_uri"), "redirect should include API callback URL")
	require.NotEmpty(t, parsed.Query().Get("state"), "redirect should include oauth state")

	setCookie := w.Result().Header.Get("Set-Cookie")
	require.Contains(t, setCookie, "linear_integration_oauth_state=", "StartLinearOAuth should set linear oauth state cookie")
}

func TestIntegrationHandler_StartLinearOAuth_NotConfigured(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/login", nil)
	w := httptest.NewRecorder()

	handler.StartLinearOAuth(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "LINEAR_OAUTH_NOT_CONFIGURED")
}

func TestIntegrationHandler_HandleLinearOAuthCallback_SavesCredentialAndIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	credentialStore := db.NewOrgCredentialStore(mock, nil)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://api.linear.app/oauth/token":
			respBody := `{"access_token":"linear-access-token","token_type":"Bearer","scope":"read"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(respBody)),
			}, nil
		case "https://api.linear.app/graphql":
			respBody := `{"data":{"viewer":{"organization":{"id":"lin-org-1","name":"Acme"}}}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(respBody)),
			}, nil
		default:
			return nil, errors.New("unexpected request URL")
		}
	})

	handler := NewIntegrationHandler(store, credentialStore, "linear-client-id", "linear-client-secret", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{Transport: transport}

	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/callback?code=test-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "linear_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleLinearOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "HandleLinearOAuthCallback should redirect after successful OAuth")
	require.Equal(t, "http://localhost:3000/integrations?linear=connected", w.Header().Get("Location"), "HandleLinearOAuthCallback should redirect to integrations page with success state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_HandleLinearOAuthCallback_StateMismatch(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/callback?code=test-code&state=wrong-state", nil)
	req.AddCookie(&http.Cookie{Name: "linear_integration_oauth_state", Value: "correct-state"})
	w := httptest.NewRecorder()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "linear-client-id", "linear-client-secret", "http://localhost:8080", "http://localhost:3000")
	handler.HandleLinearOAuthCallback(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_STATE")
}

func TestIntegrationHandler_HandleLinearOAuthCallback_MissingCode(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/callback?state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "linear_integration_oauth_state", Value: "test-state"})
	w := httptest.NewRecorder()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "linear-client-id", "linear-client-secret", "http://localhost:8080", "http://localhost:3000")
	handler.HandleLinearOAuthCallback(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "MISSING_CODE")
}

func TestIntegrationHandler_ConnectLinear_CreatesIntegrationWhenMissing(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/linear/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectLinear(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "ConnectLinear should return created status for a new integration")

	var resp models.SingleResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "ConnectLinear response should be valid JSON")
	require.Equal(t, integrationID, resp.Data.ID, "ConnectLinear should return the created integration ID")
	require.Equal(t, orgID, resp.Data.OrgID, "ConnectLinear should return the org from request context")
	require.Equal(t, models.IntegrationProviderLinear, resp.Data.Provider, "ConnectLinear should create a linear integration")
	require.Equal(t, models.IntegrationStatusActive, resp.Data.Status, "ConnectLinear should create an active integration")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_ConnectLinear_ReturnsExistingIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(integrationID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/linear/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectLinear(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ConnectLinear should return OK when integration already exists")

	var resp models.SingleResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "ConnectLinear response should be valid JSON")
	require.Equal(t, integrationID, resp.Data.ID, "ConnectLinear should return the existing integration ID")
	require.Equal(t, models.IntegrationProviderLinear, resp.Data.Provider, "ConnectLinear should return a linear integration")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_ConnectLinear_ReturnsInternalErrorWhenLookupFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db unavailable"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/linear/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectLinear(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "ConnectLinear should return 500 when lookup fails")
	require.Contains(t, w.Body.String(), "CONNECT_LINEAR_FAILED", "ConnectLinear should return a stable error code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_ConnectLinear_ReturnsInternalErrorWhenCreateFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("insert failed"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/linear/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectLinear(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "ConnectLinear should return 500 when create fails")
	require.Contains(t, w.Body.String(), "CONNECT_LINEAR_FAILED", "ConnectLinear should return a stable error code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// ──────────────────────────────────────────────────────────────────────────────
// Sentry OAuth
// ──────────────────────────────────────────────────────────────────────────────

func TestIntegrationHandler_StartSentryOAuth_RedirectsToSentryAuthorize(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithSentryOAuth("sentry-client-id", "sentry-client-secret"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/sentry/login", nil)
	w := httptest.NewRecorder()

	handler.StartSentryOAuth(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)

	redirectURL := w.Header().Get("Location")
	require.NotEmpty(t, redirectURL)

	parsed, parseErr := url.Parse(redirectURL)
	require.NoError(t, parseErr)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "sentry.io", parsed.Host)
	require.Equal(t, "/oauth/authorize/", parsed.Path)
	require.Equal(t, "sentry-client-id", parsed.Query().Get("client_id"))
	require.Equal(t, "code", parsed.Query().Get("response_type"))
	require.Equal(t, "http://localhost:8080/api/v1/integrations/sentry/callback", parsed.Query().Get("redirect_uri"))
	require.Contains(t, parsed.Query().Get("scope"), "org:read")
	require.Contains(t, parsed.Query().Get("scope"), "project:read")
	require.Contains(t, parsed.Query().Get("scope"), "event:read")
	require.NotEmpty(t, parsed.Query().Get("state"))

	setCookie := w.Result().Header.Get("Set-Cookie")
	require.Contains(t, setCookie, "sentry_integration_oauth_state=")
}

func TestIntegrationHandler_StartSentryOAuth_NotConfigured(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/sentry/login", nil)
	w := httptest.NewRecorder()

	handler.StartSentryOAuth(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "SENTRY_OAUTH_NOT_CONFIGURED")
}

func TestIntegrationHandler_HandleSentryOAuthCallback_SavesCredentialAndIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	credentialStore := db.NewOrgCredentialStore(mock, nil)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://sentry.io/oauth/token/":
			respBody := `{"access_token":"sentry-access-token","refresh_token":"sentry-refresh-token","token_type":"bearer","scope":"org:read project:read event:read"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(respBody)),
			}, nil
		case req.URL.String() == "https://sentry.io/api/0/organizations/":
			respBody := `[{"slug":"acme-corp","name":"Acme Corp"}]`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(respBody)),
			}, nil
		default:
			return nil, errors.New("unexpected request URL: " + req.URL.String())
		}
	})

	handler := NewIntegrationHandler(
		store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000",
		WithSentryOAuth("sentry-client-id", "sentry-client-secret"),
	)
	handler.client = &http.Client{Transport: transport}

	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/sentry/callback?code=sentry-auth-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "sentry_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleSentryOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)
	require.Equal(t, "http://localhost:3000/integrations?sentry=connected", w.Header().Get("Location"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_HandleSentryOAuthCallback_StateMismatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithSentryOAuth("sentry-client-id", "sentry-client-secret"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/sentry/callback?code=test-code&state=wrong-state", nil)
	req.AddCookie(&http.Cookie{Name: "sentry_integration_oauth_state", Value: "correct-state"})
	w := httptest.NewRecorder()

	handler.HandleSentryOAuthCallback(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_STATE")
}

func TestIntegrationHandler_HandleSentryOAuthCallback_MissingCode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithSentryOAuth("sentry-client-id", "sentry-client-secret"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/sentry/callback?state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "sentry_integration_oauth_state", Value: "test-state"})
	w := httptest.NewRecorder()

	handler.HandleSentryOAuthCallback(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "MISSING_CODE")
}

func TestIntegrationHandler_HandleSentryOAuthCallback_TokenExchangeFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"invalid_grant"}`)),
		}, nil
	})

	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithSentryOAuth("sentry-client-id", "sentry-client-secret"),
	)
	handler.client = &http.Client{Transport: transport}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/sentry/callback?code=bad-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "sentry_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleSentryOAuthCallback(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "TOKEN_EXCHANGE_FAILED")
}

func TestIntegrationHandler_HandleSentryOAuthCallback_CredentialStoreUnavailable(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://sentry.io/oauth/token/":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`{"access_token":"tok","refresh_token":"ref","token_type":"bearer"}`)),
			}, nil
		case req.URL.String() == "https://sentry.io/api/0/organizations/":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`[{"slug":"acme","name":"Acme"}]`)),
			}, nil
		default:
			return nil, errors.New("unexpected request")
		}
	})

	// Credential store is nil
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithSentryOAuth("sentry-id", "sentry-secret"),
	)
	handler.client = &http.Client{Transport: transport}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/sentry/callback?code=test-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "sentry_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleSentryOAuthCallback(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "CREDENTIAL_STORE_UNAVAILABLE")
}

func TestIntegrationHandler_ConnectSentry_CreatesIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/sentry/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectSentry(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var resp models.SingleResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, models.IntegrationProviderSentry, resp.Data.Provider)
	require.Equal(t, models.IntegrationStatusActive, resp.Data.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ConnectSentry_ReturnsExisting(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(integrationID, orgID, "sentry", json.RawMessage(`{}`), "active", nil, now),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/sentry/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectSentry(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp models.SingleResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, integrationID, resp.Data.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ──────────────────────────────────────────────────────────────────────────────
// GitHub Integration OAuth
// ──────────────────────────────────────────────────────────────────────────────

func TestIntegrationHandler_StartGitHubOAuth_RedirectsToGitHubAuthorize(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithGitHubIntegrationOAuth("github-client-id", "github-client-secret"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/github/login", nil)
	w := httptest.NewRecorder()

	handler.StartGitHubOAuth(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)

	redirectURL := w.Header().Get("Location")
	require.NotEmpty(t, redirectURL)

	parsed, parseErr := url.Parse(redirectURL)
	require.NoError(t, parseErr)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "github.com", parsed.Host)
	require.Equal(t, "/login/oauth/authorize", parsed.Path)
	require.Equal(t, "github-client-id", parsed.Query().Get("client_id"))
	require.Equal(t, "http://localhost:8080/api/v1/integrations/github/callback", parsed.Query().Get("redirect_uri"))
	require.Contains(t, parsed.Query().Get("scope"), "repo")
	require.Contains(t, parsed.Query().Get("scope"), "read:org")
	require.NotEmpty(t, parsed.Query().Get("state"))

	setCookie := w.Result().Header.Get("Set-Cookie")
	require.Contains(t, setCookie, "github_integration_oauth_state=")
}

func TestIntegrationHandler_StartGitHubOAuth_NotConfigured(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/github/login", nil)
	w := httptest.NewRecorder()

	handler.StartGitHubOAuth(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "GITHUB_OAUTH_NOT_CONFIGURED")
}

func TestIntegrationHandler_StartGitHubOAuth_AppSlugSetsStateCookie(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithGitHubIntegrationOAuth("github-client-id", "github-client-secret"),
		WithGitHubAppSlug("my-app"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/github/login", nil)
	w := httptest.NewRecorder()

	handler.StartGitHubOAuth(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)

	redirectURL := w.Header().Get("Location")
	require.NotEmpty(t, redirectURL)

	parsed, parseErr := url.Parse(redirectURL)
	require.NoError(t, parseErr)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "github.com", parsed.Host)
	require.Equal(t, "/apps/my-app/installations/new", parsed.Path)
	require.NotEmpty(t, parsed.Query().Get("state"), "state parameter must be set for CSRF validation on callback")

	setCookie := w.Result().Header.Get("Set-Cookie")
	require.Contains(t, setCookie, "github_integration_oauth_state=", "state cookie must be set so the callback can validate it")
}

func TestIntegrationHandler_HandleGitHubOAuthCallback_SavesCredentialAndIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	credentialStore := db.NewOrgCredentialStore(mock, nil)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == "https://github.com/login/oauth/access_token" {
			respBody := `{"access_token":"gho_github-access-token","token_type":"bearer","scope":"repo,read:org"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(respBody)),
			}, nil
		}
		return nil, errors.New("unexpected request URL: " + req.URL.String())
	})

	handler := NewIntegrationHandler(
		store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000",
		WithGitHubIntegrationOAuth("github-client-id", "github-client-secret"),
	)
	handler.client = &http.Client{Transport: transport}

	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/github/callback?code=gh-auth-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "github_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleGitHubOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)
	require.Equal(t, "http://localhost:3000/integrations?github=connected", w.Header().Get("Location"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_HandleGitHubOAuthCallback_StateMismatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithGitHubIntegrationOAuth("gh-id", "gh-secret"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/github/callback?code=test-code&state=wrong-state", nil)
	req.AddCookie(&http.Cookie{Name: "github_integration_oauth_state", Value: "correct-state"})
	w := httptest.NewRecorder()

	handler.HandleGitHubOAuthCallback(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_STATE")
}

func TestIntegrationHandler_HandleGitHubOAuthCallback_MissingCode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithGitHubIntegrationOAuth("gh-id", "gh-secret"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/github/callback?state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "github_integration_oauth_state", Value: "test-state"})
	w := httptest.NewRecorder()

	handler.HandleGitHubOAuthCallback(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "MISSING_CODE")
}

func TestIntegrationHandler_HandleGitHubOAuthCallback_TokenExchangeFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"bad_verification_code"}`)),
		}, nil
	})

	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithGitHubIntegrationOAuth("gh-id", "gh-secret"),
	)
	handler.client = &http.Client{Transport: transport}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/github/callback?code=bad-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "github_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleGitHubOAuthCallback(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "TOKEN_EXCHANGE_FAILED")
}

func TestIntegrationHandler_ConnectGitHub_CreatesIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/github/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectGitHub(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var resp models.SingleResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, models.IntegrationProviderGitHub, resp.Data.Provider)
	require.Equal(t, models.IntegrationStatusActive, resp.Data.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ConnectGitHub_ReturnsExisting(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(integrationID, orgID, "github", json.RawMessage(`{}`), "active", nil, now),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/github/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectGitHub(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp models.SingleResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, integrationID, resp.Data.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ConnectGitHub_DBError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db unavailable"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/github/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectGitHub(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "CONNECT_GITHUB_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ──────────────────────────────────────────────────────────────────────────────
// Shared: ensureIntegration
// ──────────────────────────────────────────────────────────────────────────────

func TestIntegrationHandler_EnsureIntegration_CreateFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	// No existing integrations
	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("insert failed"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/sentry/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectSentry(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "CONNECT_SENTRY_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ──────────────────────────────────────────────────────────────────────────────
// OAuth state helper tests
// ──────────────────────────────────────────────────────────────────────────────

func TestSetOAuthState(t *testing.T) {
	t.Parallel()

	t.Run("sets cookie and returns state", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()

		state, err := setOAuthState(w, "test_oauth_state")
		require.NoError(t, err)
		require.NotEmpty(t, state)

		cookies := w.Result().Cookies()
		require.Len(t, cookies, 1)
		c := cookies[0]
		require.Equal(t, "test_oauth_state", c.Name)
		require.Equal(t, state, c.Value)
		require.Equal(t, "/", c.Path)
		require.Equal(t, 600, c.MaxAge)
		require.True(t, c.HttpOnly)
		require.Equal(t, http.SameSiteLaxMode, c.SameSite)
	})

	t.Run("returns unique states on successive calls", func(t *testing.T) {
		t.Parallel()
		w1 := httptest.NewRecorder()
		w2 := httptest.NewRecorder()

		state1, err1 := setOAuthState(w1, "s")
		state2, err2 := setOAuthState(w2, "s")
		require.NoError(t, err1)
		require.NoError(t, err2)
		require.NotEqual(t, state1, state2)
	})
}

func TestValidateOAuthCallback(t *testing.T) {
	t.Parallel()

	t.Run("valid state and code", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/callback?state=abc&code=mycode", nil)
		req.AddCookie(&http.Cookie{Name: "test_state", Value: "abc"})
		w := httptest.NewRecorder()

		code, ok := validateOAuthCallback(w, req, "test_state")
		require.True(t, ok)
		require.Equal(t, "mycode", code)

		// Cookie should be cleared
		cookies := w.Result().Cookies()
		require.Len(t, cookies, 1)
		require.Equal(t, "test_state", cookies[0].Name)
		require.Equal(t, -1, cookies[0].MaxAge, "cookie should be cleared")
	})

	t.Run("missing state cookie", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/callback?state=abc&code=mycode", nil)
		w := httptest.NewRecorder()

		code, ok := validateOAuthCallback(w, req, "test_state")
		require.False(t, ok)
		require.Empty(t, code)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_STATE")
	})

	t.Run("state cookie does not match query param", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/callback?state=abc&code=mycode", nil)
		req.AddCookie(&http.Cookie{Name: "test_state", Value: "xyz"})
		w := httptest.NewRecorder()

		code, ok := validateOAuthCallback(w, req, "test_state")
		require.False(t, ok)
		require.Empty(t, code)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_STATE")
	})

	t.Run("state cookie present but query param missing", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/callback?code=mycode", nil)
		req.AddCookie(&http.Cookie{Name: "test_state", Value: "abc"})
		w := httptest.NewRecorder()

		code, ok := validateOAuthCallback(w, req, "test_state")
		require.False(t, ok)
		require.Empty(t, code)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_STATE")
	})

	t.Run("valid state but missing code", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/callback?state=abc", nil)
		req.AddCookie(&http.Cookie{Name: "test_state", Value: "abc"})
		w := httptest.NewRecorder()

		code, ok := validateOAuthCallback(w, req, "test_state")
		require.False(t, ok)
		require.Empty(t, code)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_CODE")
	})

	t.Run("valid state but empty code param", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/callback?state=abc&code=", nil)
		req.AddCookie(&http.Cookie{Name: "test_state", Value: "abc"})
		w := httptest.NewRecorder()

		code, ok := validateOAuthCallback(w, req, "test_state")
		require.False(t, ok)
		require.Empty(t, code)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "MISSING_CODE")
	})
}
