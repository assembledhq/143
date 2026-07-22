package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInternalSessionTabManagerSendGeneratesClientMessageID(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/internal/session-tabs/tab-1/messages", r.URL.Path, "manager should post to the target tab messages endpoint")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody), "request body should be valid JSON")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer server.Close()

	manager := NewInternalSessionTabManager("token", server.URL)
	_, err := manager.SendTabMessage(context.Background(), SendSessionTabMessageParams{
		TabID:   "tab-1",
		Message: "run tests",
	})
	require.NoError(t, err, "SendTabMessage should succeed")

	clientMessageID, ok := gotBody["client_message_id"].(string)
	require.True(t, ok, "manager should send a client_message_id")
	require.NotEmpty(t, clientMessageID, "manager should generate an idempotency key when omitted")
	require.Contains(t, clientMessageID, "agent-tool-", "generated idempotency key should be namespaced")
}

func TestInternalSessionTabManagerSendRequiresMessageOrFile(t *testing.T) {
	t.Parallel()

	manager := NewInternalSessionTabManager("token", "http://internal-api")
	_, err := manager.SendTabMessage(context.Background(), SendSessionTabMessageParams{
		TabID: "tab-1",
	})
	require.Error(t, err, "SendTabMessage should reject empty messages before making an HTTP request")
	require.Contains(t, err.Error(), "message is required", "SendTabMessage should describe the missing message")
}
