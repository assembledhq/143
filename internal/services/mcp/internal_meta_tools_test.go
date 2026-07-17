package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewInternalMetaToolSourceNormalizesInternalAPIBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		apiURL   string
		expected string
	}{
		{name: "canonical origin", apiURL: "https://143.dev", expected: "https://143.dev"},
		{name: "legacy internal path", apiURL: "https://143.dev/api/v1/internal", expected: "https://143.dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source, ok := NewInternalMetaToolSource(staticToolSource{}, "token", tt.apiURL).(*internalMetaToolSource)
			require.True(t, ok, "constructor should return the internal meta tool source")
			require.Equal(t, tt.expected, source.apiURL, "internal meta tools should append routes to an origin-only base URL")
		})
	}
}
