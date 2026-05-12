package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/crypto"
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

func TestIntegrationHandler_ListIntegrations_SuppressesStaleDuplicateAuthError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now().UTC()
	activeID := uuid.New()
	staleID := uuid.New()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	rows := pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
		AddRow(activeID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now).
		AddRow(staleID, orgID, "linear", json.RawMessage(`{"last_auth_error":"Linear rejected the access token (HTTP 401). Reconnect to continue syncing.","last_auth_error_at":"2026-05-05T22:49:11Z"}`), "error", nil, now.Add(-time.Hour))
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListIntegrations(w, req)

	require.Equal(t, http.StatusOK, w.Code, "should return 200 for duplicate integration rows")
	var resp models.ListResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode as integration list")
	require.Len(t, resp.Data, 2, "response should preserve rows while deriving safe status fields")
	require.Nil(t, resp.Data[0].AuthError, "active Linear row without markers should not have auth_error")
	require.Nil(t, resp.Data[1].AuthError, "stale errored duplicate should not surface auth_error while an active Linear row exists")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_ListIntegrations_SurfacesAuthErrorWhenNoActiveDuplicate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now().UTC()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	rows := pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
		AddRow(uuid.New(), orgID, "linear", json.RawMessage(`{"last_auth_error":"Linear rejected the access token (HTTP 401). Reconnect to continue syncing.","last_auth_error_at":"2026-05-05T22:49:11Z"}`), "error", nil, now)
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListIntegrations(w, req)

	require.Equal(t, http.StatusOK, w.Code, "should return 200 for errored integration row")
	var resp models.ListResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode as integration list")
	require.Len(t, resp.Data, 1, "response should include the errored integration")
	require.NotNil(t, resp.Data[0].AuthError, "auth_error should surface when there is no active Linear duplicate")
	require.Equal(t, "Linear rejected the access token (HTTP 401). Reconnect to continue syncing.", resp.Data[0].AuthError.Reason, "auth_error reason should be preserved")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_ListIntegrations_DerivesGitHubAppInstalled(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now().UTC()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	rows := pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
		AddRow(uuid.New(), orgID, "github", []byte(`{}`), "active", nil, now).
		AddRow(uuid.New(), orgID, "github", []byte(`{"installation_id":12345}`), "active", nil, now).
		AddRow(uuid.New(), orgID, "linear", []byte(`{}`), "active", nil, now)
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListIntegrations(w, req)

	require.Equal(t, http.StatusOK, w.Code, "list integrations should succeed")

	var resp struct {
		Data []struct {
			Provider           string `json:"provider"`
			GitHubAppInstalled *bool  `json:"github_app_installed,omitempty"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "list integrations response should decode")
	require.Len(t, resp.Data, 3, "list integrations should return every integration row")
	require.NotNil(t, resp.Data[0].GitHubAppInstalled, "github integrations should include derived app-installed status")
	require.Equal(t, false, *resp.Data[0].GitHubAppInstalled, "github integration without installation_id should report not installed")
	require.NotNil(t, resp.Data[1].GitHubAppInstalled, "github integrations with installation_id should include derived app-installed status")
	require.Equal(t, true, *resp.Data[1].GitHubAppInstalled, "github integration with installation_id should report installed")
	require.Nil(t, resp.Data[2].GitHubAppInstalled, "non-github integrations should not include github app status")
	require.NoError(t, mock.ExpectationsWereMet(), "all integration-store expectations should be met")
}

func TestIntegrationHandler_ListIntegrations_DerivesGitHubAppInstalled_FromRepoFallback(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now().UTC()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.repoStore = db.NewRepositoryStore(mock)

	rows := pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
		AddRow(uuid.New(), orgID, "github", []byte(`{}`), "active", nil, now)
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)
	mock.ExpectQuery("SELECT installation_id FROM repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"installation_id"}).AddRow(int64(12345)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListIntegrations(w, req)

	require.Equal(t, http.StatusOK, w.Code, "list integrations should succeed")

	var resp struct {
		Data []struct {
			GitHubAppInstalled *bool `json:"github_app_installed,omitempty"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "list integrations response should decode")
	require.Len(t, resp.Data, 1, "list integrations should return the github integration")
	require.NotNil(t, resp.Data[0].GitHubAppInstalled, "github integration should include derived app-installed status")
	require.Equal(t, true, *resp.Data[0].GitHubAppInstalled, "repo fallback should mark the github app as installed")
	require.NoError(t, mock.ExpectationsWereMet(), "all integration-store expectations should be met")
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

func TestIntegrationHandler_DisconnectIntegration_DisconnectsGitHubRepos(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.repoStore = db.NewRepositoryStore(mock)

	rows := pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
		AddRow(integrationID, orgID, "github", []byte(`{"installation_id":12345}`), "active", nil, now)
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE integrations SET status = @status WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE repositories SET status = 'disconnected', updated_at = now\\(\\) WHERE org_id = @org_id AND integration_id = @integration_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/github/disconnect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.DisconnectIntegration(w, req)

	require.Equal(t, http.StatusNoContent, w.Code, "disconnect integration should succeed")
	require.NoError(t, mock.ExpectationsWereMet(), "disconnect integration should also disconnect github repos")
}

func TestIntegrationHandler_DisconnectIntegration_GitHubRepoDisconnectError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.repoStore = db.NewRepositoryStore(mock)

	rows := pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
		AddRow(integrationID, orgID, "github", []byte(`{"installation_id":12345}`), "active", nil, now)
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE integrations SET status = @status WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE repositories SET status = 'disconnected', updated_at = now\\(\\) WHERE org_id = @org_id AND integration_id = @integration_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/github/disconnect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.DisconnectIntegration(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "disconnect integration should surface github repo disconnect failures")
	require.Contains(t, w.Body.String(), "UPDATE_FAILED", "disconnect integration should return update failed on github repo disconnect errors")
	require.NoError(t, mock.ExpectationsWereMet(), "all integration-store expectations should be met")
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
	require.Equal(t, strings.Join(models.LinearAgentRequiredScopes, ","), parsed.Query().Get("scope"),
		"redirect must include the agent scopes (app:assignable, app:mentionable) so Linear provisions the @143 agent user; "+
			"offline_access is intentionally NOT in the list — Linear rejects it as invalid (see PR #816) and returns refresh_token automatically without any special scope")
	require.NotEmpty(t, parsed.Query().Get("state"), "redirect should include oauth state")

	setCookie := w.Result().Header.Get("Set-Cookie")
	require.Contains(t, setCookie, "linear_integration_oauth_state=", "StartLinearOAuth should set linear oauth state cookie")
}

func TestIntegrationHandler_ExchangeLinearCode_UsesFormEncodedBody(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "linear-client-id", "linear-client-secret", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodPost, req.Method, "exchangeLinearCode should POST to Linear")
			require.Equal(t, "application/x-www-form-urlencoded", req.Header.Get("Content-Type"), "exchangeLinearCode should use Linear's required form content type")

			body, readErr := io.ReadAll(req.Body)
			require.NoError(t, readErr, "exchangeLinearCode request body should be readable")
			values, parseErr := url.ParseQuery(string(body))
			require.NoError(t, parseErr, "exchangeLinearCode should send a URL-encoded body")
			require.Equal(t, "authorization_code", values.Get("grant_type"), "exchangeLinearCode should send the authorization_code grant")
			require.Equal(t, "linear-code", values.Get("code"), "exchangeLinearCode should include the callback code")
			require.Equal(t, "http://localhost:8080/api/v1/integrations/linear/callback", values.Get("redirect_uri"), "exchangeLinearCode should include the callback URL")
			require.Equal(t, "linear-client-id", values.Get("client_id"), "exchangeLinearCode should include the client id")
			require.Equal(t, "linear-client-secret", values.Get("client_secret"), "exchangeLinearCode should include the client secret")

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`{"access_token":"linear-token","token_type":"Bearer","expires_in":86400,"scope":"read write","refresh_token":"linear-refresh"}`)),
			}, nil
		}),
	}

	token, err := handler.exchangeLinearCode(context.Background(), "linear-code")

	require.NoError(t, err, "exchangeLinearCode should parse a successful token response")
	require.Equal(t, "linear-token", token.AccessToken, "exchangeLinearCode should return the access token")
	require.Equal(t, "linear-refresh", token.RefreshToken, "exchangeLinearCode should capture the refresh token")
	require.Equal(t, 86400, token.ExpiresIn, "exchangeLinearCode should capture the expires_in TTL so callers can compute ExpiresAt")
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

// TestIntegrationHandler_HandleLinearOAuthCallback_PersistsRefreshTokenAndExpiry
// is the round-trip integration test for Linear's refresh-token response:
// when Linear includes refresh_token + expires_in, those fields must end
// up inside the persisted LinearConfig JSON. Without this test, a future
// refactor that drops the merge (e.g. forgets to map ExpiresIn to ExpiresAt)
// would compile, pass unit tests, and silently regress the entire refresh
// capability — every new connection would look healthy until the access
// token aged out.
//
// The fakeCrypto is nil so OrgCredentialStore uses DevEncrypt — a
// reversible "v0:<plaintext>" wrapper. We capture the encrypted bytes
// from pgxmock, strip the prefix, and assert on the JSON fields. This
// is the highest-value test in this file because it verifies the
// refresh-flow plumbing end-to-end through the OAuth callback rather
// than via a unit mock.
func TestIntegrationHandler_HandleLinearOAuthCallback_PersistsRefreshTokenAndExpiry(t *testing.T) {
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
		switch req.URL.String() {
		case "https://api.linear.app/oauth/token":
			respBody := `{"access_token":"lin_at","refresh_token":"lin_rt","token_type":"Bearer","scope":"read,write","expires_in":7200}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(respBody))}, nil
		case "https://api.linear.app/graphql":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(`{"data":{"viewer":{"organization":{"id":"lin-org-1","name":"Acme"}}}}`))}, nil
		default:
			return nil, errors.New("unexpected request URL")
		}
	})

	handler := NewIntegrationHandler(store, credentialStore, "linear-client-id", "linear-client-secret", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{Transport: transport}

	// Custom matcher captures the encrypted credential blob so we can
	// decrypt and assert on the JSON. argMatcher must be the 4th
	// pgxmock arg (the @config bytes) per OrgCredentialStore.UpsertWithLabel.
	var capturedConfigBytes []byte
	configCapture := pgxmock.QueryMatcherFunc(func(_ string, _ string) error { return nil })
	_ = configCapture
	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), capturingArg(&capturedConfigBytes), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())

	// Decrypt the captured bytes (DevEncrypt = "v0:" prefix + plaintext).
	require.NotEmpty(t, capturedConfigBytes, "callback should have written the credential")
	plaintext, err := crypto.DevDecrypt(capturedConfigBytes)
	require.NoError(t, err, "captured bytes should be DevEncrypt'd")

	var persisted models.LinearConfig
	require.NoError(t, json.Unmarshal(plaintext, &persisted), "persisted config should decode as LinearConfig")

	require.Equal(t, "lin_at", persisted.AccessToken, "access_token must be persisted")
	require.Equal(t, "lin_rt", persisted.RefreshToken, "refresh_token must be persisted — without this the whole refresh flow breaks")
	require.Equal(t, "Bearer", persisted.TokenType)
	require.Equal(t, "read,write", persisted.Scope)
	require.Equal(t, "lin-org-1", persisted.WorkspaceID)
	require.Equal(t, "Acme", persisted.WorkspaceName)
	// expires_in=7200 → ExpiresAt ~2h from now. Allow a generous skew
	// because the callback computes ExpiresAt = time.Now()+expires_in
	// and the test reads ExpiresAt some milliseconds later.
	require.WithinDuration(t, time.Now().Add(2*time.Hour), persisted.ExpiresAt, 30*time.Second, "ExpiresAt must reflect expires_in=7200")
}

// capturingArg returns a pgxmock argument matcher that records the value
// it sees into the supplied byte slice pointer. Used for asserting on
// encrypted config payloads that we want to decrypt and inspect.
func capturingArg(dest *[]byte) pgxmock.Argument {
	return capturingArgImpl{dest: dest}
}

type capturingArgImpl struct {
	dest *[]byte
}

func (c capturingArgImpl) Match(v interface{}) bool {
	switch b := v.(type) {
	case []byte:
		*c.dest = append((*c.dest)[:0], b...)
	case string:
		*c.dest = append((*c.dest)[:0], []byte(b)...)
	default:
		return false
	}
	return true
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

// TestIntegrationHandler_HandleLinearOAuthCallback_AsyncRefreshAndEnqueue
// pins the post-OAuth refresh strategy: the inline RefreshTeamKeys hook
// fires in a detached background goroutine (so the redirect doesn't block
// on Linear's API), and the worker enqueue fires unconditionally as the
// durable fallback so a slow / failing inline path still has a guaranteed
// retry. Both paths share the same dedupe key so re-installs collapse.
func TestIntegrationHandler_HandleLinearOAuthCallback_AsyncRefreshAndEnqueue(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	jobID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	credentialStore := db.NewOrgCredentialStore(mock, nil)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://api.linear.app/oauth/token":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`{"access_token":"linear-access-token","token_type":"Bearer","scope":"read"}`)),
			}, nil
		case "https://api.linear.app/graphql":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`{"data":{"viewer":{"organization":{"id":"lin-org-1","name":"Acme"}}}}`)),
			}, nil
		default:
			return nil, errors.New("unexpected request URL")
		}
	})

	handler := NewIntegrationHandler(store, credentialStore, "linear-client-id", "linear-client-secret", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{Transport: transport}
	handler.SetLinearJobStore(db.NewJobStore(mock))

	inlineCalls := make(chan uuid.UUID, 1)
	handler.SetLinearTeamKeyRefresher(func(_ context.Context, gotOrg uuid.UUID) error {
		// Buffered channel keeps this non-blocking even if the test exits
		// before this goroutine runs; the assertion below uses a generous
		// timeout to catch the goroutine in CI without flaking.
		select {
		case inlineCalls <- gotOrg:
		default:
		}
		return nil
	})

	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))
	// Worker enqueue always fires now: redirect must not block on inline
	// refresh, so the worker job is the durable fallback.
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/callback?code=test-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "linear_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleLinearOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "callback should redirect immediately, not wait for the team-key refresh")
	select {
	case got := <-inlineCalls:
		require.Equal(t, orgID, got, "inline refresher must receive the callback's org id")
	case <-time.After(2 * time.Second):
		t.Fatal("inline RefreshTeamKeys goroutine never ran within 2s")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "exactly one INSERT INTO jobs should fire as the durable fallback")
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

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

func TestIntegrationHandler_ConnectLinear_ReactivatesErroredIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(integrationID, orgID, "linear", json.RawMessage(`{"workspace_id":"wks-1","last_auth_error":"prior","last_auth_error_at":"2026-05-02T20:02:11Z"}`), "error", nil, now),
		)

	// Single atomic UPDATE: status and config flip together so the row
	// can't be observed mid-flip.
	mock.ExpectExec("UPDATE integrations SET status = @status, config = @config").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/linear/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectLinear(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ConnectLinear should reuse an errored integration")

	var resp models.SingleResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "ConnectLinear response should be valid JSON")
	require.Equal(t, integrationID, resp.Data.ID, "ConnectLinear should return the existing errored integration ID")
	require.Equal(t, models.IntegrationStatusActive, resp.Data.Status, "ConnectLinear should reactivate the errored integration")
	require.NotContains(t, string(resp.Data.Config), "last_auth_error", "ConnectLinear should strip stale auth-error markers")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestIntegrationHandler_ConnectLinear_ConvergesDuplicateErroredRow pins
// the duplicate-row cleanup. When ListReusableForReconnect returns the
// canonical active row plus a stale errored duplicate (historical state
// from before ensureIntegration was the only write path), the duplicate
// must also have its auth_error markers stripped and status flipped to
// active. Otherwise /api/v1/integrations would surface auth_error from
// the orphan row and keep the Reconnect CTA visible after a healthy
// reconnect against the canonical row.
func TestIntegrationHandler_ConnectLinear_ConvergesDuplicateErroredRow(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	canonicalID := uuid.New()
	duplicateID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	// Active row first (per the SQL ORDER BY), then the errored duplicate.
	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(canonicalID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now).
				AddRow(duplicateID, orgID, "linear", json.RawMessage(`{"last_auth_error":"prior","last_auth_error_at":"2026-05-05T22:49:11Z"}`), "error", nil, now),
		)

	// Canonical row is already active+empty: no UPDATE for it. The
	// duplicate is errored with auth-error markers, so a single atomic
	// flip should fire for that one ID.
	mock.ExpectExec("UPDATE integrations SET status = @status, config = @config").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/linear/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectLinear(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ConnectLinear should reuse the canonical active row")
	var resp models.SingleResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "ConnectLinear response should be valid JSON")
	require.Equal(t, canonicalID, resp.Data.ID, "ConnectLinear should return the canonical (active) integration ID, not the duplicate")
	require.NoError(t, mock.ExpectationsWereMet(), "duplicate row must also be flipped to active+stripped")
}

// TestIntegrationHandler_ConnectLinear_PropagatesDuplicateConvergeFailure
// pins that DB errors during duplicate-row convergence surface as 500
// rather than getting swallowed. Silent failure here is what produced
// the stale-duplicate-row bug to begin with — a "successful" reconnect
// that quietly left an orphan errored row visible to the integrations
// list, keeping the Reconnect CTA pinned on after the canonical row was
// healthy.
func TestIntegrationHandler_ConnectLinear_PropagatesDuplicateConvergeFailure(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	canonicalID := uuid.New()
	duplicateID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(canonicalID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now).
				AddRow(duplicateID, orgID, "linear", json.RawMessage(`{"last_auth_error":"prior","last_auth_error_at":"2026-05-05T22:49:11Z"}`), "error", nil, now),
		)
	// Duplicate-row UPDATE blows up — the OAuth flow must surface this
	// as 500, not return 200 with a half-converged DB.
	mock.ExpectExec("UPDATE integrations SET status = @status, config = @config").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("update failed"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/linear/connect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectLinear(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "ConnectLinear must return 500 when duplicate-row converge fails")
	require.Contains(t, w.Body.String(), "CONNECT_LINEAR_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ConnectLinear_ReturnsInternalErrorWhenLookupFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

func TestIntegrationHandler_HandleGitHubOAuthCallback_DelegatesToAppInstalled(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithGitHubIntegrationOAuth("gh-id", "gh-secret"),
	)

	// When installation_id and setup_action=install are present, the callback
	// handler should delegate to HandleGitHubAppInstalled instead of trying
	// to exchange a code. Expect the ensureIntegration + UpdateConfig queries.
	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))

	mock.ExpectExec("UPDATE integrations SET config").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/integrations/github/callback?code=some-code&installation_id=12345&setup_action=install&state=test-state", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleGitHubOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)
	require.Equal(t, "http://localhost:3000/integrations?github=connected", w.Header().Get("Location"))
	require.NoError(t, mock.ExpectationsWereMet())
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

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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
	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
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

// ──────────────────────────────────────────────────────────────────────────────
// Notion connect (token-based)
// ──────────────────────────────────────────────────────────────────────────────

func TestIntegrationHandler_ConnectNotion_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	store := db.NewIntegrationStore(mock)
	credentialStore := db.NewOrgCredentialStore(mock, nil)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "https://api.notion.com/v1/users/me", req.URL.String())
		require.Equal(t, "Bearer ntn_test_token", req.Header.Get("Authorization"))
		require.Equal(t, "2022-06-28", req.Header.Get("Notion-Version"))

		respBody := `{"bot":{"workspace_name":"Test Workspace"}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(respBody)),
		}, nil
	})

	handler := NewIntegrationHandler(
		store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000",
	)
	handler.client = &http.Client{Transport: transport}

	// Expect credential upsert.
	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	// Expect integration check (none exists).
	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	// Expect integration creation.
	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))

	body := `{"access_token":"ntn_test_token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/notion/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectNotion(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	require.Contains(t, w.Body.String(), `"provider":"notion"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ConnectNotion_MissingToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	body := `{"access_token":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/notion/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectNotion(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "MISSING_TOKEN")
}

func TestIntegrationHandler_ConnectNotion_InvalidToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"status":401,"code":"unauthorized"}`)),
		}, nil
	})

	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{Transport: transport}

	body := `{"access_token":"bad_token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/notion/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectNotion(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_TOKEN")
	require.Contains(t, w.Body.String(), "invalid or expired token")
}

func TestIntegrationHandler_ConnectNotion_InvalidJSON(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/notion/connect", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectNotion(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_JSON")
}
