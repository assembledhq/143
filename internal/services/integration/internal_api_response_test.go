package integration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTruncateInternalAPIErrorBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "empty body", body: "", want: ""},
		{name: "body at limit", body: strings.Repeat("a", maxInternalAPIErrorBodyBytes), want: strings.Repeat("a", maxInternalAPIErrorBodyBytes)},
		{name: "body over limit", body: strings.Repeat("a", maxInternalAPIErrorBodyBytes+1), want: strings.Repeat("a", maxInternalAPIErrorBodyBytes) + "...(truncated)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, truncateInternalAPIErrorBody([]byte(tt.body)), "error body truncation should preserve the existing byte limit and marker")
		})
	}
}
