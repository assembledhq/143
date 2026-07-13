package db

import (
	"encoding/json"
	"testing"
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
