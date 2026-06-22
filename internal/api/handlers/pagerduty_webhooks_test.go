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

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// pagerDutyV1Signature builds a valid PagerDuty `v1=<hmac>` signature header for
// a body and secret, mirroring how PagerDuty signs webhook deliveries.
func pagerDutyV1Signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyPagerDutyV1Signature(t *testing.T) {
	t.Parallel()

	secret := "this-is-a-signing-secret"
	body := []byte(`{"event":{"id":"evt-1"}}`)
	valid := pagerDutyV1Signature(secret, body)

	require.True(t, verifyPagerDutyV1Signature(secret, body, valid), "a correct v1 signature should verify")
	require.True(t, verifyPagerDutyV1Signature(secret, body, "v1=deadbeef,"+valid), "any matching signature in a rotation list should verify")
	require.False(t, verifyPagerDutyV1Signature("other-secret", body, valid), "a signature under a different secret should not verify")
	require.False(t, verifyPagerDutyV1Signature(secret, []byte(`{"event":{"id":"evt-2"}}`), valid), "a signature over a different body should not verify (body binding)")
	require.False(t, verifyPagerDutyV1Signature(secret, body, "v1=not-hex"), "a malformed signature should not verify")
	require.False(t, verifyPagerDutyV1Signature(secret, body, ""), "an empty signature header should not verify")
}

func TestPagerDutyWebhookHandler_HandlePersistsLedgerAndEnqueuesJob(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	deliveryID := uuid.New()
	eventID := uuid.New()
	secret := "pd-secret"
	payload := `{
		"event": {
			"id": "evt-1",
			"event_type": "incident.triggered",
			"resource_type": "incident",
			"occurred_at": "2026-06-19T12:34:56Z",
			"data": {"id": "PABC123", "title": "API latency", "status": "triggered"}
		}
	}`

	webhooks := &pagerDutyWebhookDeliveryStoreFake{deliveryID: deliveryID}
	events := &pagerDutyWebhookInboundEventStoreFake{eventID: eventID, created: true}
	jobs := &pagerDutyWebhookJobStoreFake{jobID: uuid.New()}
	handler := NewPagerDutyWebhookHandler(PagerDutyWebhookHandlerConfig{
		Webhooks: webhooks,
		Integrations: &pagerDutyWebhookIntegrationStoreFake{integration: models.Integration{
			ID:       genericIntegrationID,
			OrgID:    orgID,
			Provider: models.IntegrationProviderPagerDuty,
			Status:   models.IntegrationStatusActive,
		}},
		PagerDutyIntegrations: &pagerDutyWebhookProviderIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:            pagerDutyIntegrationID,
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			Status:        models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &pagerDutyWebhookCredentialStoreFake{credential: &models.DecryptedCredential{
			Provider: models.ProviderPagerDuty,
			Config:   models.PagerDutyConfig{WebhookSecret: secret},
		}},
		InboundEvents: events,
		Jobs:          jobs,
		RequireSecret: true,
		Logger:        testLoggerPagerDutyWebhook(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pagerduty?integration_id="+genericIntegrationID.String(), stringsReaderPagerDutyWebhook(payload))
	req.Header.Set("X-143-PagerDuty-Secret", secret)
	req.Header.Set("X-PagerDuty-Webhook-Delivery-ID", "delivery-1")
	rec := httptest.NewRecorder()

	handler.Handle(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "PagerDuty webhook should ack after durable enqueue")
	require.Equal(t, "pagerduty", webhooks.delivery.Provider, "webhook delivery should use PagerDuty provider")
	require.Equal(t, genericIntegrationID, webhooks.delivery.IntegrationID, "webhook delivery should link to generic integration")
	require.Equal(t, "delivery-1", *webhooks.delivery.DeliveryID, "webhook delivery should preserve provider delivery id")
	require.Equal(t, "incident.triggered", webhooks.delivery.EventType, "webhook delivery should record event type")
	require.Equal(t, deliveryID, webhooks.processedID, "webhook delivery should be marked processed after event enqueue")
	require.Equal(t, orgID, events.event.OrgID, "inbound event should be org-scoped")
	require.Equal(t, &pagerDutyIntegrationID, events.event.PagerDutyIntegrationID, "inbound event should link to provider integration")
	require.Equal(t, &deliveryID, events.event.WebhookDeliveryID, "inbound event should link to generic webhook delivery")
	require.Equal(t, "evt-1", events.event.ProviderEventID, "inbound event should preserve provider event id")
	require.Equal(t, models.PagerDutyEventIncidentTriggered, events.event.EventType, "inbound event should preserve PagerDuty event type")
	require.Equal(t, "PABC123", *events.event.IncidentID, "inbound event should preserve incident id")
	require.NotContains(t, string(webhooks.delivery.Headers), "pd-secret", "webhook delivery headers should redact PagerDuty shared secret")
	require.NotContains(t, string(events.event.Headers), "pd-secret", "inbound event headers should redact PagerDuty shared secret")
	require.Contains(t, string(webhooks.delivery.Headers), "delivery-1", "webhook delivery headers should retain non-sensitive delivery metadata")
	require.Equal(t, models.JobTypePagerDutyIngestEvent, jobs.jobType, "webhook should enqueue PagerDuty ingest job")
	require.Equal(t, "pagerduty_ingest:"+eventID.String(), *jobs.dedupeKey, "webhook should dedupe worker jobs by inbound event id")
}

func TestPagerDutyWebhookHandler_HandleRetriesExistingDeliveryAndEnqueuesExistingEvent(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	deliveryID := uuid.New()
	eventID := uuid.New()
	secret := "pd-secret"
	payload := `{"event":{"id":"evt-1","event_type":"incident.triggered","resource_type":"incident","data":{"id":"PABC123","title":"API latency","status":"triggered"}}}`
	retryableDelivery := false

	webhooks := &pagerDutyWebhookDeliveryStoreFake{
		deliveryID:          deliveryID,
		createOrGetInserted: &retryableDelivery,
	}
	events := &pagerDutyWebhookInboundEventStoreFake{eventID: eventID, created: false}
	jobs := &pagerDutyWebhookJobStoreFake{jobID: uuid.New()}
	handler := NewPagerDutyWebhookHandler(PagerDutyWebhookHandlerConfig{
		Webhooks: webhooks,
		Integrations: &pagerDutyWebhookIntegrationStoreFake{integration: models.Integration{
			ID:       genericIntegrationID,
			OrgID:    orgID,
			Provider: models.IntegrationProviderPagerDuty,
			Status:   models.IntegrationStatusActive,
		}},
		PagerDutyIntegrations: &pagerDutyWebhookProviderIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:            pagerDutyIntegrationID,
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			Status:        models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &pagerDutyWebhookCredentialStoreFake{credential: &models.DecryptedCredential{
			Provider: models.ProviderPagerDuty,
			Config:   models.PagerDutyConfig{WebhookSecret: secret},
		}},
		InboundEvents: events,
		Jobs:          jobs,
		RequireSecret: true,
		Logger:        testLoggerPagerDutyWebhook(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pagerduty?integration_id="+genericIntegrationID.String(), stringsReaderPagerDutyWebhook(payload))
	req.Header.Set("X-143-PagerDuty-Secret", secret)
	req.Header.Set("X-PagerDuty-Webhook-Delivery-ID", "delivery-1")
	rec := httptest.NewRecorder()

	handler.Handle(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "PagerDuty retry should ack after enqueueing the existing inbound event")
	require.Equal(t, models.JobTypePagerDutyIngestEvent, jobs.jobType, "retryable duplicate delivery should enqueue the existing event")
	require.Equal(t, "pagerduty_ingest:"+eventID.String(), *jobs.dedupeKey, "retryable duplicate delivery should dedupe by the existing event id")
	require.Equal(t, deliveryID, webhooks.processedID, "retryable duplicate delivery should be marked processed after enqueue")
	require.Equal(t, uuid.Nil, webhooks.ignoredID, "retryable duplicate delivery should not be marked ignored")
}

func TestPagerDutyWebhookHandler_HandleVerifiesSelectedInstallCredential(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	credentialID := uuid.New()
	eventID := uuid.New()
	payload := `{"event":{"id":"evt-1","event_type":"incident.triggered","resource_type":"incident","data":{"id":"PABC123","title":"API latency","status":"triggered"}}}`

	webhooks := &pagerDutyWebhookDeliveryStoreFake{deliveryID: uuid.New()}
	events := &pagerDutyWebhookInboundEventStoreFake{eventID: eventID, created: true}
	jobs := &pagerDutyWebhookJobStoreFake{jobID: uuid.New()}
	credentials := &pagerDutyWebhookCredentialStoreFake{
		credential: &models.DecryptedCredential{
			Provider: models.ProviderPagerDuty,
			Config:   models.PagerDutyConfig{WebhookSecret: "wrong-org-wide-secret"},
		},
		credentialByID: &models.DecryptedCredential{
			ID:       credentialID,
			OrgID:    orgID,
			Provider: models.ProviderPagerDuty,
			Config:   models.PagerDutyConfig{WebhookSecret: "install-secret"},
		},
	}
	handler := NewPagerDutyWebhookHandler(PagerDutyWebhookHandlerConfig{
		Webhooks: webhooks,
		Integrations: &pagerDutyWebhookIntegrationStoreFake{integration: models.Integration{
			ID:       genericIntegrationID,
			OrgID:    orgID,
			Provider: models.IntegrationProviderPagerDuty,
			Status:   models.IntegrationStatusActive,
		}},
		PagerDutyIntegrations: &pagerDutyWebhookProviderIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:            pagerDutyIntegrationID,
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			CredentialRef: "org_credential:" + credentialID.String(),
			Status:        models.PagerDutyIntegrationStatusActive,
		}},
		Credentials:   credentials,
		InboundEvents: events,
		Jobs:          jobs,
		RequireSecret: true,
		Logger:        testLoggerPagerDutyWebhook(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pagerduty?integration_id="+genericIntegrationID.String()+"&pagerduty_integration_id="+pagerDutyIntegrationID.String(), stringsReaderPagerDutyWebhook(payload))
	req.Header.Set("X-143-PagerDuty-Secret", "install-secret")
	rec := httptest.NewRecorder()

	handler.Handle(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "webhook should verify against the selected provider integration credential")
	require.Equal(t, credentialID, credentials.getByID, "webhook should load the credential referenced by the provider integration")
	require.Equal(t, pagerDutyIntegrationID, *events.event.PagerDutyIntegrationID, "webhook should persist the selected provider integration id")
	require.Equal(t, models.JobTypePagerDutyIngestEvent, jobs.jobType, "webhook should enqueue ingestion after install-scoped verification")
}

func TestPagerDutyWebhookHandler_HandleRejectsInvalidSecret(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	payload := `{"event":{"id":"evt-1","event_type":"incident.triggered","data":{"id":"PABC123"}}}`
	webhooks := &pagerDutyWebhookDeliveryStoreFake{}
	handler := NewPagerDutyWebhookHandler(PagerDutyWebhookHandlerConfig{
		Webhooks: webhooks,
		Integrations: &pagerDutyWebhookIntegrationStoreFake{integration: models.Integration{
			ID:       genericIntegrationID,
			OrgID:    orgID,
			Provider: models.IntegrationProviderPagerDuty,
			Status:   models.IntegrationStatusActive,
		}},
		PagerDutyIntegrations: &pagerDutyWebhookProviderIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:            uuid.New(),
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			Status:        models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &pagerDutyWebhookCredentialStoreFake{credential: &models.DecryptedCredential{
			Provider: models.ProviderPagerDuty,
			Config:   models.PagerDutyConfig{WebhookSecret: "expected"},
		}},
		InboundEvents: &pagerDutyWebhookInboundEventStoreFake{},
		Jobs:          &pagerDutyWebhookJobStoreFake{},
		RequireSecret: true,
		Logger:        testLoggerPagerDutyWebhook(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pagerduty?integration_id="+genericIntegrationID.String(), stringsReaderPagerDutyWebhook(payload))
	req.Header.Set("X-143-PagerDuty-Secret", "wrong")
	rec := httptest.NewRecorder()

	handler.Handle(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code, "PagerDuty webhook should reject invalid shared secret")
	require.Equal(t, uuid.Nil, webhooks.delivery.ID, "invalid secret should not persist a webhook delivery")
}

func TestPagerDutyWebhookHandler_HandleAcceptsDegradedProviderIntegration(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	secret := "pd-secret"
	payload := `{"event":{"id":"evt-1","event_type":"incident.triggered","data":{"id":"PABC123","title":"API latency","status":"triggered"}}}`
	webhooks := &pagerDutyWebhookDeliveryStoreFake{deliveryID: uuid.New()}
	events := &pagerDutyWebhookInboundEventStoreFake{eventID: uuid.New(), created: true}
	jobs := &pagerDutyWebhookJobStoreFake{jobID: uuid.New()}
	handler := NewPagerDutyWebhookHandler(PagerDutyWebhookHandlerConfig{
		Webhooks: webhooks,
		Integrations: &pagerDutyWebhookIntegrationStoreFake{integration: models.Integration{
			ID:       genericIntegrationID,
			OrgID:    orgID,
			Provider: models.IntegrationProviderPagerDuty,
			Status:   models.IntegrationStatusActive,
		}},
		PagerDutyIntegrations: &pagerDutyWebhookProviderIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:            pagerDutyIntegrationID,
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			Status:        models.PagerDutyIntegrationStatusDegraded,
		}},
		Credentials: &pagerDutyWebhookCredentialStoreFake{credential: &models.DecryptedCredential{
			Provider: models.ProviderPagerDuty,
			Config:   models.PagerDutyConfig{WebhookSecret: secret},
		}},
		InboundEvents: events,
		Jobs:          jobs,
		RequireSecret: true,
		Logger:        testLoggerPagerDutyWebhook(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pagerduty?integration_id="+genericIntegrationID.String(), stringsReaderPagerDutyWebhook(payload))
	req.Header.Set("X-143-PagerDuty-Secret", secret)
	rec := httptest.NewRecorder()

	handler.Handle(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "signed webhooks should still ingest for degraded PagerDuty integrations")
	require.Equal(t, models.JobTypePagerDutyIngestEvent, jobs.jobType, "degraded integration webhooks should still enqueue ingestion")
	require.Equal(t, pagerDutyIntegrationID, *events.event.PagerDutyIntegrationID, "degraded integration webhook should preserve provider integration id")
}

func TestPagerDutyWebhookHandler_HandleStartSessionActionStartsMappedIncidentSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	repositoryID := uuid.New()
	sessionID := uuid.New()
	secret := "pd-secret"
	baseBranch := "release"
	payload := `{"incident":{"id":"PABC123"},"message":"Investigate from PagerDuty"}`
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: pagerDutyIntegrationID,
		IncidentID:             "PABC123",
		Title:                  "API latency",
		ServiceID:              strPtrPagerDutyIntegrationTest("PSVC"),
	}
	starter := &pagerDutySessionStarterFake{session: models.Session{ID: sessionID, OrgID: orgID}}
	webhooks := &pagerDutyWebhookDeliveryStoreFake{deliveryID: uuid.New()}
	incidents := &pagerDutyIncidentStoreFake{incident: incident}
	handler := NewPagerDutyWebhookHandler(PagerDutyWebhookHandlerConfig{
		Webhooks: webhooks,
		Integrations: &pagerDutyWebhookIntegrationStoreFake{integration: models.Integration{
			ID:       genericIntegrationID,
			OrgID:    orgID,
			Provider: models.IntegrationProviderPagerDuty,
			Status:   models.IntegrationStatusActive,
		}},
		PagerDutyIntegrations: &pagerDutyWebhookProviderIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:            pagerDutyIntegrationID,
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			Status:        models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &pagerDutyWebhookCredentialStoreFake{credential: &models.DecryptedCredential{
			Provider: models.ProviderPagerDuty,
			Config:   models.PagerDutyConfig{WebhookSecret: secret},
		}},
		InboundEvents: &pagerDutyWebhookInboundEventStoreFake{},
		Jobs:          &pagerDutyWebhookJobStoreFake{},
		Incidents:     incidents,
		Mappings: &pagerDutyMappingStoreFake{mapping: models.PagerDutyServiceRepoMapping{
			OrgID:                  orgID,
			PagerDutyIntegrationID: pagerDutyIntegrationID,
			PagerDutyServiceID:     "PSVC",
			RepositoryID:           repositoryID,
			BaseBranch:             &baseBranch,
			Enabled:                true,
		}},
		SessionStarter: starter,
		RequireSecret:  true,
		Logger:         testLoggerPagerDutyWebhook(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pagerduty/start-session?integration_id="+genericIntegrationID.String(), stringsReaderPagerDutyWebhook(payload))
	signature := pagerDutyV1Signature(secret, []byte(payload))
	req.Header.Set("X-PagerDuty-Signature", signature)
	rec := httptest.NewRecorder()

	handler.HandleStartSessionAction(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "PagerDuty custom action should create a session for the mirrored incident")
	require.Equal(t, orgID, starter.input.OrgID, "custom action should start the session in the integration org")
	require.Equal(t, pagerDutyIntegrationID, incidents.getIntegrationID, "custom action should scope incident lookup to the selected PagerDuty integration")
	require.Equal(t, incident.IncidentID, starter.input.Incident.IncidentID, "custom action should use the mirrored incident context")
	require.Equal(t, repositoryID, starter.input.RepositoryID, "custom action should resolve the mapped repository")
	require.Equal(t, &baseBranch, starter.input.BaseBranch, "custom action should use the mapped base branch")
	require.Equal(t, "Investigate from PagerDuty", starter.input.Message, "custom action should pass the responder message")
	require.Equal(t, "incident.action.start_session", webhooks.delivery.EventType, "custom action should record a webhook delivery ledger row")
	require.NotContains(t, string(webhooks.delivery.Headers), "pd-secret", "custom action delivery headers should redact PagerDuty shared secret")
	require.NotContains(t, string(webhooks.delivery.Headers), signature, "custom action delivery headers should redact PagerDuty signatures")
	require.Equal(t, webhooks.delivery.ID, webhooks.processedID, "custom action should mark the delivery processed after session start")
	var resp models.SingleResponse[models.Session]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "custom action response should be valid JSON")
	require.Equal(t, sessionID, resp.Data.ID, "custom action should return the created session")
}

func TestPagerDutyWebhookHandler_HandleStartSessionActionRetriesExistingDelivery(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	repositoryID := uuid.New()
	sessionID := uuid.New()
	secret := "pd-secret"
	payload := `{"incident":{"id":"PABC123"},"message":"Investigate from PagerDuty"}`
	retryableDelivery := false
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: pagerDutyIntegrationID,
		IncidentID:             "PABC123",
		Title:                  "API latency",
		ServiceID:              strPtrPagerDutyIntegrationTest("PSVC"),
	}
	starter := &pagerDutySessionStarterFake{session: models.Session{ID: sessionID, OrgID: orgID}}
	webhooks := &pagerDutyWebhookDeliveryStoreFake{
		deliveryID:          uuid.New(),
		createOrGetInserted: &retryableDelivery,
	}
	handler := NewPagerDutyWebhookHandler(PagerDutyWebhookHandlerConfig{
		Webhooks: webhooks,
		Integrations: &pagerDutyWebhookIntegrationStoreFake{integration: models.Integration{
			ID:       genericIntegrationID,
			OrgID:    orgID,
			Provider: models.IntegrationProviderPagerDuty,
			Status:   models.IntegrationStatusActive,
		}},
		PagerDutyIntegrations: &pagerDutyWebhookProviderIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:            pagerDutyIntegrationID,
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			Status:        models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &pagerDutyWebhookCredentialStoreFake{credential: &models.DecryptedCredential{
			Provider: models.ProviderPagerDuty,
			Config:   models.PagerDutyConfig{WebhookSecret: secret},
		}},
		InboundEvents: &pagerDutyWebhookInboundEventStoreFake{},
		Jobs:          &pagerDutyWebhookJobStoreFake{},
		Incidents:     &pagerDutyIncidentStoreFake{incident: incident},
		Mappings: &pagerDutyMappingStoreFake{mapping: models.PagerDutyServiceRepoMapping{
			OrgID:                  orgID,
			PagerDutyIntegrationID: pagerDutyIntegrationID,
			PagerDutyServiceID:     "PSVC",
			RepositoryID:           repositoryID,
			Enabled:                true,
		}},
		SessionStarter: starter,
		RequireSecret:  true,
		Logger:         testLoggerPagerDutyWebhook(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pagerduty/start-session?integration_id="+genericIntegrationID.String(), stringsReaderPagerDutyWebhook(payload))
	req.Header.Set("X-143-PagerDuty-Secret", secret)
	req.Header.Set("X-PagerDuty-Webhook-Delivery-ID", "delivery-start-1")
	rec := httptest.NewRecorder()

	handler.HandleStartSessionAction(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "PagerDuty action retry should start the mapped incident session")
	require.Equal(t, repositoryID, starter.input.RepositoryID, "retryable action delivery should resolve the mapped repository")
	require.Equal(t, "PABC123", starter.input.Incident.IncidentID, "retryable action delivery should use the mirrored incident")
	require.Equal(t, webhooks.delivery.ID, webhooks.processedID, "retryable action delivery should be marked processed after session start")
}

func TestPagerDutyWebhookHandler_HandleStartSessionActionIngestsIncidentPayloadWhenMirrorMissing(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	genericIntegrationID := uuid.New()
	pagerDutyIntegrationID := uuid.New()
	repositoryID := uuid.New()
	issueID := uuid.New()
	sessionID := uuid.New()
	secret := "pd-secret"
	payload := `{
		"incident": {
			"id": "PNEW123",
			"incident_number": 456,
			"html_url": "https://example.pagerduty.com/incidents/PNEW123",
			"title": "Checkout API latency",
			"status": "triggered",
			"urgency": "high",
			"service": {"id": "PSVC", "summary": "Checkout API"}
		},
		"message": "Investigate from PagerDuty"
	}`
	starter := &pagerDutySessionStarterFake{session: models.Session{ID: sessionID, OrgID: orgID}}
	webhooks := &pagerDutyWebhookDeliveryStoreFake{deliveryID: uuid.New()}
	incidents := &pagerDutyIncidentStoreFake{getErr: pgx.ErrNoRows}
	ingester := &pagerDutyWebhookIssueIngesterFake{model: &models.Issue{ID: issueID, OrgID: orgID}}
	statuses := &pagerDutyWebhookIssueStatusUpdaterFake{}
	handler := NewPagerDutyWebhookHandler(PagerDutyWebhookHandlerConfig{
		Webhooks: webhooks,
		Integrations: &pagerDutyWebhookIntegrationStoreFake{integration: models.Integration{
			ID:       genericIntegrationID,
			OrgID:    orgID,
			Provider: models.IntegrationProviderPagerDuty,
			Status:   models.IntegrationStatusActive,
		}},
		PagerDutyIntegrations: &pagerDutyWebhookProviderIntegrationStoreFake{integration: models.PagerDutyIntegration{
			ID:            pagerDutyIntegrationID,
			OrgID:         orgID,
			IntegrationID: &genericIntegrationID,
			Status:        models.PagerDutyIntegrationStatusActive,
		}},
		Credentials: &pagerDutyWebhookCredentialStoreFake{credential: &models.DecryptedCredential{
			Provider: models.ProviderPagerDuty,
			Config:   models.PagerDutyConfig{WebhookSecret: secret},
		}},
		InboundEvents: &pagerDutyWebhookInboundEventStoreFake{},
		Jobs:          &pagerDutyWebhookJobStoreFake{},
		Incidents:     incidents,
		IssueIngester: ingester,
		IssueStatuses: statuses,
		Mappings: &pagerDutyMappingStoreFake{mapping: models.PagerDutyServiceRepoMapping{
			OrgID:                  orgID,
			PagerDutyIntegrationID: pagerDutyIntegrationID,
			PagerDutyServiceID:     "PSVC",
			RepositoryID:           repositoryID,
			Enabled:                true,
		}},
		SessionStarter: starter,
		RequireSecret:  true,
		Logger:         testLoggerPagerDutyWebhook(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/pagerduty/start-session?integration_id="+genericIntegrationID.String(), stringsReaderPagerDutyWebhook(payload))
	req.Header.Set("X-143-PagerDuty-Secret", secret)
	req.Header.Set("X-PagerDuty-Webhook-Delivery-ID", "delivery-start-1")
	rec := httptest.NewRecorder()

	handler.HandleStartSessionAction(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "custom action should ingest the incident payload and create a session")
	require.Equal(t, orgID, ingester.orgID, "custom action ingestion should be org-scoped")
	require.Equal(t, "PNEW123", ingester.normalized.ExternalID, "custom action ingestion should normalize the incident id")
	require.Equal(t, models.IssueStatusOpen, statuses.status, "custom action ingestion should update the issue status")
	require.Equal(t, issueID, statuses.issueID, "custom action ingestion should update the ingested issue")
	require.Equal(t, "PNEW123", incidents.upsertIncident.IncidentID, "custom action should upsert the PagerDuty incident mirror")
	require.NotNil(t, incidents.upsertIncident.IssueID, "upserted incident mirror should link to the ingested issue")
	require.Equal(t, issueID, *incidents.upsertIncident.IssueID, "upserted incident mirror should link to the ingested issue id")
	require.Equal(t, "PNEW123", starter.input.Incident.IncidentID, "custom action should start from the freshly ingested incident")
	require.Equal(t, issueID, *starter.input.Incident.IssueID, "custom action should carry the issue link into the session starter")
	require.Equal(t, "delivery-start-1", starter.input.ProviderEventID, "custom action should pass the delivery id as provider event id")
}

type pagerDutyWebhookIntegrationStoreFake struct {
	integration models.Integration
}

func (s *pagerDutyWebhookIntegrationStoreFake) GetByID(_ context.Context, id uuid.UUID) (models.Integration, error) {
	if s.integration.ID != id {
		return models.Integration{}, errPagerDutyWebhookTestUnexpectedLookup
	}
	return s.integration, nil
}

type pagerDutyWebhookProviderIntegrationStoreFake struct {
	integration models.PagerDutyIntegration
}

func (s *pagerDutyWebhookProviderIntegrationStoreFake) GetByID(_ context.Context, orgID, id uuid.UUID) (models.PagerDutyIntegration, error) {
	if s.integration.OrgID != orgID || s.integration.ID != id {
		return models.PagerDutyIntegration{}, errPagerDutyWebhookTestUnexpectedLookup
	}
	return s.integration, nil
}

func (s *pagerDutyWebhookProviderIntegrationStoreFake) GetByIntegrationID(_ context.Context, orgID, integrationID uuid.UUID) (models.PagerDutyIntegration, error) {
	if s.integration.OrgID != orgID || s.integration.IntegrationID == nil || *s.integration.IntegrationID != integrationID {
		return models.PagerDutyIntegration{}, errPagerDutyWebhookTestUnexpectedLookup
	}
	return s.integration, nil
}

type pagerDutyWebhookCredentialStoreFake struct {
	credential     *models.DecryptedCredential
	credentialByID *models.DecryptedCredential
	getByID        uuid.UUID
}

func (s *pagerDutyWebhookCredentialStoreFake) Get(_ context.Context, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if provider != models.ProviderPagerDuty {
		return nil, errPagerDutyWebhookTestUnexpectedLookup
	}
	return s.credential, nil
}

func (s *pagerDutyWebhookCredentialStoreFake) GetByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error) {
	s.getByID = id
	if s.credentialByID != nil {
		return s.credentialByID, nil
	}
	return s.credential, nil
}

type pagerDutyWebhookDeliveryStoreFake struct {
	deliveryID          uuid.UUID
	createOrGetInserted *bool
	delivery            models.WebhookDelivery
	processedID         uuid.UUID
	ignoredID           uuid.UUID
}

func (s *pagerDutyWebhookDeliveryStoreFake) CreateOrGet(_ context.Context, delivery *models.WebhookDelivery) (bool, error) {
	if s.deliveryID == uuid.Nil {
		s.deliveryID = uuid.New()
	}
	delivery.ID = s.deliveryID
	s.delivery = *delivery
	if s.createOrGetInserted != nil {
		return *s.createOrGetInserted, nil
	}
	return true, nil
}

func (s *pagerDutyWebhookDeliveryStoreFake) MarkProcessed(_ context.Context, delivery *models.WebhookDelivery, _ *string) error {
	s.processedID = delivery.ID
	return nil
}

func (s *pagerDutyWebhookDeliveryStoreFake) MarkIgnored(_ context.Context, delivery *models.WebhookDelivery) error {
	s.ignoredID = delivery.ID
	return nil
}

type pagerDutyWebhookInboundEventStoreFake struct {
	eventID uuid.UUID
	created bool
	event   models.PagerDutyInboundEvent
}

func (s *pagerDutyWebhookInboundEventStoreFake) CreateOrGet(_ context.Context, event *models.PagerDutyInboundEvent) (bool, error) {
	if s.eventID == uuid.Nil {
		s.eventID = uuid.New()
	}
	event.ID = s.eventID
	s.event = *event
	return s.created, nil
}

type pagerDutyWebhookJobStoreFake struct {
	jobID     uuid.UUID
	jobType   string
	payload   any
	dedupeKey *string
}

func (s *pagerDutyWebhookJobStoreFake) Enqueue(_ context.Context, _ uuid.UUID, _ string, jobType string, payload any, _ int, dedupeKey *string) (uuid.UUID, error) {
	if s.jobID == uuid.Nil {
		s.jobID = uuid.New()
	}
	s.jobType = jobType
	s.payload = payload
	s.dedupeKey = dedupeKey
	return s.jobID, nil
}

type pagerDutyWebhookTestError struct{}

func (pagerDutyWebhookTestError) Error() string { return "unexpected pagerduty webhook test lookup" }

var errPagerDutyWebhookTestUnexpectedLookup = pagerDutyWebhookTestError{}

func stringsReaderPagerDutyWebhook(s string) *strings.Reader {
	return strings.NewReader(s)
}

func testLoggerPagerDutyWebhook() zerolog.Logger {
	return zerolog.Nop()
}

type pagerDutyWebhookIssueIngesterFake struct {
	orgID      uuid.UUID
	normalized ingestion.NormalizedIssue
	model      *models.Issue
	err        error
}

func (s *pagerDutyWebhookIssueIngesterFake) IngestNormalized(_ context.Context, orgID uuid.UUID, issue ingestion.NormalizedIssue) (*models.Issue, error) {
	s.orgID = orgID
	s.normalized = issue
	if s.err != nil {
		return nil, s.err
	}
	if s.model != nil {
		return s.model, nil
	}
	return &models.Issue{ID: uuid.New(), OrgID: orgID}, nil
}

type pagerDutyWebhookIssueStatusUpdaterFake struct {
	orgID   uuid.UUID
	issueID uuid.UUID
	status  models.IssueStatus
	err     error
}

func (s *pagerDutyWebhookIssueStatusUpdaterFake) UpdateStatus(_ context.Context, orgID, issueID uuid.UUID, status models.IssueStatus) error {
	s.orgID = orgID
	s.issueID = issueID
	s.status = status
	return s.err
}
