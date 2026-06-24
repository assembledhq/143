package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
)

const slackMessageSecret = "test-secret-32-chars-long-enough-xxx"

type fakeSlackInstallationStore struct {
	installation models.SlackInstallation
	err          error
}

func (f fakeSlackInstallationStore) GetActiveByOrg(context.Context, uuid.UUID) (models.SlackInstallation, error) {
	return f.installation, f.err
}

type fakeSlackCredentialStore struct {
	cred *models.DecryptedCredential
	err  error
}

func (f fakeSlackCredentialStore) Get(context.Context, uuid.UUID, models.ProviderName) (*models.DecryptedCredential, error) {
	return f.cred, f.err
}

type recordedOutbound struct {
	msg *models.SlackOutboundMessage
}

type fakeSlackOutboundStore struct {
	records []recordedOutbound
}

func (f *fakeSlackOutboundStore) Upsert(_ context.Context, msg *models.SlackOutboundMessage) error {
	f.records = append(f.records, recordedOutbound{msg: msg})
	return nil
}

type fakeSlackPoster struct {
	gotChannel  string
	gotThreadTS string
	gotText     string
	result      ingestion.SlackPostedMessage
	err         error
}

func (f *fakeSlackPoster) PostMessage(_ context.Context, _, channelID, threadTS, text string) (ingestion.SlackPostedMessage, error) {
	f.gotChannel = channelID
	f.gotThreadTS = threadTS
	f.gotText = text
	return f.result, f.err
}

func newSlackMessageHandler(
	t *testing.T,
	sessions internalSessionGetter,
	installations slackMessageInstallationStore,
	credentials slackMessageCredentialStore,
	outbound slackMessageOutboundStore,
	poster slackInternalMessagePoster,
) *InternalSlackMessageHandler {
	t.Helper()
	h := &InternalSlackMessageHandler{
		sessionStore:  sessions,
		installations: installations,
		credentials:   credentials,
		outbound:      outbound,
		poster:        poster,
		signingSecret: slackMessageSecret,
		logger:        zerolog.Nop(),
	}
	return h
}

func slackSendRequest(t *testing.T, token, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/slack/messages", bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func validSlackCredential() *models.DecryptedCredential {
	return &models.DecryptedCredential{
		Provider: models.ProviderSlack,
		Config:   models.SlackConfig{AccessToken: "xoxb-test-token"},
	}
}

func TestInternalSlackMessageHandler_Send_Success(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(slackMessageSecret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should mint a session token")

	poster := &fakeSlackPoster{result: ingestion.SlackPostedMessage{Channel: "C123", Timestamp: "1700000000.000100"}}
	outbound := &fakeSlackOutboundStore{}
	handler := newSlackMessageHandler(t,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeSlackInstallationStore{installation: models.SlackInstallation{TeamID: "T999"}},
		fakeSlackCredentialStore{cred: validSlackCredential()},
		outbound,
		poster,
	)

	rr := httptest.NewRecorder()
	handler.Send(rr, slackSendRequest(t, token, `{"channel_id":"C123","text":"Automation finished.","thread_ts":"1700000000.000001"}`))

	require.Equal(t, http.StatusOK, rr.Code, "valid send should succeed")
	require.JSONEq(t, `{"status":"sent","channel_id":"C123","message_ts":"1700000000.000100"}`, rr.Body.String(),
		"response should echo delivery status and message coordinates")
	require.Equal(t, "C123", poster.gotChannel, "poster should receive the target channel")
	require.Equal(t, "1700000000.000001", poster.gotThreadTS, "poster should receive the thread timestamp")
	require.Equal(t, "Automation finished.", poster.gotText, "poster should receive the message text")
	require.Len(t, outbound.records, 1, "a sent notification should be recorded")
	require.Equal(t, "sent", outbound.records[0].msg.Status, "recorded notification should be marked sent")
	require.Equal(t, "1700000000.000100", outbound.records[0].msg.SlackMessageTS, "recorded notification should store the real Slack ts")
	require.Equal(t, "T999", outbound.records[0].msg.SlackTeamID, "recorded notification should carry the active install team")
}

func TestInternalSlackMessageHandler_Send_MissingToken(t *testing.T) {
	t.Parallel()

	handler := newSlackMessageHandler(t,
		fakeInternalSessionLookup{},
		fakeSlackInstallationStore{},
		fakeSlackCredentialStore{},
		&fakeSlackOutboundStore{},
		&fakeSlackPoster{},
	)
	rr := httptest.NewRecorder()
	handler.Send(rr, slackSendRequest(t, "", `{"channel_id":"C123","text":"hi"}`))

	require.Equal(t, http.StatusUnauthorized, rr.Code, "missing token should be rejected before any work")
	require.Contains(t, rr.Body.String(), "UNAUTHORIZED", "missing token should use the unauthorized error code")
}

func TestInternalSlackMessageHandler_Send_RejectsMissingChannelAndText(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(slackMessageSecret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should mint a session token")

	cases := []struct {
		name string
		body string
		code string
	}{
		{"missing channel", `{"text":"hi"}`, "INVALID_CHANNEL"},
		{"missing text", `{"channel_id":"C123"}`, "INVALID_TEXT"},
		{"blank text", `{"channel_id":"C123","text":"   "}`, "INVALID_TEXT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			poster := &fakeSlackPoster{}
			handler := newSlackMessageHandler(t,
				fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
				fakeSlackInstallationStore{installation: models.SlackInstallation{TeamID: "T999"}},
				fakeSlackCredentialStore{cred: validSlackCredential()},
				&fakeSlackOutboundStore{},
				poster,
			)
			rr := httptest.NewRecorder()
			handler.Send(rr, slackSendRequest(t, token, tc.body))

			require.Equal(t, http.StatusBadRequest, rr.Code, "malformed send should be rejected")
			require.Contains(t, rr.Body.String(), tc.code, "response should use the expected validation error code")
			require.Empty(t, poster.gotChannel, "poster should not be called for invalid input")
		})
	}
}

func TestInternalSlackMessageHandler_Send_RejectsAutomationGoalImprovement(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionThreadTokenWithClaims(
		slackMessageSecret,
		orgID,
		repoID,
		sessionID,
		nil,
		nil,
		string(models.SessionOriginAutomationGoalImprovement),
		nil,
		time.Minute,
	)
	require.NoError(t, err, "automation goal improvement token should be generated")

	poster := &fakeSlackPoster{}
	handler := newSlackMessageHandler(t,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeSlackInstallationStore{installation: models.SlackInstallation{TeamID: "T999"}},
		fakeSlackCredentialStore{cred: validSlackCredential()},
		&fakeSlackOutboundStore{},
		poster,
	)
	rr := httptest.NewRecorder()
	handler.Send(rr, slackSendRequest(t, token, `{"channel_id":"C123","text":"hi"}`))

	require.Equal(t, http.StatusForbidden, rr.Code, "goal improvement sessions should not send Slack messages")
	require.Contains(t, rr.Body.String(), "TOOL_NOT_AVAILABLE", "response should explain the tool is unavailable")
	require.Empty(t, poster.gotChannel, "poster should not be called for blocked origin")
}

func TestInternalSlackMessageHandler_Send_SlackNotConnected(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(slackMessageSecret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should mint a session token")

	handler := newSlackMessageHandler(t,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeSlackInstallationStore{err: fmt.Errorf("no active install")},
		fakeSlackCredentialStore{cred: validSlackCredential()},
		&fakeSlackOutboundStore{},
		&fakeSlackPoster{},
	)
	rr := httptest.NewRecorder()
	handler.Send(rr, slackSendRequest(t, token, `{"channel_id":"C123","text":"hi"}`))

	require.Equal(t, http.StatusFailedDependency, rr.Code, "a missing Slack install should surface a dependency failure")
	require.Contains(t, rr.Body.String(), "SLACK_NOT_CONNECTED", "response should explain Slack is not connected")
}

func TestInternalSlackMessageHandler_Send_PosterFailureRecordsAttempt(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(slackMessageSecret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should mint a session token")

	outbound := &fakeSlackOutboundStore{}
	handler := newSlackMessageHandler(t,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeSlackInstallationStore{installation: models.SlackInstallation{TeamID: "T999"}},
		fakeSlackCredentialStore{cred: validSlackCredential()},
		outbound,
		&fakeSlackPoster{err: fmt.Errorf("channel_not_found")},
	)
	rr := httptest.NewRecorder()
	handler.Send(rr, slackSendRequest(t, token, `{"channel_id":"C123","text":"hi"}`))

	require.Equal(t, http.StatusBadGateway, rr.Code, "a Slack API failure should surface as a bad gateway")
	require.Contains(t, rr.Body.String(), "SLACK_SEND_FAILED", "response should explain the send failed")
	require.Len(t, outbound.records, 1, "a failed send should still be recorded for observability")
	require.Equal(t, "failed", outbound.records[0].msg.Status, "recorded attempt should be marked failed")
}
