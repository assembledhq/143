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
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	pagerdutysvc "github.com/assembledhq/143/internal/services/pagerduty"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type pagerDutyWebhookDeliveryStore interface {
	CreateOrGet(ctx context.Context, d *models.WebhookDelivery) (bool, error)
	MarkProcessed(ctx context.Context, d *models.WebhookDelivery, errMsg *string) error
	MarkIgnored(ctx context.Context, d *models.WebhookDelivery) error
}

type pagerDutyWebhookIntegrationStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Integration, error)
}

type pagerDutyWebhookProviderIntegrationStore interface {
	GetByID(ctx context.Context, orgID, id uuid.UUID) (models.PagerDutyIntegration, error)
	GetByIntegrationID(ctx context.Context, orgID, integrationID uuid.UUID) (models.PagerDutyIntegration, error)
}

type pagerDutyWebhookCredentialLookup interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
	GetByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error)
}

type pagerDutyInboundEventStore interface {
	CreateOrGet(ctx context.Context, event *models.PagerDutyInboundEvent) (bool, error)
}

type pagerDutyWebhookJobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

type pagerDutyWebhookIncidentStore interface {
	GetByIncidentID(ctx context.Context, orgID, integrationID uuid.UUID, incidentID string) (models.PagerDutyIncident, error)
	GetLatestByIncidentID(ctx context.Context, orgID uuid.UUID, incidentID string) (models.PagerDutyIncident, error)
	Upsert(ctx context.Context, incident *models.PagerDutyIncident) error
}

type pagerDutyWebhookMappingStore interface {
	GetByServiceID(ctx context.Context, orgID, integrationID uuid.UUID, serviceID string) (models.PagerDutyServiceRepoMapping, error)
}

type pagerDutyWebhookIssueIngester interface {
	IngestNormalized(ctx context.Context, orgID uuid.UUID, issue ingestion.NormalizedIssue) (*models.Issue, error)
}

type pagerDutyWebhookIssueStatusUpdater interface {
	UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status models.IssueStatus) error
}

type PagerDutyWebhookHandlerConfig struct {
	Webhooks              pagerDutyWebhookDeliveryStore
	Integrations          pagerDutyWebhookIntegrationStore
	PagerDutyIntegrations pagerDutyWebhookProviderIntegrationStore
	Credentials           pagerDutyWebhookCredentialLookup
	InboundEvents         pagerDutyInboundEventStore
	Jobs                  pagerDutyWebhookJobStore
	Incidents             pagerDutyWebhookIncidentStore
	IssueIngester         pagerDutyWebhookIssueIngester
	IssueStatuses         pagerDutyWebhookIssueStatusUpdater
	Mappings              pagerDutyWebhookMappingStore
	Repositories          repoLookup
	SessionStarter        pagerDutySessionStarter
	Metrics               *metrics.PagerDutyMetrics
	RequireSecret         bool
	Logger                zerolog.Logger
}

type PagerDutyWebhookHandler struct {
	webhooks              pagerDutyWebhookDeliveryStore
	integrations          pagerDutyWebhookIntegrationStore
	pagerDutyIntegrations pagerDutyWebhookProviderIntegrationStore
	credentials           pagerDutyWebhookCredentialLookup
	inboundEvents         pagerDutyInboundEventStore
	jobs                  pagerDutyWebhookJobStore
	incidents             pagerDutyWebhookIncidentStore
	issueIngester         pagerDutyWebhookIssueIngester
	issueStatuses         pagerDutyWebhookIssueStatusUpdater
	mappings              pagerDutyWebhookMappingStore
	repositories          repoLookup
	sessionStarter        pagerDutySessionStarter
	audit                 *db.AuditEmitter
	metrics               *metrics.PagerDutyMetrics
	requireSecret         bool
	logger                zerolog.Logger
}

func NewPagerDutyWebhookHandler(cfg PagerDutyWebhookHandlerConfig) *PagerDutyWebhookHandler {
	return &PagerDutyWebhookHandler{
		webhooks:              cfg.Webhooks,
		integrations:          cfg.Integrations,
		pagerDutyIntegrations: cfg.PagerDutyIntegrations,
		credentials:           cfg.Credentials,
		inboundEvents:         cfg.InboundEvents,
		jobs:                  cfg.Jobs,
		incidents:             cfg.Incidents,
		issueIngester:         cfg.IssueIngester,
		issueStatuses:         cfg.IssueStatuses,
		mappings:              cfg.Mappings,
		repositories:          cfg.Repositories,
		sessionStarter:        cfg.SessionStarter,
		metrics:               cfg.Metrics,
		requireSecret:         cfg.RequireSecret,
		logger:                cfg.Logger,
	}
}

func (h *PagerDutyWebhookHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

func (h *PagerDutyWebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	var pagerDutyMetrics *metrics.PagerDutyMetrics
	if h != nil {
		pagerDutyMetrics = h.metrics
	}
	eventType := "unknown"
	result := "error"
	defer func() {
		pagerDutyMetrics.RecordWebhookEvent(r.Context(), eventType, result)
	}()

	if h == nil || h.webhooks == nil || h.integrations == nil || h.pagerDutyIntegrations == nil || h.inboundEvents == nil || h.jobs == nil {
		result = "unavailable"
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_WEBHOOK_UNAVAILABLE", "PagerDuty webhook handler is not configured")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		result = "read_failed"
		writeError(w, r, http.StatusBadRequest, "READ_FAILED", "failed to read request body")
		return
	}

	integration, err := h.resolveIntegration(r)
	if err != nil {
		var lookupErr *webhookIntegrationLookupError
		if errors.As(err, &lookupErr) {
			writeError(w, r, lookupErr.status, lookupErr.code, lookupErr.message)
			return
		}
		result = "integration_lookup_failed"
		writeError(w, r, http.StatusInternalServerError, "INTEGRATION_LOOKUP_FAILED", "failed to resolve PagerDuty integration", err)
		return
	}

	pagerDutyIntegration, err := h.resolvePagerDutyProviderIntegration(r, integration)
	if err != nil {
		result = "unauthorized"
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "no active PagerDuty integration for the request")
		return
	}
	if !pagerDutyWebhookStatusAcceptsInbound(pagerDutyIntegration.Status) {
		result = "inactive"
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "PagerDuty integration is inactive")
		return
	}

	if err := h.verifySignature(r.Context(), integration.OrgID, pagerDutyIntegration, body, r.Header); err != nil {
		result = "signature_failed"
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", err.Error())
		return
	}

	parsed, err := pagerdutysvc.ParseEvent(json.RawMessage(body))
	if err != nil {
		result = "parse_failed"
		writeError(w, r, http.StatusBadRequest, "PARSE_FAILED", "failed to parse PagerDuty webhook payload", err)
		return
	}
	eventType = string(parsed.EventType)

	headersJSON, err := json.Marshal(sanitizedPagerDutyWebhookHeaders(r.Header))
	if err != nil {
		result = "headers_invalid"
		writeError(w, r, http.StatusBadRequest, "HEADERS_INVALID", "failed to encode request headers", err)
		return
	}
	signatureValid := true
	delivery := &models.WebhookDelivery{
		OrgID:          integration.OrgID,
		IntegrationID:  integration.ID,
		Provider:       string(models.IntegrationProviderPagerDuty),
		DeliveryID:     pagerDutyDeliveryID(r.Header),
		EventType:      string(parsed.EventType),
		SignatureValid: &signatureValid,
		Payload:        json.RawMessage(body),
		Headers:        headersJSON,
		Status:         "received",
	}
	createdDelivery, err := h.webhooks.CreateOrGet(r.Context(), delivery)
	if err != nil {
		if errors.Is(err, db.ErrWebhookDeliveryReplay) {
			result = "replay_ignored"
			writeJSON(w, http.StatusOK, map[string]string{"status": "replay_ignored"})
			return
		}
		result = "delivery_record_failed"
		writeError(w, r, http.StatusInternalServerError, "DELIVERY_RECORD_FAILED", "failed to record PagerDuty webhook delivery", err)
		return
	}
	if !createdDelivery {
		h.logger.Info().
			Str("delivery_id", pagerDutyDeliveryIDString(delivery)).
			Msg("retrying PagerDuty webhook delivery")
	}

	event := &models.PagerDutyInboundEvent{
		OrgID:                  integration.OrgID,
		PagerDutyIntegrationID: &pagerDutyIntegration.ID,
		WebhookDeliveryID:      &delivery.ID,
		ProviderEventID:        parsed.ProviderEventID,
		EventType:              parsed.EventType,
		ResourceType:           parsed.ResourceType,
		IncidentID:             stringPtrOrNilPagerDutyWebhook(parsed.Incident.ID),
		OccurredAt:             parsed.OccurredAt,
		Payload:                json.RawMessage(body),
		Headers:                headersJSON,
		Status:                 "received",
	}
	createdEvent, err := h.inboundEvents.CreateOrGet(r.Context(), event)
	if err != nil {
		errMsg := err.Error()
		if markErr := h.webhooks.MarkProcessed(r.Context(), delivery, &errMsg); markErr != nil {
			h.logger.Warn().Err(markErr).Msg("failed to mark PagerDuty webhook failed")
		}
		result = "event_record_failed"
		writeError(w, r, http.StatusInternalServerError, "EVENT_RECORD_FAILED", "failed to record PagerDuty inbound event", err)
		return
	}
	if !createdEvent {
		if createdDelivery {
			if err := h.webhooks.MarkIgnored(r.Context(), delivery); err != nil {
				h.logger.Warn().Err(err).Msg("failed to mark duplicate PagerDuty webhook ignored")
			}
			result = "duplicate_ignored"
			writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate_ignored"})
			return
		}
		if event.ID == uuid.Nil {
			err := errors.New("retryable PagerDuty inbound event did not return an existing event id")
			errMsg := err.Error()
			if markErr := h.webhooks.MarkProcessed(r.Context(), delivery, &errMsg); markErr != nil {
				h.logger.Warn().Err(markErr).Msg("failed to mark PagerDuty webhook failed")
			}
			result = "event_lookup_failed"
			writeError(w, r, http.StatusInternalServerError, "EVENT_LOOKUP_FAILED", "failed to load existing PagerDuty inbound event", err)
			return
		}
		h.logger.Info().
			Str("delivery_id", pagerDutyDeliveryIDString(delivery)).
			Str("event_id", event.ID.String()).
			Msg("enqueueing existing PagerDuty inbound event for retried delivery")
	}

	dedupeKey := "pagerduty_ingest:" + event.ID.String()
	payload := map[string]string{
		"org_id":   integration.OrgID.String(),
		"event_id": event.ID.String(),
	}
	jobID, err := h.jobs.Enqueue(r.Context(), integration.OrgID, "default", models.JobTypePagerDutyIngestEvent, payload, 5, &dedupeKey)
	if err != nil {
		errMsg := err.Error()
		if markErr := h.webhooks.MarkProcessed(r.Context(), delivery, &errMsg); markErr != nil {
			h.logger.Warn().Err(markErr).Msg("failed to mark PagerDuty webhook failed")
		}
		result = "enqueue_failed"
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue PagerDuty event processing", err)
		return
	}

	if err := h.webhooks.MarkProcessed(r.Context(), delivery, nil); err != nil {
		h.logger.Warn().Err(err).Msg("failed to mark PagerDuty webhook processed")
	}
	result = "queued"
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "queued",
		"event_id": event.ID,
		"job_id":   jobID,
	})
}

func (h *PagerDutyWebhookHandler) HandleStartSessionAction(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.integrations == nil || h.pagerDutyIntegrations == nil || h.incidents == nil || h.sessionStarter == nil {
		writeError(w, r, http.StatusServiceUnavailable, "PAGERDUTY_ACTION_UNAVAILABLE", "PagerDuty start-session action is not configured")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "READ_FAILED", "failed to read request body")
		return
	}

	integration, err := h.resolveIntegration(r)
	if err != nil {
		var lookupErr *webhookIntegrationLookupError
		if errors.As(err, &lookupErr) {
			writeError(w, r, lookupErr.status, lookupErr.code, lookupErr.message)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTEGRATION_LOOKUP_FAILED", "failed to resolve PagerDuty integration", err)
		return
	}
	pagerDutyIntegration, err := h.resolvePagerDutyProviderIntegration(r, integration)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "no active PagerDuty integration for the request")
		return
	}
	if !pagerDutyWebhookStatusAcceptsInbound(pagerDutyIntegration.Status) {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "PagerDuty integration is inactive")
		return
	}
	if err := h.verifySignature(r.Context(), integration.OrgID, pagerDutyIntegration, body, r.Header); err != nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", err.Error())
		return
	}
	actionReq, err := parsePagerDutyStartSessionActionRequest(body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ACTION", err.Error())
		return
	}
	delivery, _, ok := h.recordStartSessionActionDelivery(w, r, integration, body)
	if !ok {
		return
	}

	incident, err := h.loadOrIngestStartSessionActionIncident(r.Context(), integration.OrgID, pagerDutyIntegration, actionReq, body, delivery)
	if err != nil {
		h.markStartSessionActionDeliveryFailed(r, delivery, err)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "PagerDuty incident has not been ingested yet")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to load PagerDuty incident", err)
		return
	}
	if incident.PagerDutyIntegrationID != pagerDutyIntegration.ID {
		h.markStartSessionActionDeliveryFailed(r, delivery, fmt.Errorf("PagerDuty incident does not belong to this integration"))
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "PagerDuty incident does not belong to this integration")
		return
	}
	repositoryID, baseBranch, ok := h.resolveStartSessionActionRepository(w, r, integration.OrgID, incident, actionReq.RepositoryID, actionReq.BaseBranch)
	if !ok {
		h.markStartSessionActionDeliveryFailed(r, delivery, fmt.Errorf("PagerDuty incident service is not mapped to a repository"))
		return
	}

	session, err := h.sessionStarter.StartSession(r.Context(), pagerdutysvc.StartSessionInput{
		OrgID:           integration.OrgID,
		Incident:        incident,
		RepositoryID:    repositoryID,
		BaseBranch:      baseBranch,
		Message:         strings.TrimSpace(actionReq.Message),
		ProviderEventID: pagerDutyDeliveryIDString(delivery),
	})
	if err != nil {
		h.markStartSessionActionDeliveryFailed(r, delivery, err)
		if errors.Is(err, pagerdutysvc.ErrPagerDutySessionAlreadyRunning) {
			writeError(w, r, http.StatusConflict, "PAGERDUTY_SESSION_ALREADY_RUNNING", "a session is already running for this PagerDuty incident")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "SESSION_START_FAILED", "failed to start PagerDuty incident session", err)
		return
	}
	if delivery != nil {
		if err := h.webhooks.MarkProcessed(r.Context(), delivery, nil); err != nil {
			h.logger.Warn().Err(err).Msg("failed to mark PagerDuty start-session action processed")
		}
	}
	h.emitStartSessionActionAudit(r, integration.OrgID, incident, session, repositoryID)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Session]{Data: session})
}

func (h *PagerDutyWebhookHandler) resolveIntegration(r *http.Request) (models.Integration, error) {
	raw := r.URL.Query().Get("integration_id")
	if raw == "" {
		return models.Integration{}, newWebhookLookupErr(http.StatusBadRequest, "MISSING_INTEGRATION", "integration_id query parameter required")
	}
	integrationID, err := uuid.Parse(raw)
	if err != nil {
		return models.Integration{}, newWebhookLookupErr(http.StatusBadRequest, "INVALID_ID", "invalid integration_id")
	}
	integration, err := h.integrations.GetByID(r.Context(), integrationID)
	if err != nil {
		return models.Integration{}, newWebhookLookupErr(http.StatusNotFound, "NOT_FOUND", "integration not found")
	}
	if integration.Provider != models.IntegrationProviderPagerDuty {
		return models.Integration{}, newWebhookLookupErr(http.StatusBadRequest, "INVALID_PROVIDER", "integration is not a PagerDuty integration")
	}
	if integration.Status != models.IntegrationStatusActive {
		return models.Integration{}, newWebhookLookupErr(http.StatusUnauthorized, "UNAUTHORIZED", "PagerDuty integration is inactive")
	}
	return integration, nil
}

func (h *PagerDutyWebhookHandler) resolvePagerDutyProviderIntegration(r *http.Request, integration models.Integration) (models.PagerDutyIntegration, error) {
	rawProviderID := strings.TrimSpace(r.URL.Query().Get("pagerduty_integration_id"))
	if rawProviderID != "" {
		providerID, err := uuid.Parse(rawProviderID)
		if err != nil {
			return models.PagerDutyIntegration{}, err
		}
		providerIntegration, err := h.pagerDutyIntegrations.GetByID(r.Context(), integration.OrgID, providerID)
		if err != nil {
			return models.PagerDutyIntegration{}, err
		}
		if providerIntegration.IntegrationID != nil && *providerIntegration.IntegrationID != integration.ID {
			return models.PagerDutyIntegration{}, pgx.ErrNoRows
		}
		return providerIntegration, nil
	}
	return h.pagerDutyIntegrations.GetByIntegrationID(r.Context(), integration.OrgID, integration.ID)
}

type pagerDutyStartSessionActionRequest struct {
	IncidentID      string
	IncidentPayload json.RawMessage
	RepositoryID    *uuid.UUID
	BaseBranch      *string
	Message         string
}

func parsePagerDutyStartSessionActionRequest(body []byte) (pagerDutyStartSessionActionRequest, error) {
	type incidentRef struct {
		ID         string `json:"id"`
		IncidentID string `json:"incident_id"`
	}
	var req struct {
		IncidentID   string          `json:"incident_id"`
		Incident     json.RawMessage `json:"incident"`
		Data         json.RawMessage `json:"data"`
		RepositoryID *uuid.UUID      `json:"repository_id"`
		BaseBranch   *string         `json:"base_branch"`
		Message      string          `json:"message"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return pagerDutyStartSessionActionRequest{}, fmt.Errorf("invalid request body")
	}
	out := pagerDutyStartSessionActionRequest{
		IncidentID:      strings.TrimSpace(req.IncidentID),
		IncidentPayload: req.Incident,
		RepositoryID:    req.RepositoryID,
		BaseBranch:      trimOptionalStringPointer(req.BaseBranch),
		Message:         strings.TrimSpace(req.Message),
	}
	if len(out.IncidentPayload) == 0 || string(out.IncidentPayload) == "null" {
		out.IncidentPayload = req.Data
	}
	if out.IncidentID == "" && len(req.Incident) > 0 {
		var ref incidentRef
		if err := json.Unmarshal(req.Incident, &ref); err == nil {
			out.IncidentID = firstNonEmptyPagerDutyWebhook(ref.ID, ref.IncidentID)
		}
	}
	if out.IncidentID == "" && len(req.Data) > 0 {
		var ref incidentRef
		if err := json.Unmarshal(req.Data, &ref); err == nil {
			out.IncidentID = firstNonEmptyPagerDutyWebhook(ref.ID, ref.IncidentID)
		}
	}
	if out.IncidentID == "" {
		if parsed, err := pagerdutysvc.ParseEvent(json.RawMessage(body)); err == nil {
			out.IncidentID = strings.TrimSpace(parsed.Incident.ID)
		}
	}
	if out.IncidentID == "" {
		return pagerDutyStartSessionActionRequest{}, fmt.Errorf("incident_id is required")
	}
	return out, nil
}

func (h *PagerDutyWebhookHandler) loadOrIngestStartSessionActionIncident(
	ctx context.Context,
	orgID uuid.UUID,
	integration models.PagerDutyIntegration,
	actionReq pagerDutyStartSessionActionRequest,
	body []byte,
	delivery *models.WebhookDelivery,
) (models.PagerDutyIncident, error) {
	incident, err := h.incidents.GetByIncidentID(ctx, orgID, integration.ID, actionReq.IncidentID)
	if err == nil {
		return incident, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.PagerDutyIncident{}, err
	}
	if h.issueIngester == nil || h.issueStatuses == nil {
		return models.PagerDutyIncident{}, err
	}

	parsed, parseErr := parsePagerDutyStartSessionActionEvent(body, actionReq, pagerDutyDeliveryIDString(delivery))
	if parseErr != nil {
		return models.PagerDutyIncident{}, fmt.Errorf("parse PagerDuty start-session incident payload: %w", parseErr)
	}
	if parsed.Incident.ID != actionReq.IncidentID {
		return models.PagerDutyIncident{}, fmt.Errorf("PagerDuty action incident id %q does not match payload incident id %q", actionReq.IncidentID, parsed.Incident.ID)
	}
	normalized, normalizeErr := pagerdutysvc.NormalizeEvent(orgID, integration, parsed)
	if normalizeErr != nil {
		return models.PagerDutyIncident{}, fmt.Errorf("normalize PagerDuty start-session incident payload: %w", normalizeErr)
	}
	issue, ingestErr := h.issueIngester.IngestNormalized(ctx, orgID, normalized.Issue)
	if ingestErr != nil {
		return models.PagerDutyIncident{}, fmt.Errorf("ingest PagerDuty start-session issue: %w", ingestErr)
	}
	if issue == nil || issue.ID == uuid.Nil {
		return models.PagerDutyIncident{}, fmt.Errorf("ingest PagerDuty start-session issue returned no issue id")
	}
	if err := h.issueStatuses.UpdateStatus(ctx, orgID, issue.ID, normalized.IssueStatus); err != nil {
		return models.PagerDutyIncident{}, fmt.Errorf("update PagerDuty start-session issue status: %w", err)
	}
	normalized.Incident.IssueID = &issue.ID
	if err := h.incidents.Upsert(ctx, &normalized.Incident); err != nil {
		return models.PagerDutyIncident{}, fmt.Errorf("upsert PagerDuty start-session incident: %w", err)
	}
	return normalized.Incident, nil
}

func parsePagerDutyStartSessionActionEvent(body []byte, actionReq pagerDutyStartSessionActionRequest, providerEventID string) (pagerdutysvc.ParsedEvent, error) {
	if parsed, err := pagerdutysvc.ParseEvent(json.RawMessage(body)); err == nil {
		return parsed, nil
	}
	incidentPayload := actionReq.IncidentPayload
	if len(incidentPayload) == 0 || string(incidentPayload) == "null" {
		return pagerdutysvc.ParsedEvent{}, fmt.Errorf("incident payload is required")
	}
	eventID := strings.TrimSpace(providerEventID)
	if eventID == "" {
		eventID = "incident.action.start_session:" + actionReq.IncidentID
	}
	payload, err := json.Marshal(map[string]any{
		"event": map[string]any{
			"id":            eventID,
			"event_type":    string(pagerDutyStartSessionSyntheticEventType(incidentPayload)),
			"resource_type": "incident",
			"data":          json.RawMessage(incidentPayload),
		},
	})
	if err != nil {
		return pagerdutysvc.ParsedEvent{}, fmt.Errorf("encode synthetic PagerDuty event: %w", err)
	}
	return pagerdutysvc.ParseEvent(payload)
}

func pagerDutyStartSessionSyntheticEventType(incidentPayload json.RawMessage) models.PagerDutyEventType {
	var decoded map[string]any
	if err := json.Unmarshal(incidentPayload, &decoded); err != nil {
		return models.PagerDutyEventIncidentTriggered
	}
	switch strings.ToLower(strings.TrimSpace(firstStringFromMapPagerDutyWebhook(decoded, "status", "state"))) {
	case "resolved":
		return models.PagerDutyEventIncidentResolved
	case "acknowledged":
		return models.PagerDutyEventIncidentAcknowledged
	default:
		return models.PagerDutyEventIncidentTriggered
	}
}

func (h *PagerDutyWebhookHandler) resolveStartSessionActionRepository(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, incident models.PagerDutyIncident, requestedRepoID *uuid.UUID, requestedBaseBranch *string) (uuid.UUID, *string, bool) {
	if requestedRepoID != nil && *requestedRepoID != uuid.Nil {
		if !h.validatePagerDutyRepository(w, r, orgID, *requestedRepoID) {
			return uuid.Nil, nil, false
		}
		return *requestedRepoID, trimOptionalStringPointer(requestedBaseBranch), true
	}
	if h.mappings != nil && incident.ServiceID != nil && strings.TrimSpace(*incident.ServiceID) != "" {
		mapping, err := h.mappings.GetByServiceID(r.Context(), orgID, incident.PagerDutyIntegrationID, strings.TrimSpace(*incident.ServiceID))
		if err == nil {
			baseBranch := trimOptionalStringPointer(requestedBaseBranch)
			if baseBranch == nil {
				baseBranch = trimOptionalStringPointer(mapping.BaseBranch)
			}
			return mapping.RepositoryID, baseBranch, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "MAPPING_LOOKUP_FAILED", "failed to resolve PagerDuty service mapping", err)
			return uuid.Nil, nil, false
		}
	}
	if h.pagerDutyIntegrations != nil {
		integration, err := h.pagerDutyIntegrations.GetByID(r.Context(), orgID, incident.PagerDutyIntegrationID)
		if err == nil && integration.DefaultRepositoryID != nil && *integration.DefaultRepositoryID != uuid.Nil {
			return *integration.DefaultRepositoryID, trimOptionalStringPointer(requestedBaseBranch), true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "INTEGRATION_LOOKUP_FAILED", "failed to resolve PagerDuty integration defaults", err)
			return uuid.Nil, nil, false
		}
	}
	writeError(w, r, http.StatusBadRequest, "REPOSITORY_UNMAPPED", "PagerDuty incident service is not mapped to a repository")
	return uuid.Nil, nil, false
}

func (h *PagerDutyWebhookHandler) recordStartSessionActionDelivery(w http.ResponseWriter, r *http.Request, integration models.Integration, body []byte) (*models.WebhookDelivery, bool, bool) {
	if h.webhooks == nil {
		return nil, true, true
	}
	headersJSON, err := json.Marshal(sanitizedPagerDutyWebhookHeaders(r.Header))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "HEADERS_INVALID", "failed to encode request headers", err)
		return nil, false, false
	}
	signatureValid := true
	delivery := &models.WebhookDelivery{
		OrgID:          integration.OrgID,
		IntegrationID:  integration.ID,
		Provider:       string(models.IntegrationProviderPagerDuty),
		DeliveryID:     pagerDutyDeliveryID(r.Header),
		EventType:      "incident.action.start_session",
		SignatureValid: &signatureValid,
		Payload:        json.RawMessage(body),
		Headers:        headersJSON,
		Status:         "received",
	}
	created, err := h.webhooks.CreateOrGet(r.Context(), delivery)
	if err != nil {
		if errors.Is(err, db.ErrWebhookDeliveryReplay) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "replay_ignored"})
			return delivery, false, false
		}
		writeError(w, r, http.StatusInternalServerError, "DELIVERY_RECORD_FAILED", "failed to record PagerDuty start-session action delivery", err)
		return nil, false, false
	}
	return delivery, created, true
}

func (h *PagerDutyWebhookHandler) markStartSessionActionDeliveryFailed(r *http.Request, delivery *models.WebhookDelivery, cause error) {
	if h == nil || h.webhooks == nil || delivery == nil || cause == nil {
		return
	}
	errMsg := cause.Error()
	if err := h.webhooks.MarkProcessed(r.Context(), delivery, &errMsg); err != nil {
		h.logger.Warn().Err(err).Msg("failed to mark PagerDuty start-session action failed")
	}
}

func (h *PagerDutyWebhookHandler) emitStartSessionActionAudit(r *http.Request, orgID uuid.UUID, incident models.PagerDutyIncident, session models.Session, repositoryID uuid.UUID) {
	if h == nil || h.audit == nil {
		return
	}
	resourceID := session.ID.String()
	sessionID := session.ID
	h.audit.EmitWebhookAction(r.Context(), db.WebhookActionParams{
		OrgID:        orgID,
		ProviderName: string(models.IntegrationProviderPagerDuty),
		Action:       models.AuditActionSessionCreated,
		ResourceType: models.AuditResourceSession,
		ResourceID:   &resourceID,
		Details: marshalAuditDetails(h.logger, map[string]any{
			"provider":      string(models.IntegrationProviderPagerDuty),
			"incident_id":   incident.IncidentID,
			"repository_id": repositoryID,
		}),
		IPAddress: parseClientIP(r),
		SessionID: &sessionID,
	})
}

func (h *PagerDutyWebhookHandler) verifySignature(ctx context.Context, orgID uuid.UUID, integration models.PagerDutyIntegration, body []byte, headers http.Header) error {
	secret, err := h.resolveSecret(ctx, orgID, integration)
	if err != nil {
		return err
	}
	if secret == "" {
		if h.requireSecret {
			return errors.New("PagerDuty webhook secret is empty")
		}
		return nil
	}
	if provided := strings.TrimSpace(headers.Get("X-143-PagerDuty-Secret")); provided != "" {
		if hmac.Equal([]byte(provided), []byte(secret)) {
			return nil
		}
		return errors.New("invalid PagerDuty webhook secret")
	}
	for _, header := range []string{"X-PagerDuty-Signature", "X-PagerDuty-Webhook-Signature", "X-PagerDuty-Hmac-SHA256"} {
		signature := strings.TrimSpace(headers.Get(header))
		if signature == "" {
			continue
		}
		if verifyPagerDutyHMAC(secret, body, signature) {
			return nil
		}
		return errors.New("invalid PagerDuty webhook signature")
	}
	return errors.New("missing PagerDuty webhook signature")
}

func (h *PagerDutyWebhookHandler) resolveSecret(ctx context.Context, orgID uuid.UUID, integration models.PagerDutyIntegration) (string, error) {
	if h.credentials == nil {
		return "", nil
	}
	var cred *models.DecryptedCredential
	credentialID, err := pagerDutyCredentialIDFromRef(integration.CredentialRef)
	if err == nil {
		cred, err = h.credentials.GetByID(ctx, orgID, credentialID)
	} else if strings.TrimSpace(integration.CredentialRef) == "org_credential:pagerduty" || strings.TrimSpace(integration.CredentialRef) == "" {
		cred, err = h.credentials.Get(ctx, orgID, models.ProviderPagerDuty)
	} else {
		return "", fmt.Errorf("invalid PagerDuty credential reference")
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("failed to load PagerDuty webhook credential")
	}
	if cfg, ok := cred.Config.(models.PagerDutyConfig); ok {
		return cfg.WebhookSecret, nil
	}
	return "", nil
}

func (h *PagerDutyWebhookHandler) validatePagerDutyRepository(w http.ResponseWriter, r *http.Request, orgID, repoID uuid.UUID) bool {
	if _, err := requireActiveRepo(r.Context(), h.repositories, orgID, repoID); err != nil {
		switch {
		case errors.Is(err, errRepoDisconnected):
			writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected")
		case errors.Is(err, errRepoStoreUnconfigured):
			writeError(w, r, http.StatusServiceUnavailable, "REPOSITORY_LOOKUP_UNAVAILABLE", "repository validation is not configured")
		default:
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "repository was not found in this org")
		}
		return false
	}
	return true
}

func verifyPagerDutyHMAC(secret string, body []byte, signature string) bool {
	signature = strings.TrimPrefix(signature, "sha256=")
	sig, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

func sanitizedPagerDutyWebhookHeaders(headers http.Header) http.Header {
	sanitized := make(http.Header, len(headers))
	for name, values := range headers {
		if pagerDutyWebhookHeaderIsSensitive(name) {
			sanitized[name] = []string{"[REDACTED]"}
			continue
		}
		copied := make([]string, len(values))
		copy(copied, values)
		sanitized[name] = copied
	}
	return sanitized
}

func pagerDutyWebhookHeaderIsSensitive(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return false
	}
	if normalized == "authorization" || normalized == "cookie" || normalized == "set-cookie" {
		return true
	}
	return strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "signature") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "api-key")
}

func pagerDutyWebhookStatusAcceptsInbound(status models.PagerDutyIntegrationStatus) bool {
	return status == models.PagerDutyIntegrationStatusActive || status == models.PagerDutyIntegrationStatusDegraded
}

func pagerDutyDeliveryID(headers http.Header) *string {
	for _, header := range []string{"X-PagerDuty-Webhook-Delivery-ID", "X-PagerDuty-Request-ID", "X-Request-ID"} {
		value := strings.TrimSpace(headers.Get(header))
		if value != "" {
			return &value
		}
	}
	return nil
}

func pagerDutyDeliveryIDString(delivery *models.WebhookDelivery) string {
	if delivery == nil || delivery.DeliveryID == nil {
		return ""
	}
	return strings.TrimSpace(*delivery.DeliveryID)
}

func stringPtrOrNilPagerDutyWebhook(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func firstNonEmptyPagerDutyWebhook(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstStringFromMapPagerDutyWebhook(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
