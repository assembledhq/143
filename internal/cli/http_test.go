package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// httptest.Server.Close flushes http.DefaultTransport's idle connections,
// including other parallel tests' connections. CLI entry points create their
// own clients using that transport, so drain only this fixture's server.
func closeCLIHTTPTestServer(t *testing.T, server *httptest.Server) {
	t.Helper()
	defer server.Client().CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Config.Shutdown(ctx); err != nil {
		t.Errorf("drain HTTP test fixture: %v", err)
		require.NoError(t, server.Config.Close(), "timed-out fixture must still release its connections")
	}
}

func TestHTTPFixtureCleanupPreservesOtherClientConnections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
	}{
		{name: "successful response", status: http.StatusNoContent},
		{name: "unavailable response", status: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			unrelated := httptest.NewServer(http.NotFoundHandler())
			defer closeCLIHTTPTestServer(t, unrelated)
			serving := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer closeCLIHTTPTestServer(t, serving)
			client := NewClient(Config{ServerURL: serving.URL})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			request := func(ctx context.Context) {
				t.Helper()
				err := client.Do(ctx, http.MethodGet, "/status", nil, nil)
				if tt.status == http.StatusNoContent {
					require.NoError(t, err, "fixture should return a successful response")
					return
				}
				var apiErr *APIError
				require.ErrorAs(t, err, &apiErr, "fixture should preserve the server's structured HTTP error")
				require.Equal(t, tt.status, apiErr.Status, "HTTP error must retain the server's exact status")
			}
			request(ctx)
			closeCLIHTTPTestServer(t, unrelated)
			var reused bool
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
				reused = info.Reused
			}})
			request(ctx)
			require.True(t, reused, "closing another fixture must not flush this client's idle connection")
		})
	}
}
