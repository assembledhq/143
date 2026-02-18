package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func setupIngestionHandler(t *testing.T, mock pgxmock.PgxPoolIface) *IngestionWebhookHandler {
	t.Helper()
	webhookStore := db.NewWebhookDeliveryStore(mock)
	integrationStore := db.NewIntegrationStore(mock)
	issueStore := db.NewIssueStore(mock)
	jobStore := db.NewJobStore(mock)
	svc := ingestion.NewService(issueStore, webhookStore, jobStore, zerolog.Nop())
	return NewIngestionWebhookHandler(webhookStore, integrationStore, svc, zerolog.Nop())
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

			handler := setupIngestionHandler(t, mock)
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

	handler := setupIngestionHandler(t, mock)

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
