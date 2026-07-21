package handlers

import (
	"context"
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
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	prService := ghservice.NewPRService(nil, db.NewPullRequestStore(mock), nil, nil, nil, nil, nil, zerolog.Nop())
	handler := NewWebhookHandler(&config.Config{}, db.NewOrganizationStore(mock), db.NewUserStore(mock), db.NewRepositoryStore(mock), db.NewIntegrationStore(mock), prService)

	mock.ExpectQuery("SELECT r.id AS repository_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"repository_id", "org_id", "org_name", "github_id", "full_name", "status"}).
			AddRow(repoID, orgID, "Owning Org", int64(1001), "assembledhq/143", "active"))
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
	rr := httptest.NewRecorder()

	handler.handlePullRequest(rr, req, body)

	require.Equal(t, http.StatusOK, rr.Code, "pull request webhook should be acknowledged")
	require.Contains(t, rr.Body.String(), "processed", "pull request webhook should be processed")
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
	require.NoError(t, mock.ExpectationsWereMet(), "existing pull request mirror should be refreshed from the webhook payload")
}

func TestWebhook_ReassessesRequestedCodeReviewAfterPullRequestEdit(t *testing.T) {
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
		HeadSHA: "head-sha", TriggerSource: models.CodeReviewTriggerSourceTeamReviewer,
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
	oldHead := "head-sha"
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
		"action":"edited",
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

	require.NoError(t, err, "edited pull request should trigger code review reassessment")
	require.Equal(t, prID, jobs.reassessmentPayload.PullRequestID, "reassessment should target the reviewed pull request")
	require.Equal(t, priorSessionID, jobs.reassessmentPayload.PriorSessionID, "queued reassessment should remain ordered behind the assessment active when the event arrived")
	require.Equal(t, "head-sha", jobs.reassessmentPayload.HeadSHA, "queued reassessment should capture the current PR head")
	require.Equal(t, "pull_request:delivery-143", jobs.reassessmentPayload.ChangeKey, "queued reassessment should retain the delivery idempotency key")
	require.Equal(t, 0, sessions.createCalls, "webhook should defer session creation to the durable starter job")
	require.NoError(t, mock.ExpectationsWereMet(), "pull request mirror refresh should be org-scoped")
}

func TestWebhook_CodeReviewReassessmentIgnores143ReviewWrites(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()
	handler := &WebhookHandler{pullRequests: db.NewPullRequestStore(mock), codeReviews: &codereviewsvc.Service{}}
	body := []byte(`{
		"action":"submitted","repository":{"full_name":"assembledhq/143"},"pull_request":{"number":42},
		"review":{"body":"Updated assessment\n\n<!-- 143-code-review-output:abc -->"}
	}`)

	err = handler.reassessCodeReviewsForGitHubEvent(context.Background(), db.GitHubRepoOwner{
		OrgID: uuid.New(), RepositoryID: uuid.New(), FullName: "assembledhq/143", Status: "active",
	}, "pull_request_review", body, "delivery-self")

	require.NoError(t, err, "143-authored review webhook should be ignored without a feedback loop")
	require.NoError(t, mock.ExpectationsWereMet(), "self-authored review should not query pull request state")
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
		{name: "description edit", eventType: "pull_request", action: "edited", expected: true},
		{name: "human review", eventType: "pull_request_review", action: "submitted", expected: true},
		{name: "review dismissal", eventType: "pull_request_review", action: "dismissed", expected: true},
		{name: "inline review edit", eventType: "pull_request_review_comment", action: "edited", expected: true},
		{name: "thread resolution", eventType: "pull_request_review_thread", action: "resolved", expected: true},
		{name: "checks complete", eventType: "check_suite", action: "completed", expected: true},
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
}

func (s *codeReviewWebhookMetadataStore) CreateSessionMetadata(_ context.Context, metadata *models.CodeReviewSessionMetadata) error {
	metadata.ID = uuid.New()
	s.created = *metadata
	return nil
}

func (s *codeReviewWebhookMetadataStore) GetByOutputKey(context.Context, uuid.UUID, string) (models.CodeReviewSessionMetadata, error) {
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
	return false, nil
}

func (s *codeReviewWebhookMetadataStore) FailReview(context.Context, uuid.UUID, uuid.UUID, string) (models.CodeReviewSessionMetadata, error) {
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
	jobID               uuid.UUID
	payload             codereviewsvc.RunCodeReviewJobPayload
	reassessmentPayload codereviewsvc.ReviewChangedInput
}

func (s *codeReviewWebhookJobStore) EnqueueWithOpts(_ context.Context, _ uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	typed, ok := opts.Payload.(codereviewsvc.RunCodeReviewJobPayload)
	if ok {
		s.payload = typed
	}
	changed, ok := opts.Payload.(codereviewsvc.ReviewChangedInput)
	if ok {
		s.reassessmentPayload = changed
	}
	return s.jobID, nil
}

func (s *codeReviewWebhookJobStore) HasActiveByDedupeKey(context.Context, uuid.UUID, string, string) (bool, error) {
	return true, nil
}
