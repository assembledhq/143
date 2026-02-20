package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIChatProvider_Complete_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path, "should call chat completions endpoint")
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"), "should send bearer token")

		var req chatCompletionsRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req), "should decode request")
		require.Equal(t, "gpt-4o", req.Model, "should send correct model")
		require.Len(t, req.Messages, 2, "should send system and user messages")
		require.Equal(t, "system", req.Messages[0].Role, "first message should be system")
		require.Equal(t, "user", req.Messages[1].Role, "second message should be user")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatCompletionsResponse{
			Choices: []chatChoice{{Message: chatMessage{Content: "hello from openai"}}},
		})
	}))
	defer server.Close()

	p := NewOpenAIChatProvider("test-key", WithOpenAIChatBaseURL(server.URL), WithOpenAIChatHTTPClient(server.Client()))
	require.Equal(t, "openai_chat", p.Name(), "provider name should be openai_chat")

	resp, err := p.Complete(context.Background(), "gpt-4o", "system", "user prompt")
	require.NoError(t, err, "should complete without error")
	require.Equal(t, "hello from openai", resp, "should return message content")
}

func TestOpenAIChatProvider_Complete_NoChoices(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatCompletionsResponse{Choices: []chatChoice{}})
	}))
	defer server.Close()

	p := NewOpenAIChatProvider("key", WithOpenAIChatBaseURL(server.URL), WithOpenAIChatHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "model", "sys", "user")
	require.Error(t, err, "should return error when no choices")
	require.Contains(t, err.Error(), "no choices", "error should mention missing choices")
}

func TestOpenAIChatProvider_Complete_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server down"))
	}))
	defer server.Close()

	p := NewOpenAIChatProvider("key", WithOpenAIChatBaseURL(server.URL), WithOpenAIChatHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "model", "sys", "user")
	require.Error(t, err, "should return error on server error")
	require.ErrorIs(t, err, ErrServerError, "should wrap server error")
}
