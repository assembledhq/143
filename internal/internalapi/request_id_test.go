package internalapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeParentRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "valid request id", value: " host.example/random-000123 ", want: "host.example/random-000123"},
		{name: "empty request id", value: " "},
		{name: "oversized request id", value: strings.Repeat("a", 129)},
		{name: "header injection", value: "request-id\nspoofed"},
		{name: "spaces inside id", value: "request id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, NormalizeParentRequestID(tt.value), "parent request id should be safe for headers and structured logs")
		})
	}
}
