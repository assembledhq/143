package codereview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestVisualEvidenceProductionTransportRejectsLoopbackTLS(t *testing.T) {
	t.Parallel()

	var received atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		received.Add(1)
	}))
	defer server.Close()

	downloader := newVisualEvidenceDownloader(zerolog.Nop())
	defer downloader.client.CloseIdleConnections()
	require.IsType(t, telemetry.NewGitHubAssetTransport(nil, zerolog.Nop()), downloader.client.Transport, "production downloader should retain the selective asset observer around its safe transport")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/image.png", nil)
	require.NoError(t, err, "loopback TLS fixture should construct a valid HTTP request")

	// Bypass fetch's URL validation to exercise the production dial guard itself.
	// Neither the constructor's client nor its wrapped transport is replaced.
	response, err := downloader.client.Do(request)
	require.ErrorContains(t, err, "visual evidence SSRF guard: refusing non-public address", "production transport should refuse loopback before connecting or negotiating TLS")
	require.Nil(t, response, "blocked loopback transport should not produce an HTTP response")
	require.Equal(t, int64(0), received.Load(), "loopback TLS fixture should receive no HTTP request")
}
