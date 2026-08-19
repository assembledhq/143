package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	automationevents "github.com/assembledhq/143/internal/services/automations"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type webhookAutomationEventRecorder struct {
	calls []automationevents.GitHubEventTriggerRequest
}

func TestCodeReviewCommentSourceVersion(t *testing.T) {
	t.Parallel()

	updatedAt := time.Date(2026, time.August, 2, 7, 30, 45, 123_000_000, time.UTC)
	var original ghservice.IssueCommentEvent
	original.Comment.UpdatedAt = &updatedAt
	original.Comment.Body = "Please reconsider the authorization finding."
	redelivery := original
	edited := original
	edited.Comment.Body = "Please reconsider the authorization and tenancy findings."

	originalVersion := codeReviewCommentSourceVersion(original)

	require.Positive(t, originalVersion, "source versions should be valid positive database values")
	require.Equal(t, originalVersion, codeReviewCommentSourceVersion(redelivery), "an identical webhook redelivery should retain its source version")
	require.NotEqual(t, originalVersion, codeReviewCommentSourceVersion(edited), "distinct bodies should retain separate immutable versions even when GitHub timestamps match")
}

func (r *webhookAutomationEventRecorder) TriggerGitHubEvent(_ context.Context, req automationevents.GitHubEventTriggerRequest) error {
	r.calls = append(r.calls, req)
	return nil
}

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

func TestWebhook_VerifySignature_ProductionRequiresConfiguredSecret(t *testing.T) {
	t.Parallel()

	handler := &WebhookHandler{cfg: &config.Config{Env: "production"}}

	require.False(t, handler.verifySignature([]byte(`{"ok":true}`), ""), "production webhooks should fail closed when no secret is configured")
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
			name:   "installation created records installation without auto-claiming repos",
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
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusOK,
			expectedBody: "installation created",
		},
		{
			name:   "installation created does not provision repos from webhook",
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
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
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
			name:   "installation_repositories event does not auto-claim added repos",
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
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
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
			name:    "issue_comment event ignored when pr service not configured",
			secret:  "test-secret",
			event:   "issue_comment",
			payload: `{"action":"created","issue":{"number":1},"comment":{"body":"hello"}}`,
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
			name:    "status event ignored when pr service not configured",
			secret:  "test-secret",
			event:   "status",
			payload: `{"state":"failure","sha":"head-sha","context":"ci/circleci: frontend_lint_format_license"}`,
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

func TestWebhook_HandleInstallationDeleted_DeactivatesInstallationLinks(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	handler := setupWebhookHandler(t, mock, "")
	handler.SetGitHubInstallationStore(db.NewGitHubInstallationStore(mock))

	mock.ExpectExec("UPDATE github_installations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE github_installation_org_links").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectExec("DELETE FROM github_org_members").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 10))
	mock.ExpectExec("UPDATE repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	body := []byte(`{"action":"deleted","installation":{"id":12345,"account":{"id":100,"login":"test-org"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "installation")
	rr := httptest.NewRecorder()

	handler.HandleGitHub(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "installation deleted webhook should be acknowledged")
	require.Contains(t, rr.Body.String(), "installation deleted", "response should describe deleted installation")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWebhook_HandleOrganizationMemberAdded(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	handler := setupWebhookHandler(t, mock, "test-secret")
	handler.SetGitHubInstallationStore(db.NewGitHubInstallationStore(mock))

	mock.ExpectExec("INSERT INTO github_org_members").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	body := []byte(`{"action":"member_added","installation":{"id":12345,"account":{"id":100,"login":"acme","type":"Organization"}},"membership":{"user":{"id":42,"login":"alice"}}}`)
	sig := computeTestSignature("test-secret", body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "organization")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()

	handler.HandleGitHub(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "organization member_added webhook should be acknowledged")
	require.Contains(t, rr.Body.String(), "organization updated", "response should describe the organization update")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWebhook_HandleOrganizationMemberRemoved(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	handler := setupWebhookHandler(t, mock, "test-secret")
	handler.SetGitHubInstallationStore(db.NewGitHubInstallationStore(mock))

	mock.ExpectExec("DELETE FROM github_org_members").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	body := []byte(`{"action":"member_removed","installation":{"id":12345,"account":{"id":100,"login":"acme","type":"Organization"}},"membership":{"user":{"id":42,"login":"alice"}}}`)
	sig := computeTestSignature("test-secret", body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "organization")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()

	handler.HandleGitHub(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "organization member_removed webhook should be acknowledged")
	require.Contains(t, rr.Body.String(), "organization updated", "response should describe the organization update")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWebhook_HandleOrganizationMemberRemovedIgnoresZeroUserID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	handler := setupWebhookHandler(t, mock, "test-secret")
	handler.SetGitHubInstallationStore(db.NewGitHubInstallationStore(mock))

	// No DB expectations — zero user ID should be skipped without a DB call.
	body := []byte(`{"action":"member_removed","installation":{"id":12345,"account":{"id":100,"login":"acme","type":"Organization"}},"membership":{"user":{"id":0,"login":""}}}`)
	sig := computeTestSignature("test-secret", body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "organization")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()

	handler.HandleGitHub(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "zero user ID should be silently skipped")
	require.NoError(t, mock.ExpectationsWereMet(), "no database calls should be made for zero user ID")
}

func TestWebhook_HandleOrganizationRenamed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	handler := setupWebhookHandler(t, mock, "test-secret")
	handler.SetGitHubInstallationStore(db.NewGitHubInstallationStore(mock))

	mock.ExpectExec("UPDATE github_installation_org_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	body := []byte(`{"action":"renamed","installation":{"id":12345,"account":{"id":100,"login":"acme-new","type":"Organization"}},"organization":{"id":100,"login":"acme-new"}}`)
	sig := computeTestSignature("test-secret", body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "organization")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()

	handler.HandleGitHub(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "organization renamed webhook should be acknowledged")
	require.Contains(t, rr.Body.String(), "organization updated", "response should describe the organization update")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWebhook_HandleOrganizationUnknownActionIgnored(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	handler := setupWebhookHandler(t, mock, "test-secret")
	handler.SetGitHubInstallationStore(db.NewGitHubInstallationStore(mock))

	body := []byte(`{"action":"some_other_action","installation":{"id":12345,"account":{"id":100,"login":"acme","type":"Organization"}},"membership":{"user":{"id":42,"login":"alice"}}}`)
	sig := computeTestSignature("test-secret", body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "organization")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()

	handler.HandleGitHub(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "unknown organization actions should be silently ignored")
	require.Contains(t, rr.Body.String(), "ignored", "response should indicate the event was ignored")
	require.NoError(t, mock.ExpectationsWereMet(), "no database calls should be made for unknown actions")
}

func TestWebhook_HandlePullRequestScopesLookupToActiveOwner(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	now := time.Now().UTC()
	repoStore := db.NewRepositoryStore(mock)
	prService := ghservice.NewPRService(nil, db.NewPullRequestStore(mock), nil, nil, nil, repoStore, nil, zerolog.Nop())
	triggerRecorder := &webhookAutomationEventRecorder{}
	prService.SetAutomationEventTriggerer(triggerRecorder)
	handler := NewWebhookHandler(&config.Config{}, db.NewOrganizationStore(mock), db.NewUserStore(mock), db.NewRepositoryStore(mock), db.NewIntegrationStore(mock), prService)

	mock.ExpectQuery("SELECT r.id AS repository_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"repository_id", "org_id", "org_name", "github_id", "full_name", "status"}).
			AddRow(repoID, orgID, "Owning Org", int64(1001), "assembledhq/143", "active"))
	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description",
			"clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at",
		}).AddRow(repoID, orgID, uuid.New(), int64(1001), "assembledhq/143", "main", false, nil, nil,
			"https://github.com/assembledhq/143.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))
	mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
			"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
			"health_version", "merge_when_ready_state", "merge_when_ready_requested_by", "merge_when_ready_requested_at",
			"merge_when_ready_head_sha", "merge_when_ready_health_version", "merge_when_ready_error",
			"merge_when_ready_updated_at", "merged_at", "created_at", "updated_at",
		}).AddRow(prID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", nil, "open", "pending", "app", "", nil, nil, nil,
			"unknown", false, 0, false, nil, int64(0),
			models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil,
			nil, now, now))

	body := []byte(`{"action":"opened","number":42,"repository":{"id":1001,"full_name":"assembledhq/143"},"pull_request":{"head":{"sha":"abc"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Delivery", "delivery-123")
	rr := httptest.NewRecorder()

	handler.handlePullRequest(rr, req, body)

	require.Equal(t, http.StatusOK, rr.Code, "pull request webhook should be acknowledged")
	require.Contains(t, rr.Body.String(), "processed", "pull request webhook should be processed")
	require.Len(t, triggerRecorder.calls, 1, "pull request webhook should trigger one automation event")
	require.Equal(t, "delivery-123", triggerRecorder.calls[0].ProviderEventID, "webhook handler should forward the GitHub delivery id")
	require.NoError(t, mock.ExpectationsWereMet(), "webhook should scope pull request lookup to the active owner org")
}

func TestWebhook_HandleCodeReviewRequestedCreatesMirrorWithBody(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	jobID := uuid.New()
	now := time.Now().UTC()
	cfg := models.DefaultCodeReviewPolicyConfig()
	policies := &codeReviewWebhookPolicyStore{policyID: policyID, config: cfg}
	metadata := &codeReviewWebhookMetadataStore{}
	sessions := &codeReviewWebhookSessionStore{}
	jobs := &codeReviewWebhookJobStore{jobID: jobID}
	codeReviews := codereviewsvc.NewService(policies, metadata, sessions, jobs, zerolog.Nop(), codereviewsvc.Config{
		AppReviewerLogins: []string{"143-code-reviewer"},
	})
	handler := &WebhookHandler{
		pullRequests: db.NewPullRequestStore(mock),
		codeReviews:  codeReviews,
	}

	mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "assembledhq/143", "github_pr_number": 42}).
		WillReturnRows(pgxmock.NewRows(codeReviewWebhookPullRequestColumns()))
	mock.ExpectQuery("INSERT INTO pull_requests").
		WithArgs(pgx.NamedArgs{
			"session_id":       (*uuid.UUID)(nil),
			"org_id":           orgID,
			"github_pr_number": 42,
			"github_pr_url":    "https://github.com/assembledhq/143/pull/42",
			"github_repo":      "assembledhq/143",
			"title":            "Fix approval guard",
			"body":             stringPointerArg{value: "## Summary\n\nFixes the approval guard.\n\n## Testing\n\ngo test ./..."},
			"status":           models.PullRequestStatusOpen,
			"review_status":    models.PullRequestReviewStatusPending,
			"authored_by":      models.GitIdentitySourceUser,
			"head_sha":         stringPointerArg{value: "head-sha"},
			"head_ref":         stringPointerArg{value: "feature/code-review"},
			"base_sha":         stringPointerArg{value: "base-sha"},
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(prID, now, now))

	body := []byte(`{
		"action": "review_requested",
		"number": 42,
		"repository": {"full_name": "assembledhq/143"},
		"requested_reviewer": {"login": "143-code-reviewer"},
		"pull_request": {
			"html_url": "https://github.com/assembledhq/143/pull/42",
			"title": "Fix approval guard",
			"body": "## Summary\n\nFixes the approval guard.\n\n## Testing\n\ngo test ./...",
			"user": {"login": "anya"},
			"head": {"sha": "head-sha", "ref": "feature/code-review", "repo": {"fork": false}},
			"base": {"sha": "base-sha"}
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Delivery", "delivery-create")
	rr := httptest.NewRecorder()

	ok := handler.handleCodeReviewRequested(rr, req, body, db.GitHubRepoOwner{
		RepositoryID: repoID,
		OrgID:        orgID,
		FullName:     "assembledhq/143",
		Status:       "active",
	})

	require.True(t, ok, "review_requested webhook should be processed: %s", rr.Body.String())
	require.Equal(t, prID, jobs.payload.PullRequestID, "code review job should use the created pull request mirror")
	require.Equal(t, "anya", jobs.payload.PullRequestAuthor, "code review job should preserve the GitHub PR author")
	require.Contains(t, jobs.payload.OutputKey, ":review-request:", "webhook delivery identity should participate in policy-independent output dedupe")
	require.NoError(t, mock.ExpectationsWereMet(), "pull request mirror should be created with the webhook PR body")
}

func TestWebhook_HandleCodeReviewRequestedRefreshesExistingMirror(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	jobID := uuid.New()
	now := time.Now().UTC()
	cfg := models.DefaultCodeReviewPolicyConfig()
	policies := &codeReviewWebhookPolicyStore{policyID: policyID, config: cfg}
	priorReviewID := int64(143)
	priorReviewURL := "https://github.com/assembledhq/143/pull/42#pullrequestreview-143"
	metadata := &codeReviewWebhookMetadataStore{latest: models.CodeReviewSessionMetadata{
		ID: uuid.New(), SessionID: uuid.New(), Status: models.CodeReviewSessionStatusCompleted,
		ReviewOutputKey: "prior-output", GitHubReviewID: &priorReviewID, GitHubReviewURL: &priorReviewURL,
	}}
	sessions := &codeReviewWebhookSessionStore{}
	jobs := &codeReviewWebhookJobStore{jobID: jobID}
	codeReviews := codereviewsvc.NewService(policies, metadata, sessions, jobs, zerolog.Nop(), codereviewsvc.Config{
		AppReviewerLogins: []string{"143-code-reviewer"},
	})
	handler := &WebhookHandler{
		pullRequests: db.NewPullRequestStore(mock),
		codeReviews:  codeReviews,
	}

	staleBody := "stale description"
	staleHead := "stale-head"
	staleRef := "stale-ref"
	staleBase := "stale-base"
	mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "assembledhq/143", "github_pr_number": 42}).
		WillReturnRows(pgxmock.NewRows(codeReviewWebhookPullRequestColumns()).AddRow(
			prID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Stale title", &staleBody, "open", "pending", "app", "", &staleHead, &staleRef, &staleBase,
			"unknown", false, 0, false, nil, int64(0),
			models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil,
			nil, now, now,
		))
	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*github_pr_url = @github_pr_url[\\s\\S]*body = @body[\\s\\S]*head_sha = @head_sha[\\s\\S]*base_sha = @base_sha").
		WithArgs(pgx.NamedArgs{
			"id":            prID,
			"org_id":        orgID,
			"github_pr_url": "https://github.com/assembledhq/143/pull/42",
			"title":         "Fresh title",
			"body":          stringPointerArg{value: "Fresh body with testing evidence"},
			"head_sha":      stringPointerArg{value: "fresh-head"},
			"head_ref":      stringPointerArg{value: "feature/code-review"},
			"base_sha":      stringPointerArg{value: "fresh-base"},
			"merge_state":   models.PullRequestMergeStateUnknown,
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	body := []byte(`{
		"action": "review_requested",
		"number": 42,
		"repository": {"full_name": "assembledhq/143"},
		"requested_reviewer": {"login": "143-code-reviewer"},
		"pull_request": {
			"html_url": "https://github.com/assembledhq/143/pull/42",
			"title": "Fresh title",
			"body": "Fresh body with testing evidence",
			"user": {"login": "anya"},
			"head": {"sha": "fresh-head", "ref": "feature/code-review", "repo": {"fork": false}},
			"base": {"sha": "fresh-base"}
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Delivery", "delivery-refresh")
	rr := httptest.NewRecorder()

	ok := handler.handleCodeReviewRequested(rr, req, body, db.GitHubRepoOwner{
		RepositoryID: repoID,
		OrgID:        orgID,
		FullName:     "assembledhq/143",
		Status:       "active",
	})

	require.True(t, ok, "review_requested webhook should be processed: %s", rr.Body.String())
	require.Equal(t, prID, jobs.payload.PullRequestID, "code review job should use the existing pull request mirror")
	require.Equal(t, "fresh-head", jobs.payload.HeadSHA, "code review job should target the fresh webhook head SHA")
	require.Contains(t, jobs.payload.OutputKey, ":review-request:", "refreshed mirror review should be keyed independently by the delivery identity")
	require.Equal(t, &priorReviewID, jobs.payload.ExistingGitHubReviewID, "explicit request with review history should update the existing GitHub review")
	require.Equal(t, "prior-output", jobs.payload.PreviousOutputKey, "explicit request with review history should preserve prior inline-comment markers")
	require.NoError(t, mock.ExpectationsWereMet(), "existing pull request mirror should be refreshed from the webhook payload")
}

func TestWebhook_ReassessesRequestedCodeReviewAfterNewCommits(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	priorSessionID := uuid.New()
	priorReviewID := int64(143)
	priorReviewURL := "https://github.com/assembledhq/143/pull/42#pullrequestreview-143"
	now := time.Now().UTC()
	metadata := &codeReviewWebhookMetadataStore{latest: models.CodeReviewSessionMetadata{
		ID: uuid.New(), SessionID: priorSessionID, RepositoryID: repoID, PullRequestID: prID, PolicyID: policyID,
		HeadSHA: "old-head-sha", TriggerSource: models.CodeReviewTriggerSourceTeamReviewer,
		Status: models.CodeReviewSessionStatusCompleted, ReviewOutputKey: "prior-output",
		GitHubReviewID: &priorReviewID, GitHubReviewURL: &priorReviewURL,
	}}
	sessions := &codeReviewWebhookSessionStore{}
	jobs := &codeReviewWebhookJobStore{jobID: uuid.New()}
	cfg := models.DefaultCodeReviewPolicyConfig()
	codeReviews := codereviewsvc.NewService(
		&codeReviewWebhookPolicyStore{policyID: policyID, config: cfg}, metadata, sessions, jobs, zerolog.Nop(), codereviewsvc.Config{},
	)
	handler := &WebhookHandler{pullRequests: db.NewPullRequestStore(mock), codeReviews: codeReviews}

	oldBody := "Old description"
	oldHead := "old-head-sha"
	oldRef := "feature/code-review"
	oldBase := "base-sha"
	mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "assembledhq/143", "github_pr_number": 42}).
		WillReturnRows(pgxmock.NewRows(codeReviewWebhookPullRequestColumns()).AddRow(
			prID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Old title", &oldBody, "open", "pending", "user", "", &oldHead, &oldRef, &oldBase,
			"unknown", false, 0, false, nil, int64(0),
			models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil,
			nil, now, now,
		))
	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*github_pr_url = @github_pr_url[\\s\\S]*body = @body[\\s\\S]*head_sha = @head_sha").
		WithArgs(pgx.NamedArgs{
			"id": prID, "org_id": orgID,
			"github_pr_url": "https://github.com/assembledhq/143/pull/42",
			"title":         "Updated title", "body": stringPointerArg{value: "Updated description with test evidence"},
			"head_sha": stringPointerArg{value: "head-sha"}, "head_ref": stringPointerArg{value: "feature/code-review"},
			"base_sha": stringPointerArg{value: "base-sha"}, "merge_state": models.PullRequestMergeStateUnknown,
		}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	body := []byte(`{
		"action":"synchronize",
		"number":42,
		"repository":{"full_name":"assembledhq/143"},
		"pull_request":{
			"number":42,"html_url":"https://github.com/assembledhq/143/pull/42",
			"title":"Updated title","body":"Updated description with test evidence","user":{"login":"anya"},
			"head":{"sha":"head-sha","ref":"feature/code-review","repo":{"fork":false}},
			"base":{"sha":"base-sha"}
		}
	}`)
	err = handler.reassessCodeReviewsForGitHubEvent(context.Background(), db.GitHubRepoOwner{
		OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/143", Status: "active",
	}, "pull_request", body, "delivery-143")

	require.NoError(t, err, "new commits should trigger code review reassessment")
	require.Equal(t, prID, jobs.reassessmentPayload.PullRequestID, "reassessment should target the reviewed pull request")
	require.Equal(t, priorSessionID, jobs.reassessmentPayload.PriorSessionID, "queued reassessment should remain ordered behind the assessment active when the event arrived")
	require.Equal(t, "head-sha", jobs.reassessmentPayload.HeadSHA, "queued reassessment should capture the current PR head")
	require.Contains(t, jobs.reassessmentPayload.ChangeKey, "material:", "queued reassessment should use a material-state key")
	require.NotContains(t, jobs.reassessmentPayload.ChangeKey, "delivery-143", "queued reassessment should not use the webhook delivery id")
	require.Equal(t, 0, sessions.createCalls, "webhook should defer session creation to the durable starter job")
	require.NoError(t, mock.ExpectationsWereMet(), "pull request mirror refresh should be org-scoped")
}

func TestWebhook_DoesNotReassessApprovedCodeReviewAfterNewCommits(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repositoryID := uuid.New()
	pullRequestID := uuid.New()
	policyID := uuid.New()
	headSHA := "new-head-sha"
	baseSHA := "base-sha"
	metadata := &codeReviewWebhookMetadataStore{
		approved: true,
		latest: models.CodeReviewSessionMetadata{
			ID: uuid.New(), SessionID: uuid.New(), RepositoryID: repositoryID,
			PullRequestID: pullRequestID, PolicyID: policyID, HeadSHA: "approved-head-sha",
			TriggerSource: models.CodeReviewTriggerSourceTeamReviewer,
			Status:        models.CodeReviewSessionStatusCompleted, ReviewOutputKey: "approved-output",
		},
	}
	jobs := &codeReviewWebhookJobStore{jobID: uuid.New()}
	codeReviews := codereviewsvc.NewService(
		&codeReviewWebhookPolicyStore{policyID: policyID, config: models.DefaultCodeReviewPolicyConfig()},
		metadata,
		&codeReviewWebhookSessionStore{},
		jobs,
		zerolog.Nop(),
		codereviewsvc.Config{},
	)
	handler := &WebhookHandler{codeReviews: codeReviews}
	event := codeReviewReassessmentWebhook{Action: "synchronize"}

	err := handler.reassessCodeReviewTarget(
		context.Background(),
		db.GitHubRepoOwner{OrgID: orgID, RepositoryID: repositoryID, FullName: "assembledhq/143", Status: "active"},
		"pull_request",
		event,
		models.PullRequest{
			ID: pullRequestID, OrgID: orgID, GitHubRepo: "assembledhq/143", GitHubPRNumber: 42,
			GitHubPRURL: "https://github.com/assembledhq/143/pull/42", Title: "Changed after approval",
			HeadSHA: &headSHA, BaseSHA: &baseSHA,
		},
	)

	require.NoError(t, err, "approved synchronize event should be handled without error")
	require.Empty(t, jobs.reassessmentPayloads, "new commits after approval should not enqueue automatic reviewer work")
}

func TestCodeReviewMaterialChangeKey(t *testing.T) {
	t.Parallel()

	headSHA := "head-sha"
	newHeadSHA := "new-head-sha"
	base := models.PullRequest{HeadSHA: &headSHA, CIStatus: models.PullRequestCIStatusSuccess}
	ciChanged := base
	ciChanged.CIStatus = models.PullRequestCIStatusFailure
	headChanged := base
	headChanged.HeadSHA = &newHeadSHA

	tests := []struct {
		name       string
		left       models.PullRequest
		right      models.PullRequest
		expectSame bool
	}{
		{
			name:       "same head ignores CI state",
			left:       base,
			right:      ciChanged,
			expectSame: true,
		},
		{
			name:       "new head uses a new key",
			left:       base,
			right:      headChanged,
			expectSame: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			left, err := codeReviewMaterialChangeKey(tt.left)
			require.NoError(t, err, "left material state should serialize")
			right, err := codeReviewMaterialChangeKey(tt.right)
			require.NoError(t, err, "right material state should serialize")
			if tt.expectSame {
				require.Equal(t, left, right, "the same code head should share a dedupe key")
				return
			}
			require.NotEqual(t, left, right, "a new code head should use a new dedupe key")
		})
	}
}

func TestWebhook_CodeReviewReassessmentUsesSameKeyForEquivalentDeliveries(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	priorSessionID := uuid.New()
	now := time.Now().UTC()
	metadata := &codeReviewWebhookMetadataStore{latest: models.CodeReviewSessionMetadata{
		ID: uuid.New(), SessionID: priorSessionID, RepositoryID: repoID, PullRequestID: prID, PolicyID: policyID,
		HeadSHA: "previous-head-sha", TriggerSource: models.CodeReviewTriggerSourceTeamReviewer,
		Status: models.CodeReviewSessionStatusCompleted, ReviewOutputKey: "prior-output",
	}}
	jobs := &codeReviewWebhookJobStore{jobID: uuid.New()}
	cfg := models.DefaultCodeReviewPolicyConfig()
	codeReviews := codereviewsvc.NewService(
		&codeReviewWebhookPolicyStore{policyID: policyID, config: cfg}, metadata,
		&codeReviewWebhookSessionStore{}, jobs, zerolog.Nop(), codereviewsvc.Config{},
	)
	handler := &WebhookHandler{pullRequests: db.NewPullRequestStore(mock), codeReviews: codeReviews}
	body := "Description with tests"
	headSHA := "head-sha"
	headRef := "feature/code-review"
	baseSHA := "base-sha"
	expectPullRequest := func() {
		mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "assembledhq/143", "github_pr_number": 42}).
			WillReturnRows(pgxmock.NewRows(codeReviewWebhookPullRequestColumns()).AddRow(
				prID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
				"Improve reviewer reassessment", &body, "open", "pending", "user", "success", &headSHA, &headRef, &baseSHA,
				"clean", false, 0, false, nil, int64(1),
				models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil,
				nil, now, now,
			))
		mock.ExpectExec("UPDATE pull_requests[\\s\\S]*github_pr_url = @github_pr_url[\\s\\S]*body = @body[\\s\\S]*head_sha = @head_sha").
			WithArgs(pgx.NamedArgs{
				"id": prID, "org_id": orgID,
				"github_pr_url": "https://github.com/assembledhq/143/pull/42",
				"title":         "Improve reviewer reassessment", "body": stringPointerArg{value: body},
				"head_sha": stringPointerArg{value: headSHA}, "head_ref": stringPointerArg{value: headRef},
				"base_sha": stringPointerArg{value: baseSHA}, "merge_state": models.PullRequestMergeStateUnknown,
			}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	eventBody := []byte(`{
		"action":"synchronize","number":42,"repository":{"full_name":"assembledhq/143"},
		"pull_request":{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42",
		"title":"Improve reviewer reassessment","body":"Description with tests","user":{"login":"anya"},
		"head":{"sha":"head-sha","ref":"feature/code-review","repo":{"fork":false}},"base":{"sha":"base-sha"}}
	}`)
	owner := db.GitHubRepoOwner{OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/143", Status: "active"}
	for _, deliveryID := range []string{"delivery-one", "delivery-two"} {
		expectPullRequest()
		require.NoError(t, handler.reassessCodeReviewsForGitHubEvent(
			context.Background(), owner, "pull_request", eventBody, deliveryID,
		), "equivalent delivery should queue without error")
	}

	require.Len(t, jobs.reassessmentPayloads, 2, "both deliveries should reach the durable queue boundary")
	require.Equal(t, jobs.reassessmentPayloads[0].ChangeKey, jobs.reassessmentPayloads[1].ChangeKey, "equivalent deliveries should share a semantic dedupe key")
	require.NotContains(t, jobs.reassessmentPayloads[0].ChangeKey, "delivery-", "semantic dedupe key should not contain delivery identity")
	require.NoError(t, mock.ExpectationsWereMet(), "equivalent deliveries should use org-scoped pull request lookups")
}

func TestCodeReviewEventChangesAssessment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType string
		action    string
		expected  bool
	}{
		{name: "new commits", eventType: "pull_request", action: "synchronize", expected: true},
		{name: "description edit", eventType: "pull_request", action: "edited", expected: false},
		{name: "pull request reopened", eventType: "pull_request", action: "reopened", expected: false},
		{name: "human review", eventType: "pull_request_review", action: "submitted", expected: false},
		{name: "review dismissal", eventType: "pull_request_review", action: "dismissed", expected: false},
		{name: "inline review creation", eventType: "pull_request_review_comment", action: "created", expected: false},
		{name: "inline review edit", eventType: "pull_request_review_comment", action: "edited", expected: false},
		{name: "inline review deletion", eventType: "pull_request_review_comment", action: "deleted", expected: false},
		{name: "thread resolution", eventType: "pull_request_review_thread", action: "resolved", expected: false},
		{name: "thread reopening", eventType: "pull_request_review_thread", action: "unresolved", expected: false},
		{name: "check suite complete", eventType: "check_suite", action: "completed", expected: false},
		{name: "check run complete", eventType: "check_run", action: "completed", expected: false},
		{name: "commit status changed", eventType: "status", action: "", expected: false},
		{name: "unrelated label", eventType: "pull_request", action: "labeled", expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			event := codeReviewReassessmentWebhook{Action: tt.action}
			require.Equal(t, tt.expected, codeReviewEventChangesAssessment(tt.eventType, event), "event classifier should trigger only pass-relevant changes")
		})
	}
}

func TestWebhook_HandleIssueComment_InvalidJSON(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	prService := ghservice.NewPRService(nil, db.NewPullRequestStore(mock), nil, nil, nil, nil, nil, zerolog.Nop())
	handler := NewWebhookHandler(&config.Config{}, db.NewOrganizationStore(mock), db.NewUserStore(mock), db.NewRepositoryStore(mock), db.NewIntegrationStore(mock), prService)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(`{bad json`))
	rr := httptest.NewRecorder()
	handler.handleIssueComment(rr, req, []byte(`{bad json`))
	require.Equal(t, http.StatusBadRequest, rr.Code, "handleIssueComment should reject malformed JSON")
	require.Contains(t, rr.Body.String(), "INVALID_JSON", "handleIssueComment should encode the invalid JSON error")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWebhook_HandleIssueComment_SkipsNonPRIssues(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	prService := ghservice.NewPRService(nil, db.NewPullRequestStore(mock), nil, nil, nil, nil, nil, zerolog.Nop())
	handler := NewWebhookHandler(&config.Config{}, db.NewOrganizationStore(mock), db.NewUserStore(mock), db.NewRepositoryStore(mock), db.NewIntegrationStore(mock), prService)

	// issue_comment on a plain issue (no pull_request field) — must be processed without DB lookups.
	body := []byte(`{"action":"created","repository":{"id":0,"full_name":"acme/app"},"issue":{"number":7},"comment":{"body":"hi"},"sender":{"login":"alice"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	handler.handleIssueComment(rr, req, body)
	require.Equal(t, http.StatusOK, rr.Code, "issue_comment on non-PR issue should be processed silently")
	require.Contains(t, rr.Body.String(), "processed", "issue_comment handler should report processed for non-PR issues")
	require.NoError(t, mock.ExpectationsWereMet(), "no DB calls should be made for non-PR issue comments")
}

type codeReviewPullRequestLoaderStub struct {
	orgID        uuid.UUID
	repositoryID uuid.UUID
	number       int
	snapshot     ghservice.CodeReviewPullRequestSnapshot
	err          error
}

func (s *codeReviewPullRequestLoaderStub) GetCodeReviewPullRequestSnapshot(_ context.Context, orgID, repositoryID uuid.UUID, number int) (ghservice.CodeReviewPullRequestSnapshot, error) {
	s.orgID = orgID
	s.repositoryID = repositoryID
	s.number = number
	return s.snapshot, s.err
}

func TestWebhook_HandleCodeReviewMentionedStartsContextualReview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	now := time.Now().UTC()
	commentBody := "@assembledhq/143-code-reviewer Review again. This doesn't need a screenshot; it only changes when the behavior appears."
	remote := ghservice.CodeReviewPullRequestSnapshot{
		Number:      54903,
		HTMLURL:     "https://github.com/assembledhq/assembled/pull/54903",
		Title:       "Fix Slack notification fallback",
		Body:        "Restore notification rows when Slack auth is unavailable.",
		State:       "open",
		AuthorLogin: "assembled-author",
		HeadSHA:     "fresh-head",
		HeadRef:     "fix/slack-fallback",
		BaseSHA:     "fresh-base",
	}
	loader := &codeReviewPullRequestLoaderStub{snapshot: remote}
	policies := &codeReviewWebhookPolicyStore{policyID: policyID, config: models.DefaultCodeReviewPolicyConfig()}
	metadata := &codeReviewWebhookMetadataStore{}
	sessions := &codeReviewWebhookSessionStore{}
	jobs := &codeReviewWebhookJobStore{jobID: uuid.New()}
	codeReviews := codereviewsvc.NewService(policies, metadata, sessions, jobs, zerolog.Nop(), codereviewsvc.Config{
		TeamSlugs: []string{"143-code-reviewer"},
	})
	handler := &WebhookHandler{
		pullRequests:  db.NewPullRequestStore(mock),
		codeReviews:   codeReviews,
		codeReviewPRs: loader,
	}

	staleBody := "old body"
	staleHead := "old-head"
	staleRef := "old-ref"
	staleBase := "old-base"
	mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "assembledhq/assembled", "github_pr_number": 54903}).
		WillReturnRows(pgxmock.NewRows(codeReviewWebhookPullRequestColumns()).AddRow(
			prID, nil, orgID, 54903, remote.HTMLURL, "assembledhq/assembled",
			"Old title", &staleBody, "open", "pending", "user", "", &staleHead, &staleRef, &staleBase,
			"unknown", false, 0, false, nil, int64(0),
			models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil,
			nil, now, now,
		))
	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*github_pr_url = @github_pr_url[\\s\\S]*body = @body[\\s\\S]*head_sha = @head_sha[\\s\\S]*base_sha = @base_sha").
		WithArgs(pgx.NamedArgs{
			"id":            prID,
			"org_id":        orgID,
			"github_pr_url": remote.HTMLURL,
			"title":         remote.Title,
			"body":          stringPointerArg{value: remote.Body},
			"head_sha":      stringPointerArg{value: remote.HeadSHA},
			"head_ref":      stringPointerArg{value: remote.HeadRef},
			"base_sha":      stringPointerArg{value: remote.BaseSHA},
			"merge_state":   models.PullRequestMergeStateUnknown,
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	var event ghservice.IssueCommentEvent
	event.Action = "created"
	event.DeliveryID = "delivery-comment-54903"
	event.Repository.FullName = "assembledhq/assembled"
	event.Issue.Number = 54903
	event.Issue.PullRequest = &struct{}{}
	event.Comment.ID = 5124237355
	event.Comment.Body = commentBody
	event.Comment.HTMLURL = "https://github.com/assembledhq/assembled/pull/54903#issuecomment-5124237355"
	event.Comment.User.Login = "assembled-matthew"
	event.Comment.User.Type = "User"
	event.Comment.AuthorAssociation = "MEMBER"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", nil)
	rr := httptest.NewRecorder()

	ok, captured := handler.handleCodeReviewMentioned(rr, req, event, db.GitHubRepoOwner{
		OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/assembled", Status: "active",
	})

	require.True(t, ok, "configured team mention should be processed: %s", rr.Body.String())
	require.False(t, captured, "ordinary review request should not be captured as a dispute")
	require.Equal(t, orgID, loader.orgID, "handler should scope the GitHub snapshot to the owning organization")
	require.Equal(t, repoID, loader.repositoryID, "handler should load the configured repository")
	require.Equal(t, 54903, loader.number, "handler should load the commented pull request")
	require.Equal(t, prID, jobs.payload.PullRequestID, "review job should target the mirrored pull request")
	require.Equal(t, remote.HeadSHA, jobs.payload.HeadSHA, "review job should use the current GitHub head")
	require.NotNil(t, jobs.payload.RequestContext, "review job should carry the triggering comment")
	require.Equal(t, commentBody, jobs.payload.RequestContext.Body, "orchestrator context should preserve the human clarification")
	require.Equal(t, "assembled-matthew", jobs.payload.RequestContext.AuthorLogin, "orchestrator context should preserve the requester")
	require.NoError(t, mock.ExpectationsWereMet(), "comment trigger should use org-scoped mirror reads and writes")
}

func TestWebhook_HandleCodeReviewMentionedIgnoresUnconfiguredTeamBeforeGitHubLoad(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	loader := &codeReviewPullRequestLoaderStub{err: errors.New("GitHub should not be called")}
	codeReviews := codereviewsvc.NewService(
		&codeReviewWebhookPolicyStore{},
		&codeReviewWebhookMetadataStore{},
		&codeReviewWebhookSessionStore{},
		&codeReviewWebhookJobStore{},
		zerolog.Nop(),
		codereviewsvc.Config{TeamSlugs: []string{"143-code-reviewer"}},
	)
	handler := &WebhookHandler{codeReviews: codeReviews, codeReviewPRs: loader}
	var event ghservice.IssueCommentEvent
	event.Repository.FullName = "assembledhq/assembled"
	event.Issue.Number = 54903
	event.Comment.Body = "@assembledhq/frontend-platform can you take a look?"
	event.Comment.User.Type = "User"
	event.Comment.AuthorAssociation = "MEMBER"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", nil)
	rr := httptest.NewRecorder()

	ok, captured := handler.handleCodeReviewMentioned(rr, req, event, db.GitHubRepoOwner{
		OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/assembled", Status: "active",
	})

	require.True(t, ok, "unconfigured team mention should be ignored")
	require.False(t, captured, "unconfigured team mention should not be captured as a dispute")
	require.Equal(t, 0, loader.number, "unconfigured team mention should not spend a GitHub API request")
	require.Empty(t, rr.Body.String(), "ignored team mention should not write an error response")
}

func TestWebhook_HandleCodeReviewMentionedDoesNotStartAReviewWhenDisputeCaptureFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	now := time.Now().UTC()
	remote := ghservice.CodeReviewPullRequestSnapshot{
		Number: 54903, State: "open", HTMLURL: "https://github.com/assembledhq/assembled/pull/54903",
		Title: "Fix invoice rounding", Body: "body", HeadSHA: "head-sha", HeadRef: "feature", BaseSHA: "base-sha",
	}
	loader := &codeReviewPullRequestLoaderStub{snapshot: remote}
	jobs := &codeReviewWebhookJobStore{jobID: uuid.New()}
	// A fully wired review service: if the handler falls through, a review
	// really does start, so the assertion below can tell the difference.
	codeReviews := codereviewsvc.NewService(
		&codeReviewWebhookPolicyStore{policyID: uuid.New(), config: models.DefaultCodeReviewPolicyConfig()},
		&codeReviewWebhookMetadataStore{},
		&codeReviewWebhookSessionStore{},
		jobs,
		zerolog.Nop(),
		codereviewsvc.Config{TeamSlugs: []string{"143-code-reviewer"}},
	)
	// An unconfigured dispute service makes FileFromGitHub fail the way a
	// transient outage would.
	handler := &WebhookHandler{
		codeReviews:        codeReviews,
		codeReviewPRs:      loader,
		pullRequests:       db.NewPullRequestStore(mock),
		codeReviewDisputes: codereviewsvc.NewDisputeService(nil, nil, nil, nil, nil, "", zerolog.Nop()),
	}

	staleBody := "old body"
	staleHead := "old-head"
	staleRef := "old-ref"
	staleBase := "old-base"
	mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "assembledhq/assembled", "github_pr_number": 54903}).
		WillReturnRows(pgxmock.NewRows(codeReviewWebhookPullRequestColumns()).AddRow(
			prID, nil, orgID, 54903, remote.HTMLURL, "assembledhq/assembled",
			"Old title", &staleBody, "open", "pending", "user", "", &staleHead, &staleRef, &staleBase,
			"unknown", false, 0, false, nil, int64(0),
			models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil,
			nil, now, now,
		))
	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*github_pr_url = @github_pr_url").
		WithArgs(pgx.NamedArgs{
			"id":            prID,
			"org_id":        orgID,
			"github_pr_url": remote.HTMLURL,
			"title":         remote.Title,
			"body":          stringPointerArg{value: remote.Body},
			"head_sha":      stringPointerArg{value: remote.HeadSHA},
			"head_ref":      stringPointerArg{value: remote.HeadRef},
			"base_sha":      stringPointerArg{value: remote.BaseSHA},
			"merge_state":   models.PullRequestMergeStateUnknown,
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	var event ghservice.IssueCommentEvent
	event.Action = "created"
	event.DeliveryID = "delivery-dispute-capture-failure"
	event.Repository.FullName = "assembledhq/assembled"
	event.Issue.Number = 54903
	event.Issue.PullRequest = &struct{}{}
	event.Comment.ID = 5124237399
	event.Comment.Body = "@assembledhq/143-code-reviewer I disagree, this should have been approved"
	event.Comment.User.Login = "assembled-matthew"
	event.Comment.User.Type = "User"
	event.Comment.AuthorAssociation = "MEMBER"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", nil)
	rr := httptest.NewRecorder()

	ok, captured := handler.handleCodeReviewMentioned(rr, req, event, db.GitHubRepoOwner{
		OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/assembled", Status: "active",
	})

	require.True(t, ok, "a dispute capture failure should not fail the webhook: %s", rr.Body.String())
	require.False(t, captured, "an uncaptured comment should still be recorded as ordinary PR feedback")
	// Falling through would answer a transient capture failure with a whole new
	// agent fan-out, which is the opposite of what the commenter asked for.
	require.Equal(t, uuid.Nil, jobs.payload.SessionID, "a failed dispute capture must not start a code review")
	require.Empty(t, rr.Body.String(), "a non-fatal capture failure should not write an error response")
}

// Inline finding replies enter the same asynchronous LLM classification path.
// Comments classified as not_a_dispute are later released back to ordinary PR
// feedback follow-through, so the webhook does not guess from wording.
func TestWebhook_HandleCodeReviewInlineDisputeDefersMeaningToTriage(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	rootID := int64(778899)
	tests := []struct {
		name string
		body string
	}{
		{name: "actionable reply is classified asynchronously", body: "Good catch, please apply that rename."},
		{name: "acknowledgement is classified asynchronously", body: "Thanks, fixing now."},
		{name: "objection is classified asynchronously", body: "I disagree, the helper already handles nil."},
		{name: "question is classified asynchronously", body: "Why is this flagged as sensitive?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()
			// The pull request lookup is the first structural step after provenance.
			// Every human reply should reach it regardless of comment wording.
			mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "assembledhq/assembled", "github_pr_number": 54903}).
				WillReturnError(pgx.ErrNoRows)
			handler := &WebhookHandler{
				pullRequests:       db.NewPullRequestStore(mock),
				codeReviewDisputes: codereviewsvc.NewDisputeService(nil, nil, nil, nil, nil, "", zerolog.Nop()),
			}
			var event ghservice.PullRequestReviewCommentEvent
			event.Action = "created"
			event.Repository.FullName = "assembledhq/assembled"
			event.PullRequest.Number = 54903
			event.Comment.ID = 991122
			event.Comment.InReplyToID = &rootID
			event.Comment.Body = tt.body
			event.Comment.User.Login = "assembled-matthew"
			event.Comment.User.Type = "User"
			event.Comment.AuthorAssociation = "MEMBER"
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", nil)

			captured := handler.handleCodeReviewInlineDispute(req, event, db.GitHubRepoOwner{
				OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/assembled", Status: "active",
			})

			require.False(t, captured, "the missing pull request means no intake row can be stored")
			require.NoError(t, mock.ExpectationsWereMet(), "comment meaning must not be inferred before durable LLM triage")
		})
	}
}

// When dispute intake is unavailable, a trusted created mention retains the
// normal first-review behavior. The production path captures post-decision
// mentions and lets LLM triage return review_request asynchronously.
func TestWebhook_HandleCodeReviewMentionedStartsReviewForTrustedQuestionWithoutObjection(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	now := time.Now().UTC()
	remote := ghservice.CodeReviewPullRequestSnapshot{
		Number: 54903, State: "open", HTMLURL: "https://github.com/assembledhq/assembled/pull/54903",
		Title: "Fix invoice rounding", Body: "body", HeadSHA: "head-sha", HeadRef: "feature", BaseSHA: "base-sha",
	}
	loader := &codeReviewPullRequestLoaderStub{snapshot: remote}
	jobs := &codeReviewWebhookJobStore{jobID: uuid.New()}
	codeReviews := codereviewsvc.NewService(
		&codeReviewWebhookPolicyStore{policyID: uuid.New(), config: models.DefaultCodeReviewPolicyConfig()},
		&codeReviewWebhookMetadataStore{},
		&codeReviewWebhookSessionStore{},
		jobs,
		zerolog.Nop(),
		codereviewsvc.Config{TeamSlugs: []string{"143-code-reviewer"}},
	)
	handler := &WebhookHandler{
		codeReviews:   codeReviews,
		codeReviewPRs: loader,
		pullRequests:  db.NewPullRequestStore(mock),
	}

	staleBody := "old body"
	staleHead := "old-head"
	staleRef := "old-ref"
	staleBase := "old-base"
	mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "assembledhq/assembled", "github_pr_number": 54903}).
		WillReturnRows(pgxmock.NewRows(codeReviewWebhookPullRequestColumns()).AddRow(
			prID, nil, orgID, 54903, remote.HTMLURL, "assembledhq/assembled",
			"Old title", &staleBody, "open", "pending", "user", "", &staleHead, &staleRef, &staleBase,
			"unknown", false, 0, false, nil, int64(0),
			models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil,
			nil, now, now,
		))
	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*github_pr_url = @github_pr_url").
		WithArgs(pgx.NamedArgs{
			"id":            prID,
			"org_id":        orgID,
			"github_pr_url": remote.HTMLURL,
			"title":         remote.Title,
			"body":          stringPointerArg{value: remote.Body},
			"head_sha":      stringPointerArg{value: remote.HeadSHA},
			"head_ref":      stringPointerArg{value: remote.HeadRef},
			"base_sha":      stringPointerArg{value: remote.BaseSHA},
			"merge_state":   models.PullRequestMergeStateUnknown,
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	var event ghservice.IssueCommentEvent
	event.Action = "created"
	event.DeliveryID = "delivery-trusted-question"
	event.Repository.FullName = "assembledhq/assembled"
	event.Issue.Number = 54903
	event.Issue.PullRequest = &struct{}{}
	event.Comment.ID = 5124237400
	event.Comment.Body = "@assembledhq/143-code-reviewer can you re-review this?"
	event.Comment.User.Login = "assembled-matthew"
	event.Comment.User.Type = "User"
	event.Comment.AuthorAssociation = "MEMBER"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", nil)
	rr := httptest.NewRecorder()

	ok, captured := handler.handleCodeReviewMentioned(rr, req, event, db.GitHubRepoOwner{
		OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/assembled", Status: "active",
	})

	require.True(t, ok, "a trusted review request should not fail the webhook: %s", rr.Body.String())
	require.False(t, captured, "without dispute intake, a trusted created mention should fall through to the normal review path")
	require.NotEqual(t, uuid.Nil, jobs.payload.SessionID, "an explicit re-review request must still start a review")
}

// When dispute intake is unavailable, edited mentions cannot safely start a
// review. The configured production path retains the edit as a new immutable
// intake version and lets provenance checks prevent a review request route.
func TestWebhook_HandleCodeReviewMentionedIgnoresEditedTrustedQuestionWithoutDisputeIntake(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	loader := &codeReviewPullRequestLoaderStub{err: errors.New("GitHub should not be called")}
	codeReviews := codereviewsvc.NewService(
		&codeReviewWebhookPolicyStore{},
		&codeReviewWebhookMetadataStore{},
		&codeReviewWebhookSessionStore{},
		&codeReviewWebhookJobStore{},
		zerolog.Nop(),
		codereviewsvc.Config{TeamSlugs: []string{"143-code-reviewer"}},
	)
	handler := &WebhookHandler{
		codeReviews:   codeReviews,
		codeReviewPRs: loader,
	}
	var event ghservice.IssueCommentEvent
	event.Action = "edited"
	event.DeliveryID = "delivery-edited-question"
	event.Repository.FullName = "assembledhq/assembled"
	event.Issue.Number = 54903
	event.Issue.PullRequest = &struct{}{}
	event.Comment.ID = 5124237401
	event.Comment.Body = "@assembledhq/143-code-reviewer can you re-review this?"
	event.Comment.User.Login = "assembled-matthew"
	event.Comment.User.Type = "User"
	event.Comment.AuthorAssociation = "MEMBER"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", nil)
	rr := httptest.NewRecorder()

	ok, captured := handler.handleCodeReviewMentioned(rr, req, event, db.GitHubRepoOwner{
		OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/assembled", Status: "active",
	})

	require.True(t, ok, "an edited question should not fail the webhook")
	require.False(t, captured, "no dispute intake is configured for the edited mention")
	require.Equal(t, 0, loader.number, "an edited trusted question should not start a review when durable intake is unavailable")
	require.Empty(t, rr.Body.String(), "an ignored edit should not write an error response")
}

func TestWebhook_HandleCodeReviewMentionedIgnoresEditedNonDisputeBeforeGitHubLoad(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	loader := &codeReviewPullRequestLoaderStub{err: errors.New("GitHub should not be called")}
	codeReviews := codereviewsvc.NewService(
		&codeReviewWebhookPolicyStore{},
		&codeReviewWebhookMetadataStore{},
		&codeReviewWebhookSessionStore{},
		&codeReviewWebhookJobStore{},
		zerolog.Nop(),
		codereviewsvc.Config{TeamSlugs: []string{"143-code-reviewer"}},
	)
	handler := &WebhookHandler{codeReviews: codeReviews, codeReviewPRs: loader}
	var event ghservice.IssueCommentEvent
	event.Action = "edited"
	event.DeliveryID = "delivery-edit-54903"
	event.Repository.FullName = "assembledhq/assembled"
	event.Issue.Number = 54903
	event.Comment.Body = "@assembledhq/143-code-reviewer please take a look"
	event.Comment.User.Login = "assembled-matthew"
	event.Comment.User.Type = "User"
	event.Comment.AuthorAssociation = "MEMBER"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", nil)
	rr := httptest.NewRecorder()

	ok, captured := handler.handleCodeReviewMentioned(rr, req, event, db.GitHubRepoOwner{
		OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/assembled", Status: "active",
	})

	require.True(t, ok, "an edited mention should not fail the webhook")
	require.False(t, captured, "no dispute intake is configured for the edited mention")
	// Every edit carries a fresh delivery ID, so reaching the mention path
	// again would force a whole new agent fan-out for a typo fix.
	require.Equal(t, 0, loader.number, "an edited non-dispute mention should not spend a GitHub API request or start a review")
	require.Empty(t, rr.Body.String(), "an ignored edit should not write an error response")
}

func TestWebhook_HandleCodeReviewMentionedCapturesEveryHumanPostDecisionMention(t *testing.T) {
	t.Parallel()

	comments := []string{
		"@assembledhq/143-code-reviewer while this affects EKS, it is a minimal established server timeout change. can you check again",
		"@assembledhq/143-code-reviewer this is adding security tightening; SAML and magic-link usage is low enough that this is only a security improvement",
		"@assembledhq/143-code-reviewer while this technically touches OAuth code, it only fixes the mutex error path and follows a standard Go pattern",
		"@assembledhq/143-code-reviewer I don't believe this is a sensitive change judgment; this is only minor security hardening",
		"@assembledhq/143-code-reviewer can you re-review this?",
		"@assembledhq/143-code-reviewer thanks",
	}
	for _, comment := range comments {
		comment := comment
		t.Run(comment, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()

			orgID := uuid.New()
			repositoryID := uuid.New()
			pullRequestID := uuid.New()
			sessionID := uuid.New()
			policyID := uuid.New()
			commentID := int64(5196810672)
			now := time.Now().UTC()
			remote := ghservice.CodeReviewPullRequestSnapshot{
				Number: 55422, State: "open", HTMLURL: "https://github.com/assembledhq/assembled/pull/55422",
				Title: "Harden authentication", Body: "body", AuthorLogin: "assembled-matthew",
				HeadSHA: "head-sha", HeadRef: "feature", BaseSHA: "base-sha",
			}
			loader := &codeReviewPullRequestLoaderStub{snapshot: remote}
			jobs := &codeReviewWebhookJobStore{jobID: uuid.New()}
			codeReviews := codereviewsvc.NewService(
				&codeReviewWebhookPolicyStore{policyID: policyID, config: models.DefaultCodeReviewPolicyConfig()},
				&codeReviewWebhookMetadataStore{},
				&codeReviewWebhookSessionStore{},
				jobs,
				zerolog.Nop(),
				codereviewsvc.Config{TeamSlugs: []string{"143-code-reviewer"}},
			)
			disputeStore := &webhookCaptureDisputeStore{}
			decision := models.CodeReviewDecisionNeedsHumanReview
			disputeReviews := webhookCaptureDisputeReviewStore{
				metadata: models.CodeReviewSessionMetadata{
					OrgID: orgID, SessionID: sessionID, PullRequestID: pullRequestID,
					RepositoryID: repositoryID, PolicyID: policyID, HeadSHA: "reviewed-head",
					Status: models.CodeReviewSessionStatusCompleted, Decision: &decision,
				},
				item: models.CodeReviewListItem{
					PullRequestAuthor: "assembled-matthew", PullRequestTitle: remote.Title,
					GitHubRepo: "assembledhq/assembled", GitHubPRNumber: remote.Number, GitHubPRURL: remote.HTMLURL,
				},
			}
			disputeService := codereviewsvc.NewDisputeService(
				disputeStore,
				disputeReviews,
				webhookCaptureDisputePullRequestStore{pullRequest: models.PullRequest{
					ID: pullRequestID, OrgID: orgID, GitHubRepo: "assembledhq/assembled",
					GitHubPRNumber: remote.Number, Title: remote.Title,
				}},
				&codeReviewWebhookJobStore{},
				nil,
				"",
				zerolog.Nop(),
			)
			handler := &WebhookHandler{
				codeReviews: codeReviews, codeReviewPRs: loader,
				pullRequests: db.NewPullRequestStore(mock), codeReviewDisputes: disputeService,
			}

			staleBody := "old body"
			staleHead := "old-head"
			staleRef := "old-ref"
			staleBase := "old-base"
			mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*WHERE org_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "assembledhq/assembled", "github_pr_number": 55422}).
				WillReturnRows(pgxmock.NewRows(codeReviewWebhookPullRequestColumns()).AddRow(
					pullRequestID, nil, orgID, 55422, remote.HTMLURL, "assembledhq/assembled",
					"Old title", &staleBody, "open", "pending", "user", "", &staleHead, &staleRef, &staleBase,
					"unknown", false, 0, false, nil, int64(0),
					models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil,
					nil, now, now,
				))
			mock.ExpectExec("UPDATE pull_requests[\\s\\S]*github_pr_url = @github_pr_url").
				WithArgs(pgx.NamedArgs{
					"id": pullRequestID, "org_id": orgID, "github_pr_url": remote.HTMLURL,
					"title": remote.Title, "body": stringPointerArg{value: remote.Body},
					"head_sha": stringPointerArg{value: remote.HeadSHA}, "head_ref": stringPointerArg{value: remote.HeadRef},
					"base_sha": stringPointerArg{value: remote.BaseSHA}, "merge_state": models.PullRequestMergeStateUnknown,
				}).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			var event ghservice.IssueCommentEvent
			event.Action = "created"
			event.DeliveryID = "delivery-post-decision-comment"
			event.Repository.FullName = "assembledhq/assembled"
			event.Issue.Number = 55422
			event.Issue.PullRequest = &struct{}{}
			event.Comment.ID = commentID
			event.Comment.Body = comment
			event.Comment.HTMLURL = remote.HTMLURL + "#issuecomment-5196810672"
			event.Comment.User.Login = "assembled-matthew"
			event.Comment.User.Type = "User"
			event.Comment.AuthorAssociation = "MEMBER"
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", nil)
			rr := httptest.NewRecorder()

			ok, captured := handler.handleCodeReviewMentioned(rr, req, event, db.GitHubRepoOwner{
				OrgID: orgID, RepositoryID: repositoryID, FullName: "assembledhq/assembled", Status: "active",
			})

			require.True(t, ok, "post-decision mention capture should not fail the webhook: %s", rr.Body.String())
			require.True(t, captured, "every human mention after a completed decision should enter durable LLM triage")
			require.Equal(t, uuid.Nil, jobs.payload.SessionID, "webhook wording must not directly start a new code review")
			require.Len(t, disputeStore.created, 1, "the comment should produce exactly one durable intake row")
			require.Equal(t, comment, disputeStore.created[0].Body, "the complete comment should be retained as untrusted classification evidence")
			var signals map[string]any
			require.NoError(t, json.Unmarshal(disputeStore.created[0].QueueSignals, &signals), "intake queue signals should be valid JSON")
			require.Equal(t, true, signals["review_request_allowed"], "a trusted new comment may be returned to the normal review path after classification")
			require.NoError(t, mock.ExpectationsWereMet(), "post-decision capture should keep mirror access org-scoped")
		})
	}
}

func TestWebhook_HandleCodeReviewMentionedRejectsUntrustedAuthorsBeforeGitHubLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		authorAssociation     string
		authorType            string
		senderType            string
		performedViaGitHubApp *ghservice.FeedbackGitHubAppIdentity
	}{
		{
			name:              "external commenter",
			authorAssociation: "NONE",
			authorType:        "User",
			senderType:        "User",
		},
		{
			name:              "bot commenter",
			authorAssociation: "MEMBER",
			authorType:        "Bot",
			senderType:        "Bot",
		},
		{
			name:                  "GitHub App commenter",
			authorAssociation:     "MEMBER",
			authorType:            "User",
			senderType:            "User",
			performedViaGitHubApp: &ghservice.FeedbackGitHubAppIdentity{ID: 143, Slug: "review-helper"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			repoID := uuid.New()
			loader := &codeReviewPullRequestLoaderStub{err: errors.New("GitHub should not be called")}
			codeReviews := codereviewsvc.NewService(
				&codeReviewWebhookPolicyStore{},
				&codeReviewWebhookMetadataStore{},
				&codeReviewWebhookSessionStore{},
				&codeReviewWebhookJobStore{},
				zerolog.Nop(),
				codereviewsvc.Config{TeamSlugs: []string{"143-code-reviewer"}},
			)
			handler := &WebhookHandler{
				codeReviews:   codeReviews,
				codeReviewPRs: loader,
			}
			var event ghservice.IssueCommentEvent
			event.Action = "created"
			event.Repository.FullName = "assembledhq/assembled"
			event.Issue.Number = 54903
			event.Comment.Body = "@assembledhq/143-code-reviewer review again"
			event.Comment.User.Type = tt.authorType
			event.Comment.AuthorAssociation = tt.authorAssociation
			event.Comment.PerformedViaGitHubApp = tt.performedViaGitHubApp
			event.Sender.Type = tt.senderType
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", nil)
			rr := httptest.NewRecorder()

			ok, captured := handler.handleCodeReviewMentioned(rr, req, event, db.GitHubRepoOwner{
				OrgID: orgID, RepositoryID: repoID, FullName: "assembledhq/assembled", Status: "active",
			})

			require.True(t, ok, "untrusted mention should be ignored without failing the webhook")
			require.False(t, captured, "untrusted mention without dispute intake should not suppress ordinary feedback")
			require.Equal(t, 0, loader.number, "untrusted mention should not spend a GitHub API request")
			require.Empty(t, rr.Body.String(), "ignored mention should not write an error response")
		})
	}
}

type webhookCaptureDisputeStore struct {
	created []models.CodeReviewDispute
}

func (s *webhookCaptureDisputeStore) CreateAndEnqueueTriage(_ context.Context, dispute *models.CodeReviewDispute, _ db.CodeReviewDisputeIntakeGuard) (bool, error) {
	dispute.ID = uuid.New()
	s.created = append(s.created, *dispute)
	return true, nil
}

func (*webhookCaptureDisputeStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewDispute, error) {
	return models.CodeReviewDispute{}, pgx.ErrNoRows
}

func (*webhookCaptureDisputeStore) ListBySession(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, int) (models.CodeReviewDisputePage, error) {
	return models.CodeReviewDisputePage{}, nil
}

func (*webhookCaptureDisputeStore) ListQueue(context.Context, uuid.UUID, models.CodeReviewDisputeListFilters) (models.CodeReviewDisputePage, error) {
	return models.CodeReviewDisputePage{}, nil
}

func (*webhookCaptureDisputeStore) ListRecentKinds(context.Context, uuid.UUID, int) ([]string, error) {
	return nil, nil
}

func (*webhookCaptureDisputeStore) SetTriage(context.Context, uuid.UUID, uuid.UUID, models.CodeReviewDisputeTriageResult, bool, string) (models.CodeReviewDispute, error) {
	return models.CodeReviewDispute{}, nil
}

func (*webhookCaptureDisputeStore) FailTriage(context.Context, uuid.UUID, uuid.UUID, string, bool) error {
	return nil
}

func (*webhookCaptureDisputeStore) RecordAuthorization(context.Context, models.CodeReviewDisputeAuthorization) error {
	return nil
}

func (*webhookCaptureDisputeStore) AdmitAndEnqueueReassessment(context.Context, models.CodeReviewDispute, *uuid.UUID, string, time.Duration, int, any) (bool, error) {
	return false, nil
}

func (*webhookCaptureDisputeStore) CompleteReassessment(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.CodeReviewSessionStatus, *models.CodeReviewDecision, string) error {
	return nil
}

func (*webhookCaptureDisputeStore) MarkReassessmentFailed(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (*webhookCaptureDisputeStore) Escalate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) (models.CodeReviewDispute, error) {
	return models.CodeReviewDispute{}, nil
}

func (*webhookCaptureDisputeStore) Adjudicate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.CodeReviewDisputeAdjudicationUpdate) (models.CodeReviewDispute, error) {
	return models.CodeReviewDispute{}, nil
}

type webhookCaptureDisputeReviewStore struct {
	metadata models.CodeReviewSessionMetadata
	item     models.CodeReviewListItem
}

func (s webhookCaptureDisputeReviewStore) GetBySessionID(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	return s.metadata, nil
}

func (s webhookCaptureDisputeReviewStore) GetLatestCompletedByPullRequest(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	return s.metadata, nil
}

func (s webhookCaptureDisputeReviewStore) GetByGitHubFindingComment(context.Context, uuid.UUID, int64) (models.CodeReviewSessionMetadata, error) {
	return s.metadata, nil
}

func (s webhookCaptureDisputeReviewStore) GetListItemBySessionID(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewListItem, error) {
	return s.item, nil
}

func (webhookCaptureDisputeReviewStore) GetRiskReasonCodesBySession(context.Context, uuid.UUID, uuid.UUID) ([]models.CodeReviewRiskReasonCode, error) {
	return nil, nil
}

func (webhookCaptureDisputeReviewStore) ListFindings(context.Context, uuid.UUID, uuid.UUID, bool) ([]models.CodeReviewFinding, error) {
	return nil, nil
}

type webhookCaptureDisputePullRequestStore struct {
	pullRequest models.PullRequest
}

func (s webhookCaptureDisputePullRequestStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.PullRequest, error) {
	return s.pullRequest, nil
}

func (webhookCaptureDisputePullRequestStore) GetHealthCurrent(context.Context, uuid.UUID, uuid.UUID) (models.PullRequestHealthCurrent, error) {
	return models.PullRequestHealthCurrent{}, nil
}

func codeReviewWebhookPullRequestColumns() []string {
	return []string{
		"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
		"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
		"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
		"health_version", "merge_when_ready_state", "merge_when_ready_requested_by", "merge_when_ready_requested_at",
		"merge_when_ready_head_sha", "merge_when_ready_health_version", "merge_when_ready_error",
		"merge_when_ready_updated_at", "merged_at", "created_at", "updated_at",
	}
}

type stringPointerArg struct {
	value string
}

func (m stringPointerArg) Match(value any) bool {
	ptr, ok := value.(*string)
	return ok && ptr != nil && *ptr == m.value
}

type codeReviewWebhookPolicyStore struct {
	policyID uuid.UUID
	config   models.CodeReviewPolicyConfig
}

func (s *codeReviewWebhookPolicyStore) ResolvePolicy(context.Context, uuid.UUID) (models.CodeReviewResolvedPolicy, error) {
	record := models.CodeReviewPolicyRecord{
		ID:                 s.policyID,
		Version:            1,
		Enabled:            s.config.Enabled,
		ApprovalMode:       s.config.ApprovalMode,
		DescriptionPolicy:  s.config.DescriptionPolicy,
		RiskPolicy:         s.config.RiskPolicy,
		AgentRoster:        s.config.AgentRoster,
		InlineCommentLimit: s.config.InlineCommentLimit,
	}
	return models.CodeReviewResolvedPolicy{Config: s.config, Source: "organization", Policy: &record}, nil
}

func (s *codeReviewWebhookPolicyStore) SavePolicy(context.Context, uuid.UUID, models.CodeReviewPolicyConfig, *uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	return models.CodeReviewPolicyRecord{}, nil
}

type codeReviewWebhookMetadataStore struct {
	latest    models.CodeReviewSessionMetadata
	created   models.CodeReviewSessionMetadata
	submitted models.CodeReviewSessionMetadata
	approved  bool
}

func (s *codeReviewWebhookMetadataStore) CreateSessionMetadata(_ context.Context, metadata *models.CodeReviewSessionMetadata) error {
	metadata.ID = uuid.New()
	s.created = *metadata
	return nil
}

func (s *codeReviewWebhookMetadataStore) GetByOutputKey(context.Context, uuid.UUID, string) (models.CodeReviewSessionMetadata, error) {
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *codeReviewWebhookMetadataStore) GetBySessionID(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *codeReviewWebhookMetadataStore) GetLatestByPullRequestHead(context.Context, uuid.UUID, uuid.UUID, string, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *codeReviewWebhookMetadataStore) GetLatestByPullRequest(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	if s.latest.ID != uuid.Nil {
		return s.latest, nil
	}
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *codeReviewWebhookMetadataStore) GetLatestSubmittedByPullRequest(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	if s.submitted.ID != uuid.Nil {
		return s.submitted, nil
	}
	if s.latest.GitHubReviewID != nil {
		return s.latest, nil
	}
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *codeReviewWebhookMetadataStore) HasApprovedByPullRequest(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return s.approved, nil
}

func (s *codeReviewWebhookMetadataStore) FailReview(context.Context, uuid.UUID, uuid.UUID, string) (models.CodeReviewSessionMetadata, error) {
	return models.CodeReviewSessionMetadata{}, nil
}

func (s *codeReviewWebhookMetadataStore) FailReviewWithStatus(context.Context, uuid.UUID, db.FailCodeReviewParams) (models.CodeReviewSessionMetadata, error) {
	return models.CodeReviewSessionMetadata{}, nil
}

func (s *codeReviewWebhookMetadataStore) MarkSupersededBy(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	return models.CodeReviewSessionMetadata{}, nil
}

func (s *codeReviewWebhookMetadataStore) MarkStaleForPullRequestExceptHead(context.Context, uuid.UUID, uuid.UUID, string, *uuid.UUID) (int64, error) {
	return 0, nil
}

type codeReviewWebhookSessionStore struct {
	getResult   models.Session
	createCalls int
}

func (s *codeReviewWebhookSessionStore) Create(_ context.Context, session *models.Session) error {
	s.createCalls++
	session.ID = uuid.New()
	return nil
}

func (s *codeReviewWebhookSessionStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	return s.getResult, nil
}

func (s *codeReviewWebhookSessionStore) UpdateStatus(context.Context, uuid.UUID, uuid.UUID, models.SessionStatus) error {
	return nil
}

func (s *codeReviewWebhookSessionStore) UpdateFailure(context.Context, uuid.UUID, uuid.UUID, string, string, []string, bool) error {
	return nil
}

type codeReviewWebhookJobStore struct {
	jobID                uuid.UUID
	payload              codereviewsvc.RunCodeReviewJobPayload
	reassessmentPayload  codereviewsvc.ReviewChangedInput
	reassessmentPayloads []codereviewsvc.ReviewChangedInput
}

func (s *codeReviewWebhookJobStore) EnqueueWithOpts(_ context.Context, _ uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	typed, ok := opts.Payload.(codereviewsvc.RunCodeReviewJobPayload)
	if ok {
		s.payload = typed
	}
	changed, ok := opts.Payload.(codereviewsvc.ReviewChangedInput)
	if ok {
		s.reassessmentPayload = changed
		s.reassessmentPayloads = append(s.reassessmentPayloads, changed)
	}
	return s.jobID, nil
}

func (s *codeReviewWebhookJobStore) HasActiveByDedupeKey(context.Context, uuid.UUID, string, string) (bool, error) {
	return true, nil
}
