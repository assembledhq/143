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
	body := `{"action":"create","type":"Issue","data":{"id":"LIN-1","title":"Bug","priority":1,"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-02T00:00:00Z"}}`

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
