package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesProvider_Complete_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path, "should call responses endpoint")
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"), "should send bearer token")

		var req responsesRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req), "should decode request")
		require.Equal(t, "gpt-4o", req.Model, "should send correct model")
		require.Equal(t, "system", req.Instructions, "should send instructions")
		require.Equal(t, "user prompt", req.Input, "should send input")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			Output: []responsesOutput{{
				Type: "message",
				Content: []responsesOutputContent{{Type: "output_text", Text: "hello from responses"}},
			}},
		})
	}))
	defer server.Close()

	p := NewOpenAIResponsesProvider("test-key", WithOpenAIResponsesBaseURL(server.URL), WithOpenAIResponsesHTTPClient(server.Client()))
	require.Equal(t, "openai_responses", p.Name(), "provider name should be openai_responses")

	resp, err := p.Complete(context.Background(), "gpt-4o", "system", "user prompt", "")
	require.NoError(t, err, "should complete without error")
	require.Equal(t, "hello from responses", resp, "should return text content")
}

func TestOpenAIResponsesProvider_Complete_NoTextContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{Output: []responsesOutput{}})
	}))
	defer server.Close()

	p := NewOpenAIResponsesProvider("key", WithOpenAIResponsesBaseURL(server.URL), WithOpenAIResponsesHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "model", "sys", "user", "")
	require.Error(t, err, "should return error when no text content")
	require.Contains(t, err.Error(), "no text content", "error should mention missing text content")
}

func TestOpenAIResponsesProvider_Complete_RateLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	}))
	defer server.Close()

	p := NewOpenAIResponsesProvider("key", WithOpenAIResponsesBaseURL(server.URL), WithOpenAIResponsesHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "model", "sys", "user", "")
	require.Error(t, err, "should return error on rate limit")
	require.ErrorIs(t, err, ErrRateLimit, "should wrap rate limit error")
}
