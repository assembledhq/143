package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	automationservice "github.com/assembledhq/143/internal/services/automations"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// mockWebhookSecretLookup implements webhookSecretLookup for testing.
type mockWebhookSecretLookup struct {
	cred *models.DecryptedCredential
	err  error
}

func (m *mockWebhookSecretLookup) Get(_ context.Context, _ uuid.UUID, _ models.ProviderName) (*models.DecryptedCredential, error) {
	return m.cred, m.err
}

type recordingLinearAutomationTriggerer struct {
	requests []automationservice.LinearIssueEventTriggerRequest
	err      error
}

func (r *recordingLinearAutomationTriggerer) TriggerLinearIssueEvent(_ context.Context, req automationservice.LinearIssueEventTriggerRequest) error {
	r.requests = append(r.requests, req)
	return r.err
}

func setupIngestionHandler(t *testing.T, mock pgxmock.PgxPoolIface, credStore webhookSecretLookup) *IngestionWebhookHandler {
	t.Helper()
	webhookStore := db.NewWebhookDeliveryStore(mock)
	integrationStore := db.NewIntegrationStore(mock)
	issueStore := db.NewIssueStore(mock)
	jobStore := db.NewJobStore(mock)
	svc := ingestion.NewService(issueStore, webhookStore, jobStore, zerolog.Nop())
	return NewIngestionWebhookHandler(webhookStore, integrationStore, credStore, svc, zerolog.Nop())
}

// signBody computes HMAC-SHA256 of body with the given secret and returns the
// hex-encoded signature.
func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func signedLinearIssueWebhookBody(ts time.Time) string {
	return fmt.Sprintf(`{"webhookTimestamp":%d,"action":"create","type":"Issue","data":{"id":"LIN-1","title":"Bug","priority":1,"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-02T00:00:00Z"}}`, ts.UnixMilli())
}

func TestIngestionWebhook_HandleSentry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		url          string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface)
		expectedCode int
		expectedBody string
	}{
		{
			name: "processes actionable sentry event successfully",
			url:  "", // will be set with a valid integration_id
			body: `{"action":"created","data":{"issue":{"id":"123","title":"Test Error","metadata":{"value":"desc"},"count":"5","userCount":2,"level":"error","firstSeen":"2024-01-01T00:00:00Z","lastSeen":"2024-01-02T00:00:00Z"}}}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				integrationID := uuid.New()
				orgID := uuid.New()
				now := time.Now()

				// 1. GetByID integration
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
							AddRow(integrationID, orgID, "sentry", json.RawMessage(`{}`), "active", nil, now),
					)

				// 2. Create webhook delivery (9 named args)
				mock.ExpectQuery("INSERT INTO webhook_deliveries").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "received_at", "created_at"}).
							AddRow(uuid.New(), now, now),
					)

				// 3. Upsert issue (16 named args)
				mock.ExpectQuery("INSERT INTO issues").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
							AddRow(uuid.New(), now, now),
					)

				// 4. Enqueue job (6 named args)
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(
						pgxmock.NewRows([]string{"id"}).
							AddRow(uuid.New()),
					)

				// 5. MarkProcessed (4 named args)
				mock.ExpectExec("UPDATE webhook_deliveries").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			expectedCode: http.StatusOK,
			expectedBody: "processed",
		},
		{
			name:         "returns bad request when integration_id is missing",
			url:          "/api/v1/webhooks/sentry",
			body:         `{}`,
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_INTEGRATION",
		},
		{
			name:         "returns bad request when integration_id is not a valid UUID",
			url:          "/api/v1/webhooks/sentry?integration_id=not-a-uuid",
			body:         `{}`,
			setupMock:    func(mock pgxmock.PgxPoolIface) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
		{
			name: "returns not found when integration does not exist",
			url:  "", // will be set with a valid integration_id
			body: `{}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}),
					)
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "NOT_FOUND",
		},
		{
			name: "returns OK with ignored status for non-actionable event",
			url:  "", // will be set with a valid integration_id
			body: `{"action":"resolved","data":{"issue":{"id":"123","title":"Test Error","metadata":{"value":"desc"},"count":"5","userCount":2,"level":"error","firstSeen":"2024-01-01T00:00:00Z","lastSeen":"2024-01-02T00:00:00Z"}}}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				integrationID := uuid.New()
				orgID := uuid.New()
				now := time.Now()

				// 1. GetByID integration
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
							AddRow(integrationID, orgID, "sentry", json.RawMessage(`{}`), "active", nil, now),
					)

				// 2. Create webhook delivery (9 named args)
				mock.ExpectQuery("INSERT INTO webhook_deliveries").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "received_at", "created_at"}).
							AddRow(uuid.New(), now, now),
					)

				// 3. MarkProcessed (4 named args)
				mock.ExpectExec("UPDATE webhook_deliveries").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			expectedCode: http.StatusOK,
			expectedBody: "ignored",
		},
		{
			name: "returns bad request when actionable event has parse failure",
			url:  "", // will be set with a valid integration_id
			body: `{"action":"created","data":{"issue":{"id":"","title":"Test Error"}}}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				integrationID := uuid.New()
				orgID := uuid.New()
				now := time.Now()

				// 1. GetByID integration
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
							AddRow(integrationID, orgID, "sentry", json.RawMessage(`{}`), "active", nil, now),
					)

				// 2. Create webhook delivery (9 named args)
				mock.ExpectQuery("INSERT INTO webhook_deliveries").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "received_at", "created_at"}).
							AddRow(uuid.New(), now, now),
					)

				// 3. MarkProcessed with error (4 named args)
				mock.ExpectExec("UPDATE webhook_deliveries").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "PARSE_FAILED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			// No credential configured — skips signature verification
			handler := setupIngestionHandler(t, mock, nil)
			tt.setupMock(mock)

			url := tt.url
			if url == "" {
				url = "/api/v1/webhooks/sentry?integration_id=" + uuid.New().String()
			}

			req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(tt.body))
			w := httptest.NewRecorder()

			handler.HandleSentry(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestIngestionWebhook_HandleLinear_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	handler := setupIngestionHandler(t, mock, nil)
	triggerer := &recordingLinearAutomationTriggerer{}
	handler.SetLinearAutomationTriggerer(triggerer)

	integrationID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	// 1. GetByID integration
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(integrationID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now),
		)

	// 2. Create webhook delivery (9 named args)
	mock.ExpectQuery("INSERT INTO webhook_deliveries").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "received_at", "created_at"}).
				AddRow(uuid.New(), now, now),
		)

	// 3. Upsert issue (16 named args)
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(uuid.New(), now, now),
		)

	// 4. Enqueue job (6 named args)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).
				AddRow(uuid.New()),
		)

	// 5. MarkProcessed (4 named args)
	mock.ExpectExec("UPDATE webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	body := `{"action":"create","type":"Issue","data":{"id":"LIN-1","title":"Bug","priority":1,"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-02T00:00:00Z"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleLinear(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK for processed linear webhook")
	require.Contains(t, w.Body.String(), "processed", "response should contain processed status")
	require.Len(t, triggerer.requests, 1, "linear issue create should dispatch one automation trigger request")
	triggerRequest := triggerer.requests[0]
	require.Equal(t, orgID, triggerRequest.OrgID, "linear automation trigger should preserve org scope")
	require.Equal(t, models.LinearAutomationEventIssueCreated, triggerRequest.EventType, "linear create action should dispatch issue.created")
	require.Equal(t, "linear:create:LIN-1:2024-01-02T00:00:00Z", triggerRequest.ProviderEventID, "linear automation trigger should use a deterministic provider event id")
	require.Equal(t, "LIN-1", triggerRequest.Issue.ID, "linear automation trigger should preserve issue id")
	require.Equal(t, "Bug", triggerRequest.Issue.Title, "linear automation trigger should preserve issue title")
	require.Equal(t, 1, triggerRequest.Issue.Priority, "linear automation trigger should preserve issue priority")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestIngestionWebhook_HandleLinear_MultiTenantWorkspaceRouting pins the SaaS
// routing contract: a single Linear OAuth app installed across many workspaces
// shares one webhook URL with no per-install query param. The handler must
// resolve the owning 143 org by matching the payload's `organizationId` against
// integrations.config->>'workspace_id', then verify the HMAC against that
// org's secret. Without this path, every install would need its own webhook
// URL — which Linear's OAuth app model doesn't allow.
func TestIngestionWebhook_HandleLinear_MultiTenantWorkspaceRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           string
		workspaceID    string
		integrationCfg string
		lookupErr      error
		expectStatus   int
		expectInBody   string
	}{
		{
			name:           "resolves integration via payload workspace id",
			body:           `{"organizationId":"lin-org-A","action":"create","type":"Issue","data":{"id":"LIN-1","title":"Bug","priority":1,"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-02T00:00:00Z"}}`,
			workspaceID:    "lin-org-A",
			integrationCfg: `{"workspace_id":"lin-org-A"}`,
			expectStatus:   http.StatusOK,
			expectInBody:   "processed",
		},
		{
			name:         "rejects with 400 when payload has no organizationId and no query param",
			body:         `{"action":"create","type":"Issue","data":{"id":"LIN-1"}}`,
			expectStatus: http.StatusBadRequest,
			expectInBody: "MISSING_INTEGRATION",
		},
		{
			name:         "rejects with 401 when workspace id has no active integration",
			body:         `{"organizationId":"lin-org-unknown","action":"create","type":"Issue"}`,
			workspaceID:  "lin-org-unknown",
			lookupErr:    pgx.ErrNoRows,
			expectStatus: http.StatusUnauthorized,
			expectInBody: "UNAUTHORIZED",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			handler := setupIngestionHandler(t, mock, nil)
			integrationID := uuid.New()
			orgID := uuid.New()
			now := time.Now()

			if tt.workspaceID != "" {
				// Workspace-id lookup query. On lookupErr cases this is the
				// only DB call we expect — the handler rejects before any
				// downstream work fires.
				if tt.lookupErr != nil {
					mock.ExpectQuery("SELECT .+ FROM integrations .+workspace_id").
						WithArgs(pgxmock.AnyArg()).
						WillReturnError(tt.lookupErr)
				} else {
					mock.ExpectQuery("SELECT .+ FROM integrations .+workspace_id").
						WithArgs(pgxmock.AnyArg()).
						WillReturnRows(
							pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
								AddRow(integrationID, orgID, "linear", json.RawMessage(tt.integrationCfg), "active", nil, now),
						)
				}
			}

			if tt.expectStatus == http.StatusOK {
				// Full happy-path expectations identical to the
				// integration_id-based test, just downstream of the new
				// resolver. Confirms the resolved integration flows through
				// the existing ingestion adapter unchanged.
				mock.ExpectQuery("INSERT INTO webhook_deliveries").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(pgxmock.NewRows([]string{"id", "received_at", "created_at"}).AddRow(uuid.New(), now, now))
				mock.ExpectQuery("INSERT INTO issues").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectExec("UPDATE webhook_deliveries").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}

			// No integration_id query param — that's the whole point.
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear", strings.NewReader(tt.body))
			w := httptest.NewRecorder()

			handler.HandleLinear(w, req)

			require.Equal(t, tt.expectStatus, w.Code, "status code should match expected resolver outcome")
			require.Contains(t, w.Body.String(), tt.expectInBody)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestIngestionWebhook_HandleLinearAgentEnqueueFailureDoesNotMarkProcessed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	handler := setupIngestionHandler(t, mock, nil)
	orgID := uuid.New()
	integrationID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()
	enabled := true
	handler.SetLinearAgentDispatcher(&LinearAgentDispatcher{
		logger:        zerolog.Nop(),
		agentSessions: db.NewLinearAgentSessionStore(mock),
		jobs:          &fakeJobs{err: errors.New("job store down")},
		settingsLoader: func(context.Context, uuid.UUID) (models.LinearAgentSettings, error) {
			return models.LinearAgentSettings{Enabled: &enabled}, nil
		},
		featureEnabled: true,
	})

	mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(integrationID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now),
		)
	mock.ExpectQuery("INSERT INTO webhook_deliveries").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "received_at", "created_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery("INSERT INTO linear_agent_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at", "inserted",
		}).AddRow(
			rowID, orgID, integrationID, "as_enqueue_fail",
			"iss_1", "ACS-1",
			"", "",
			nil, "pending", &now,
			now, now, true,
		))

	body := `{"type":"AgentSessionEvent","action":"created","payload":{"agentSession":{"id":"as_enqueue_fail","issueId":"iss_1","issue":{"id":"iss_1","identifier":"ACS-1","teamId":"team_1"}}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
	req.Header.Set("Linear-Event", "AgentSessionEvent")
	w := httptest.NewRecorder()

	handler.HandleLinear(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "enqueue failures should ask Linear to retry instead of marking the delivery processed")
	require.Contains(t, w.Body.String(), "DISPATCH_FAILED", "response should identify the dispatch failure")
	require.NoError(t, mock.ExpectationsWereMet(), "delivery must remain unprocessed so a replay can retry the enqueue")
}

// TestIngestionWebhook_HandleLinear_QueryParamStillWorks pins the
// backward-compat contract for self-hosted installs that paste a
// `?integration_id=<uuid>` URL into Linear. The new workspace-id resolver
// must not regress this path — query param is always honored when present
// and takes priority over body sniffing.
func TestIngestionWebhook_HandleLinear_QueryParamStillWorks(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := setupIngestionHandler(t, mock, nil)
	integrationID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(integrationID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now),
		)
	mock.ExpectQuery("INSERT INTO webhook_deliveries").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "received_at", "created_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectExec("UPDATE webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Body has a *different* workspace id than what the query param points
	// to. The query param must win; if the resolver ever started preferring
	// the body sniff, this test would direct the delivery to the wrong org
	// — that's the regression we're guarding.
	body := `{"organizationId":"lin-org-DIFFERENT","action":"create","type":"Issue","data":{"id":"LIN-1","title":"Bug","priority":1,"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-02T00:00:00Z"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleLinear(w, req)

	require.Equal(t, http.StatusOK, w.Code, "query-param routing must continue to work for self-hosted installs")
	require.Contains(t, w.Body.String(), "processed")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestIngestionWebhook_HandleLinear_RejectsDisconnectedIntegration pins the
// rule that a stale ?integration_id=<uuid> URL pointing at a non-active
// integration must be rejected at the resolver, before any HMAC verify or
// downstream dispatch. Without this guard, a disconnected integration whose
// signing secret has rotated could still spawn agent sessions or webhook
// deliveries via a self-hosted-style URL.
func TestIngestionWebhook_HandleLinear_RejectsDisconnectedIntegration(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"inactive", "error"} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			handler := setupIngestionHandler(t, mock, nil)
			integrationID := uuid.New()
			orgID := uuid.New()
			now := time.Now()

			mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
						AddRow(integrationID, orgID, "linear", json.RawMessage(`{}`), status, nil, now),
				)

			body := `{"action":"create","type":"Issue","data":{"id":"LIN-1","title":"Bug","priority":1,"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-02T00:00:00Z"}}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
			w := httptest.NewRecorder()

			handler.HandleLinear(w, req)

			require.Equal(t, http.StatusUnauthorized, w.Code, "disconnected integration must produce 401, not silently proceed")
			require.NoError(t, mock.ExpectationsWereMet(), "no downstream calls should have been made after rejection")
		})
	}
}

func TestIngestionWebhook_SignatureVerification(t *testing.T) {
	t.Parallel()

	secret := "test-webhook-secret"
	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	// Helper to set up integration lookup mock
	setupIntegrationMock := func(mock pgxmock.PgxPoolIface) {
		mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
					AddRow(integrationID, orgID, "sentry", json.RawMessage(`{}`), "active", nil, now),
			)
	}

	// Helper to set up full processing mocks (integration + delivery + issue + job + mark)
	setupFullProcessingMock := func(mock pgxmock.PgxPoolIface) {
		setupIntegrationMock(mock)

		// Create webhook delivery (9 named args)
		mock.ExpectQuery("INSERT INTO webhook_deliveries").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "received_at", "created_at"}).
					AddRow(uuid.New(), now, now),
			)

		// Upsert issue (16 named args)
		mock.ExpectQuery("INSERT INTO issues").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
					AddRow(uuid.New(), now, now),
			)

		// Enqueue job (6 named args)
		mock.ExpectQuery("INSERT INTO jobs").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(
				pgxmock.NewRows([]string{"id"}).
					AddRow(uuid.New()),
			)

		// MarkProcessed (4 named args)
		mock.ExpectExec("UPDATE webhook_deliveries").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}

	sentryBody := `{"action":"created","data":{"issue":{"id":"123","title":"Test Error","metadata":{"value":"desc"},"count":"5","userCount":2,"level":"error","firstSeen":"2024-01-01T00:00:00Z","lastSeen":"2024-01-02T00:00:00Z"}}}`

	t.Run("valid signature with per-org secret passes", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		credMock := &mockWebhookSecretLookup{
			cred: &models.DecryptedCredential{
				ID:       uuid.New(),
				OrgID:    orgID,
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{WebhookSecret: secret},
				Status:   "active",
			},
		}
		handler := setupIngestionHandler(t, mock, credMock)
		setupFullProcessingMock(mock)

		sig := signBody(secret, []byte(sentryBody))
		url := "/api/v1/webhooks/sentry?integration_id=" + integrationID.String()
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(sentryBody))
		req.Header.Set("X-Sentry-Hook-Signature", sig)
		w := httptest.NewRecorder()

		handler.HandleSentry(w, req)
		require.Equal(t, http.StatusOK, w.Code, "valid signature should pass")
		require.Contains(t, w.Body.String(), "processed")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("invalid signature with per-org secret returns 401", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		credMock := &mockWebhookSecretLookup{
			cred: &models.DecryptedCredential{
				ID:       uuid.New(),
				OrgID:    orgID,
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{WebhookSecret: secret},
				Status:   "active",
			},
		}
		handler := setupIngestionHandler(t, mock, credMock)
		setupIntegrationMock(mock)

		url := "/api/v1/webhooks/sentry?integration_id=" + integrationID.String()
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(sentryBody))
		req.Header.Set("X-Sentry-Hook-Signature", "bad-signature")
		w := httptest.NewRecorder()

		handler.HandleSentry(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "invalid signature should return 401")
		require.Contains(t, w.Body.String(), "UNAUTHORIZED")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("missing signature header with per-org secret returns 401", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		credMock := &mockWebhookSecretLookup{
			cred: &models.DecryptedCredential{
				ID:       uuid.New(),
				OrgID:    orgID,
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{WebhookSecret: secret},
				Status:   "active",
			},
		}
		handler := setupIngestionHandler(t, mock, credMock)
		setupIntegrationMock(mock)

		url := "/api/v1/webhooks/sentry?integration_id=" + integrationID.String()
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(sentryBody))
		// No signature header set
		w := httptest.NewRecorder()

		handler.HandleSentry(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "missing signature should return 401")
		require.Contains(t, w.Body.String(), "UNAUTHORIZED")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no credential configured skips verification", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// Credential lookup returns error (not found)
		credMock := &mockWebhookSecretLookup{
			err: fmt.Errorf("get sentry credential: %w", pgx.ErrNoRows),
		}
		handler := setupIngestionHandler(t, mock, credMock)
		setupFullProcessingMock(mock)

		url := "/api/v1/webhooks/sentry?integration_id=" + integrationID.String()
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(sentryBody))
		// No signature header — but should still pass because no credential
		w := httptest.NewRecorder()

		handler.HandleSentry(w, req)
		require.Equal(t, http.StatusOK, w.Code, "should skip verification when no credential configured")
		require.Contains(t, w.Body.String(), "processed")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("credential lookup operational error rejects request", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		credMock := &mockWebhookSecretLookup{
			err: fmt.Errorf("get sentry credential: %w", context.DeadlineExceeded),
		}
		handler := setupIngestionHandler(t, mock, credMock)
		setupIntegrationMock(mock)

		url := "/api/v1/webhooks/sentry?integration_id=" + integrationID.String()
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(sentryBody))
		w := httptest.NewRecorder()

		handler.HandleSentry(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "operational credential errors should reject webhook requests")
		require.Contains(t, w.Body.String(), "UNAUTHORIZED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestIngestionWebhook_HandleLinear_UsesDocumentedSignatureHeader(t *testing.T) {
	t.Parallel()

	secret := "linear-webhook-secret"
	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()
	body := signedLinearIssueWebhookBody(time.Now())

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	credMock := &mockWebhookSecretLookup{
		cred: &models.DecryptedCredential{
			ID:       uuid.New(),
			OrgID:    orgID,
			Provider: models.ProviderLinear,
			Config:   models.LinearConfig{WebhookSecret: secret},
			Status:   "active",
		},
	}
	handler := setupIngestionHandler(t, mock, credMock)

	mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(integrationID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now),
		)
	mock.ExpectQuery("INSERT INTO webhook_deliveries").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "received_at", "created_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectExec("UPDATE webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
	req.Header.Set("Linear-Signature", signBody(secret, []byte(body)))
	w := httptest.NewRecorder()

	handler.HandleLinear(w, req)

	require.Equal(t, http.StatusOK, w.Code, "documented Linear-Signature header should verify successfully")
	require.Contains(t, w.Body.String(), "processed", "verified Linear webhook should process normally")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIngestionWebhook_HandleLinear_AcceptsDelayedSignedWebhookRetry(t *testing.T) {
	t.Parallel()

	secret := "linear-webhook-secret"
	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()
	body := signedLinearIssueWebhookBody(time.Now().Add(-2 * time.Minute))

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	credMock := &mockWebhookSecretLookup{
		cred: &models.DecryptedCredential{
			ID:       uuid.New(),
			OrgID:    orgID,
			Provider: models.ProviderLinear,
			Config:   models.LinearConfig{WebhookSecret: secret},
			Status:   "active",
		},
	}
	handler := setupIngestionHandler(t, mock, credMock)

	mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
				AddRow(integrationID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now),
		)
	mock.ExpectQuery("INSERT INTO webhook_deliveries").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "received_at", "created_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectExec("UPDATE webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
	req.Header.Set("Linear-Signature", signBody(secret, []byte(body)))
	w := httptest.NewRecorder()

	handler.HandleLinear(w, req)

	require.Equal(t, http.StatusOK, w.Code, "delayed signed Linear retries should pass signature verification")
	require.Contains(t, w.Body.String(), "processed", "delayed signed Linear retry should reach normal delivery processing")
	require.NoError(t, mock.ExpectationsWereMet(), "delayed webhook should be recorded and processed normally")
}

// TestIngestionWebhook_GlobalLinearSecretFallback pins the SaaS-deployment
// contract: when no per-org Linear webhook secret is configured, the
// signature verifier falls back to the shared LINEAR_WEBHOOK_SIGNING_SECRET
// — the per-OAuth-app secret Linear shows once at app-creation time. Without
// this fallback, the multi-tenant deployment can't verify any inbound
// webhook because there's no install-time UI for entering the per-app
// secret.
//
// The fallback is Linear-specific by design: Sentry's OAuth flow gives each
// install its own webhook secret, so applying a process-wide override to
// Sentry would be a regression.
func TestIngestionWebhook_GlobalLinearSecretFallback(t *testing.T) {
	t.Parallel()

	globalSecret := "global-linear-app-secret"
	perOrgSecret := "per-org-override"
	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()
	body := signedLinearIssueWebhookBody(time.Now())

	// Helpers — every subtest exercises the same downstream path once the
	// signature check resolves, so the DB expectations are shared.
	expectIntegrationLookup := func(mock pgxmock.PgxPoolIface) {
		mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
					AddRow(integrationID, orgID, "linear", json.RawMessage(`{}`), "active", nil, now),
			)
	}
	expectProcessed := func(mock pgxmock.PgxPoolIface) {
		mock.ExpectQuery("INSERT INTO webhook_deliveries").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id", "received_at", "created_at"}).AddRow(uuid.New(), now, now))
		mock.ExpectQuery("INSERT INTO issues").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
		mock.ExpectQuery("INSERT INTO jobs").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
		mock.ExpectExec("UPDATE webhook_deliveries").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}

	t.Run("global secret verifies when per-org credential missing", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// credStore returns ErrNoRows — the SaaS case where the org never
		// pasted its own secret because there isn't one to paste.
		credMock := &mockWebhookSecretLookup{err: fmt.Errorf("get linear credential: %w", pgx.ErrNoRows)}
		handler := setupIngestionHandler(t, mock, credMock)
		handler.SetGlobalLinearWebhookSecret(globalSecret)
		handler.SetRequireSecret(true)

		expectIntegrationLookup(mock)
		expectProcessed(mock)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
		req.Header.Set("Linear-Signature", signBody(globalSecret, []byte(body)))
		w := httptest.NewRecorder()

		handler.HandleLinear(w, req)
		require.Equal(t, http.StatusOK, w.Code, "global secret must verify when no per-org credential exists")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("global secret verifies when per-org credential has empty webhook_secret", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// Per-org row exists (Linear OAuth completed) but has no
		// WebhookSecret — exactly the state after an OAuth callback in a
		// SaaS deployment, since the OAuth code never has access to the
		// app-level signing secret.
		credMock := &mockWebhookSecretLookup{
			cred: &models.DecryptedCredential{
				ID: uuid.New(), OrgID: orgID, Provider: models.ProviderLinear,
				Config: models.LinearConfig{WebhookSecret: ""}, Status: "active",
			},
		}
		handler := setupIngestionHandler(t, mock, credMock)
		handler.SetGlobalLinearWebhookSecret(globalSecret)
		handler.SetRequireSecret(true)

		expectIntegrationLookup(mock)
		expectProcessed(mock)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
		req.Header.Set("Linear-Signature", signBody(globalSecret, []byte(body)))
		w := httptest.NewRecorder()

		handler.HandleLinear(w, req)
		require.Equal(t, http.StatusOK, w.Code, "global secret must fill in when per-org override is empty")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("per-org secret wins when both are set", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		credMock := &mockWebhookSecretLookup{
			cred: &models.DecryptedCredential{
				ID: uuid.New(), OrgID: orgID, Provider: models.ProviderLinear,
				Config: models.LinearConfig{WebhookSecret: perOrgSecret}, Status: "active",
			},
		}
		handler := setupIngestionHandler(t, mock, credMock)
		handler.SetGlobalLinearWebhookSecret(globalSecret)
		handler.SetRequireSecret(true)

		expectIntegrationLookup(mock)
		expectProcessed(mock)

		// Sign with the *per-org* secret. If precedence ever flipped this
		// would fail with 401 because the global secret would be used for
		// verification.
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
		req.Header.Set("Linear-Signature", signBody(perOrgSecret, []byte(body)))
		w := httptest.NewRecorder()

		handler.HandleLinear(w, req)
		require.Equal(t, http.StatusOK, w.Code, "per-org override must take precedence over the global secret")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no per-org and no global with requireSecret=true returns 401", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		credMock := &mockWebhookSecretLookup{err: fmt.Errorf("get linear credential: %w", pgx.ErrNoRows)}
		handler := setupIngestionHandler(t, mock, credMock)
		// Global left unset.
		handler.SetRequireSecret(true)

		expectIntegrationLookup(mock)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/linear?integration_id="+integrationID.String(), strings.NewReader(body))
		req.Header.Set("Linear-Signature", signBody(globalSecret, []byte(body)))
		w := httptest.NewRecorder()

		handler.HandleLinear(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "no secret + requireSecret=true must reject")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("global secret does NOT apply to Sentry", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// Sentry credential has no webhook_secret. The Linear global
		// fallback must not leak into Sentry verification — Sentry's
		// per-install secret is the right thing there, and a
		// process-wide fallback would silently authorize forged Sentry
		// webhooks if the Linear secret leaked.
		credMock := &mockWebhookSecretLookup{
			cred: &models.DecryptedCredential{
				ID: uuid.New(), OrgID: orgID, Provider: models.ProviderSentry,
				Config: models.SentryConfig{WebhookSecret: ""}, Status: "active",
			},
		}
		handler := setupIngestionHandler(t, mock, credMock)
		handler.SetGlobalLinearWebhookSecret(globalSecret)
		handler.SetRequireSecret(true)

		mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
					AddRow(integrationID, orgID, "sentry", json.RawMessage(`{}`), "active", nil, now),
			)

		sentryBody := `{"action":"created","data":{}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/sentry?integration_id="+integrationID.String(), strings.NewReader(sentryBody))
		req.Header.Set("X-Sentry-Hook-Signature", signBody(globalSecret, []byte(sentryBody)))
		w := httptest.NewRecorder()

		handler.HandleSentry(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "global Linear secret must not be applied to Sentry verification")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestLinearIssueAutomationTriggerRequestFromCreatePayload(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	deliveryID := "linear-delivery-123"
	body := []byte(`{
		"action":"create",
		"type":"Issue",
		"data":{
			"id":"lin-1",
			"identifier":"ENG-123",
			"title":"Checkout button fails",
			"description":"The primary checkout button fails.",
			"url":"https://linear.app/acme/issue/ENG-123",
			"priority":1,
			"state":{"name":"Triage","type":"triage"},
			"labels":[{"name":"bug"},{"name":"checkout"}],
			"issueType":{"name":"Bug"},
			"createdAt":"2026-06-25T12:00:00Z",
			"updatedAt":"2026-06-25T12:01:00Z",
			"team":{"id":"team-1","key":"ENG","name":"Engineering"}
		}
	}`)

	req, ok, err := linearIssueAutomationTriggerRequest(orgID, &models.WebhookDelivery{DeliveryID: &deliveryID}, body)

	require.NoError(t, err, "Linear issue create payload should convert into an automation trigger request")
	require.True(t, ok, "Linear issue create payload should be automation-triggerable")
	require.Equal(t, orgID, req.OrgID, "trigger request should preserve org scope")
	require.Equal(t, models.LinearAutomationEventIssueCreated, req.EventType, "create action should map to issue.created")
	require.Equal(t, deliveryID, req.ProviderEventID, "trigger request should use Linear delivery id for idempotency")
	require.NotNil(t, req.OccurredAt, "trigger request should keep the event timestamp")
	require.Equal(t, "ENG-123", req.Issue.Identifier, "trigger request should preserve the Linear issue identifier")
	require.Equal(t, "Bug", req.Issue.IssueType, "trigger request should preserve issue type")
	require.Equal(t, []string{"bug", "checkout"}, req.Issue.Labels, "trigger request should preserve labels for filtering")
	require.Equal(t, "urgent", req.Issue.PriorityName, "trigger request should map Linear priority numbers to names")
	require.Equal(t, "ENG", req.Issue.TeamKey, "trigger request should preserve team key for filtering")
}
