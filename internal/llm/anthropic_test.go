package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicProvider_Complete_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path, "should call messages endpoint")
		require.Equal(t, "test-key", r.Header.Get("x-api-key"), "should send API key header")
		require.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"), "should send version header")

		var req anthropicRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req), "should decode request body")
		require.Equal(t, "test-model", req.Model, "should send correct model")
		require.Equal(t, "system", req.System, "should send system prompt")
		require.Len(t, req.Messages, 1, "should send one message")
		require.Equal(t, "user", req.Messages[0].Role, "message role should be user")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []anthropicContentBlock{{Type: "text", Text: "hello from anthropic"}},
		})
	}))
	defer server.Close()

	p := NewAnthropicProvider("test-key", WithAnthropicBaseURL(server.URL), WithAnthropicHTTPClient(server.Client()))
	require.Equal(t, "anthropic", p.Name(), "provider name should be anthropic")

	resp, err := p.Complete(context.Background(), "test-model", "system", "user prompt", "")
	require.NoError(t, err, "should complete without error")
	require.Equal(t, "hello from anthropic", resp, "should return text content")
}

func TestAnthropicProvider_Complete_RateLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	}))
	defer server.Close()

	p := NewAnthropicProvider("key", WithAnthropicBaseURL(server.URL), WithAnthropicHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "model", "sys", "user", "")
	require.Error(t, err, "should return error on rate limit")
	require.ErrorIs(t, err, ErrRateLimit, "should wrap rate limit error")
}

func TestAnthropicProvider_Complete_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	p := NewAnthropicProvider("key", WithAnthropicBaseURL(server.URL), WithAnthropicHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "model", "sys", "user", "")
	require.Error(t, err, "should return error on server error")
	require.ErrorIs(t, err, ErrServerError, "should wrap server error")
}

func TestAnthropicProvider_Complete_AuthError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid key"))
	}))
	defer server.Close()

	p := NewAnthropicProvider("bad-key", WithAnthropicBaseURL(server.URL), WithAnthropicHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "model", "sys", "user", "")
	require.Error(t, err, "should return error on auth failure")
	require.ErrorIs(t, err, ErrAuthError, "should wrap auth error")
}

func TestAnthropicProvider_Complete_NoTextContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []anthropicContentBlock{{Type: "image", Text: ""}},
		})
	}))
	defer server.Close()

	p := NewAnthropicProvider("key", WithAnthropicBaseURL(server.URL), WithAnthropicHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "model", "sys", "user", "")
	require.Error(t, err, "should return error when no text content")
	require.Contains(t, err.Error(), "no text content", "error should mention missing text content")
}
