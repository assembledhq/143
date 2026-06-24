package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInternalSlackMessageSenderSendMessage(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var gotPath string
	var gotBody SendMessageParams
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody), "request body should decode as Slack send params")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"status":"sent","channel_id":"C123","message_ts":"1700000000.000100"}`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	sender := NewInternalSlackMessageSender("tok", server.URL)
	got, err := sender.SendMessage(context.Background(), SendMessageParams{
		ChannelID: "C123",
		Text:      "Automation completed.",
		ThreadTS:  "1700000000.000001",
	})

	require.NoError(t, err, "valid Slack send should succeed")
	require.Equal(t, "/slack/messages", gotPath, "sender should call the internal Slack messages endpoint")
	require.Equal(t, "Bearer tok", gotAuth, "sender should pass the internal bearer token")
	require.Equal(t, SendMessageParams{ChannelID: "C123", Text: "Automation completed.", ThreadTS: "1700000000.000001"}, gotBody, "sender should forward Slack message params")
	require.Equal(t, &SendMessageResult{Status: "sent", ChannelID: "C123", MessageTS: "1700000000.000100"}, got, "sender should decode delivery status")
}
