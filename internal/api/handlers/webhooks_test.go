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
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func computeTestSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func setupWebhookHandler(t *testing.T, mock pgxmock.PgxPoolIface, secret string) *WebhookHandler {
	t.Helper()
	cfg := &config.Config{
		GitHubWebhookSecret: secret,
	}
	orgStore := db.NewOrganizationStore(mock)
	userStore := db.NewUserStore(mock)
	repoStore := db.NewRepositoryStore(mock)
	integrationStore := db.NewIntegrationStore(mock)
	return NewWebhookHandler(cfg, orgStore, userStore, repoStore, integrationStore, nil)
}

func TestWebhook_HandleGitHub(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		secret       string
		event        string
		payload      string
		signature    func(secret string, body []byte) string
		setupMock    func(mock pgxmock.PgxPoolIface)
		expectedCode int
		expectedBody string
	}{
		{
			name:   "installation created provisions org and repo",
			secret: "test-secret",
			event:  "installation",
			payload: `{
				"action": "created",
				"installation": {
					"id": 12345,
					"account": {"id": 100, "login": "test-org"}
				},
				"repositories": [
					{"id": 1001, "full_name": "test-org/repo1", "private": false}
				]
			}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				orgID := uuid.New()
				integrationID := uuid.New()

				// 1. GetByGitHubInstallationID -> no existing integration
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE provider").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}),
					)

				// 2. GetByGitHubID -> no user found (empty rows)
				mock.ExpectQuery("SELECT .+ FROM users WHERE github_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}),
					)

				// 3. Create org (2 named args)
				mock.ExpectQuery("INSERT INTO organizations").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
							AddRow(orgID, now, now),
					)

				// 4. Create integration (4 named args)
				mock.ExpectQuery("INSERT INTO integrations").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "created_at"}).
							AddRow(integrationID, now),
					)

				// 5. UpsertFromGitHub repo (12 named args)
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
			},
			expectedCode: http.StatusOK,
			expectedBody: "installation created",
		},
		{
			name:   "installation created reuses existing integration",
			secret: "test-secret",
			event:  "installation",
			payload: `{
				"action": "created",
				"installation": {
					"id": 12345,
					"account": {"id": 100, "login": "test-org"}
				},
				"repositories": [
					{"id": 1001, "full_name": "test-org/repo1", "private": false}
				]
			}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				orgID := uuid.New()
				integrationID := uuid.New()

				// 1. GetByGitHubInstallationID -> finds existing integration
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE provider").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
							AddRow(integrationID, orgID, "github", []byte(`{"installation_id":12345}`), "active", nil, now),
					)

				// 2. GetByID org -> found
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
							AddRow(orgID, "Test Org", []byte(`{}`), now, now),
					)

				// 3. UpsertFromGitHub repo (12 named args) — no integration created
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
			},
			expectedCode: http.StatusOK,
			expectedBody: "installation created",
		},
		{
			name:   "installation deleted disconnects repositories",
			secret: "test-secret",
			event:  "installation",
			payload: `{
				"action": "deleted",
				"installation": {
					"id": 12345,
					"account": {"id": 100, "login": "test-org"}
				},
				"repositories": []
			}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE repositories").
					WithArgs(pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 3))
			},
			expectedCode: http.StatusOK,
			expectedBody: "installation deleted",
		},
		{
			name:    "invalid signature returns unauthorized",
			secret:  "test-secret",
			event:   "installation",
			payload: `{"action":"created","installation":{"id":1,"account":{"id":1,"login":"x"}}}`,
			signature: func(secret string, body []byte) string {
				return "sha256=invalid"
			},
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusUnauthorized,
			expectedBody: "INVALID_SIGNATURE",
		},
		{
			name:    "unknown event type is ignored",
			secret:  "test-secret",
			event:   "push",
			payload: `{}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusOK,
			expectedBody: "ignored",
		},
		{
			name:    "invalid JSON returns bad request",
			secret:  "test-secret",
			event:   "installation",
			payload: `not valid json{`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_JSON",
		},
		{
			name:   "installation_repositories event adds repos",
			secret: "test-secret",
			event:  "installation_repositories",
			payload: `{
				"action": "added",
				"installation": {
					"id": 12345,
					"account": {"id": 100, "login": "test-org"}
				},
				"repositories_added": [
					{"id": 2001, "full_name": "test-org/new-repo", "private": true}
				],
				"repositories_removed": []
			}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				orgID := uuid.New()
				integrationID := uuid.New()

				// 1. GetByGitHubInstallationID -> returns integration
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE provider").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
							AddRow(integrationID, orgID, "github", []byte(`{"installation_id":12345}`), "active", nil, now),
					)

				// 2. UpsertFromGitHub repo (12 named args)
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
			},
			expectedCode: http.StatusOK,
			expectedBody: "repositories updated",
		},
		{
			name:    "pull_request event ignored when pr service not configured",
			secret:  "test-secret",
			event:   "pull_request",
			payload: `{"action":"opened","pull_request":{"number":1}}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusOK,
			expectedBody: "pr_service_not_configured",
		},
		{
			name:    "pull_request_review event ignored when pr service not configured",
			secret:  "test-secret",
			event:   "pull_request_review",
			payload: `{"action":"submitted","review":{"id":1}}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusOK,
			expectedBody: "pr_service_not_configured",
		},
		{
			name:    "pull_request_review_comment event ignored when pr service not configured",
			secret:  "test-secret",
			event:   "pull_request_review_comment",
			payload: `{"action":"created","comment":{"id":1}}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusOK,
			expectedBody: "pr_service_not_configured",
		},
		{
			name:    "check_run event ignored when pr service not configured",
			secret:  "test-secret",
			event:   "check_run",
			payload: `{"action":"completed","check_run":{"id":1}}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusOK,
			expectedBody: "pr_service_not_configured",
		},
		{
			name:   "installation_repositories event removes repos",
			secret: "test-secret",
			event:  "installation_repositories",
			payload: `{
				"action": "removed",
				"installation": {
					"id": 12345,
					"account": {"id": 100, "login": "test-org"}
				},
				"repositories_added": [],
				"repositories_removed": [
					{"id": 2001, "full_name": "test-org/old-repo", "private": false}
				]
			}`,
			signature: func(secret string, body []byte) string {
				return computeTestSignature(secret, body)
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				orgID := uuid.New()
				integrationID := uuid.New()

				// 1. GetByGitHubInstallationID -> returns integration
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE provider").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
							AddRow(integrationID, orgID, "github", []byte(`{"installation_id":12345}`), "active", nil, now),
					)

				// 2. DisconnectByGitHubID
				mock.ExpectExec("UPDATE repositories").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			expectedCode: http.StatusOK,
			expectedBody: "repositories updated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			handler := setupWebhookHandler(t, mock, tt.secret)
			tt.setupMock(mock)

			body := []byte(tt.payload)
			sig := tt.signature(tt.secret, body)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
			req.Header.Set("X-GitHub-Event", tt.event)
			req.Header.Set("X-Hub-Signature-256", sig)
			w := httptest.NewRecorder()

			handler.HandleGitHub(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWebhook_VerifySignature_NoSecret(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	handler := setupWebhookHandler(t, mock, "")

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "ping")
	// No signature header
	w := httptest.NewRecorder()

	handler.HandleGitHub(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should allow request through when no secret is configured")

	var resp map[string]string
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, "ignored", resp["status"], "should return ignored status for unknown ping event")
}

func TestWebhook_HandleCheckRun(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	prService := ghservice.NewPRService(nil, db.NewPullRequestStore(mock), nil, nil, nil, nil, nil, zerolog.Nop())
	handler := NewWebhookHandler(&config.Config{}, db.NewOrganizationStore(mock), db.NewUserStore(mock), db.NewRepositoryStore(mock), db.NewIntegrationStore(mock), prService)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(`{bad json`))
	rr := httptest.NewRecorder()
	handler.handleCheckRun(rr, req, []byte(`{bad json`))
	require.Equal(t, http.StatusBadRequest, rr.Code, "handleCheckRun should reject malformed JSON")
	require.Contains(t, rr.Body.String(), "INVALID_JSON", "handleCheckRun should encode the invalid JSON error")

	req = httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(`{}`))
	rr = httptest.NewRecorder()
	handler.handleCheckRun(rr, req, []byte(`{"action":"queued","repository":{"full_name":"assembledhq/143"},"check_run":{"pull_requests":[]}}`))
	require.Equal(t, http.StatusOK, rr.Code, "handleCheckRun should accept successfully processed events")
	require.Contains(t, rr.Body.String(), "processed", "handleCheckRun should acknowledge processed events")
}
