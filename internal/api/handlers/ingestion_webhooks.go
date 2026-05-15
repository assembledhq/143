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
	"strings"

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
	"linear": "Linear-Signature",
}

// providerDeliveryIDHeader maps provider name to the HTTP header carrying
// the per-delivery unique id we use as the replay-protection key. When
// present it gets persisted as webhook_deliveries.delivery_id, where the
// partial UNIQUE INDEX (provider, delivery_id) WHERE delivery_id IS NOT
// NULL guarantees that a re-signed replay collides on insert and the
// ingestion handler returns 200 "replay" without touching downstream
// dispatchers. Providers absent from this map fall back to the existing
// (no-replay-protection) flow — HMAC verification still applies.
//
// Sentry is intentionally absent: their webhook headers don't include a
// stable per-delivery uuid, and its inbound side is idempotent at the
// issue-fingerprint layer, so the additional protection isn't worth a
// header guess. Add only when the upstream provider documents a stable
// delivery id.
var providerDeliveryIDHeader = map[string]string{
	"linear": "Linear-Delivery",
}

func (h *IngestionWebhookHandler) handleProvider(w http.ResponseWriter, r *http.Request, provider string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "READ_FAILED", "failed to read request body")
		return
	}

	// Resolve which integration this delivery belongs to. Two paths:
	//
	//   1. Per-install URL (`?integration_id=<uuid>`) — the original
	//      self-hosted model where each customer pastes their unique
	//      webhook URL into the provider dashboard. Always honored when
	//      present; takes priority so existing installs don't change
	//      behavior.
	//
	//   2. Payload-driven workspace lookup — required for a single
	//      multi-tenant Linear OAuth app, which Linear forces to use one
	//      shared webhook URL across every workspace it's installed in.
	//      The payload's `organizationId` (the Linear workspace id) is
	//      matched against integrations.config->>'workspace_id' via the
	//      partial UNIQUE index `idx_integrations_linear_workspace`.
	//
	// SECURITY: lookup runs *before* HMAC verification because we need
	// the integration to know which org's secret to verify against. The
	// integration row itself contains no secrets and the lookup is bounded
	// by an indexed equality match, so the pre-auth surface is just "an
	// attacker can probe whether a workspace id is connected." That's the
	// same surface the existing `?integration_id=` path exposes (probing
	// whether an integration uuid exists), so this isn't a regression.
	integration, err := h.resolveIntegrationForWebhook(r, provider, body)
	if err != nil {
		var resolveErr *webhookIntegrationLookupError
		if errors.As(err, &resolveErr) {
			writeError(w, r, resolveErr.status, resolveErr.code, resolveErr.message)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTEGRATION_LOOKUP_FAILED", "failed to resolve integration", err)
		return
	}
	integrationID := integration.ID

	// Verify webhook signature using per-org credential from DB.
	if err := h.verifyProviderSignature(r.Context(), integration.OrgID, provider, body, r.Header); err != nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", err.Error())
		return
	}

	// Record webhook delivery. delivery_id (when the provider supplies a
	// stable per-delivery header) makes Create return ErrWebhookDeliveryReplay
	// on a duplicate signed body — we ack 200 and skip dispatch so a
	// replayed payload can't double-fire bootstrap activities or worker
	// jobs. HMAC verification has already passed at this point, so we
	// know the body is authentic; replay detection is the orthogonal
	// "we've already processed this exact authentic body" check.
	deliveryID := readDeliveryID(provider, r.Header)
	delivery := &models.WebhookDelivery{
		OrgID:         integration.OrgID,
		IntegrationID: integrationID,
		Provider:      provider,
		DeliveryID:    deliveryID,
		EventType:     r.Header.Get("X-Event-Type"),
		Payload:       json.RawMessage(body),
		Status:        "received",
	}
	if err := h.webhookStore.Create(r.Context(), delivery); err != nil {
		if errors.Is(err, db.ErrWebhookDeliveryReplay) {
			zerolog.Ctx(r.Context()).Info().
				Str("provider", provider).
				Str("delivery_id", deliveryIDLogValue(deliveryID)).
				Msg("ignoring replayed webhook delivery")
			writeJSON(w, http.StatusOK, map[string]string{"status": "replay_ignored"})
			return
		}
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
		var parsedEnvelope *linearAgentEventEnvelope
		if eventType == "" {
			eventType, parsedEnvelope = sniffLinearEventEnvelope(body)
		}
		if eventType == LinearAgentEventAgentSession || eventType == LinearAgentEventAppUserNotification {
			result := h.linearAgent.Dispatch(r.Context(), &integration, eventType, body, parsedEnvelope)
			if result.Err != nil {
				writeError(w, r, http.StatusInternalServerError, "DISPATCH_FAILED", "failed to dispatch linear agent event", result.Err)
				return
			}
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

// readDeliveryID extracts the per-delivery unique id for replay
// protection. Returns nil when the provider has no header registered or
// the header is absent — Create then writes NULL into delivery_id and the
// partial UNIQUE INDEX excludes the row from collision checks (the
// existing no-replay-protection behavior).
func readDeliveryID(provider string, headers http.Header) *string {
	header, ok := providerDeliveryIDHeader[provider]
	if !ok {
		return nil
	}
	value := strings.TrimSpace(headers.Get(header))
	if value == "" {
		return nil
	}
	return &value
}

// deliveryIDLogValue defangs nil delivery ids for logging without
// branching at every call site.
func deliveryIDLogValue(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// sniffLinearEventType inspects the JSON envelope's top-level `type` field to
// decide whether this is an agent event when the `Linear-Event` header is
// missing. Returns "" when the type can't be determined; the caller falls
// through to ingestion.
func sniffLinearEventType(body []byte) LinearAgentEventType {
	eventType, _ := sniffLinearEventEnvelope(body)
	return eventType
}

// sniffLinearEventEnvelope parses the Linear event envelope once so the
// dispatcher can reuse it instead of reparsing the webhook body after the
// missing-header fallback has already identified an agent event.
func sniffLinearEventEnvelope(body []byte) (LinearAgentEventType, *linearAgentEventEnvelope) {
	var env linearAgentEventEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", nil
	}
	switch LinearAgentEventType(env.Type) {
	case LinearAgentEventAgentSession, LinearAgentEventAppUserNotification:
		return LinearAgentEventType(env.Type), &env
	}
	return "", nil
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

// webhookIntegrationLookupError is returned by resolveIntegrationForWebhook
// to communicate a structured HTTP error back to the caller without coupling
// the resolver to http.ResponseWriter. The handler converts it into a
// writeError call. Concrete codes mirror the previous inline behavior so
// metrics/dashboards on `code` continue to work.
type webhookIntegrationLookupError struct {
	status  int
	code    string
	message string
}

func (e *webhookIntegrationLookupError) Error() string { return e.message }

func newWebhookLookupErr(status int, code, message string) *webhookIntegrationLookupError {
	return &webhookIntegrationLookupError{status: status, code: code, message: message}
}

// linearWebhookEnvelopePreamble is the minimal shape we need to sniff out
// of an inbound Linear webhook body to determine which org owns it. Linear
// puts `organizationId` at the top level of every webhook payload
// regardless of event type; this avoids dragging in the agent-specific
// envelope just to do org resolution.
type linearWebhookEnvelopePreamble struct {
	OrganizationID string `json:"organizationId"`
}

// resolveIntegrationForWebhook returns the integration owning this inbound
// webhook delivery. Order of precedence:
//
//  1. `?integration_id=<uuid>` query param — the per-install URL from
//     the existing self-hosted model. Honored when present so existing
//     URLs keep working unchanged.
//  2. For Linear: body sniff of `organizationId` → look up the active
//     integration whose stored Linear workspace_id matches. Required to
//     support a single multi-tenant OAuth app where Linear sends every
//     workspace's events to the same URL.
//
// SECURITY: the body is already bounded to 1MB by the io.LimitReader in
// handleProvider, so the sniff allocates O(payload size) at worst. We parse
// just the minimal preamble (not the full envelope) so a malformed agent
// payload still resolves to the right org and gets logged as a parse
// failure further down the path. Lookups never reveal anything secret —
// the integration row is metadata only.
func (h *IngestionWebhookHandler) resolveIntegrationForWebhook(r *http.Request, provider string, body []byte) (models.Integration, error) {
	if raw := r.URL.Query().Get("integration_id"); raw != "" {
		integrationID, err := uuid.Parse(raw)
		if err != nil {
			return models.Integration{}, newWebhookLookupErr(http.StatusBadRequest, "INVALID_ID", "invalid integration_id")
		}
		integration, err := h.integrationStore.GetByID(r.Context(), integrationID)
		if err != nil {
			return models.Integration{}, newWebhookLookupErr(http.StatusNotFound, "NOT_FOUND", "integration not found")
		}
		return integration, nil
	}

	if provider == "linear" {
		var pre linearWebhookEnvelopePreamble
		if err := json.Unmarshal(body, &pre); err != nil || pre.OrganizationID == "" {
			// Distinguish "missing org id" from "broken JSON" only in the
			// log — to the caller both look the same. Either way they can't
			// route the delivery. Linear's `organizationId` is a uuid string,
			// not a structured object, so a malformed payload is the only
			// realistic way this branch fires.
			return models.Integration{}, newWebhookLookupErr(http.StatusBadRequest, "MISSING_INTEGRATION",
				"could not resolve linear org: payload missing organizationId and no integration_id query param provided")
		}
		integration, err := h.integrationStore.GetActiveLinearByWorkspaceID(r.Context(), pre.OrganizationID)
		if err != nil {
			// 401 rather than 404: from the caller's perspective an
			// unrecognized workspace id is functionally equivalent to "no
			// authorized credential" — the install was never completed (or
			// has been disconnected). Using 401 keeps the response shape
			// consistent with what a bad HMAC would produce and avoids
			// leaking which workspace ids are connected.
			zerolog.Ctx(r.Context()).Info().
				Str("workspace_id", pre.OrganizationID).
				Err(err).
				Msg("linear webhook references workspace_id with no active integration; rejecting")
			return models.Integration{}, newWebhookLookupErr(http.StatusUnauthorized, "UNAUTHORIZED", "no active integration for the request payload")
		}
		return integration, nil
	}

	return models.Integration{}, newWebhookLookupErr(http.StatusBadRequest, "MISSING_INTEGRATION", "integration_id query parameter required")
}
