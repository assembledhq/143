package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	ghapp "github.com/assembledhq/143/internal/services/github"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeGitHubAppService struct {
	token string
	err   error
}

func (f *fakeGitHubAppService) GetInstallationToken(context.Context, int64) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.token, nil
}

type fakeGitHubAppUserAuth struct {
	credential *models.GitHubAppUserConfig
	err        error
}

func (f fakeGitHubAppUserAuth) GetValidCredential(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.credential, nil
}

type fakeGitHubMembershipStore struct {
	membership models.OrganizationMembership
	err        error
}

func (f fakeGitHubMembershipStore) Get(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
	if f.err != nil {
		return models.OrganizationMembership{}, f.err
	}
	return f.membership, nil
}

type fakeIntegrationCredentialStore struct {
	credentials map[models.ProviderName]*models.DecryptedCredential
	err         error
	disabled    *[]models.ProviderName
}

func (f fakeIntegrationCredentialStore) Get(_ context.Context, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if f.err != nil {
		return nil, f.err
	}
	credential, ok := f.credentials[provider]
	if !ok {
		return nil, nil
	}
	return credential, nil
}

func (f fakeIntegrationCredentialStore) Upsert(context.Context, uuid.UUID, models.ProviderConfig) error {
	return nil
}

func (f fakeIntegrationCredentialStore) Disable(_ context.Context, _ uuid.UUID, provider models.ProviderName) error {
	if f.err != nil {
		return f.err
	}
	if f.disabled != nil {
		*f.disabled = append(*f.disabled, provider)
	}
	return nil
}

type fakeSlackUserInfoClient struct {
	user ingestion.SlackUser
	err  error
}

func (f fakeSlackUserInfoClient) FetchUserInfo(context.Context, string, string) (ingestion.SlackUser, error) {
	if f.err != nil {
		return ingestion.SlackUser{}, f.err
	}
	return f.user, nil
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

func TestIntegrationHandler_ListIntegrations_DerivesSafeCredentialMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now().UTC()
	store := db.NewIntegrationStore(mock)
	credentialStore := fakeIntegrationCredentialStore{
		credentials: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderNotion: {
				OrgID:    orgID,
				Provider: models.ProviderNotion,
				Config: models.NotionConfig{
					AccessToken:   "secret-notion-token",
					WorkspaceName: "Acme HQ",
				},
			},
			models.ProviderCircleCI: {
				OrgID:    orgID,
				Provider: models.ProviderCircleCI,
				Config: models.CircleCIConfig{
					AuthToken:   "secret-circle-token",
					ProjectSlug: "gh/acme/api",
				},
			},
			models.ProviderMezmo: {
				OrgID:    orgID,
				Provider: models.ProviderMezmo,
				Config: models.MezmoConfig{
					APIKey:  "secret-mezmo-key",
					BaseURL: "https://logs.acme.com",
					Dataset: "prod",
				},
			},
		},
	}
	handler := NewIntegrationHandler(store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000")

	rows := pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
		AddRow(uuid.New(), orgID, "notion", json.RawMessage(`{}`), "active", nil, now).
		AddRow(uuid.New(), orgID, "circleci", json.RawMessage(`{}`), "active", nil, now).
		AddRow(uuid.New(), orgID, "mezmo", json.RawMessage(`{}`), "active", nil, now)
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListIntegrations(w, req)

	require.Equal(t, http.StatusOK, w.Code, "list integrations should succeed")
	var resp models.ListResponse[models.Integration]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode as integration list")
	require.Len(t, resp.Data, 3, "response should include all integrations")

	var notionIntegration, circleciIntegration, mezmoIntegration *models.Integration
	for i := range resp.Data {
		switch resp.Data[i].Provider {
		case models.IntegrationProviderNotion:
			notionIntegration = &resp.Data[i]
		case models.IntegrationProviderCircleCI:
			circleciIntegration = &resp.Data[i]
		case models.IntegrationProviderMezmo:
			mezmoIntegration = &resp.Data[i]
		}
	}
	require.NotNil(t, notionIntegration, "Notion integration should be present in response")
	require.NotNil(t, notionIntegration.NotionWorkspaceName, "Notion workspace name should be derived from credential metadata")
	require.Equal(t, "Acme HQ", *notionIntegration.NotionWorkspaceName, "Notion workspace name should be exposed without token data")
	require.NotNil(t, circleciIntegration, "CircleCI integration should be present in response")
	require.NotNil(t, circleciIntegration.CircleCIProjectSlug, "CircleCI project slug should be derived from credential metadata")
	require.Equal(t, "gh/acme/api", *circleciIntegration.CircleCIProjectSlug, "CircleCI project slug should be exposed without token data")
	require.NotNil(t, mezmoIntegration, "Mezmo integration should be present in response")
	require.Nil(t, mezmoIntegration.MezmoDataset, "Mezmo dataset should not be exposed because dataset scoping is unsupported")
	require.NotNil(t, mezmoIntegration.MezmoBaseURL, "Mezmo base URL should be derived from credential metadata")
	require.Equal(t, "https://logs.acme.com", *mezmoIntegration.MezmoBaseURL, "Mezmo base URL should be exposed without key data")
	require.NotContains(t, w.Body.String(), "secret-notion-token", "response should not expose Notion token")
	require.NotContains(t, w.Body.String(), "secret-circle-token", "response should not expose CircleCI token")
	require.NotContains(t, w.Body.String(), "secret-mezmo-key", "response should not expose Mezmo key")
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
	handler.githubInstallations = db.NewGitHubInstallationStore(mock)
	handler.githubInstallations = db.NewGitHubInstallationStore(mock)

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
	handler.githubInstallations = db.NewGitHubInstallationStore(mock)

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
	mock.ExpectExec("UPDATE github_installation_org_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

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

// Disconnecting a credential-backed provider must disable the stored org
// credential, not just flip the integration row to inactive — otherwise the
// sandbox env-injection path (which reads org_credentials, not integrations)
// keeps handing the secret to agents after the user thinks they revoked it.
func TestIntegrationHandler_DisconnectIntegration_DisablesCredential(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	store := db.NewIntegrationStore(mock)

	disabled := make([]models.ProviderName, 0, 1)
	credentialStore := fakeIntegrationCredentialStore{disabled: &disabled}
	handler := NewIntegrationHandler(store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000")

	rows := pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
		AddRow(integrationID, orgID, "mezmo", []byte(`{}`), "active", nil, now)
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE integrations SET status = @status WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/mezmo/disconnect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.DisconnectIntegration(w, req)

	require.Equal(t, http.StatusNoContent, w.Code, "disconnect should succeed")
	require.Equal(t, []models.ProviderName{models.ProviderMezmo}, disabled,
		"disconnect should disable the stored mezmo credential so it stops being injected")
	require.NoError(t, mock.ExpectationsWereMet(), "all integration-store expectations should be met")
}

// A credential-disable failure must fail the disconnect rather than silently
// leaving an injectable secret behind.
func TestIntegrationHandler_DisconnectIntegration_CredentialDisableError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	store := db.NewIntegrationStore(mock)

	credentialStore := fakeIntegrationCredentialStore{err: context.DeadlineExceeded}
	handler := NewIntegrationHandler(store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000")

	rows := pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
		AddRow(integrationID, orgID, "mezmo", []byte(`{}`), "active", nil, now)
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE integrations SET status = @status WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/mezmo/disconnect", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.DisconnectIntegration(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "a credential-disable failure must fail the disconnect")
	require.Contains(t, w.Body.String(), "UPDATE_FAILED")
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
	expectNoExistingCredentialLookup(mock)
	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), capturingArg(&capturedConfigBytes), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))
	expectLinearWorkspaceIDPersist(mock)

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

func TestIntegrationHandler_HandleLinearOAuthCallback_PreservesWebhookSecretOnReauth(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	credentialID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	credentialStore := db.NewOrgCredentialStore(mock, nil)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://api.linear.app/oauth/token":
			respBody := `{"access_token":"lin_at_new","refresh_token":"lin_rt_new","token_type":"Bearer","scope":"read,write","expires_in":7200}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(respBody))}, nil
		case "https://api.linear.app/graphql":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(`{"data":{"viewer":{"organization":{"id":"lin-org-1","name":"Acme"}}}}`))}, nil
		default:
			return nil, errors.New("unexpected request URL")
		}
	})

	handler := NewIntegrationHandler(store, credentialStore, "linear-client-id", "linear-client-secret", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{Transport: transport}

	existingConfig := crypto.DevEncrypt([]byte(`{"webhook_secret":"lin_whsec_existing","access_token":"lin_at_old"}`))
	mock.ExpectQuery("SELECT id, org_id, provider, label, config").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "label", "config", "status", "last_verified_at", "last_used_at", "created_by", "created_at", "updated_at"}).
			AddRow(credentialID, orgID, string(models.ProviderLinear), "", existingConfig, "active", nil, nil, nil, now, now))

	var capturedConfigBytes []byte
	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), capturingArg(&capturedConfigBytes), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(credentialID))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))
	expectLinearWorkspaceIDPersist(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/callback?code=test-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "linear_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleLinearOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "callback should redirect after successful Linear OAuth")
	require.NoError(t, mock.ExpectationsWereMet(), "callback should read existing credential before upserting the refreshed config")

	plaintext, err := crypto.DevDecrypt(capturedConfigBytes)
	require.NoError(t, err, "captured bytes should be DevEncrypt'd")
	var persisted models.LinearConfig
	require.NoError(t, json.Unmarshal(plaintext, &persisted), "persisted config should decode as LinearConfig")
	require.Equal(t, "lin_whsec_existing", persisted.WebhookSecret, "Linear webhook secret must survive OAuth reauthorization")
	require.Equal(t, "lin_at_new", persisted.AccessToken, "OAuth callback should still persist the new access token")
}

// capturingArg returns a pgxmock argument matcher that records the value
// it sees into the supplied byte slice pointer. Used for asserting on
// encrypted config payloads that we want to decrypt and inspect.
func capturingArg(dest *[]byte) pgxmock.Argument {
	return capturingArgImpl{dest: dest}
}

func expectNoExistingCredentialLookup(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery("SELECT id, org_id, provider, label, config").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "label", "config", "status", "last_verified_at", "last_used_at", "created_by", "created_at", "updated_at"}))
}

func expectLinearWorkspaceIDPersist(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec("UPDATE integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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

	expectNoExistingCredentialLookup(mock)
	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))
	expectLinearWorkspaceIDPersist(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/callback?code=test-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "linear_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleLinearOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "HandleLinearOAuthCallback should redirect after successful OAuth")
	require.Equal(t, "http://localhost:3000/integrations?linear=connected", w.Header().Get("Location"), "HandleLinearOAuthCallback should redirect to integrations page with success state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_HandleLinearOAuthCallback_FailsWhenWorkspaceIDPersistenceFails(t *testing.T) {
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

	expectNoExistingCredentialLookup(mock)
	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))

	mock.ExpectExec("UPDATE integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("duplicate workspace binding"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/callback?code=test-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "linear_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleLinearOAuthCallback(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "callback should fail when workspace_id cannot be persisted")
	require.Contains(t, w.Body.String(), "CONNECT_LINEAR_FAILED", "response should make the Linear connection failure explicit")
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

	expectNoExistingCredentialLookup(mock)
	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))

	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))
	expectLinearWorkspaceIDPersist(mock)
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

func TestIntegrationHandler_HandleLinearOAuthCallback_RetriesTransientTeamKeyIntegrationMiss(t *testing.T) {
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

	inlineCalls := make(chan uuid.UUID, 2)
	var refreshAttempts atomic.Int32
	handler.SetLinearTeamKeyRefresher(func(_ context.Context, gotOrg uuid.UUID) error {
		inlineCalls <- gotOrg
		if refreshAttempts.Add(1) == 1 {
			return errors.New("lookup linear integration: linear integration not found")
		}
		return nil
	})

	expectNoExistingCredentialLookup(mock)
	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))
	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))
	expectLinearWorkspaceIDPersist(mock)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/callback?code=test-code&state=test-state", nil)
	req.AddCookie(&http.Cookie{Name: "linear_integration_oauth_state", Value: "test-state"})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleLinearOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "callback should redirect even when the inline refresh has to retry")
	for i := 0; i < 2; i++ {
		select {
		case got := <-inlineCalls:
			require.Equal(t, orgID, got, "inline refresher retry should keep the callback org id")
		case <-time.After(2 * time.Second):
			t.Fatal("inline RefreshTeamKeys retry never ran within 2s")
		}
	}
	require.NoError(t, mock.ExpectationsWereMet(), "worker fallback should still enqueue exactly once")
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

// TestIntegrationHandler_HandleLinearOAuthCallback_BootstrapperFiresWhenAgentScopesGranted
// pins the contract that the OAuth callback invokes the agent bootstrapper
// (auto-enable + auto-default-repo) exactly when the returned token carries
// the agent scopes. The bootstrapper is best-effort: a failure from it must
// not break the OAuth redirect, since the admin can always configure
// manually from Settings → Integrations → Linear → Agent.
func TestIntegrationHandler_HandleLinearOAuthCallback_BootstrapperFiresWhenAgentScopesGranted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		scope          string
		bootstrapErr   error
		expectFired    bool
		expectRedirect int
	}{
		{
			name:           "agent scopes granted → bootstrapper fires",
			scope:          "read,write,app:assignable,app:mentionable",
			expectFired:    true,
			expectRedirect: http.StatusTemporaryRedirect,
		},
		{
			name:           "legacy scopes only → bootstrapper does not fire",
			scope:          "read,write",
			expectFired:    false,
			expectRedirect: http.StatusTemporaryRedirect,
		},
		{
			name:           "agent scopes granted, bootstrapper errors → callback still redirects",
			scope:          "read,write,app:assignable,app:mentionable",
			bootstrapErr:   errors.New("boom"),
			expectFired:    true,
			expectRedirect: http.StatusTemporaryRedirect,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			integrationID := uuid.New()
			now := time.Now().UTC()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := db.NewIntegrationStore(mock)
			credentialStore := db.NewOrgCredentialStore(mock, nil)

			tokenBody := `{"access_token":"lin_at","token_type":"Bearer","scope":"` + tt.scope + `"}`
			viewerBody := `{"data":{"viewer":{"organization":{"id":"lin-org-1","name":"Acme"},"id":"appuser-1","name":"143"}}}`
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case "https://api.linear.app/oauth/token":
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(tokenBody))}, nil
				case "https://api.linear.app/graphql":
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(viewerBody))}, nil
				default:
					return nil, errors.New("unexpected request URL")
				}
			})

			handler := NewIntegrationHandler(store, credentialStore, "linear-client-id", "linear-client-secret", "http://localhost:8080", "http://localhost:3000")
			handler.client = &http.Client{Transport: transport}

			var fired bool
			var firedOrgID uuid.UUID
			handler.SetLinearAgentBootstrapper(func(_ context.Context, gotOrg uuid.UUID) error {
				fired = true
				firedOrgID = gotOrg
				return tt.bootstrapErr
			})

			expectNoExistingCredentialLookup(mock)
			mock.ExpectQuery("INSERT INTO org_credentials").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			mock.ExpectQuery("SELECT .+ FROM integrations .+ provider = @provider .+ status IN \\('active', 'error'\\)").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))
			mock.ExpectQuery("INSERT INTO integrations").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(integrationID, now))
			expectLinearWorkspaceIDPersist(mock)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/callback?code=test-code&state=test-state", nil)
			req.AddCookie(&http.Cookie{Name: "linear_integration_oauth_state", Value: "test-state"})
			req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
			w := httptest.NewRecorder()

			handler.HandleLinearOAuthCallback(w, req)

			require.Equal(t, tt.expectRedirect, w.Code, "OAuth callback should redirect regardless of bootstrap outcome — bootstrapper is best-effort")
			require.Equal(t, tt.expectFired, fired, "bootstrapper firing must be gated on HasAgentScopes()")
			if tt.expectFired {
				require.Equal(t, orgID, firedOrgID, "bootstrapper must receive the callback's org id")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
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

func TestIntegrationHandler_LinkSlackUserMeRejectsEmailMismatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	installationID := uuid.New()
	integrationID := uuid.New()
	handler := NewIntegrationHandler(
		db.NewIntegrationStore(mock),
		fakeIntegrationCredentialStore{credentials: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderSlack: {
				OrgID:    orgID,
				Provider: models.ProviderSlack,
				Config:   models.SlackConfig{AccessToken: "xoxb-token", TeamID: "T123"},
			},
		}},
		"", "", "http://localhost:8080", "http://localhost:3000",
		WithSlackUserInfoClient(fakeSlackUserInfoClient{user: slackTestUser("U999", "other@example.com")}),
	)
	handler.slackInstallationStore = db.NewSlackInstallationStore(mock)
	handler.slackUserLinkStore = db.NewSlackUserLinkStore(mock)
	expectSlackInstallationByOrg(mock, orgID, installationID, integrationID)

	body := strings.NewReader(`{"slack_user_id":"U999","slack_email":"other@example.com","slack_display_name":"Other"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/slack/user-links/me", body)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Email: "user@example.com"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.LinkSlackUserMe(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "Slack self-link should reject Slack users whose email does not match the authenticated user")
	require.NoError(t, mock.ExpectationsWereMet(), "Slack self-link should not upsert a mismatched Slack identity")
}

func TestIntegrationHandler_UpsertSlackUserLinkAdmin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	installationID := uuid.New()
	integrationID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	email := "eng@example.com"
	handler := NewIntegrationHandler(
		db.NewIntegrationStore(mock),
		nil,
		"", "", "http://localhost:8080", "http://localhost:3000",
		WithIntegrationMembershipStore(fakeGitHubMembershipStore{membership: models.OrganizationMembership{
			UserID: userID,
			OrgID:  orgID,
			Role:   models.RoleMember,
		}}),
	)
	handler.slackInstallationStore = db.NewSlackInstallationStore(mock)
	handler.slackUserLinkStore = db.NewSlackUserLinkStore(mock)

	expectSlackInstallationByOrg(mock, orgID, installationID, integrationID)
	mock.ExpectQuery(`ON CONFLICT \(org_id, slack_team_id, slack_user_id\)`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "slack_installation_id", "user_id", "slack_team_id", "slack_user_id",
			"slack_email", "slack_display_name", "source", "linked_at", "created_at", "updated_at",
		}).AddRow(
			linkID, orgID, installationID, &userID, "T123", "U123", &email, "Eng User",
			models.SlackUserLinkSourceAdminLinked, &now, now, now,
		))

	body := strings.NewReader(fmt.Sprintf(`{"user_id":%q,"slack_user_id":"U123","slack_email":"eng@example.com","slack_display_name":"Eng User"}`, userID.String()))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/slack/user-links", body)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.UpsertSlackUserLinkAdmin(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "admin Slack user-link upsert should return OK")
	var resp models.SingleResponse[models.SlackUserLink]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Slack user-link response should be valid JSON")
	require.Equal(t, models.SlackUserLinkSourceAdminLinked, resp.Data.Source, "admin upsert should mark the link as admin linked")
	require.Equal(t, &userID, resp.Data.UserID, "admin upsert should link the requested user")
	require.NoError(t, mock.ExpectationsWereMet(), "admin Slack user-link upsert should satisfy database expectations")
}

func TestIntegrationHandler_DeleteSlackUserLinkAdmin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	linkID := uuid.New()
	handler := NewIntegrationHandler(db.NewIntegrationStore(mock), nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.slackUserLinkStore = db.NewSlackUserLinkStore(mock)

	mock.ExpectExec(`DELETE FROM slack_user_links`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/slack/user-links/"+linkID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", linkID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.DeleteSlackUserLinkAdmin(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "admin Slack user-link delete should return OK")
	require.NoError(t, mock.ExpectationsWereMet(), "admin Slack user-link delete should satisfy database expectations")
}

func TestIntegrationHandler_DeleteSlackUserLinkAdmin_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	linkID := uuid.New()
	handler := NewIntegrationHandler(db.NewIntegrationStore(mock), nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.slackUserLinkStore = db.NewSlackUserLinkStore(mock)

	mock.ExpectExec(`DELETE FROM slack_user_links`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/slack/user-links/"+linkID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", linkID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.DeleteSlackUserLinkAdmin(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "admin Slack user-link delete should return 404 for non-existent link")
	require.NoError(t, mock.ExpectationsWereMet(), "admin Slack user-link delete not-found should satisfy database expectations")
}

func TestIntegrationHandler_PatchSlackChannelSettingsRejectsForeignRepository(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	foreignRepoID := uuid.New()
	installationID := uuid.New()
	integrationID := uuid.New()
	handler := NewIntegrationHandler(db.NewIntegrationStore(mock), nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.slackInstallationStore = db.NewSlackInstallationStore(mock)
	handler.slackChannelStore = db.NewSlackChannelSettingsStore(mock)
	handler.repoStore = db.NewRepositoryStore(mock)
	expectSlackInstallationByOrg(mock, orgID, installationID, integrationID)
	mock.ExpectQuery(`FROM repositories\s+WHERE id = @id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at",
		}))

	body := strings.NewReader(fmt.Sprintf(`{"default_repository_id":"%s"}`, foreignRepoID))
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/integrations/slack/channels/C123", body)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slack_channel_id", "C123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.PatchSlackChannelSettings(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "Slack channel settings should reject repository IDs outside the active org")
	require.NoError(t, mock.ExpectationsWereMet(), "Slack channel settings should validate repository ownership before upsert")
}

func slackTestUser(id, email string) ingestion.SlackUser {
	user := ingestion.SlackUser{ID: id}
	user.Profile.Email = email
	user.Profile.DisplayName = "Slack User"
	return user
}

func expectSlackInstallationByOrg(mock pgxmock.PgxPoolIface, orgID, installationID, integrationID uuid.UUID) {
	now := time.Now()
	mock.ExpectQuery(`FROM slack_installations\s+WHERE org_id = @org_id AND status = 'active'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "team_id", "team_name", "enterprise_id", "api_app_id",
			"bot_user_id", "bot_id", "scope", "status", "installed_by_user_id", "installed_at",
			"last_event_at", "created_at", "updated_at",
		}).AddRow(
			installationID, orgID, integrationID, "T123", "Acme", nil, "A123",
			"U143", "B143", []string{"users:read.email"}, models.SlackInstallationStatusActive, nil, now,
			nil, now, now,
		))
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

	expectNoExistingCredentialLookup(mock)
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

func TestIntegrationHandler_HandleSentryOAuthCallback_PreservesWebhookSecretOnReauth(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	credentialID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	credentialStore := db.NewOrgCredentialStore(mock, nil)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://sentry.io/oauth/token/":
			respBody := `{"access_token":"sentry-access-token-new","refresh_token":"sentry-refresh-token-new","token_type":"bearer","scope":"org:read project:read event:read"}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(respBody))}, nil
		case req.URL.String() == "https://sentry.io/api/0/organizations/":
			respBody := `[{"slug":"acme-corp","name":"Acme Corp"}]`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(respBody))}, nil
		default:
			return nil, errors.New("unexpected request URL: " + req.URL.String())
		}
	})

	handler := NewIntegrationHandler(
		store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000",
		WithSentryOAuth("sentry-client-id", "sentry-client-secret"),
	)
	handler.client = &http.Client{Transport: transport}

	existingConfig := crypto.DevEncrypt([]byte(`{"webhook_secret":"sentry_whsec_existing","access_token":"sentry-access-token-old"}`))
	mock.ExpectQuery("SELECT id, org_id, provider, label, config").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "label", "config", "status", "last_verified_at", "last_used_at", "created_by", "created_at", "updated_at"}).
			AddRow(credentialID, orgID, string(models.ProviderSentry), "", existingConfig, "active", nil, nil, nil, now, now))

	var capturedConfigBytes []byte
	mock.ExpectQuery("INSERT INTO org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), capturingArg(&capturedConfigBytes), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(credentialID))

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

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "callback should redirect after successful Sentry OAuth")
	require.NoError(t, mock.ExpectationsWereMet(), "callback should read existing credential before upserting the refreshed config")

	plaintext, err := crypto.DevDecrypt(capturedConfigBytes)
	require.NoError(t, err, "captured bytes should be DevEncrypt'd")
	var persisted models.SentryConfig
	require.NoError(t, json.Unmarshal(plaintext, &persisted), "persisted config should decode as SentryConfig")
	require.Equal(t, "sentry_whsec_existing", persisted.WebhookSecret, "Sentry webhook secret must survive OAuth reauthorization")
	require.Equal(t, "sentry-access-token-new", persisted.AccessToken, "OAuth callback should still persist the new access token")
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

	orgID := uuid.New()
	userID := uuid.New()
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
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
	req = req.WithContext(ctx)
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

func TestIntegrationHandler_ListInstallationRepos_FollowsPagination(t *testing.T) {
	t.Parallel()

	handler := NewIntegrationHandler(nil, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	requested := make([]string, 0, 2)
	handler.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requested = append(requested, req.URL.String())
		switch len(requested) {
		case 1:
			require.Equal(t, githubAPIURL+"/installation/repositories?per_page=100", req.URL.String(), "first page should request installation repositories")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Link": []string{`<` + githubAPIURL + `/installation/repositories?per_page=100&page=2>; rel="next"`},
				},
				Body: io.NopCloser(strings.NewReader(`{"repositories":[{"id":1,"full_name":"org/one"}]}`)),
			}, nil
		case 2:
			require.Equal(t, githubAPIURL+"/installation/repositories?per_page=100&page=2", req.URL.String(), "second page should follow GitHub Link header")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(`{"repositories":[{"id":2,"full_name":"org/two"}]}`)),
			}, nil
		default:
			require.Fail(t, "listInstallationRepos should not request more than two pages")
			return nil, errors.New("unexpected request")
		}
	})}

	repos, err := handler.listInstallationRepos(context.Background(), "installation-token")

	require.NoError(t, err, "listInstallationRepos should read every GitHub page")
	require.Equal(t, []githubInstallationRepo{
		{ID: 1, FullName: "org/one"},
		{ID: 2, FullName: "org/two"},
	}, repos, "listInstallationRepos should concatenate paginated repositories")
	require.Len(t, requested, 2, "listInstallationRepos should request exactly two pages")
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
	userID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(
		store, nil, "", "", "http://localhost:8080", "http://localhost:3000",
		WithGitHubIntegrationOAuth("gh-id", "gh-secret"),
		WithIntegrationMembershipStore(fakeGitHubMembershipStore{membership: models.OrganizationMembership{
			UserID: userID,
			OrgID:  orgID,
			Role:   models.RoleAdmin,
		}}),
	)
	state, err := handler.signGitHubSetupState(githubSetupStatePayload{
		Nonce:     "nonce",
		UserID:    userID,
		OrgID:     orgID,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err, "test should create a valid setup state")

	// When installation_id and setup_action=update are present, the callback
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
		"/api/v1/integrations/github/callback?code=some-code&installation_id=12345&setup_action=update&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: githubIntegrationOAuthStateCookie, Value: state})
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleGitHubOAuthCallback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)
	require.Equal(t, "http://localhost:3000/settings/integrations?github=connected&select_repos=1", w.Header().Get("Location"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_HandleGitHubAppInstalled_RejectsMissingInstallationIDBeforeCreatingIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	handler := NewIntegrationHandler(
		db.NewIntegrationStore(mock),
		nil,
		"",
		"",
		"http://localhost:8080",
		"http://localhost:3000",
		WithIntegrationMembershipStore(fakeGitHubMembershipStore{membership: models.OrganizationMembership{
			UserID: userID,
			OrgID:  orgID,
			Role:   models.RoleAdmin,
		}}),
	)
	state, err := handler.signGitHubSetupState(githubSetupStatePayload{
		Nonce:     "nonce",
		UserID:    userID,
		OrgID:     orgID,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err, "test should create a valid setup state")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/github/installed?setup_action=install&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: githubIntegrationOAuthStateCookie, Value: state})
	w := httptest.NewRecorder()

	handler.HandleGitHubAppInstalled(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "setup callback should reject missing installation id")
	require.Contains(t, w.Body.String(), "MISSING_INSTALLATION_ID", "response should identify missing installation id")
	require.NoError(t, mock.ExpectationsWereMet(), "missing installation id should not create an integration")
}

func TestIntegrationHandler_HandleGitHubOAuthCallback_RejectsInvalidAppSetupState(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	handler := NewIntegrationHandler(
		db.NewIntegrationStore(mock),
		nil,
		"",
		"",
		"http://localhost:8080",
		"http://localhost:3000",
		WithGitHubIntegrationOAuth("gh-id", "gh-secret"),
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/integrations/github/callback?code=some-code&installation_id=12345&setup_action=install&state=missing-cookie", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.HandleGitHubOAuthCallback(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "app setup callback should reject missing setup state")
	require.Contains(t, w.Body.String(), "INVALID_GITHUB_SETUP_STATE", "response should identify invalid setup state")
	require.NoError(t, mock.ExpectationsWereMet(), "invalid setup state should not touch the database")
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

func TestIntegrationHandler_ClaimGitHubInstallationRepositories_RequiresUserAuth(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	handler := NewIntegrationHandler(
		db.NewIntegrationStore(mock),
		nil,
		"",
		"",
		"http://localhost:8080",
		"http://localhost:3000",
		WithGitHubApp(&fakeGitHubAppService{token: "installation-token"}, db.NewRepositoryStore(mock)),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/github/repositories/claim", strings.NewReader(`{"installation_id":12345,"github_ids":[67890]}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Email: "admin@example.com", Name: "Admin", Role: models.RoleAdmin})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ClaimGitHubInstallationRepositories(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "claim should reject requests when GitHub user auth is not wired")
	require.Contains(t, w.Body.String(), "GITHUB_USER_AUTH_REQUIRED", "claim should return an actionable GitHub user auth error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_GitHubInstallationLink_FallsBackToRepoInstallationForExplicitID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	handler := NewIntegrationHandler(
		db.NewIntegrationStore(mock),
		nil,
		"",
		"",
		"http://localhost:8080",
		"http://localhost:3000",
	)
	handler.githubInstallations = db.NewGitHubInstallationStore(mock)
	handler.repoStore = db.NewRepositoryStore(mock)

	mock.ExpectQuery("SELECT l.id, l.org_id, l.integration_id, l.installation_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "integration_id", "installation_id", "account_login", "linked_by_user_id", "status", "auto_join_enabled", "created_at", "updated_at"}))
	mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE org_id = @org_id AND provider = @provider AND status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
			AddRow(integrationID, orgID, "github", json.RawMessage(`{}`), "active", nil, now))
	mock.ExpectQuery("SELECT installation_id FROM repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"installation_id"}).AddRow(int64(12345)))

	link, ok := handler.githubInstallationLink(context.Background(), orgID, 12345)

	require.True(t, ok, "githubInstallationLink should synthesize a link from legacy repository installation data")
	require.Equal(t, orgID, link.OrgID, "fallback link should remain scoped to the requesting org")
	require.NotNil(t, link.IntegrationID, "fallback link should preserve the active integration id for claims")
	require.Equal(t, integrationID, *link.IntegrationID, "fallback link should use the active GitHub integration")
	require.Equal(t, int64(12345), link.InstallationID, "fallback link should use the requested installation id")
	require.Equal(t, "unknown", link.AccountLogin, "fallback link should have a stable account label when config is missing it")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_GitHubInstallationLink_DoesNotFallBackToDeletedInstallationConfig(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	handler := NewIntegrationHandler(
		db.NewIntegrationStore(mock),
		nil,
		"",
		"",
		"http://localhost:8080",
		"http://localhost:3000",
	)
	handler.githubInstallations = db.NewGitHubInstallationStore(mock)

	mock.ExpectQuery("SELECT l.id, l.org_id, l.integration_id, l.installation_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "integration_id", "installation_id", "account_login", "linked_by_user_id", "status", "created_at", "updated_at"}))
	mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE org_id = @org_id AND provider = @provider AND status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
			AddRow(integrationID, orgID, "github", json.RawMessage(`{"installation_id":12345,"account_login":"acme"}`), "active", nil, now))
	mock.ExpectQuery("SELECT installation_id, account_id, account_login, account_type, repository_selection, status, roster_synced_at, created_at, updated_at").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"installation_id", "account_id", "account_login", "account_type", "repository_selection", "status", "roster_synced_at", "created_at", "updated_at"}).
			AddRow(int64(12345), int64(987), "acme", nil, nil, "deleted", nil, now, now))

	link, ok := handler.githubInstallationLink(context.Background(), orgID, 12345)

	require.False(t, ok, "githubInstallationLink should not synthesize a link for a deleted GitHub installation")
	require.Equal(t, models.GitHubInstallationOrgLink{}, link, "deleted installation fallback should return an empty link")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationHandler_ClaimGitHubInstallationRepositories_MapsUniqueOwnershipRaceToConflict(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	integrationID := uuid.New()
	otherOrgID := uuid.New()
	otherRepoID := uuid.New()
	now := time.Now().UTC()

	handler := NewIntegrationHandler(
		db.NewIntegrationStore(mock),
		nil,
		"",
		"",
		"http://localhost:8080",
		"http://localhost:3000",
		WithGitHubApp(&fakeGitHubAppService{token: "installation-token"}, db.NewRepositoryStore(mock)),
		WithGitHubAppUserAuth(fakeGitHubAppUserAuth{credential: &models.GitHubAppUserConfig{AccessToken: "ghu_user"}}),
	)
	handler.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case githubAPIURL + "/installation/repositories?per_page=100":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"repositories":[{
					"id":67890,
					"full_name":"acme/api",
					"default_branch":"main",
					"private":true,
					"clone_url":"https://github.com/acme/api.git"
				}]}`)),
			}, nil
		case githubAPIURL + "/repos/acme/api":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		default:
			return nil, errors.New("unexpected request URL: " + req.URL.String())
		}
	})}

	mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE org_id = @org_id AND provider = @provider AND status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
			AddRow(integrationID, orgID, "github", json.RawMessage(`{"installation_id":12345,"account_login":"acme"}`), "active", nil, now))
	mock.ExpectQuery("SELECT r.id AS repository_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"repository_id", "org_id", "org_name", "github_id", "full_name", "status"}))
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO repositories").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "idx_repositories_active_github_id"})
	mock.ExpectRollback()
	mock.ExpectQuery("SELECT r.id AS repository_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"repository_id", "org_id", "org_name", "github_id", "full_name", "status"}).
			AddRow(otherRepoID, otherOrgID, "Platform", int64(67890), "acme/api", "active"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/github/repositories/claim", strings.NewReader(`{"github_ids":[67890]}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Email: "admin@example.com", Name: "Admin", Role: models.RoleAdmin})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ClaimGitHubInstallationRepositories(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "claim should map concurrent active-owner uniqueness violations to a conflict")
	require.Contains(t, w.Body.String(), "REPOSITORY_OWNERSHIP_CONFLICT", "claim conflict should return the ownership conflict code")
	require.Contains(t, w.Body.String(), "Platform", "claim conflict should include the current owner when it can be reloaded")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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

func TestIntegrationHandler_ConnectCircleCI_Success(t *testing.T) {
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
		require.Equal(t, "https://circleci.com/api/v2/me", req.URL.String())
		require.Equal(t, "cci_token", req.Header.Get("Circle-Token"))

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"id":"abc","login":"octocat"}`)),
		}, nil
	})

	handler := NewIntegrationHandler(
		store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000",
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

	body := `{"auth_token":"cci_token","project_slug":"gh/octocat/hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/circleci/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectCircleCI(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
	require.Contains(t, w.Body.String(), `"provider":"circleci"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ConnectCircleCI_MissingProjectSlug(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	body := `{"auth_token":"tok"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/circleci/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectCircleCI(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "MISSING_PROJECT_SLUG",
		"a token alone is not enough — CircleCI needs the VCS-prefixed project slug")
}

func TestIntegrationHandler_ConnectCircleCI_RejectsMalformedSlug(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	body := `{"auth_token":"tok","project_slug":"octocat/hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/circleci/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectCircleCI(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_PROJECT_SLUG",
		"two-segment slugs miss the VCS prefix CircleCI requires (gh/org/repo)")
}

func TestIntegrationHandler_ConnectCircleCI_InvalidToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)

	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"message":"Permission denied"}`)),
		}, nil
	})

	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{Transport: transport}

	body := `{"auth_token":"bad","project_slug":"gh/octocat/hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/circleci/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectCircleCI(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_TOKEN")
}

func TestIntegrationHandler_ConnectMezmo_Success(t *testing.T) {
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
		require.Equal(t, http.MethodGet, req.Method, "validation should use the documented Mezmo export method")
		require.Equal(t, "https", req.URL.Scheme, "validation should use the default Mezmo API scheme")
		require.Equal(t, "api.mezmo.com", req.URL.Host, "validation should use the default Mezmo API host")
		require.Equal(t, "/v2/export", req.URL.Path, "validation should use the documented Mezmo export path")
		require.Equal(t, "Token mezmo_key", req.Header.Get("Authorization"), "validation should send the documented Mezmo access-token auth header")
		require.Equal(t, "1", req.URL.Query().Get("size"), "validation should request a minimal export")

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"lines":[]}`)),
		}, nil
	})

	handler := NewIntegrationHandler(
		store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000",
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

	body := `{"api_key":"mezmo_key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
	require.Contains(t, w.Body.String(), `"provider":"mezmo"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ConnectMezmo_MissingAPIKey(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	body := `{"dataset":"prod"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "MISSING_API_KEY")
}

func TestIntegrationHandler_ConnectMezmo_RejectsMalformedBaseURL(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	body := `{"api_key":"key","base_url":"not-a-url"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_BASE_URL")
}

func TestIntegrationHandler_ConnectMezmo_HonorsCustomBaseURL(t *testing.T) {
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
		require.Equal(t, "https://logs.example.com/v2/export", req.URL.Scheme+"://"+req.URL.Host+req.URL.Path,
			"validation should target the operator-supplied base URL")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"lines":[]}`)),
		}, nil
	})

	handler := NewIntegrationHandler(store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000")
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

	body := `{"api_key":"mezmo_key","base_url":"https://logs.example.com/"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_ConnectMezmo_InvalidToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)

	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"forbidden"}`)),
		}, nil
	})

	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{Transport: transport}

	body := `{"api_key":"bad"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_TOKEN")
}

func TestIntegrationHandler_ConnectMezmo_InvalidJSON(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_JSON")
}

func TestIntegrationHandler_ConnectMezmo_RejectsDataset(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)
	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")

	body := `{"api_key":"mezmo_key","dataset":"production"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "ConnectMezmo should reject unsupported dataset scopes")
	require.Contains(t, w.Body.String(), "UNSUPPORTED_DATASET", "response should identify unsupported dataset scope")
}

func TestIntegrationHandler_ConnectMezmo_RejectsNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	store := db.NewIntegrationStore(mock)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "/v2/export", req.URL.Path, "validation should probe the documented Mezmo export endpoint")
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"No Resource Found"}`)),
		}, nil
	})

	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{Transport: transport}

	body := `{"api_key":"mezmo_key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "missing Mezmo endpoint should reject the connection")
	require.Contains(t, w.Body.String(), "INVALID_TOKEN", "response should identify validation failure")
	require.Contains(t, w.Body.String(), "mezmo API endpoint not found", "response should explain that the endpoint probe failed")
}

// A 4xx that isn't an auth rejection (e.g. a query-shape quirk) means the key
// was accepted, so connect should still succeed — we validate the key, not the
// probe query.
func TestIntegrationHandler_ConnectMezmo_AcceptsNonAuth4xx(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	store := db.NewIntegrationStore(mock)
	credentialStore := db.NewOrgCredentialStore(mock, nil)

	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"unknown dataset"}`)),
		}, nil
	})

	handler := NewIntegrationHandler(store, credentialStore, "", "", "http://localhost:8080", "http://localhost:3000")
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

	body := `{"api_key":"mezmo_key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

// A 5xx means Mezmo is unhealthy; block the connect so the user retries rather
// than persisting a credential we couldn't verify against a working API.
func TestIntegrationHandler_ConnectMezmo_RejectsServerError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewIntegrationStore(mock)

	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"upstream"}`)),
		}, nil
	})

	handler := NewIntegrationHandler(store, nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.client = &http.Client{Transport: transport}

	body := `{"api_key":"mezmo_key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mezmo/connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ConnectMezmo(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_TOKEN")
}

// fakeGitHubOrgAutoJoinService is a test double for githubOrgAutoJoinService.
type fakeGitHubOrgAutoJoinService struct {
	details    ghapp.InstallationDetails
	detailsErr error
}

func (f *fakeGitHubOrgAutoJoinService) GetInstallationDetails(_ context.Context, _ int64) (ghapp.InstallationDetails, error) {
	return f.details, f.detailsErr
}

func (f *fakeGitHubOrgAutoJoinService) ListOrgMembers(_ context.Context, _ int64, _ string) ([]ghapp.OrgMember, error) {
	return nil, nil
}

func TestIntegrationHandler_ListGitHubOrgAutoJoin_PermissionGranted(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	accountType := "Organization"
	now := time.Now().UTC()

	mock.ExpectQuery("FROM github_installation_org_links").
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{
			"installation_id", "account_login", "account_type", "auto_join_enabled", "roster_synced_at", "captured_by_other_org",
		}).AddRow(int64(12345), "acme", &accountType, true, &now, false))

	details := ghapp.InstallationDetails{}
	details.Account.Login = "acme"
	details.Account.Type = "Organization"
	details.Permissions.Members = "read"

	handler := NewIntegrationHandler(db.NewIntegrationStore(mock), nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.githubInstallations = db.NewGitHubInstallationStore(mock)
	handler.githubOrgAutoJoin = &fakeGitHubOrgAutoJoinService{details: details}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/team/github-orgs", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListGitHubOrgAutoJoin(w, req)

	require.Equal(t, http.StatusOK, w.Code, "list should return 200")
	var resp githubOrgAutoJoinResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp), "response should decode")
	require.Len(t, resp.GitHubOrgs, 1, "should return one org row")
	require.Equal(t, int64(12345), resp.GitHubOrgs[0].InstallationID)
	require.Equal(t, "granted", resp.GitHubOrgs[0].MembersPermission, "should report granted when members permission is 'read'")
	require.True(t, resp.GitHubOrgs[0].AutoJoinEnabled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_UpdateGitHubOrgAutoJoin_DisableSuccess(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	linkID := uuid.New()
	now := time.Now().UTC()

	// SetOrgLinkAutoJoin(false) RETURNING the updated link.
	mock.ExpectQuery("UPDATE github_installation_org_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "installation_id", "account_login", "linked_by_user_id", "status", "auto_join_enabled", "created_at", "updated_at",
		}).AddRow(linkID, orgID, nil, int64(12345), "acme", nil, "active", false, now, now))

	// ClearRosterForInstallation.
	mock.ExpectExec("DELETE FROM github_org_members").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 5))

	handler := NewIntegrationHandler(db.NewIntegrationStore(mock), nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.githubInstallations = db.NewGitHubInstallationStore(mock)

	body := `{"auto_join_enabled":false}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/team/github-orgs/12345", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("installation_id", "12345")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	handler.UpdateGitHubOrgAutoJoin(w, req)

	require.Equal(t, http.StatusOK, w.Code, "disable should return 200")
	require.NoError(t, mock.ExpectationsWereMet(), "disable must clear the roster")
}

func TestIntegrationHandler_UpdateGitHubOrgAutoJoin_EnableMissingPermission(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	linkID := uuid.New()
	now := time.Now().UTC()
	accountType := "Organization"

	// GetOrgLink.
	mock.ExpectQuery("SELECT l.id, l.org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "installation_id", "account_login", "linked_by_user_id", "status", "auto_join_enabled", "created_at", "updated_at",
		}).AddRow(linkID, orgID, nil, int64(12345), "acme", nil, "active", false, now, now))

	// GetByInstallationID.
	mock.ExpectQuery("SELECT installation_id, account_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"installation_id", "account_id", "account_login", "account_type", "repository_selection", "status", "roster_synced_at", "created_at", "updated_at",
		}).AddRow(int64(12345), int64(99), "acme", &accountType, nil, "active", nil, now, now))

	// GetInstallationDetails returns members permission as "none" (not yet approved).
	details := ghapp.InstallationDetails{}
	details.Account.Login = "acme"
	details.Account.Type = "Organization"
	details.Permissions.Members = "none"

	handler := NewIntegrationHandler(db.NewIntegrationStore(mock), nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.githubInstallations = db.NewGitHubInstallationStore(mock)
	handler.githubOrgAutoJoin = &fakeGitHubOrgAutoJoinService{details: details}

	body := `{"auto_join_enabled":true}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/team/github-orgs/12345", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("installation_id", "12345")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	handler.UpdateGitHubOrgAutoJoin(w, req)

	require.Equal(t, http.StatusPreconditionFailed, w.Code, "missing members permission should return 412")
	require.Contains(t, w.Body.String(), "MEMBERS_PERMISSION_MISSING", "response should name the missing permission error")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationHandler_UpdateGitHubOrgAutoJoin_EnableNotAnOrganization(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	linkID := uuid.New()
	now := time.Now().UTC()
	userAccountType := "User"

	mock.ExpectQuery("SELECT l.id, l.org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "installation_id", "account_login", "linked_by_user_id", "status", "auto_join_enabled", "created_at", "updated_at",
		}).AddRow(linkID, orgID, nil, int64(12345), "someguy", nil, "active", false, now, now))

	mock.ExpectQuery("SELECT installation_id, account_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"installation_id", "account_id", "account_login", "account_type", "repository_selection", "status", "roster_synced_at", "created_at", "updated_at",
		}).AddRow(int64(12345), int64(99), "someguy", &userAccountType, nil, "active", nil, now, now))

	details := ghapp.InstallationDetails{}
	details.Account.Login = "someguy"
	details.Account.Type = "User"
	details.Permissions.Members = "read"

	handler := NewIntegrationHandler(db.NewIntegrationStore(mock), nil, "", "", "http://localhost:8080", "http://localhost:3000")
	handler.githubInstallations = db.NewGitHubInstallationStore(mock)
	handler.githubOrgAutoJoin = &fakeGitHubOrgAutoJoinService{details: details}

	body := `{"auto_join_enabled":true}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/team/github-orgs/12345", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("installation_id", "12345")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	handler.UpdateGitHubOrgAutoJoin(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "user-account installation should return 422")
	require.Contains(t, w.Body.String(), "NOT_AN_ORGANIZATION", "response should name the account type error")
	require.NoError(t, mock.ExpectationsWereMet())
}
