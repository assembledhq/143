package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func computeTestSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func setupWebhookHandler(t *testing.T, secret string) (pgxmock.PgxPoolIface, *WebhookHandler) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)

	cfg := &config.Config{
		GitHubWebhookSecret: secret,
	}
	orgStore := db.NewOrganizationStore(mock)
	repoStore := db.NewRepositoryStore(mock)
	integrationStore := db.NewIntegrationStore(mock)
	handler := NewWebhookHandler(cfg, orgStore, repoStore, integrationStore)
	return mock, handler
}

func TestWebhook_HandleGitHub_InstallationCreated(t *testing.T) {
	secret := "test-secret"
	mock, handler := setupWebhookHandler(t, secret)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	integrationID := uuid.New()

	payload := `{
		"action": "created",
		"installation": {
			"id": 12345,
			"account": {"id": 100, "login": "test-org"}
		},
		"repositories": [
			{"id": 1001, "full_name": "test-org/repo1", "private": false}
		]
	}`
	body := []byte(payload)
	sig := computeTestSignature(secret, body)

	// 1. GetBySlug -> empty rows (no existing org) (1 named arg: slug)
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE slug").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "name", "slug", "settings", "created_at", "updated_at"}),
		)

	// 2. Create org (3 named args: name, slug, settings)
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(orgID, now, now),
		)

	// 3. Create integration (4 named args: org_id, provider, config, status)
	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(integrationID, now),
		)

	// 4. UpsertFromGitHub repo (12 named args)
	mock.ExpectQuery("INSERT INTO repositories").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(uuid.New(), now, now),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "installation")
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()

	handler.HandleGitHub(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "installation created")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWebhook_HandleGitHub_InstallationDeleted(t *testing.T) {
	secret := "test-secret"
	mock, handler := setupWebhookHandler(t, secret)
	defer mock.Close()

	payload := `{
		"action": "deleted",
		"installation": {
			"id": 12345,
			"account": {"id": 100, "login": "test-org"}
		},
		"repositories": []
	}`
	body := []byte(payload)
	sig := computeTestSignature(secret, body)

	// DisconnectByInstallationID (1 named arg: installation_id)
	mock.ExpectExec("UPDATE repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "installation")
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()

	handler.HandleGitHub(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "installation deleted")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWebhook_HandleGitHub_InvalidSignature(t *testing.T) {
	secret := "test-secret"
	_, handler := setupWebhookHandler(t, secret)

	payload := `{"action":"created","installation":{"id":1,"account":{"id":1,"login":"x"}}}`
	body := []byte(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "installation")
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	w := httptest.NewRecorder()

	handler.HandleGitHub(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_SIGNATURE")
}

func TestWebhook_HandleGitHub_UnknownEvent(t *testing.T) {
	secret := "test-secret"
	_, handler := setupWebhookHandler(t, secret)

	payload := `{}`
	body := []byte(payload)
	sig := computeTestSignature(secret, body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()

	handler.HandleGitHub(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ignored")
}

func TestWebhook_HandleGitHub_InvalidJSON(t *testing.T) {
	secret := "test-secret"
	_, handler := setupWebhookHandler(t, secret)

	payload := `not valid json{`
	body := []byte(payload)
	sig := computeTestSignature(secret, body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "installation")
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()

	handler.HandleGitHub(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_JSON")
}

func TestWebhook_HandleGitHub_InstallationRepos(t *testing.T) {
	secret := "test-secret"
	mock, handler := setupWebhookHandler(t, secret)
	defer mock.Close()

	now := time.Now()

	payload := `{
		"action": "added",
		"installation": {
			"id": 12345,
			"account": {"id": 100, "login": "test-org"}
		},
		"repositories_added": [
			{"id": 2001, "full_name": "test-org/new-repo", "private": true}
		],
		"repositories_removed": []
	}`
	body := []byte(payload)
	sig := computeTestSignature(secret, body)

	// UpsertFromGitHub for added repo (12 named args)
	mock.ExpectQuery("INSERT INTO repositories").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(uuid.New(), now, now),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "installation_repositories")
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()

	handler.HandleGitHub(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "repositories updated")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWebhook_VerifySignature_NoSecret(t *testing.T) {
	// Empty secret allows any request through
	mock, handler := setupWebhookHandler(t, "")
	defer mock.Close()

	payload := `{}`
	body := []byte(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "ping")
	// No signature header
	w := httptest.NewRecorder()

	handler.HandleGitHub(w, req)
	// Should pass through to the default case (unknown event) -> 200 "ignored"
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "ignored", resp["status"])
}
