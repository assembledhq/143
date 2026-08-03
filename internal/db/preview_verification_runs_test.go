package db

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizedJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input json.RawMessage
		want  string
	}{
		{name: "nil becomes empty array", input: nil, want: "[]"},
		{name: "empty becomes empty array", input: json.RawMessage(""), want: "[]"},
		// A nil Go slice marshals to JSON null; it must not reach the jsonb array CHECK.
		{name: "json null becomes empty array", input: json.RawMessage("null"), want: "[]"},
		{name: "padded json null becomes empty array", input: json.RawMessage("  null "), want: "[]"},
		{name: "populated array is preserved", input: json.RawMessage(`[{"path":"/"}]`), want: `[{"path":"/"}]`},
		{name: "empty array is preserved", input: json.RawMessage("[]"), want: "[]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(normalizedJSON(tc.input)); got != tc.want {
				t.Fatalf("normalizedJSON(%q) = %q, want %q", string(tc.input), got, tc.want)
			}
		})
	}
}

func TestNormalizePreviewVerificationStepCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    json.RawMessage
		expected string
	}{
		{
			name:     "adds capture to historical step",
			input:    json.RawMessage(`[{"index":1,"artifact":{"id":"legacy-1"}}]`),
			expected: `[{"index":1,"artifact":{"id":"legacy-1"},"capture":{"id":"legacy-1"}}]`,
		},
		{
			name:     "adds legacy key to current step",
			input:    json.RawMessage(`[{"index":1,"capture":{"id":"capture-1"}}]`),
			expected: `[{"index":1,"artifact":{"id":"capture-1"},"capture":{"id":"capture-1"}}]`,
		},
		{
			name:     "preserves invalid json for caller diagnostics",
			input:    json.RawMessage(`{`),
			expected: `{`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := normalizePreviewVerificationStepCompatibility(tt.input)
			if json.Valid(tt.input) {
				require.JSONEq(t, tt.expected, string(actual), "normalizer should expose both verification step keys")
				return
			}
			require.Equal(t, tt.expected, string(actual), "normalizer should preserve malformed historical JSON")
		})
	}
}
