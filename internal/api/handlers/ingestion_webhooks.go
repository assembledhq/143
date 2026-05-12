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

	// linearAgent is the optional inbound-agent dispatcher. When set, the
	// Linear webhook handler branches on Linear-Event header and routes
	// AgentSessionEvent payloads through the agent path *before* the
	// existing ingestion adapter sees them. nil-safe: when unset, the
	// existing ingestion behavior is preserved exactly.
	linearAgent *LinearAgentDispatcher

	// requireSecret rejects webhook requests when the per-org secret is
	// missing or unverifiable. Off in development so a fresh local install
	// can accept loopback test deliveries without configuring credentials;
	// on in production where an unsigned webhook is always an attack
	// (anyone can POST to the public ingestion URL).
	requireSecret bool
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

// SetRequireSecret toggles fail-closed signature verification. Callers
// wire this to `cfg.Env == "production"` from main/router so prod
// rejects unsigned webhooks while local dev can still loop-test without
// configuring credentials.
func (h *IngestionWebhookHandler) SetRequireSecret(require bool) {
	h.requireSecret = require
}

// SetLinearAgentDispatcher wires the inbound-agent dispatcher
// post-construction. Separated from the constructor because the
// dispatcher's wiring depends on the JobStore + agent-side stores which
// the boot sequence resolves later. Safe to call at any boot stage; if
// never called, the agent path stays dark and Linear webhooks fall
// through to the existing ingestion code.
func (h *IngestionWebhookHandler) SetLinearAgentDispatcher(d *LinearAgentDispatcher) {
	h.linearAgent = d
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
		writeError(w, r, http.StatusBadRequest, "READ_FAILED", "failed to read request body")
		return
	}

	// Get integration ID from query param or header
	integrationIDStr := r.URL.Query().Get("integration_id")
	if integrationIDStr == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_INTEGRATION", "integration_id query parameter required")
		return
	}
	integrationID, err := uuid.Parse(integrationIDStr)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid integration_id")
		return
	}

	// Look up integration to get org_id
	integration, err := h.integrationStore.GetByID(r.Context(), integrationID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "integration not found")
		return
	}

	// Verify webhook signature using per-org credential from DB.
	if err := h.verifyProviderSignature(r.Context(), integration.OrgID, provider, body, r.Header); err != nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", err.Error())
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
		zerolog.Ctx(r.Context()).Error().Err(err).Msg("failed to record webhook delivery")
	}

	// Linear inbound-agent branch. Linear sends AgentSessionEvent payloads
	// to the same webhook URL as the legacy ingestion stream; we
	// distinguish via the `Linear-Event` header (or the envelope's `type`
	// field as a defensive fallback). When the dispatcher is wired and the
	// header indicates an agent event, route through the agent path
	// *before* the ingestion adapter so we don't double-process the body.
	//
	// 5s SLA: Dispatch is ack-fast — it does an idempotent INSERT, an
	// optional best-effort bootstrap-thought emit, and a job enqueue.
	// Total budget well under 1s under normal load.
	if provider == "linear" && h.linearAgent != nil {
		eventType := LinearAgentEventType(r.Header.Get("Linear-Event"))
		if eventType == "" {
			eventType = sniffLinearEventType(body)
		}
		if eventType == LinearAgentEventAgentSession || eventType == LinearAgentEventAppUserNotification {
			result := h.linearAgent.Dispatch(r.Context(), &integration, eventType, body)
			if err := h.webhookStore.MarkProcessed(r.Context(), delivery, nil); err != nil {
				zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to mark webhook processed")
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":           result.Status,
				"agent_session_id": result.AgentSessionID,
				"job_id":           result.JobID,
			})
			return
		}
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
		writeError(w, r, http.StatusBadRequest, "PARSE_FAILED", "failed to parse webhook payload", err)
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
		writeError(w, r, http.StatusInternalServerError, "INGEST_FAILED", "failed to ingest issue", err)
		return
	}

	if err := h.webhookStore.MarkProcessed(r.Context(), delivery, nil); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to mark webhook processed")
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

// sniffLinearEventType inspects the JSON envelope's top-level `type` field
// to decide whether this is an agent event when the `Linear-Event` header
// is missing. Bounded to the first 256 bytes — Linear puts `type` at the
// top of the document, so a small read avoids parsing the whole payload
// twice. Returns "" when the type can't be determined; the caller falls
// through to ingestion.
func sniffLinearEventType(body []byte) LinearAgentEventType {
	const maxScan = 256
	scan := body
	if len(scan) > maxScan {
		scan = scan[:maxScan]
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(scan, &head); err != nil {
		// Truncated body may not parse cleanly; that's fine, fall back
		// to a full unmarshal of the original payload.
		if err := json.Unmarshal(body, &head); err != nil {
			return ""
		}
	}
	switch LinearAgentEventType(head.Type) {
	case LinearAgentEventAgentSession, LinearAgentEventAppUserNotification:
		return LinearAgentEventType(head.Type)
	}
	return ""
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
		if h.requireSecret {
			return fmt.Errorf("webhook credential store not configured")
		}
		return nil
	}

	// Map provider string to models.ProviderName
	var providerName models.ProviderName
	switch provider {
	case "sentry":
		providerName = models.ProviderSentry
	case "linear":
		providerName = models.ProviderLinear
	default:
		if h.requireSecret {
			return fmt.Errorf("unknown provider %q", provider)
		}
		return nil
	}

	cred, err := h.credStore.Get(ctx, orgID, providerName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if h.requireSecret {
				return fmt.Errorf("no webhook credential configured for %s", provider)
			}
			zerolog.Ctx(ctx).Debug().Err(err).Str("provider", provider).Msg("no webhook credential configured, skipping signature verification")
			return nil
		}
		zerolog.Ctx(ctx).Error().Err(err).Str("provider", provider).Msg("failed to load webhook credential")
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
		if h.requireSecret {
			return fmt.Errorf("credential for %s has unexpected config type", provider)
		}
		return nil
	}

	if secret == "" {
		if h.requireSecret {
			return fmt.Errorf("webhook secret for %s is empty", provider)
		}
		return nil
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
