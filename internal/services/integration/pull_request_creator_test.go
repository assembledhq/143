package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		require.Equal(t, "explicit_action", got.TriggerKind, "request body should preserve the publication trigger")

		w.WriteHeader(http.StatusAccepted)
		_, err := w.Write([]byte(`{"status":"queued","session_id":"session-123"}`))
		require.NoError(t, err, "test response should be written")
	}))
	defer server.Close()

	draft := true
	creator := NewInternalPullRequestCreator("test-token", server.URL)
	got, err := creator.CreatePullRequest(context.Background(), CreatePullRequestParams{
		SessionID:   "session-123",
		Draft:       &draft,
		TriggerKind: "explicit_action",
	})

	require.NoError(t, err, "CreatePullRequest should succeed for an accepted response")
	require.Equal(t, &CreatePullRequestResult{Status: "queued", SessionID: "session-123"}, got, "CreatePullRequest should decode the queued response")
}

func TestInternalPullRequestCreator_CreatePullRequestPreservesStructuredError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte(`{"error":{"code":"PUBLICATION_NOT_ALLOWED","message":"repository policy prevents publication"}}`))
		require.NoError(t, err, "test response should be written")
	}))
	defer server.Close()

	creator := NewInternalPullRequestCreator("test-token", server.URL)
	_, err := creator.CreatePullRequest(context.Background(), CreatePullRequestParams{})
	var creationErr *PullRequestCreationError
	require.Error(t, err, "CreatePullRequest should reject a non-202 response")
	require.True(t, errors.As(err, &creationErr), "CreatePullRequest should expose a typed API error")
	require.Equal(t, http.StatusForbidden, creationErr.StatusCode, "typed error should preserve the HTTP status")
	require.Equal(t, "PUBLICATION_NOT_ALLOWED", creationErr.Code, "typed error should preserve the API error code")
}

func TestInternalPullRequestCreator_CreatePullRequestUsesTokenScopedSession(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/internal/session/pr", r.URL.Path, "CreatePullRequest should use the token-scoped endpoint when no session id is supplied")
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

func TestInternalPullRequestCreator_CreatePullRequestDecodesTypedOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		status string
		reason *string
	}{
		{name: "review started", body: `{"status":"review_started","session_id":"session-1","publication_id":"publication-1","review_loop_id":"loop-1","pull_request_url":null,"reason":null}`, status: "review_started"},
		{name: "manual publication required", body: `{"status":"manual_publication_required","session_id":"session-1","publication_id":null,"review_loop_id":null,"pull_request_url":null,"reason":"automatic handoff disabled"}`, status: "manual_publication_required", reason: stringResultPointer("automatic handoff disabled")},
		{name: "blocked is a typed success", body: `{"status":"blocked","session_id":"session-1","reason":"needs attention"}`, status: "blocked", reason: stringResultPointer("needs attention")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, err := w.Write([]byte(tt.body))
				require.NoError(t, err, "test response should be written")
			}))
			defer server.Close()

			result, err := NewInternalPullRequestCreator("test-token", server.URL).CreatePullRequest(context.Background(), CreatePullRequestParams{})
			require.NoError(t, err, "typed 202 outcome should not be collapsed into an error")
			require.Equal(t, tt.status, result.Status, "client should preserve the coordinator status")
			require.Equal(t, tt.reason, result.Reason, "client should preserve the coordinator reason")
		})
	}
}

func stringResultPointer(value string) *string { return &value }

func TestInternalPullRequestCreator_UpdatePullRequestFromBodyFile(t *testing.T) {
	t.Parallel()

	bodyPath := filepath.Join(t.TempDir(), "pr-description.md")
	require.NoError(t, os.WriteFile(bodyPath, []byte("# Expanded summary\n\nDetails"), 0o600), "test PR description should be written")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPatch, r.Method, "UpdatePullRequest should use the metadata update method")
		require.Equal(t, "/api/v1/internal/session/pr", r.URL.Path, "UpdatePullRequest should use token-scoped session identity by default")
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "UpdatePullRequest should send the internal bearer token")

		var request UpdatePullRequestParams
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request), "request body should decode as PR update params")
		require.NotNil(t, request.Body, "body_file should be resolved before calling the internal API")
		require.Equal(t, "# Expanded summary\n\nDetails", *request.Body, "resolved body should preserve the Markdown file")

		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`{"status":"updated","session_id":"session-from-token","pull_request_id":"pr-1","pull_request_number":42,"pull_request_url":"https://github.com/acme/repo/pull/42","title":"Existing title","body":"# Expanded summary\n\nDetails"}`))
		require.NoError(t, err, "test response should be written")
	}))
	t.Cleanup(server.Close)

	result, err := NewInternalPullRequestCreator("test-token", server.URL).UpdatePullRequest(context.Background(), UpdatePullRequestParams{BodyFile: bodyPath})

	require.NoError(t, err, "UpdatePullRequest should accept a body file")
	require.Equal(t, "updated", result.Status, "UpdatePullRequest should decode the completed status")
	require.Equal(t, 42, result.PullRequestNumber, "UpdatePullRequest should identify the changed Pull Request")
}

func TestNewInternalPullRequestCreatorUsesLongerUpdateTimeout(t *testing.T) {
	t.Parallel()

	creator := NewInternalPullRequestCreator("test-token", "https://platform.example.com")

	require.Equal(t, 10*time.Second, creator.client.Timeout, "asynchronous Pull Request creation should retain its short internal API timeout")
	require.Equal(t, internalPullRequestUpdateTimeout, creator.updateClient.Timeout, "synchronous Pull Request updates should cover token issuance and the GitHub request window")
}

func TestInternalPullRequestCreator_UpdatePullRequestValidatesBodySources(t *testing.T) {
	t.Parallel()

	body := "inline"
	tests := []struct {
		name   string
		params UpdatePullRequestParams
	}{
		{name: "missing metadata", params: UpdatePullRequestParams{}},
		{name: "inline body and file", params: UpdatePullRequestParams{Body: &body, BodyFile: "/tmp/pr.md"}},
		{name: "missing body file", params: UpdatePullRequestParams{BodyFile: "/path/that/does/not/exist"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewInternalPullRequestCreator("test-token", "http://127.0.0.1").UpdatePullRequest(context.Background(), tt.params)
			require.Error(t, err, "invalid body sources should fail before an API request")
		})
	}
}
