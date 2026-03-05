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

func TestNewIntegrationHandler(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	require.NotNil(t, handler, "handler should not be nil")
}

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
	require.Contains(t, setCookie, "linear_oauth_state=", "StartLinearOAuth should set linear oauth state cookie")
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
	req.AddCookie(&http.Cookie{Name: "linear_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleLinearOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "HandleLinearOAuthCallback should redirect after successful OAuth")
	require.Equal(t, "http://localhost:3000/integrations?linear=connected", w.Header().Get("Location"), "HandleLinearOAuthCallback should redirect to integrations page with success state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
