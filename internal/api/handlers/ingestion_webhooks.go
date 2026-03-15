package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// webhookSecretLookup is the subset of OrgCredentialStore needed to look up
// per-org webhook secrets at request time.
type webhookSecretLookup interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

type IngestionWebhookHandler struct {
	webhookStore     *db.WebhookDeliveryStore
	integrationStore *db.IntegrationStore
	credStore        webhookSecretLookup
	ingestionSvc     *ingestion.Service
	logger           zerolog.Logger
}

func NewIngestionWebhookHandler(
	webhookStore *db.WebhookDeliveryStore,
	integrationStore *db.IntegrationStore,
	credStore webhookSecretLookup,
	ingestionSvc *ingestion.Service,
	logger zerolog.Logger,
) *IngestionWebhookHandler {
	return &IngestionWebhookHandler{
		webhookStore:     webhookStore,
		integrationStore: integrationStore,
		credStore:        credStore,
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

// providerSignatureHeader maps provider name to the HTTP header carrying the
// webhook signature.
var providerSignatureHeader = map[string]string{
	"sentry": "X-Sentry-Hook-Signature",
	"linear": "X-Linear-Signature",
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

	// Verify webhook signature using per-org credential from DB.
	if err := h.verifyProviderSignature(r.Context(), integration.OrgID, provider, body, r.Header); err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", err.Error())
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
		if markErr := h.webhookStore.MarkProcessed(r.Context(), delivery, &errMsg); markErr != nil {
			zerolog.Ctx(r.Context()).Warn().Err(markErr).Msg("failed to mark webhook processed")
		}
		h.logger.Error().Err(err).Str("provider", provider).Msg("failed to parse webhook")
		writeError(w, http.StatusBadRequest, "PARSE_FAILED", "failed to parse webhook payload")
		return
	}

	if normalized == nil {
		// Non-actionable event (e.g., resolved, comment)
		if err := h.webhookStore.MarkProcessed(r.Context(), delivery, nil); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to mark webhook processed")
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	_, err = h.ingestionSvc.IngestNormalized(r.Context(), integration.OrgID, *normalized)
	if err != nil {
		errMsg := err.Error()
		if markErr := h.webhookStore.MarkProcessed(r.Context(), delivery, &errMsg); markErr != nil {
			zerolog.Ctx(r.Context()).Warn().Err(markErr).Msg("failed to mark webhook processed")
		}
		h.logger.Error().Err(err).Str("provider", provider).Msg("failed to ingest issue")
		writeError(w, http.StatusInternalServerError, "INGEST_FAILED", "failed to ingest issue")
		return
	}

	if err := h.webhookStore.MarkProcessed(r.Context(), delivery, nil); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to mark webhook processed")
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

// verifyProviderSignature looks up the per-org webhook secret from the DB and
// verifies the HMAC-SHA256 signature. If no credential is configured, verification
// is skipped (dev mode).
func (h *IngestionWebhookHandler) verifyProviderSignature(
	ctx context.Context,
	orgID uuid.UUID,
	provider string,
	body []byte,
	headers http.Header,
) error {
	if h.credStore == nil {
		return nil // no credential store — skip verification
	}

	// Map provider string to models.ProviderName
	var providerName models.ProviderName
	switch provider {
	case "sentry":
		providerName = models.ProviderSentry
	case "linear":
		providerName = models.ProviderLinear
	default:
		return nil
	}

	cred, err := h.credStore.Get(ctx, orgID, providerName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No credential configured — skip verification (dev mode)
			h.logger.Debug().Err(err).Str("provider", provider).Msg("no webhook credential configured, skipping signature verification")
			return nil
		}
		h.logger.Error().Err(err).Str("provider", provider).Msg("failed to load webhook credential")
		return fmt.Errorf("failed to load webhook credential")
	}

	// Extract webhook_secret from the typed config.
	var secret string
	switch cfg := cred.Config.(type) {
	case models.SentryConfig:
		secret = cfg.WebhookSecret
	case models.LinearConfig:
		secret = cfg.WebhookSecret
	default:
		return nil
	}

	if secret == "" {
		return nil // empty secret — skip verification
	}

	headerName := providerSignatureHeader[provider]
	signature := headers.Get(headerName)
	if signature == "" {
		return fmt.Errorf("missing webhook signature")
	}

	if !verifyHMACSHA256(secret, body, signature) {
		return fmt.Errorf("invalid webhook signature")
	}

	return nil
}

// verifyHMACSHA256 computes HMAC-SHA256 of body with the given key and compares
// the hex-encoded result against the provided signature.
func verifyHMACSHA256(secret string, body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature), []byte(expected))
}
