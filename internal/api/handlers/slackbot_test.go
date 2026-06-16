package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type slackbotInstallStoreStub struct {
	install                         models.SlackInstallation
	installsByOrg                   map[uuid.UUID]models.SlackInstallation
	requestedOrgID                  uuid.UUID
	err                             error
	getActiveByTeamAppCalled        bool
	markLastEventCalled             bool
	markDisconnectedByTeamAppCalled bool
	disconnectedTeamID              string
	disconnectedAPIAppID            string
}

func (s *slackbotInstallStoreStub) GetActiveByTeamApp(ctx context.Context, teamID, apiAppID string) (models.SlackInstallation, error) {
	s.getActiveByTeamAppCalled = true
	return s.install, s.err
}

func (s *slackbotInstallStoreStub) GetActiveByOrgTeamApp(ctx context.Context, orgID uuid.UUID, teamID, apiAppID string) (models.SlackInstallation, error) {
	s.requestedOrgID = orgID
	if s.err != nil {
		return models.SlackInstallation{}, s.err
	}
	if s.installsByOrg != nil {
		if install, ok := s.installsByOrg[orgID]; ok {
			return install, nil
		}
	}
	return s.install, nil
}

func (s *slackbotInstallStoreStub) MarkDisconnected(ctx context.Context, orgID, installationID uuid.UUID) error {
	return s.err
}

func (s *slackbotInstallStoreStub) MarkDisconnectedByTeamApp(ctx context.Context, teamID, apiAppID string) error {
	s.markDisconnectedByTeamAppCalled = true
	s.disconnectedTeamID = teamID
	s.disconnectedAPIAppID = apiAppID
	return s.err
}

func (s *slackbotInstallStoreStub) MarkLastEvent(ctx context.Context, orgID, installationID uuid.UUID) error {
	s.markLastEventCalled = true
	return s.err
}

type slackbotInboundStoreStub struct {
	event           models.SlackInboundEvent
	err             error
	duplicate       bool // when true, CreateReceived simulates a duplicate (returns false)
	existingEventID uuid.UUID
}

func (s *slackbotInboundStoreStub) CreateReceived(ctx context.Context, event *models.SlackInboundEvent) (bool, error) {
	s.event = *event
	if s.event.ID == uuid.Nil {
		s.event.ID = uuid.New()
	}
	if s.duplicate {
		if s.existingEventID != uuid.Nil {
			s.event.ID = s.existingEventID
		}
		*event = s.event
		return false, s.err
	}
	*event = s.event
	return true, s.err
}

func (s *slackbotInboundStoreStub) MarkEnqueued(ctx context.Context, orgID, eventID, jobID uuid.UUID) error {
	return nil
}

type slackbotWebhookDeliveryStoreStub struct {
	delivery            models.WebhookDelivery
	inserted            bool
	err                 error
	markProcessedCalled bool
	markIgnoredCalled   bool
	markErr             *string
}

func (s *slackbotWebhookDeliveryStoreStub) CreateOrGet(ctx context.Context, delivery *models.WebhookDelivery) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if s.delivery.ID != uuid.Nil {
		delivery.ID = s.delivery.ID
	}
	if delivery.ID == uuid.Nil {
		delivery.ID = uuid.New()
	}
	if s.delivery.Status != "" {
		delivery.Status = s.delivery.Status
	}
	if s.delivery.Attempts != 0 {
		delivery.Attempts = s.delivery.Attempts
	}
	s.delivery = *delivery
	return s.inserted, nil
}

func (s *slackbotWebhookDeliveryStoreStub) MarkProcessed(ctx context.Context, delivery *models.WebhookDelivery, errMsg *string) error {
	s.markProcessedCalled = true
	s.markErr = errMsg
	return nil
}

func (s *slackbotWebhookDeliveryStoreStub) MarkIgnored(ctx context.Context, delivery *models.WebhookDelivery) error {
	s.markIgnoredCalled = true
	return nil
}

type slackbotJobStoreStub struct {
	jobType string
	payload any
	orgID   uuid.UUID
}

func (s *slackbotJobStoreStub) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	s.orgID = orgID
	s.jobType = jobType
	s.payload = payload
	return uuid.New(), nil
}

type slackbotOrgSelectionStoreStub struct {
	selection models.SlackOrgSelection
	err       error
	called    bool
}

func (s *slackbotOrgSelectionStoreStub) GetBySlackUser(ctx context.Context, teamID, apiAppID, slackUserID string) (models.SlackOrgSelection, error) {
	s.called = true
	return s.selection, s.err
}

func TestSlackbotHandler_EventsVerifiesSignatureAndEnqueuesMention(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	body := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev123","event":{"type":"app_mention","channel":"C123","user":"U999","text":"<@U143> fix this","ts":"1710000001.000000","thread_ts":"1710000000.000000"}}`)
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "events endpoint should acknowledge valid Slack callbacks")
	require.Equal(t, "slack_start_or_continue_session", jobs.jobType, "app mention should enqueue the Slack session job")
	require.Equal(t, orgID, jobs.orgID, "job should be enqueued in the installation org")
	payload, ok := jobs.payload.(models.SlackStartSessionJobPayload)
	require.True(t, ok, "job payload should use the typed Slack start-session payload")
	require.Equal(t, "C123", payload.ChannelID, "payload should include Slack channel")
	require.Equal(t, "1710000000.000000", payload.ThreadTS, "payload should thread by root timestamp")
	require.Equal(t, "U999", payload.SlackUserID, "payload should carry triggering Slack user")
	require.Equal(t, models.SlackInboundEventTypeAppMention, inbound.event.EventType, "inbound event should persist the Slack event type")
	require.Equal(t, "slack", deliveries.delivery.Provider, "Slack callback should create a generic webhook delivery")
	require.Equal(t, "Ev123", *deliveries.delivery.DeliveryID, "webhook delivery should use the Slack event id for idempotency")
	require.Equal(t, string(models.SlackInboundEventTypeAppMention), deliveries.delivery.EventType, "webhook delivery should record the Slack event type")
	require.NotNil(t, inbound.event.WebhookDeliveryID, "inbound event should link back to the webhook delivery")
	require.Equal(t, deliveries.delivery.ID, *inbound.event.WebhookDeliveryID, "inbound event should point at the created webhook delivery")
	require.True(t, deliveries.markProcessedCalled, "accepted Slack event should mark the delivery processed after enqueue")
}

func TestSlackbotHandler_EventsRejectsInvalidSignature(t *testing.T) {
	t.Parallel()

	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"}, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/slack/events", bytes.NewReader([]byte(`{"type":"event_callback"}`)))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("X-Slack-Signature", "v0=bad")
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, "invalid Slack signatures should be rejected before parsing")
}

func TestSlackbotHandler_EventsReturnsURLVerificationChallenge(t *testing.T) {
	t.Parallel()

	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"}, nil, nil, nil)
	body, err := json.Marshal(map[string]string{"type": "url_verification", "challenge": "abc123"})
	require.NoError(t, err, "test body should marshal")
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "url verification should be acknowledged inline")
	require.JSONEq(t, `{"challenge":"abc123"}`, rr.Body.String(), "url verification response should echo challenge")
}

func TestSlackbotHandler_CommandsPersistsAndEnqueues(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	body := []byte("team_id=T123&api_app_id=A123&channel_id=C123&user_id=U999&command=%2F143&text=fix+this&trigger_id=trig-1")
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Commands(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "command endpoint should acknowledge valid Slack commands")
	require.Equal(t, "slack_start_or_continue_session", jobs.jobType, "slash command should enqueue Slack session job")
	require.Equal(t, models.SlackInboundEventTypeSlashCommand, inbound.event.EventType, "slash command should persist inbound metadata")
	payload, ok := jobs.payload.(models.SlackStartSessionJobPayload)
	require.True(t, ok, "slash command should enqueue typed start-session payload")
	require.Equal(t, "fix this", payload.Text, "slash command payload should include command text")
}

func TestSlackbotHandler_CommandsUsesSelectedOrgInstallation(t *testing.T) {
	t.Parallel()

	defaultOrgID := uuid.New()
	selectedOrgID := uuid.New()
	selectedInstallationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
	installations := &slackbotInstallStoreStub{
		install: models.SlackInstallation{
			ID:       uuid.New(),
			OrgID:    defaultOrgID,
			TeamID:   "T123",
			APIAppID: "A123",
			Status:   models.SlackInstallationStatusActive,
		},
		installsByOrg: map[uuid.UUID]models.SlackInstallation{
			selectedOrgID: {
				ID:       selectedInstallationID,
				OrgID:    selectedOrgID,
				TeamID:   "T123",
				APIAppID: "A123",
				Status:   models.SlackInstallationStatusActive,
			},
		},
	}
	orgSelections := &slackbotOrgSelectionStoreStub{selection: models.SlackOrgSelection{
		OrgID:       selectedOrgID,
		SlackTeamID: "T123",
		APIAppID:    "A123",
		SlackUserID: "U999",
	}}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		installations,
		inbound,
		jobs,
	)
	handler.SetOrgSelectionStore(orgSelections)
	handler.SetWebhookDeliveries(deliveries)

	body := []byte("team_id=T123&api_app_id=A123&channel_id=C123&user_id=U999&command=%2F143&text=fix+this&trigger_id=trig-selected")
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Commands(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "command endpoint should acknowledge valid Slack commands")
	require.True(t, orgSelections.called, "command routing should consult the Slack user's selected org")
	require.Equal(t, selectedOrgID, installations.requestedOrgID, "command routing should load the installation for the selected org")
	require.Equal(t, selectedOrgID, jobs.orgID, "command job should be enqueued in the selected org")
	require.Equal(t, selectedOrgID, inbound.event.OrgID, "inbound command should persist under the selected org")
	payload, ok := jobs.payload.(models.SlackStartSessionJobPayload)
	require.True(t, ok, "command should enqueue typed start-session payload")
	require.Equal(t, selectedInstallationID.String(), payload.SlackInstallationID, "job payload should carry the selected org installation")
}

func TestSlackbotHandler_InteractionsPersistsAndEnqueues(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	payload := `{"type":"block_actions","api_app_id":"A123","trigger_id":"trig-1","team":{"id":"T123"},"user":{"id":"U999"},"channel":{"id":"C123"},"message":{"ts":"1710000001.000000"},"view":{"id":"V123"},"actions":[{"action_id":"slack_open_session","value":"session-1"}]}`
	body := []byte("payload=" + url.QueryEscape(payload))
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Interactions(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "interaction endpoint should acknowledge valid Slack interactions")
	require.Equal(t, "slack_handle_interaction", jobs.jobType, "interaction should enqueue handler job")
	require.Equal(t, models.SlackInboundEventTypeInteraction, inbound.event.EventType, "interaction should persist inbound metadata")
	interactionPayload, ok := jobs.payload.(models.SlackInteractionJobPayload)
	require.True(t, ok, "interaction should enqueue typed interaction payload")
	require.Equal(t, "slack_open_session", interactionPayload.ActionID, "interaction payload should include action id")
	require.Equal(t, "trig-1", interactionPayload.TriggerID, "interaction payload should include trigger id for modal actions")
	require.Equal(t, "V123", interactionPayload.ViewID, "interaction payload should include view id for modal updates")
}

func TestSlackbotHandler_SignedFixtureSmoke(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		endpoint        string
		body            []byte
		call            func(*SlackbotHandler, http.ResponseWriter, *http.Request)
		expectedJobType string
		expectedEvent   models.SlackInboundEventType
	}{
		{
			name:            "events api app mention",
			endpoint:        "/api/v1/webhooks/slack/events",
			body:            []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev-smoke","event":{"type":"app_mention","channel":"C123","user":"U999","text":"<@U143> fix this","ts":"1710000001.000000"}}`),
			call:            (*SlackbotHandler).Events,
			expectedJobType: "slack_start_or_continue_session",
			expectedEvent:   models.SlackInboundEventTypeAppMention,
		},
		{
			name:            "slash command",
			endpoint:        "/api/v1/webhooks/slack/commands",
			body:            []byte("team_id=T123&api_app_id=A123&channel_id=C123&user_id=U999&command=%2F143&text=fix+this&trigger_id=trig-smoke"),
			call:            (*SlackbotHandler).Commands,
			expectedJobType: "slack_start_or_continue_session",
			expectedEvent:   models.SlackInboundEventTypeSlashCommand,
		},
		{
			name:            "interaction callback",
			endpoint:        "/api/v1/webhooks/slack/interactions",
			body:            []byte("payload=" + url.QueryEscape(`{"type":"block_actions","api_app_id":"A123","trigger_id":"trig-smoke","team":{"id":"T123"},"user":{"id":"U999"},"channel":{"id":"C123"},"message":{"ts":"1710000001.000000"},"actions":[{"action_id":"slack_open_session","value":"session-1"}]}`)),
			call:            (*SlackbotHandler).Interactions,
			expectedJobType: "slack_handle_interaction",
			expectedEvent:   models.SlackInboundEventTypeInteraction,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			installationID := uuid.New()
			inbound := &slackbotInboundStoreStub{}
			jobs := &slackbotJobStoreStub{}
			deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
			handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
				&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
				inbound,
				jobs,
			)
			handler.SetWebhookDeliveries(deliveries)
			req := signedSlackRequest(t, tt.body, "secret", time.Now())
			req.URL.Path = tt.endpoint
			rr := httptest.NewRecorder()

			tt.call(handler, rr, req)

			require.Equal(t, http.StatusOK, rr.Code, "signed Slack fixture should be acknowledged")
			require.Equal(t, tt.expectedJobType, jobs.jobType, "signed Slack fixture should enqueue expected job type")
			require.Equal(t, tt.expectedEvent, inbound.event.EventType, "signed Slack fixture should persist expected event type")
		})
	}
}

func TestSanitizeSlackStoredPayloadRedactsPrivateFields(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"token":"legacy",
		"authed_users":["U1"],
		"authorizations":[{"user_id":"U1"}],
		"event":{"type":"message","channel_type":"im","channel":"D123","text":"secret customer details"}
	}`)

	var got map[string]any
	require.NoError(t, json.Unmarshal(sanitizeSlackStoredPayload(raw), &got), "sanitized payload should remain valid JSON")
	require.Equal(t, "[redacted]", got["token"], "legacy verification token should be redacted")
	require.Equal(t, "[redacted]", got["authed_users"], "transient authed users should be redacted")
	require.Equal(t, "[redacted]", got["authorizations"], "transient authorization envelope should be redacted")
	event, ok := got["event"].(map[string]any)
	require.True(t, ok, "sanitized payload should preserve event object")
	require.Equal(t, "[redacted]", event["text"], "DM event text should not be retained in raw Slack payload storage")
}

func TestSanitizeSlackStoredPayloadFormPayloadReturnsJSON(t *testing.T) {
	t.Parallel()

	raw := []byte(`token=legacy&trigger_id=trig-1&type=block_actions&payload=` + url.QueryEscape(`{"ok":true,"trigger_id":"nested-trig","response_url":"https://hooks.slack.com/actions/1"}`))

	var got map[string]any
	require.NoError(t, json.Unmarshal(sanitizeSlackStoredPayload(raw), &got), "sanitized form payload should be valid JSON")
	require.Equal(t, "[redacted]", got["token"], "legacy form token should be redacted")
	require.Equal(t, "[redacted]", got["trigger_id"], "trigger id should be redacted")
	require.Equal(t, "block_actions", got["type"], "non-secret form fields should be preserved")
	payload, ok := got["payload"].(map[string]any)
	require.True(t, ok, "nested Slack form payload should be parsed before storage")
	require.Equal(t, "[redacted]", payload["trigger_id"], "nested trigger id should be redacted")
	require.Equal(t, "[redacted]", payload["response_url"], "nested response URL should be redacted")
}

func TestSanitizeSlackStoredHeadersDropsSlackSignature(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", "Slackbot 1.0")
	headers.Set("X-Slack-Request-Timestamp", "1710000001")
	headers.Set("X-Slack-Retry-Reason", "http_timeout")
	headers.Set("X-Slack-Signature", "v0=secret")
	headers.Set("Authorization", "Bearer secret")

	var got map[string]any
	require.NoError(t, json.Unmarshal(sanitizeSlackStoredHeaders(headers), &got), "sanitized headers should be valid JSON")
	require.Equal(t, []any{"1710000001"}, got["x-slack-request-timestamp"], "safe Slack timestamp header should be retained")
	require.Equal(t, []any{"http_timeout"}, got["x-slack-retry-reason"], "safe Slack retry reason should be retained")
	require.NotContains(t, got, "x-slack-signature", "Slack request signature must not be stored")
	require.NotContains(t, got, "authorization", "authorization-like headers must not be stored")
}

func TestSlackbotHandler_InteractionsUsesViewCallbackIDForModalSubmissions(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	payload := `{"type":"view_submission","api_app_id":"A123","trigger_id":"trig-1","team":{"id":"T123"},"user":{"id":"U999"},"view":{"id":"V123","callback_id":"slack_start_session_modal","state":{"values":{"start_prompt":{"prompt":{"value":"fix this"}}}}}}`
	body := []byte("payload=" + url.QueryEscape(payload))
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Interactions(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "modal submissions should be acknowledged")
	interactionPayload, ok := jobs.payload.(models.SlackInteractionJobPayload)
	require.True(t, ok, "modal submission should enqueue typed interaction payload")
	require.Equal(t, "slack_start_session_modal", interactionPayload.CallbackID, "modal submission should use view.callback_id when top-level callback_id is absent")
}

func TestSlackbotHandler_EventsReturnsDuplicateForRedeliveredEvent(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{duplicate: true}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: false, delivery: models.WebhookDelivery{Status: "processed"}}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	body := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev123","event":{"type":"app_mention","channel":"C123","user":"U999","text":"<@U143> fix this","ts":"1710000001.000000","thread_ts":"1710000000.000000"}}`)
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "duplicate Slack events should receive a 200, not a 500")
	require.JSONEq(t, `{"status":"duplicate"}`, rr.Body.String(), "duplicate events should return status:duplicate, not enqueue a second job")
	require.Empty(t, jobs.jobType, "duplicate events must not enqueue a second job")
}

func TestSlackbotHandler_EventsRetriesFailedDeliveryWithExistingInboundEvent(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	existingDeliveryID := uuid.New()
	existingInboundID := uuid.New()
	inbound := &slackbotInboundStoreStub{duplicate: true, existingEventID: existingInboundID}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{
		inserted: false,
		delivery: models.WebhookDelivery{
			ID:     existingDeliveryID,
			Status: "failed",
		},
	}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, IntegrationID: uuid.New(), TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	body := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev123","event":{"type":"app_mention","channel":"C123","user":"U999","text":"<@U143> fix this","ts":"1710000001.000000","thread_ts":"1710000000.000000"}}`)
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "retryable failed Slack deliveries should be acknowledged after reprocessing")
	require.Equal(t, "slack_start_or_continue_session", jobs.jobType, "retryable failed Slack deliveries should enqueue downstream work")
	payload, ok := jobs.payload.(models.SlackStartSessionJobPayload)
	require.True(t, ok, "retry should enqueue typed Slack start-session payload")
	require.Equal(t, existingInboundID.String(), payload.SlackInboundEventID, "retry should reuse the existing inbound event identity")
	require.True(t, deliveries.markProcessedCalled, "successful retry should mark the existing delivery processed")
}

func TestSlackbotHandler_EventsMarksDeliveryFailedWhenInboundPersistenceFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inboundErr := errors.New("database rejected slack event")
	inbound := &slackbotInboundStoreStub{err: inboundErr}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, IntegrationID: uuid.New(), TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	body := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"EvPersistFail","event":{"type":"app_mention","channel":"C123","user":"U999","text":"<@U143> fix this","ts":"1710000001.000000"}}`)
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "inbound persistence failures should return 500 so Slack retries")
	require.Empty(t, jobs.jobType, "inbound persistence failure must not enqueue downstream work")
	require.True(t, deliveries.markProcessedCalled, "inbound persistence failure should mark the generic delivery failed")
	require.NotNil(t, deliveries.markErr, "failed delivery should record the root error message")
	require.Contains(t, *deliveries.markErr, "database rejected slack event", "failed delivery should retain the root persistence error")
}

func TestSlackbotHandler_EventsPersistsIgnoredBotMessages(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, IntegrationID: uuid.New(), TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	body := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"EvIgnored","event":{"type":"app_mention","channel":"C123","user":"U143","text":"<@U143> self echo","ts":"1710000001.000000"}}`)
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "ignored bot/self Slack events should still be acknowledged")
	require.JSONEq(t, `{"status":"ignored"}`, rr.Body.String(), "ignored bot/self Slack events should return ignored status")
	require.Equal(t, models.SlackInboundEventStatusIgnored, inbound.event.Status, "ignored Slack events should be persisted with ignored status")
	require.True(t, deliveries.markIgnoredCalled, "ignored Slack events should mark the generic delivery ignored")
	require.Empty(t, jobs.jobType, "ignored Slack events must not enqueue downstream work")
}

func TestSlackbotHandler_EventsMarksLastEventAndEnqueuesBotInviteSetup(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	installations := &slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}}
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
		installations,
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	body := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"EvInvite","event":{"type":"member_joined_channel","channel":"C123","user":"U143","ts":"1710000001.000000"}}`)
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "bot invitation event should be acknowledged")
	require.True(t, installations.markLastEventCalled, "events endpoint should update Slack installation last_event_at")
	require.Equal(t, "slack_handle_interaction", jobs.jobType, "bot channel invite should enqueue setup message job")
	payload, ok := jobs.payload.(models.SlackInteractionJobPayload)
	require.True(t, ok, "channel invite should enqueue typed interaction payload")
	require.Equal(t, "slack_member_joined_channel", payload.ActionID, "channel invite should route to setup-message action")
}

func TestSlackbotHandler_EventsDisconnectsAllActiveInstallsOnUninstall(t *testing.T) {
	t.Parallel()

	installations := &slackbotInstallStoreStub{}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
		installations,
		&slackbotInboundStoreStub{},
		&slackbotJobStoreStub{},
	)

	body := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"EvUninstall","event":{"type":"app_uninstalled","ts":"1710000001.000000"}}`)
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "uninstall events should be acknowledged")
	require.True(t, installations.markDisconnectedByTeamAppCalled, "uninstall should disconnect every active install for the team/app")
	require.Equal(t, "T123", installations.disconnectedTeamID, "uninstall disconnect should scope by Slack team")
	require.Equal(t, "A123", installations.disconnectedAPIAppID, "uninstall disconnect should scope by Slack app")
	require.False(t, installations.getActiveByTeamAppCalled, "uninstall should not resolve one arbitrary active installation")
}

func TestSlackbotHandler_PersistsSanitizedSlackPayloads(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	deliveries := &slackbotWebhookDeliveryStoreStub{inserted: true}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)
	handler.SetWebhookDeliveries(deliveries)

	body := []byte("team_id=T123&api_app_id=A123&channel_id=C123&user_id=U999&command=%2F143&text=fix+this&trigger_id=trig-1&response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2Fsecret")
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Commands(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "command endpoint should acknowledge valid Slack commands")
	require.NotContains(t, string(inbound.event.Payload), "hooks.slack.com", "stored Slack command payload should redact response_url")
	require.NotContains(t, string(inbound.event.Payload), "trig-1", "stored Slack command payload should redact trigger_id")
	var stored map[string]any
	require.NoError(t, json.Unmarshal(inbound.event.Payload, &stored), "stored Slack command payload should be valid JSON")
	require.Equal(t, "fix this", stored["text"], "stored Slack command payload should preserve non-secret command context")
}

func signedSlackRequest(t *testing.T, body []byte, secret string, now time.Time) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/slack/events", bytes.NewReader(body))
	ts := strconv.FormatInt(now.Unix(), 10)
	base := "v0:" + ts + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, err := mac.Write([]byte(base))
	require.NoError(t, err, "HMAC write should succeed")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	return req
}
