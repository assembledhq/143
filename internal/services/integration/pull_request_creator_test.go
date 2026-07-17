package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInternalPullRequestCreator_CreatePullRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/internal/sessions/session-123/pr", r.URL.Path, "CreatePullRequest should target the internal session PR endpoint")
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "CreatePullRequest should send the internal bearer token")

		var got CreatePullRequestParams
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got), "request body should decode as PR creation params")
		require.Equal(t, "session-123", got.SessionID, "request body should include the session id")
		require.NotNil(t, got.Draft, "request body should include explicit draft setting")
		require.True(t, *got.Draft, "request body should preserve draft setting")

		w.WriteHeader(http.StatusAccepted)
		_, err := w.Write([]byte(`{"status":"queued","session_id":"session-123"}`))
		require.NoError(t, err, "test response should be written")
	}))
	defer server.Close()

	draft := true
	creator := NewInternalPullRequestCreator("test-token", server.URL)
	got, err := creator.CreatePullRequest(context.Background(), CreatePullRequestParams{
		SessionID: "session-123",
		Draft:     &draft,
	})

	require.NoError(t, err, "CreatePullRequest should succeed for an accepted response")
	require.Equal(t, &CreatePullRequestResult{Status: "queued", SessionID: "session-123"}, got, "CreatePullRequest should decode the queued response")
}

func TestInternalPullRequestCreator_CreatePullRequestUsesTokenScopedSession(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/session/pr", r.URL.Path, "CreatePullRequest should use the token-scoped endpoint when no session id is supplied")
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "CreatePullRequest should authenticate the token-scoped request")
		w.WriteHeader(http.StatusAccepted)
		_, err := w.Write([]byte(`{"status":"queued","session_id":"session-from-token"}`))
		require.NoError(t, err, "test response should be written")
	}))
	defer server.Close()

	creator := NewInternalPullRequestCreator("test-token", server.URL)
	result, err := creator.CreatePullRequest(context.Background(), CreatePullRequestParams{})
	require.NoError(t, err, "CreatePullRequest should not require environment session context")
	require.Equal(t, "session-from-token", result.SessionID, "CreatePullRequest should return the token-scoped session")
}
