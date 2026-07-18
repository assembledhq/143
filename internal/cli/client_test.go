package cli

import (
	"testing"

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
