package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenRouterProvider_Complete_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path, "should call chat completions endpoint")
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"), "should send bearer token")
		require.Equal(t, "https://example.com", r.Header.Get("HTTP-Referer"), "should send referer header")
		require.Equal(t, "TestApp", r.Header.Get("X-Title"), "should send title header")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatCompletionsResponse{
			Choices: []chatChoice{{Message: chatMessage{Content: "hello from openrouter"}}},
		})
	}))
	defer server.Close()

	p := NewOpenRouterProvider("test-key",
		WithOpenRouterBaseURL(server.URL),
		WithOpenRouterHTTPClient(server.Client()),
		WithOpenRouterAppName("TestApp"),
		WithOpenRouterSiteURL("https://example.com"),
	)
	require.Equal(t, "openrouter", p.Name(), "provider name should be openrouter")

	resp, err := p.Complete(context.Background(), "anthropic/claude-sonnet-4-5", "system", "user prompt", "")
	require.NoError(t, err, "should complete without error")
	require.Equal(t, "hello from openrouter", resp, "should return message content")
}

func TestOpenRouterProvider_Complete_NoHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("HTTP-Referer"), "should not send referer when not configured")
		require.Empty(t, r.Header.Get("X-Title"), "should not send title when not configured")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatCompletionsResponse{
			Choices: []chatChoice{{Message: chatMessage{Content: "ok"}}},
		})
	}))
	defer server.Close()

	p := NewOpenRouterProvider("key", WithOpenRouterBaseURL(server.URL), WithOpenRouterHTTPClient(server.Client()))
	resp, err := p.Complete(context.Background(), "model", "sys", "user", "")
	require.NoError(t, err, "should complete without error")
	require.Equal(t, "ok", resp, "should return message content")
}

func TestOpenRouterProvider_Complete_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("bad gateway"))
	}))
	defer server.Close()

	p := NewOpenRouterProvider("key", WithOpenRouterBaseURL(server.URL), WithOpenRouterHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "model", "sys", "user", "")
	require.Error(t, err, "should return error on server error")
	require.ErrorIs(t, err, ErrServerError, "should wrap server error")
}
