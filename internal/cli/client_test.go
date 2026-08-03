package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewClientNormalizesInternalAPIBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		server   string
		expected string
	}{
		{name: "canonical origin", server: "https://143.dev", expected: "https://143.dev"},
		{name: "legacy internal path", server: "https://143.dev/api/v1/internal", expected: "https://143.dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewClient(Config{ServerURL: tt.server})
			require.Equal(t, tt.expected, client.baseURL, "client should append internal routes to an origin-only base URL")
		})
	}
}

func TestClientWithRequestTimeoutClonesHTTPClient(t *testing.T) {
	t.Parallel()

	client := NewClient(Config{ServerURL: "https://143.dev"})
	client.http.Transport = http.DefaultTransport
	previewClient := client.WithRequestTimeout(previewWaitTimeout)

	require.NotSame(t, client, previewClient, "timeout override should return an isolated API client")
	require.NotSame(t, client.http, previewClient.http, "timeout override should not mutate the shared HTTP client")
	require.Equal(t, 60*time.Second, client.http.Timeout, "ordinary API calls should retain the default request timeout")
	require.Equal(t, previewWaitTimeout, previewClient.http.Timeout, "preview calls should use the preview operation timeout")
	require.Same(t, client.http.Transport, previewClient.http.Transport, "timeout override should preserve the configured transport")
}

func TestClientDoParsesRetryAfter(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewClient(Config{ServerURL: server.URL})
	err := client.Do(context.Background(), http.MethodGet, "/status", nil, nil)

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr, "non-success response should return a structured API error")
	require.Equal(t, 7*time.Second, apiErr.RetryAfter, "API error should preserve server-requested retry delay")
	require.False(t, errors.Is(err, context.DeadlineExceeded), "HTTP status errors should not be reported as request timeouts")
}
