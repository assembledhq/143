package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
)

type slackMessageInstallationStore interface {
	GetActiveByOrg(ctx context.Context, orgID uuid.UUID) (models.SlackInstallation, error)
}

type slackMessageCredentialStore interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

type slackMessageOutboundStore interface {
	Upsert(ctx context.Context, msg *models.SlackOutboundMessage) error
}

type slackInternalMessagePoster interface {
	PostMessage(ctx context.Context, accessToken, channelID, threadTS, text string) (ingestion.SlackPostedMessage, error)
}

type InternalSlackMessageHandler struct {
	sessionStore  internalSessionGetter
	installations slackMessageInstallationStore
	credentials   slackMessageCredentialStore
	outbound      slackMessageOutboundStore
	poster        slackInternalMessagePoster
	signingSecret string
	logger        zerolog.Logger
}

func NewInternalSlackMessageHandler(
	sessionStore internalSessionGetter,
	installations slackMessageInstallationStore,
	credentials slackMessageCredentialStore,
	outbound slackMessageOutboundStore,
	signingSecret string,
	logger zerolog.Logger,
) *InternalSlackMessageHandler {
	return &InternalSlackMessageHandler{
		sessionStore:  sessionStore,
		installations: installations,
		credentials:   credentials,
		outbound:      outbound,
		poster:        ingestion.NewSlackAPIClient(logger),
		signingSecret: signingSecret,
		logger:        logger,
	}
}

func (h *InternalSlackMessageHandler) SetPoster(poster slackInternalMessagePoster) {
	h.poster = poster
}

type internalSlackMessageRequest struct {
	ChannelID string `json:"channel_id"`
	Text      string `json:"text"`
	ThreadTS  string `json:"thread_ts,omitempty"`
}

type internalSlackMessageResponse struct {
	Status    string `json:"status"`
	ChannelID string `json:"channel_id"`
	MessageTS string `json:"message_ts,omitempty"`
}

func (h *InternalSlackMessageHandler) Send(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := authorizeInternalSession(w, r, h.signingSecret, h.sessionStore)
	if !ok {
		return
	}
	if claims.SessionOrigin == string(models.SessionOriginAutomationGoalImprovement) {
		writeError(w, r, http.StatusForbidden, "TOOL_NOT_AVAILABLE", "Slack message sending is not available to automation goal improvement sessions")
		return
	}

	var body internalSlackMessageRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	body.ChannelID = strings.TrimSpace(body.ChannelID)
	body.Text = strings.TrimSpace(body.Text)
	body.ThreadTS = strings.TrimSpace(body.ThreadTS)
	if body.ChannelID == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_CHANNEL", "channel_id is required")
		return
	}
	if body.Text == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_TEXT", "text is required")
		return
	}

	installation, err := h.installations.GetActiveByOrg(r.Context(), claims.OrgID)
	if err != nil {
		writeError(w, r, http.StatusFailedDependency, "SLACK_NOT_CONNECTED", "Slack is not connected for this organization", err)
		return
	}
	cred, err := h.credentials.Get(r.Context(), claims.OrgID, models.ProviderSlack)
	if err != nil {
		writeError(w, r, http.StatusFailedDependency, "SLACK_CREDENTIAL_UNAVAILABLE", "Slack credentials are unavailable", err)
		return
	}
	slackCfg, ok := cred.Config.(models.SlackConfig)
	if !ok || strings.TrimSpace(slackCfg.AccessToken) == "" {
		writeError(w, r, http.StatusFailedDependency, "SLACK_CREDENTIAL_INVALID", "Slack credentials are invalid")
		return
	}
	if h.poster == nil {
		writeError(w, r, http.StatusInternalServerError, "SLACK_POSTER_UNAVAILABLE", "Slack message poster is not configured")
		return
	}

	posted, err := h.poster.PostMessage(r.Context(), slackCfg.AccessToken, body.ChannelID, body.ThreadTS, body.Text)
	if err != nil {
		h.recordOutbound(r.Context(), claims.OrgID, installation.TeamID, body.ChannelID, slackAttemptTS("failed"), "failed", body.Text)
		writeError(w, r, http.StatusBadGateway, "SLACK_SEND_FAILED", "failed to send Slack message", err)
		return
	}
	h.recordOutbound(r.Context(), claims.OrgID, installation.TeamID, posted.Channel, posted.Timestamp, "sent", body.Text)
	writeJSON(w, http.StatusOK, internalSlackMessageResponse{
		Status:    "sent",
		ChannelID: posted.Channel,
		MessageTS: posted.Timestamp,
	})
}

func (h *InternalSlackMessageHandler) recordOutbound(ctx context.Context, orgID uuid.UUID, teamID, channelID, ts, status, text string) {
	if h.outbound == nil || ts == "" {
		return
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	msg := &models.SlackOutboundMessage{
		OrgID:           orgID,
		SlackTeamID:     teamID,
		SlackChannelID:  channelID,
		SlackMessageTS:  ts,
		MessageKind:     models.SlackOutboundMessageKindNotification,
		Status:          status,
		LastPayloadHash: hash,
	}
	if err := h.outbound.Upsert(ctx, msg); err != nil {
		h.logger.Warn().Err(err).Str("slack_message_ts", ts).Msg("failed to record internal Slack message")
	}
}

func slackAttemptTS(status string) string {
	return "attempt:notification:" + status + ":" + uuid.NewString()
}
