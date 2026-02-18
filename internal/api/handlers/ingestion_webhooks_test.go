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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupIngestionHandler(t *testing.T) (pgxmock.PgxPoolIface, *IngestionWebhookHandler) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)

	webhookStore := db.NewWebhookDeliveryStore(mock)
	integrationStore := db.NewIntegrationStore(mock)
	issueStore := db.NewIssueStore(mock)
	jobStore := db.NewJobStore(mock)
	svc := ingestion.NewService(issueStore, webhookStore, jobStore, zerolog.Nop())
	handler := NewIngestionWebhookHandler(webhookStore, integrationStore, svc, zerolog.Nop())
	return mock, handler
}

func TestIngestionWebhook_HandleSentry_Success(t *testing.T) {
	mock, handler := setupIngestionHandler(t)
	defer mock.Close()

	integrationID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	// 1. GetByID integration (1 named arg: id)
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

	// 5. MarkProcessed (4 named args: id, org_id, status, error)
	mock.ExpectExec("UPDATE webhook_deliveries").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	body := `{"action":"created","data":{"issue":{"id":"123","title":"Test Error","metadata":{"value":"desc"},"count":"5","userCount":2,"level":"error","firstSeen":"2024-01-01T00:00:00Z","lastSeen":"2024-01-02T00:00:00Z"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/sentry?integration_id="+integrationID.String(), strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleSentry(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "processed")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIngestionWebhook_HandleLinear_Success(t *testing.T) {
	mock, handler := setupIngestionHandler(t)
	defer mock.Close()

	integrationID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	// 1. GetByID integration (1 named arg)
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
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "processed")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIngestionWebhook_MissingIntegrationID(t *testing.T) {
	mock, handler := setupIngestionHandler(t)
	defer mock.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/sentry", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	handler.HandleSentry(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "MISSING_INTEGRATION")
}

func TestIngestionWebhook_InvalidIntegrationID(t *testing.T) {
	mock, handler := setupIngestionHandler(t)
	defer mock.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/sentry?integration_id=not-a-uuid", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	handler.HandleSentry(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestIngestionWebhook_IntegrationNotFound(t *testing.T) {
	mock, handler := setupIngestionHandler(t)
	defer mock.Close()

	integrationID := uuid.New()

	// GetByID returns empty rows -> pgx.CollectOneRow returns error
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/sentry?integration_id="+integrationID.String(), strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	handler.HandleSentry(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "NOT_FOUND")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIngestionWebhook_NonActionableEvent(t *testing.T) {
	mock, handler := setupIngestionHandler(t)
	defer mock.Close()

	integrationID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	// 1. GetByID integration (1 named arg)
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

	// "resolved" action is not actionable for sentry
	body := `{"action":"resolved","data":{"issue":{"id":"123","title":"Test Error","metadata":{"value":"desc"},"count":"5","userCount":2,"level":"error","firstSeen":"2024-01-01T00:00:00Z","lastSeen":"2024-01-02T00:00:00Z"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/sentry?integration_id="+integrationID.String(), strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleSentry(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ignored")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIngestionWebhook_ParseFailure(t *testing.T) {
	mock, handler := setupIngestionHandler(t)
	defer mock.Close()

	integrationID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	// 1. GetByID integration (1 named arg)
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

	// "created" action but missing issue ID -> parse failure
	body := `{"action":"created","data":{"issue":{"id":"","title":"Test Error"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/sentry?integration_id="+integrationID.String(), strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleSentry(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "PARSE_FAILED")
	assert.NoError(t, mock.ExpectationsWereMet())
}
