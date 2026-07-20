package internalapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "canonical origin", input: "https://143.dev", expected: "https://143.dev"},
		{name: "canonical origin trailing slash", input: "https://143.dev/", expected: "https://143.dev"},
		{name: "legacy internal prefix", input: "https://143.dev/api/v1/internal", expected: "https://143.dev"},
		{name: "legacy internal prefix trailing slash", input: " https://143.dev/api/v1/internal/ ", expected: "https://143.dev"},
		{name: "unrelated path remains", input: "https://example.test/proxy", expected: "https://example.test/proxy"},
		{name: "empty remains empty", input: "", expected: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, NormalizeBaseURL(tt.input), "normalization should preserve the internal API origin contract")
		})
	}
}

func TestNormalizeInternalBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{name: "canonical origin", input: "https://143.dev"},
		{name: "legacy internal prefix", input: "https://143.dev/api/v1/internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, "https://143.dev/api/v1/internal", NormalizeInternalBaseURL(tt.input), "integration clients should receive one internal route prefix")
		})
	}
}
