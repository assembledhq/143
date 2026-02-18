package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type IngestionWebhookHandler struct {
	webhookStore     *db.WebhookDeliveryStore
	integrationStore *db.IntegrationStore
	ingestionSvc     *ingestion.Service
	logger           zerolog.Logger
}

func NewIngestionWebhookHandler(
	webhookStore *db.WebhookDeliveryStore,
	integrationStore *db.IntegrationStore,
	ingestionSvc *ingestion.Service,
	logger zerolog.Logger,
) *IngestionWebhookHandler {
	return &IngestionWebhookHandler{
		webhookStore:     webhookStore,
		integrationStore: integrationStore,
		ingestionSvc:     ingestionSvc,
		logger:           logger,
	}
}

func (h *IngestionWebhookHandler) HandleSentry(w http.ResponseWriter, r *http.Request) {
	h.handleProvider(w, r, "sentry")
}

func (h *IngestionWebhookHandler) HandleLinear(w http.ResponseWriter, r *http.Request) {
	h.handleProvider(w, r, "linear")
}

func (h *IngestionWebhookHandler) handleProvider(w http.ResponseWriter, r *http.Request, provider string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		writeError(w, http.StatusBadRequest, "READ_FAILED", "failed to read request body")
		return
	}

	// Get integration ID from query param or header
	integrationIDStr := r.URL.Query().Get("integration_id")
	if integrationIDStr == "" {
		writeError(w, http.StatusBadRequest, "MISSING_INTEGRATION", "integration_id query parameter required")
		return
	}
	integrationID, err := uuid.Parse(integrationIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid integration_id")
		return
	}

	// Look up integration to get org_id
	integration, err := h.integrationStore.GetByID(r.Context(), integrationID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "integration not found")
		return
	}

	// Record webhook delivery
	delivery := &models.WebhookDelivery{
		OrgID:         integration.OrgID,
		IntegrationID: integrationID,
		Provider:      provider,
		EventType:     r.Header.Get("X-Event-Type"),
		Payload:       json.RawMessage(body),
		Status:        "received",
	}
	if err := h.webhookStore.Create(r.Context(), delivery); err != nil {
		h.logger.Error().Err(err).Msg("failed to record webhook delivery")
	}

	// Parse and ingest
	var normalized *ingestion.NormalizedIssue
	switch provider {
	case "sentry":
		adapter := ingestion.NewSentryAdapter()
		normalized, err = adapter.ParseWebhook(integrationID, json.RawMessage(body))
	case "linear":
		adapter := ingestion.NewLinearAdapter()
		normalized, err = adapter.ParseWebhook(integrationID, json.RawMessage(body))
	}

	if err != nil {
		errMsg := err.Error()
		_ = h.webhookStore.MarkProcessed(r.Context(), delivery, &errMsg)
		h.logger.Error().Err(err).Str("provider", provider).Msg("failed to parse webhook")
		writeError(w, http.StatusBadRequest, "PARSE_FAILED", "failed to parse webhook payload")
		return
	}

	if normalized == nil {
		// Non-actionable event (e.g., resolved, comment)
		_ = h.webhookStore.MarkProcessed(r.Context(), delivery, nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	_, err = h.ingestionSvc.IngestNormalized(r.Context(), integration.OrgID, *normalized)
	if err != nil {
		errMsg := err.Error()
		_ = h.webhookStore.MarkProcessed(r.Context(), delivery, &errMsg)
		h.logger.Error().Err(err).Str("provider", provider).Msg("failed to ingest issue")
		writeError(w, http.StatusInternalServerError, "INGEST_FAILED", "failed to ingest issue")
		return
	}

	_ = h.webhookStore.MarkProcessed(r.Context(), delivery, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}
