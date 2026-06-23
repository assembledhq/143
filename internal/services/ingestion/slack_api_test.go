package ingestion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newTestSlackClient(baseURL string) *SlackAPIClient {
	return &SlackAPIClient{
		client:  &http.Client{Transport: &http.Transport{}},
		logger:  zerolog.Nop(),
		baseURL: baseURL,
	}
}

func TestGroupIntoThreads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		messages []SlackMessage
		expected map[string][]SlackMessage
	}{
		{
			name:     "empty messages returns empty map",
			messages: []SlackMessage{},
			expected: map[string][]SlackMessage{},
		},
		{
			name: "messages without thread_ts each get their own key",
			messages: []SlackMessage{
				{User: "U1", Text: "hello", Timestamp: "1000.1"},
				{User: "U2", Text: "world", Timestamp: "1000.2"},
			},
			expected: map[string][]SlackMessage{
				"1000.1": {{User: "U1", Text: "hello", Timestamp: "1000.1"}},
				"1000.2": {{User: "U2", Text: "world", Timestamp: "1000.2"}},
			},
		},
		{
			name: "messages with same thread_ts grouped together",
			messages: []SlackMessage{
				{User: "U1", Text: "root", Timestamp: "1000.1", ThreadTS: "1000.1", ReplyCount: 2},
				{User: "U2", Text: "reply 1", Timestamp: "1000.2", ThreadTS: "1000.1"},
				{User: "U3", Text: "reply 2", Timestamp: "1000.3", ThreadTS: "1000.1"},
			},
			expected: map[string][]SlackMessage{
				"1000.1": {
					{User: "U1", Text: "root", Timestamp: "1000.1", ThreadTS: "1000.1", ReplyCount: 2},
					{User: "U2", Text: "reply 1", Timestamp: "1000.2", ThreadTS: "1000.1"},
					{User: "U3", Text: "reply 2", Timestamp: "1000.3", ThreadTS: "1000.1"},
				},
			},
		},
		{
			name: "mix of standalone and threaded messages",
			messages: []SlackMessage{
				{User: "U1", Text: "standalone", Timestamp: "1000.1"},
				{User: "U2", Text: "thread root", Timestamp: "1000.2", ThreadTS: "1000.2", ReplyCount: 1},
				{User: "U3", Text: "thread reply", Timestamp: "1000.3", ThreadTS: "1000.2"},
				{User: "U4", Text: "another standalone", Timestamp: "1000.4"},
			},
			expected: map[string][]SlackMessage{
				"1000.1": {{User: "U1", Text: "standalone", Timestamp: "1000.1"}},
				"1000.2": {
					{User: "U2", Text: "thread root", Timestamp: "1000.2", ThreadTS: "1000.2", ReplyCount: 1},
					{User: "U3", Text: "thread reply", Timestamp: "1000.3", ThreadTS: "1000.2"},
				},
				"1000.4": {{User: "U4", Text: "another standalone", Timestamp: "1000.4"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := GroupIntoThreads(tt.messages)
			require.Equal(t, tt.expected, result, "GroupIntoThreads should return expected thread grouping")
		})
	}
}

func TestSlackAPIClient_FetchChannelMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		expected    []SlackMessage
		expectErr   bool
		errContains string
	}{
		{
			name: "successful response with messages",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "should send auth header")
				require.Equal(t, "C123", r.URL.Query().Get("channel"), "should pass channel ID")
				require.Contains(t, r.URL.Path, "conversations.history", "should call conversations.history")

				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"messages": []SlackMessage{
						{User: "U1", Text: "hello", Timestamp: "1000.1"},
						{User: "U2", Text: "world", Timestamp: "1000.2"},
					},
					"has_more":          false,
					"response_metadata": map[string]string{"next_cursor": ""},
				})
				require.NoError(t, err)
			},
			expected: []SlackMessage{
				{User: "U1", Text: "hello", Timestamp: "1000.1"},
				{User: "U2", Text: "world", Timestamp: "1000.2"},
			},
		},
		{
			name: "slack API error (ok=false)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": "channel_not_found",
				})
				require.NoError(t, err)
			},
			expectErr:   true,
			errContains: "channel_not_found",
		},
		{
			name: "HTTP error (non-200 status)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, err := w.Write([]byte("internal error"))
				require.NoError(t, err)
			},
			expectErr:   true,
			errContains: "status 500",
		},
		{
			name: "rate limiting (429 response)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
			},
			expectErr:   true,
			errContains: "rate limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestSlackClient(server.URL)
			messages, err := client.FetchChannelMessages(context.Background(), "test-token", "C123", time.Now().Add(-1*time.Hour))

			if tt.expectErr {
				require.Error(t, err, "FetchChannelMessages should return an error")
				require.Contains(t, err.Error(), tt.errContains, "error should contain expected message")
				return
			}

			require.NoError(t, err, "FetchChannelMessages should not return an error")
			require.Equal(t, tt.expected, messages, "should return expected messages")
		})
	}
}

func TestSlackAPIClient_FetchThreadReplies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		expected    []SlackMessage
		expectErr   bool
		errContains string
	}{
		{
			name: "successful response with replies",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "should send auth header")
				require.Equal(t, "C123", r.URL.Query().Get("channel"), "should pass channel ID")
				require.Equal(t, "1000.1", r.URL.Query().Get("ts"), "should pass thread timestamp")
				require.Contains(t, r.URL.Path, "conversations.replies", "should call conversations.replies")

				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"messages": []SlackMessage{
						{User: "U1", Text: "root", Timestamp: "1000.1", ThreadTS: "1000.1"},
						{User: "U2", Text: "reply", Timestamp: "1000.2", ThreadTS: "1000.1"},
					},
				})
				require.NoError(t, err)
			},
			expected: []SlackMessage{
				{User: "U1", Text: "root", Timestamp: "1000.1", ThreadTS: "1000.1"},
				{User: "U2", Text: "reply", Timestamp: "1000.2", ThreadTS: "1000.1"},
			},
		},
		{
			name: "slack API error (ok=false)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": "thread_not_found",
				})
				require.NoError(t, err)
			},
			expectErr:   true,
			errContains: "thread_not_found",
		},
		{
			name: "HTTP error (non-200 status)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, err := w.Write([]byte("bad gateway"))
				require.NoError(t, err)
			},
			expectErr:   true,
			errContains: "status 502",
		},
		{
			name: "rate limiting (429 response)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "10")
				w.WriteHeader(http.StatusTooManyRequests)
			},
			expectErr:   true,
			errContains: "rate limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestSlackClient(server.URL)
			messages, err := client.FetchThreadReplies(context.Background(), "test-token", "C123", "1000.1")

			if tt.expectErr {
				require.Error(t, err, "FetchThreadReplies should return an error")
				require.Contains(t, err.Error(), tt.errContains, "error should contain expected message")
				return
			}

			require.NoError(t, err, "FetchThreadReplies should not return an error")
			require.Equal(t, tt.expected, messages, "should return expected replies")
		})
	}
}

func TestSlackAPIClient_WriteMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		call     func(ctx context.Context, client *SlackAPIClient) error
		wantPath string
	}{
		{
			name: "post message",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				_, err := client.PostMessage(ctx, "test-token", "C123", "1000.1", "hello")
				return err
			},
			wantPath: "/chat.postMessage",
		},
		{
			name: "update message",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				return client.UpdateMessage(ctx, "test-token", "C123", "1000.2", "updated")
			},
			wantPath: "/chat.update",
		},
		{
			name: "post message with blocks",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				_, err := client.PostMessageWithBlocks(ctx, "test-token", "C123", "1000.1", "hello", []SlackBlock{
					{Type: "section", Text: &SlackTextObject{Type: "mrkdwn", Text: "hello"}},
				})
				return err
			},
			wantPath: "/chat.postMessage",
		},
		{
			name: "post ephemeral",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				return client.PostEphemeral(ctx, "test-token", "C123", "U123", "private")
			},
			wantPath: "/chat.postEphemeral",
		},
		{
			name: "publish app home",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				return client.PublishHome(ctx, "test-token", "U123", SlackHomeView{Type: "home"})
			},
			wantPath: "/views.publish",
		},
		{
			name: "open modal",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				return client.OpenView(ctx, "test-token", "trigger", SlackHomeView{Type: "modal"})
			},
			wantPath: "/views.open",
		},
		{
			name: "update modal",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				return client.UpdateView(ctx, "test-token", "V123", SlackHomeView{Type: "modal"})
			},
			wantPath: "/views.update",
		},
		{
			name: "delete message",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				return client.DeleteMessage(ctx, "test-token", "C123", "1000.2")
			},
			wantPath: "/chat.delete",
		},
		{
			name: "unfurl",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				return client.Unfurl(ctx, "test-token", "C123", "1000.2", map[string]any{"https://143.dev/sessions/1": map[string]string{"text": "Session"}})
			},
			wantPath: "/chat.unfurl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, tt.wantPath, r.URL.Path, "should call expected Slack method")
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "should send auth header")
				require.Equal(t, "application/json", r.Header.Get("Content-Type"), "should send JSON content type")
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C123", "ts": "1000.2"})
				require.NoError(t, err, "test response should encode")
			}))
			defer server.Close()

			client := newTestSlackClient(server.URL)
			require.NoError(t, tt.call(context.Background(), client), "Slack write method should succeed")
		})
	}
}

func TestSlackAPIClient_WriteMethodSlackAPIFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		body        map[string]any
		headers     map[string]string
		errContains string
	}{
		{
			name:        "not in channel",
			status:      http.StatusOK,
			body:        map[string]any{"ok": false, "error": "not_in_channel"},
			errContains: "not_in_channel",
		},
		{
			name:        "missing scope",
			status:      http.StatusOK,
			body:        map[string]any{"ok": false, "error": "missing_scope"},
			errContains: "missing_scope",
		},
		{
			name:        "invalid auth",
			status:      http.StatusOK,
			body:        map[string]any{"ok": false, "error": "invalid_auth"},
			errContains: "invalid_auth",
		},
		{
			name:        "rate limit",
			status:      http.StatusTooManyRequests,
			headers:     map[string]string{"Retry-After": "30"},
			errContains: "rate limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/chat.postMessage", r.URL.Path, "test should exercise chat.postMessage")
				for key, value := range tt.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(tt.status)
				if tt.body != nil {
					require.NoError(t, json.NewEncoder(w).Encode(tt.body), "test response should encode")
				}
			}))
			defer server.Close()

			client := newTestSlackClient(server.URL)
			_, err := client.PostMessage(context.Background(), "test-token", "C123", "1000.1", "hello")

			require.Error(t, err, "PostMessage should return Slack API failures")
			require.Contains(t, err.Error(), tt.errContains, "PostMessage error should name the Slack API failure")
		})
	}
}

func TestSlackAPIClient_UpdateMessageWithBlocksSendsReplacementBlocks(t *testing.T) {
	t.Parallel()

	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/chat.update", r.URL.Path, "UpdateMessageWithBlocks should call chat.update")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload), "Slack update request should be valid JSON")
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C123", "ts": "1000.2"})
		require.NoError(t, err, "test response should encode")
	}))
	defer server.Close()

	blocks := []SlackBlock{
		{Type: "section", Text: &SlackTextObject{Type: "mrkdwn", Text: "*Completed*"}},
	}
	client := newTestSlackClient(server.URL)

	err := client.UpdateMessageWithBlocks(context.Background(), "test-token", "C123", "1000.2", "Completed", blocks)

	require.NoError(t, err, "UpdateMessageWithBlocks should succeed")
	require.Equal(t, "C123", payload["channel"], "Slack update should target the expected channel")
	require.Equal(t, "1000.2", payload["ts"], "Slack update should target the expected message")
	require.Equal(t, "Completed", payload["text"], "Slack update should include fallback text")
	require.NotEmpty(t, payload["blocks"], "Slack update should replace the visible Block Kit content")
}

func TestSlackAPIClient_ListChannels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		expected    []SlackChannel
		expectErr   bool
		errContains string
	}{
		{
			name: "successful response with channels",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "should send auth header")
				require.Contains(t, r.URL.Path, "conversations.list", "should call conversations.list")
				require.Equal(t, "true", r.URL.Query().Get("exclude_archived"), "should exclude archived")

				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"channels": []map[string]string{
						{"id": "C1", "name": "general"},
						{"id": "C2", "name": "random"},
					},
					"response_metadata": map[string]string{"next_cursor": ""},
				})
				require.NoError(t, err)
			},
			expected: []SlackChannel{
				{ID: "C1", Name: "general"},
				{ID: "C2", Name: "random"},
			},
		},
		{
			name: "slack API error (ok=false)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": "invalid_auth",
				})
				require.NoError(t, err)
			},
			expectErr:   true,
			errContains: "invalid_auth",
		},
		{
			name: "HTTP error (non-200 status)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, err := w.Write([]byte("service unavailable"))
				require.NoError(t, err)
			},
			expectErr:   true,
			errContains: "status 503",
		},
		{
			name: "rate limiting (429 response)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "5")
				w.WriteHeader(http.StatusTooManyRequests)
			},
			expectErr:   true,
			errContains: "rate limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestSlackClient(server.URL)
			channels, err := client.ListChannels(context.Background(), "test-token")

			if tt.expectErr {
				require.Error(t, err, "ListChannels should return an error")
				require.Contains(t, err.Error(), tt.errContains, "error should contain expected message")
				return
			}

			require.NoError(t, err, "ListChannels should not return an error")
			require.Equal(t, tt.expected, channels, "should return expected channels")
		})
	}
}

func TestSlackAPIClient_ReadHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		call     func(ctx context.Context, client *SlackAPIClient) error
		wantPath string
	}{
		{
			name: "auth test",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				_, err := client.AuthTest(ctx, "test-token")
				return err
			},
			wantPath: "/auth.test",
		},
		{
			name: "file info",
			call: func(ctx context.Context, client *SlackAPIClient) error {
				_, err := client.FetchFileInfo(ctx, "test-token", "F123")
				return err
			},
			wantPath: "/files.info",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, tt.wantPath, r.URL.Path, "should call expected Slack read method")
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "should send auth header")
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{
					"ok":      true,
					"user_id": "U143",
					"team_id": "T123",
					"file":    map[string]string{"id": "F123", "name": "trace.txt", "url_private": "https://files.slack.com/F123"},
				})
				require.NoError(t, err, "test response should encode")
			}))
			defer server.Close()

			client := newTestSlackClient(server.URL)
			require.NoError(t, tt.call(context.Background(), client), "Slack read helper should succeed")
		})
	}
}

func TestSlackAPIClient_FetchChannelInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		expected    *SlackChannel
		expectErr   bool
		errContains string
	}{
		{
			name: "successful response with channel info",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "should send auth header")
				require.Equal(t, "C123", r.URL.Query().Get("channel"), "should pass channel ID")
				require.Contains(t, r.URL.Path, "conversations.info", "should call conversations.info")

				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"channel": map[string]string{
						"id":   "C123",
						"name": "engineering",
					},
				})
				require.NoError(t, err)
			},
			expected: &SlackChannel{ID: "C123", Name: "engineering"},
		},
		{
			name: "slack API error (ok=false)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": "channel_not_found",
				})
				require.NoError(t, err)
			},
			expectErr:   true,
			errContains: "channel_not_found",
		},
		{
			name: "HTTP error (non-200 status)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, err := w.Write([]byte("forbidden"))
				require.NoError(t, err)
			},
			expectErr:   true,
			errContains: "status 403",
		},
		{
			name: "rate limiting (429 response)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "15")
				w.WriteHeader(http.StatusTooManyRequests)
			},
			expectErr:   true,
			errContains: "rate limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestSlackClient(server.URL)
			channel, err := client.FetchChannelInfo(context.Background(), "test-token", "C123")

			if tt.expectErr {
				require.Error(t, err, "FetchChannelInfo should return an error")
				require.Contains(t, err.Error(), tt.errContains, "error should contain expected message")
				return
			}

			require.NoError(t, err, "FetchChannelInfo should not return an error")
			require.Equal(t, tt.expected, channel, "should return expected channel info")
		})
	}
}
