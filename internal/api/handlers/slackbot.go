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
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

const slackMaxBodyBytes = 1 << 20

type SlackbotHandlerConfig struct {
	SigningSecret string
	FrontendURL   string
}

type SlackbotInstallationStore interface {
	GetActiveByTeamApp(ctx context.Context, teamID, apiAppID string) (models.SlackInstallation, error)
	GetActiveByOrgTeamApp(ctx context.Context, orgID uuid.UUID, teamID, apiAppID string) (models.SlackInstallation, error)
	MarkLastEvent(ctx context.Context, orgID, installationID uuid.UUID) error
	MarkDisconnected(ctx context.Context, orgID, installationID uuid.UUID) error
	MarkDisconnectedByTeamApp(ctx context.Context, teamID, apiAppID string) error
}

type SlackbotOrgSelectionStore interface {
	GetBySlackUser(ctx context.Context, teamID, apiAppID, slackUserID string) (models.SlackOrgSelection, error)
}

type SlackbotInboundEventStore interface {
	CreateReceived(ctx context.Context, event *models.SlackInboundEvent) (bool, error)
	MarkEnqueued(ctx context.Context, orgID, eventID, jobID uuid.UUID) error
}

type SlackbotJobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

type SlackbotHandler struct {
	cfg           SlackbotHandlerConfig
	installations SlackbotInstallationStore
	orgSelections SlackbotOrgSelectionStore
	inbound       SlackbotInboundEventStore
	jobs          SlackbotJobStore
	logger        zerolog.Logger
	metrics       *metrics.SlackbotMetrics
	now           func() time.Time
}

func NewSlackbotHandler(cfg SlackbotHandlerConfig, installations SlackbotInstallationStore, inbound SlackbotInboundEventStore, jobs SlackbotJobStore) *SlackbotHandler {
	return &SlackbotHandler{
		cfg:           cfg,
		installations: installations,
		inbound:       inbound,
		jobs:          jobs,
		logger:        zerolog.Nop(),
		now:           time.Now,
	}
}

func (h *SlackbotHandler) SetLogger(logger zerolog.Logger) {
	h.logger = logger
}

func (h *SlackbotHandler) SetMetrics(metrics *metrics.SlackbotMetrics) {
	h.metrics = metrics
}

func (h *SlackbotHandler) SetOrgSelectionStore(store SlackbotOrgSelectionStore) {
	h.orgSelections = store
}

func (h *SlackbotHandler) Events(w http.ResponseWriter, r *http.Request) {
	started := h.now()
	defer func() {
		h.metrics.RecordCallbackLatency(r.Context(), "events_api", "handled", float64(h.now().Sub(started).Milliseconds()))
	}()
	body, ok := h.readAndVerify(w, r)
	if !ok {
		return
	}

	var envelope slackEventEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse Slack event")
		return
	}
	if envelope.Type == "url_verification" {
		writeJSON(w, http.StatusOK, map[string]string{"challenge": envelope.Challenge})
		return
	}
	if envelope.Type != "event_callback" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	if h.installations == nil || h.inbound == nil || h.jobs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}

	eventType := envelope.Event.Type
	if envelope.Event.ChannelType == "im" && eventType == "message" {
		eventType = string(models.SlackInboundEventTypeMessageIM)
	}
	if models.SlackInboundEventType(eventType) == models.SlackInboundEventTypeAppUninstalled ||
		models.SlackInboundEventType(eventType) == models.SlackInboundEventTypeAppUninstalledTeam {
		if err := h.installations.MarkDisconnectedByTeamApp(r.Context(), envelope.TeamID, envelope.APIAppID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "SLACK_DISCONNECT_FAILED", "failed to mark Slack installations disconnected", err)
			return
		}
		h.metrics.RecordInboundEvent(r.Context(), eventType, "received")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	install, err := h.resolveInstallation(r.Context(), envelope.TeamID, envelope.APIAppID, envelope.Event.User)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SLACK_INSTALLATION_NOT_FOUND", "slack installation not found")
		return
	}
	if err := h.installations.MarkLastEvent(r.Context(), install.OrgID, install.ID); err != nil {
		h.logger.Warn().Err(err).Str("team_id", envelope.TeamID).Msg("failed to update Slack installation last_event_at")
	}
	h.metrics.RecordInboundEvent(r.Context(), eventType, "received")
	if h.shouldIgnoreEvent(install, envelope.Event) {
		h.metrics.RecordInboundEvent(r.Context(), eventType, "ignored")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	channelID := stringPtrOrNil(envelope.Event.Channel)
	userID := stringPtrOrNil(envelope.Event.User)
	eventTS := stringPtrOrNil(envelope.Event.TS)
	slackEventID := stringPtrOrNil(envelope.EventID)
	inbound := &models.SlackInboundEvent{
		OrgID:               install.OrgID,
		SlackInstallationID: install.ID,
		SlackEventID:        slackEventID,
		SlackTeamID:         envelope.TeamID,
		EventType:           models.SlackInboundEventType(eventType),
		ChannelID:           channelID,
		UserID:              userID,
		EventTS:             eventTS,
		Payload:             sanitizeSlackStoredPayload(body),
	}
	inserted, err := h.inbound.CreateReceived(r.Context(), inbound)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_EVENT_PERSIST_FAILED", "failed to persist Slack event", err)
		return
	}
	if !inserted {
		h.metrics.RecordInboundEvent(r.Context(), eventType, "duplicate")
		h.metrics.RecordDedupeHit(r.Context(), "events_api")
		writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate"})
		return
	}

	switch models.SlackInboundEventType(eventType) {
	case models.SlackInboundEventTypeAppMention, models.SlackInboundEventTypeMessageIM:
		payload := models.SlackStartSessionJobPayload{
			OrgID:               install.OrgID.String(),
			SlackInboundEventID: inbound.ID.String(),
			SlackInstallationID: install.ID.String(),
			TeamID:              envelope.TeamID,
			ChannelID:           envelope.Event.Channel,
			ThreadTS:            slackThreadTS(envelope.Event),
			MessageTS:           envelope.Event.TS,
			SlackUserID:         envelope.Event.User,
			Text:                envelope.Event.Text,
			Source:              eventType,
			FileIDs:             slackEventFileIDs(envelope.Event),
		}
		dedupeKey := "slack_event:" + envelope.EventID
		jobID, err := h.jobs.Enqueue(r.Context(), install.OrgID, "agent", "slack_start_or_continue_session", payload, 5, &dedupeKey)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "SLACK_JOB_ENQUEUE_FAILED", "failed to enqueue Slack event", err)
			return
		}
		if err := h.inbound.MarkEnqueued(r.Context(), install.OrgID, inbound.ID, jobID); err != nil {
			h.logger.Warn().Err(err).Str("slack_event_id", envelope.EventID).Msg("failed to mark Slack event enqueued")
		}
		h.metrics.RecordSessionStart(r.Context(), eventType, "enqueued")
	case models.SlackInboundEventTypeAppHomeOpened:
		payload := map[string]string{
			"org_id":                 install.OrgID.String(),
			"slack_inbound_event_id": inbound.ID.String(),
			"slack_installation_id":  install.ID.String(),
			"team_id":                envelope.TeamID,
			"slack_user_id":          envelope.Event.User,
		}
		dedupeKey := "slack_app_home:" + envelope.EventID
		jobID, err := h.jobs.Enqueue(r.Context(), install.OrgID, "default", "slack_sync_app_home", payload, 3, &dedupeKey)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "SLACK_JOB_ENQUEUE_FAILED", "failed to enqueue Slack app home sync", err)
			return
		}
		if err := h.inbound.MarkEnqueued(r.Context(), install.OrgID, inbound.ID, jobID); err != nil {
			h.logger.Warn().Err(err).Str("slack_event_id", envelope.EventID).Msg("failed to mark Slack event enqueued")
		}
	case models.SlackInboundEventTypeAppRateLimited:
		h.metrics.RecordRateLimit(r.Context(), "events_api")
		h.logger.Warn().Str("team_id", envelope.TeamID).Msg("Slack app rate limited events delivery")
	case models.SlackInboundEventTypeMemberJoined:
		payload := models.SlackInteractionJobPayload{
			OrgID:               install.OrgID.String(),
			SlackInboundEventID: inbound.ID.String(),
			SlackInstallationID: install.ID.String(),
			TeamID:              envelope.TeamID,
			ChannelID:           envelope.Event.Channel,
			UserID:              envelope.Event.User,
			ActionID:            "slack_member_joined_channel",
			RawPayload:          body,
		}
		dedupeKey := "slack_member_joined:" + envelope.EventID
		jobID, err := h.jobs.Enqueue(r.Context(), install.OrgID, "default", "slack_handle_interaction", payload, 3, &dedupeKey)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "SLACK_JOB_ENQUEUE_FAILED", "failed to enqueue Slack channel setup", err)
			return
		}
		if err := h.inbound.MarkEnqueued(r.Context(), install.OrgID, inbound.ID, jobID); err != nil {
			h.logger.Warn().Err(err).Str("slack_event_id", envelope.EventID).Msg("failed to mark Slack event enqueued")
		}
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *SlackbotHandler) Commands(w http.ResponseWriter, r *http.Request) {
	started := h.now()
	defer func() {
		h.metrics.RecordCallbackLatency(r.Context(), "slash_command", "handled", float64(h.now().Sub(started).Milliseconds()))
	}()
	body, ok := h.readAndVerify(w, r)
	if !ok {
		return
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_FORM", "failed to parse Slack command payload")
		return
	}
	if h.installations == nil || h.inbound == nil || h.jobs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}
	teamID := values.Get("team_id")
	apiAppID := values.Get("api_app_id")
	userID := values.Get("user_id")
	install, err := h.resolveInstallation(r.Context(), teamID, apiAppID, userID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SLACK_INSTALLATION_NOT_FOUND", "slack installation not found")
		return
	}
	channelID := values.Get("channel_id")
	triggerID := values.Get("trigger_id")
	eventID := "command:" + teamID + ":" + channelID + ":" + userID + ":" + values.Get("command") + ":" + triggerID
	inbound := &models.SlackInboundEvent{
		OrgID:               install.OrgID,
		SlackInstallationID: install.ID,
		SlackEventID:        &eventID,
		SlackTeamID:         teamID,
		EventType:           models.SlackInboundEventTypeSlashCommand,
		ChannelID:           stringPtrOrNil(channelID),
		UserID:              stringPtrOrNil(userID),
		Payload:             sanitizeSlackStoredPayload(body),
	}
	inserted, err := h.inbound.CreateReceived(r.Context(), inbound)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_EVENT_PERSIST_FAILED", "failed to persist Slack command", err)
		return
	}
	if !inserted {
		h.metrics.RecordDedupeHit(r.Context(), "slash_command")
	} else {
		payload := models.SlackStartSessionJobPayload{
			OrgID:               install.OrgID.String(),
			SlackInboundEventID: inbound.ID.String(),
			SlackInstallationID: install.ID.String(),
			TeamID:              teamID,
			ChannelID:           channelID,
			ThreadTS:            values.Get("thread_ts"),
			MessageTS:           triggerID,
			SlackUserID:         userID,
			Text:                values.Get("text"),
			Source:              string(models.SlackInboundEventTypeSlashCommand),
		}
		dedupeKey := "slack_command:" + eventID
		jobID, err := h.jobs.Enqueue(r.Context(), install.OrgID, "agent", "slack_start_or_continue_session", payload, 5, &dedupeKey)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "SLACK_JOB_ENQUEUE_FAILED", "failed to enqueue Slack command", err)
			return
		}
		if err := h.inbound.MarkEnqueued(r.Context(), install.OrgID, inbound.ID, jobID); err != nil {
			h.logger.Warn().Err(err).Str("slack_event_id", eventID).Msg("failed to mark Slack command enqueued")
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"response_type": "ephemeral", "text": "Starting a 143 session for this Slack request..."})
}

func (h *SlackbotHandler) Interactions(w http.ResponseWriter, r *http.Request) {
	started := h.now()
	defer func() {
		h.metrics.RecordCallbackLatency(r.Context(), "interaction", "handled", float64(h.now().Sub(started).Milliseconds()))
	}()
	body, ok := h.readAndVerify(w, r)
	if !ok {
		return
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_FORM", "failed to parse Slack interaction payload")
		return
	}
	payload := values.Get("payload")
	if payload == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_PAYLOAD", "Slack interaction payload is required")
		return
	}
	if h.installations == nil || h.inbound == nil || h.jobs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SLACKBOT_NOT_CONFIGURED", "slackbot is not configured")
		return
	}
	var interaction slackInteractionPayload
	if err := json.Unmarshal([]byte(payload), &interaction); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "failed to parse Slack interaction payload")
		return
	}
	install, err := h.resolveInstallation(r.Context(), interaction.Team.ID, interaction.APIAppID, interaction.User.ID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "SLACK_INSTALLATION_NOT_FOUND", "slack installation not found")
		return
	}
	actionID, value := firstSlackAction(interaction.Actions)
	callbackID := firstNonEmpty(interaction.CallbackID, interaction.View.CallbackID)
	h.metrics.RecordInteractionAction(r.Context(), actionID, "received")
	eventID := "interaction:" + interaction.Team.ID + ":" + interaction.User.ID + ":" + callbackID + ":" + actionID + ":" + interaction.Message.TS + ":" + interaction.View.ID
	rawPayload := json.RawMessage(payload)
	inbound := &models.SlackInboundEvent{
		OrgID:               install.OrgID,
		SlackInstallationID: install.ID,
		SlackEventID:        &eventID,
		SlackTeamID:         interaction.Team.ID,
		EventType:           models.SlackInboundEventTypeInteraction,
		ChannelID:           stringPtrOrNil(interaction.Channel.ID),
		UserID:              stringPtrOrNil(interaction.User.ID),
		EventTS:             stringPtrOrNil(interaction.Message.TS),
		Payload:             sanitizeSlackStoredPayload(rawPayload),
	}
	inserted, err := h.inbound.CreateReceived(r.Context(), inbound)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_EVENT_PERSIST_FAILED", "failed to persist Slack interaction", err)
		return
	}
	if !inserted {
		h.metrics.RecordDedupeHit(r.Context(), "interaction")
	} else {
		jobPayload := models.SlackInteractionJobPayload{
			OrgID:               install.OrgID.String(),
			SlackInboundEventID: inbound.ID.String(),
			SlackInstallationID: install.ID.String(),
			TeamID:              interaction.Team.ID,
			ChannelID:           interaction.Channel.ID,
			UserID:              interaction.User.ID,
			ActionID:            actionID,
			CallbackID:          callbackID,
			Value:               value,
			TriggerID:           interaction.TriggerID,
			ViewID:              interaction.View.ID,
			MessageTS:           interaction.Message.TS,
			RawPayload:          sanitizeSlackStoredPayload(rawPayload),
		}
		dedupeKey := "slack_interaction:" + eventID
		jobID, err := h.jobs.Enqueue(r.Context(), install.OrgID, "default", "slack_handle_interaction", jobPayload, 5, &dedupeKey)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "SLACK_JOB_ENQUEUE_FAILED", "failed to enqueue Slack interaction", err)
			return
		}
		if err := h.inbound.MarkEnqueued(r.Context(), install.OrgID, inbound.ID, jobID); err != nil {
			h.logger.Warn().Err(err).Str("slack_event_id", eventID).Msg("failed to mark Slack interaction enqueued")
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *SlackbotHandler) resolveInstallation(ctx context.Context, teamID, apiAppID, slackUserID string) (models.SlackInstallation, error) {
	if h.installations == nil {
		return models.SlackInstallation{}, fmt.Errorf("slack installation store is not configured")
	}
	if h.orgSelections != nil && strings.TrimSpace(slackUserID) != "" {
		selection, err := h.orgSelections.GetBySlackUser(ctx, teamID, apiAppID, slackUserID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return models.SlackInstallation{}, fmt.Errorf("resolve slack org selection: %w", err)
			}
		} else if selection.OrgID != uuid.Nil {
			install, err := h.installations.GetActiveByOrgTeamApp(ctx, selection.OrgID, teamID, apiAppID)
			if err != nil {
				return models.SlackInstallation{}, fmt.Errorf("resolve selected slack installation: %w", err)
			}
			return install, nil
		}
	}
	return h.installations.GetActiveByTeamApp(ctx, teamID, apiAppID)
}

func (h *SlackbotHandler) readAndVerify(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, slackMaxBodyBytes))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "READ_FAILED", "failed to read Slack request body")
		return nil, false
	}
	if !h.verifySignature(r, body) {
		h.metrics.RecordSignatureFailure(r.Context(), "invalid")
		writeError(w, r, http.StatusUnauthorized, "INVALID_SIGNATURE", "Slack signature verification failed")
		return nil, false
	}
	return body, true
}

func (h *SlackbotHandler) verifySignature(r *http.Request, body []byte) bool {
	if h.cfg.SigningSecret == "" {
		return false
	}
	tsRaw := r.Header.Get("X-Slack-Request-Timestamp")
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return false
	}
	requestTime := time.Unix(ts, 0)
	if h.now().Sub(requestTime) > 5*time.Minute || requestTime.Sub(h.now()) > 5*time.Minute {
		return false
	}
	signature := r.Header.Get("X-Slack-Signature")
	if !strings.HasPrefix(signature, "v0=") {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, "v0="))
	if err != nil {
		return false
	}
	base := fmt.Sprintf("v0:%s:%s", tsRaw, string(body))
	mac := hmac.New(sha256.New, []byte(h.cfg.SigningSecret))
	_, _ = mac.Write([]byte(base))
	return hmac.Equal(got, mac.Sum(nil))
}

func (h *SlackbotHandler) shouldIgnoreEvent(install models.SlackInstallation, event slackInnerEvent) bool {
	if event.Type == string(models.SlackInboundEventTypeMemberJoined) {
		return install.BotUserID == "" || event.User != install.BotUserID
	}
	if event.BotID != "" {
		return true
	}
	if event.User != "" && install.BotUserID != "" && event.User == install.BotUserID {
		return true
	}
	if event.Subtype != "" && event.Subtype != "file_share" {
		return true
	}
	return false
}

func sanitizeSlackStoredPayload(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	rawString := string(raw)
	values, err := url.ParseQuery(rawString)
	if err == nil && len(values) > 0 && strings.Contains(rawString, "=") {
		for key := range slackStoredPayloadSecretKeys {
			if _, ok := values[key]; ok {
				values.Set(key, "[redacted]")
			}
		}
		encoded := make(map[string]any, len(values))
		for key, vals := range values {
			if _, ok := slackStoredPayloadSecretKeys[key]; ok {
				encoded[key] = "[redacted]"
				continue
			}
			if key == "payload" && len(vals) == 1 {
				var nested any
				if err := json.Unmarshal([]byte(vals[0]), &nested); err == nil {
					sanitizeSlackJSONValue(nested)
					redactSlackDMEventText(nested)
					encoded[key] = nested
					continue
				}
			}
			if len(vals) == 1 {
				encoded[key] = vals[0]
				continue
			}
			encoded[key] = vals
		}
		sanitized, marshalErr := json.Marshal(encoded)
		if marshalErr != nil {
			return json.RawMessage(`{}`)
		}
		return sanitized
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return json.RawMessage(raw)
	}
	sanitizeSlackJSONValue(decoded)
	redactSlackDMEventText(decoded)
	sanitized, err := json.Marshal(decoded)
	if err != nil {
		return json.RawMessage(raw)
	}
	return sanitized
}

var slackStoredPayloadSecretKeys = map[string]struct{}{
	"response_url":   {},
	"trigger_id":     {},
	"token":          {},
	"authed_users":   {},
	"authorizations": {},
}

func sanitizeSlackJSONValue(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if _, ok := slackStoredPayloadSecretKeys[key]; ok {
				v[key] = "[redacted]"
				continue
			}
			sanitizeSlackJSONValue(child)
		}
	case []any:
		for _, child := range v {
			sanitizeSlackJSONValue(child)
		}
	}
}

func redactSlackDMEventText(value any) {
	root, ok := value.(map[string]any)
	if !ok {
		return
	}
	event, ok := root["event"].(map[string]any)
	if !ok {
		return
	}
	channelType, _ := event["channel_type"].(string)
	channelID, _ := event["channel"].(string)
	if channelType != "im" && channelType != "mpim" && !strings.HasPrefix(channelID, "D") {
		return
	}
	if _, ok := event["text"]; ok {
		event["text"] = "[redacted]"
	}
}

func slackThreadTS(event slackInnerEvent) string {
	if event.ThreadTS != "" {
		return event.ThreadTS
	}
	return event.TS
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type slackEventEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	TeamID    string          `json:"team_id"`
	APIAppID  string          `json:"api_app_id"`
	EventID   string          `json:"event_id"`
	Event     slackInnerEvent `json:"event"`
}

type slackInnerEvent struct {
	Type        string `json:"type"`
	Channel     string `json:"channel"`
	User        string `json:"user"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	ChannelType string `json:"channel_type"`
	Subtype     string `json:"subtype"`
	BotID       string `json:"bot_id"`
	Files       []struct {
		ID string `json:"id"`
	} `json:"files"`
}

type slackInteractionPayload struct {
	Type       string `json:"type"`
	APIAppID   string `json:"api_app_id"`
	TriggerID  string `json:"trigger_id"`
	CallbackID string `json:"callback_id"`
	Team       struct {
		ID string `json:"id"`
	} `json:"team"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Message struct {
		TS string `json:"ts"`
	} `json:"message"`
	View struct {
		ID              string `json:"id"`
		CallbackID      string `json:"callback_id"`
		PrivateMetadata string `json:"private_metadata"`
		State           struct {
			Values map[string]map[string]slackInteractionAction `json:"values"`
		} `json:"state"`
	} `json:"view"`
	Actions []slackInteractionAction `json:"actions"`
}

type slackInteractionAction struct {
	ActionID       string `json:"action_id"`
	Value          string `json:"value"`
	SelectedOption struct {
		Value string `json:"value"`
	} `json:"selected_option"`
}

func firstSlackAction(actions []slackInteractionAction) (string, string) {
	if len(actions) == 0 {
		return "", ""
	}
	if actions[0].Value == "" {
		return actions[0].ActionID, actions[0].SelectedOption.Value
	}
	return actions[0].ActionID, actions[0].Value
}

func slackEventFileIDs(event slackInnerEvent) []string {
	if len(event.Files) == 0 {
		return nil
	}
	ids := make([]string, 0, len(event.Files))
	for _, file := range event.Files {
		if file.ID != "" {
			ids = append(ids, file.ID)
		}
	}
	return ids
}
