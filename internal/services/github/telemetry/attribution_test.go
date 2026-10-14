package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestHealthOriginLogDimension(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, origin, expected string }{
		{name: "sweep", origin: "sweep", expected: "sweep"},
		{name: "queued", origin: "queued", expected: "queued"},
		{name: "direct", origin: "direct", expected: "direct"},
		{name: "unbounded value omitted", origin: "private-freeform-value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			RecordExternalRequest(context.Background(), zerolog.New(&output), RequestMetadata{HealthOrigin: tt.origin}, http.MethodGet, "/graphql", http.StatusOK, nil, time.Millisecond)
			var event map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &event), "request event should be structured JSON")
			if tt.expected == "" {
				require.NotContains(t, event, "github_health_origin", "invalid origin must not become an unbounded log dimension")
			} else {
				require.Equal(t, tt.expected, event["github_health_origin"], "request should retain the actual health read origin")
			}
		})
	}
}
