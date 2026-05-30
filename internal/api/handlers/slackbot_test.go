package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	install             models.SlackInstallation
	err                 error
	markLastEventCalled bool
}

func (s *slackbotInstallStoreStub) GetActiveByTeamApp(ctx context.Context, teamID, apiAppID string) (models.SlackInstallation, error) {
	return s.install, s.err
}

func (s *slackbotInstallStoreStub) MarkDisconnected(ctx context.Context, orgID, installationID uuid.UUID) error {
	return s.err
}

func (s *slackbotInstallStoreStub) MarkLastEvent(ctx context.Context, orgID, installationID uuid.UUID) error {
	s.markLastEventCalled = true
	return s.err
}

type slackbotInboundStoreStub struct {
	event     models.SlackInboundEvent
	err       error
	duplicate bool // when true, CreateReceived simulates a duplicate (returns false)
}

func (s *slackbotInboundStoreStub) CreateReceived(ctx context.Context, event *models.SlackInboundEvent) (bool, error) {
	s.event = *event
	if s.event.ID == uuid.Nil {
		s.event.ID = uuid.New()
	}
	if s.duplicate {
		return false, s.err
	}
	return true, s.err
}

func (s *slackbotInboundStoreStub) MarkEnqueued(ctx context.Context, orgID, eventID, jobID uuid.UUID) error {
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

func TestSlackbotHandler_EventsVerifiesSignatureAndEnqueuesMention(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)

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
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)

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

func TestSlackbotHandler_InteractionsPersistsAndEnqueues(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)

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

func TestSlackbotHandler_InteractionsUsesViewCallbackIDForModalSubmissions(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)

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
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret", FrontendURL: "https://143.test"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)

	body := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev123","event":{"type":"app_mention","channel":"C123","user":"U999","text":"<@U143> fix this","ts":"1710000001.000000","thread_ts":"1710000000.000000"}}`)
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Events(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "duplicate Slack events should receive a 200, not a 500")
	require.JSONEq(t, `{"status":"duplicate"}`, rr.Body.String(), "duplicate events should return status:duplicate, not enqueue a second job")
	require.Empty(t, jobs.jobType, "duplicate events must not enqueue a second job")
}

func TestSlackbotHandler_EventsMarksLastEventAndEnqueuesBotInviteSetup(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	installations := &slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}}
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
		installations,
		inbound,
		jobs,
	)

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

func TestSlackbotHandler_PersistsSanitizedSlackPayloads(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	installationID := uuid.New()
	inbound := &slackbotInboundStoreStub{}
	jobs := &slackbotJobStoreStub{}
	handler := NewSlackbotHandler(SlackbotHandlerConfig{SigningSecret: "secret"},
		&slackbotInstallStoreStub{install: models.SlackInstallation{ID: installationID, OrgID: orgID, TeamID: "T123", APIAppID: "A123", BotUserID: "U143", Status: models.SlackInstallationStatusActive}},
		inbound,
		jobs,
	)

	body := []byte("team_id=T123&api_app_id=A123&channel_id=C123&user_id=U999&command=%2F143&text=fix+this&trigger_id=trig-1&response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2Fsecret")
	req := signedSlackRequest(t, body, "secret", time.Now())
	rr := httptest.NewRecorder()

	handler.Commands(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "command endpoint should acknowledge valid Slack commands")
	require.NotContains(t, string(inbound.event.Payload), "hooks.slack.com", "stored Slack command payload should redact response_url")
	require.NotContains(t, string(inbound.event.Payload), "trig-1", "stored Slack command payload should redact trigger_id")
	require.Contains(t, string(inbound.event.Payload), "fix+this", "stored Slack command payload should preserve non-secret command context")
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
