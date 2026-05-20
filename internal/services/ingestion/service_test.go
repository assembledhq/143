package ingestion

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeFingerprint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		externalID string
		compare    func(t *testing.T, fp string)
	}{
		{
			name:       "deterministic for same inputs",
			source:     "sentry",
			externalID: "12345",
			compare: func(t *testing.T, fp string) {
				t.Helper()
				fp2 := computeFingerprint("sentry", "12345")
				require.Equal(t, fp, fp2, "same inputs should produce identical fingerprints")
				require.Equal(t, "sentry:97d01c7db052953ab2eed34a407e8545", fp, "fingerprint should include a readable source prefix and bounded hash")
			},
		},
		{
			name:       "different sources produce different fingerprints",
			source:     "sentry",
			externalID: "12345",
			compare: func(t *testing.T, fp string) {
				t.Helper()
				fp2 := computeFingerprint("linear", "12345")
				require.NotEqual(t, fp, fp2, "different sources should produce different fingerprints")
			},
		},
		{
			name:       "different IDs produce different fingerprints",
			source:     "sentry",
			externalID: "12345",
			compare: func(t *testing.T, fp string) {
				t.Helper()
				fp2 := computeFingerprint("sentry", "67890")
				require.NotEqual(t, fp, fp2, "different external IDs should produce different fingerprints")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fp := computeFingerprint(tt.source, tt.externalID)
			tt.compare(t, fp)
		})
	}
}

func TestNormalizeSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "fatal maps to critical", input: "fatal", expected: "critical"},
		{name: "critical maps to critical", input: "critical", expected: "critical"},
		{name: "urgent maps to critical", input: "urgent", expected: "critical"},
		{name: "numeric 0 maps to critical", input: "0", expected: "critical"},
		{name: "error maps to high", input: "error", expected: "high"},
		{name: "high maps to high", input: "high", expected: "high"},
		{name: "numeric 1 maps to high", input: "1", expected: "high"},
		{name: "warning maps to medium", input: "warning", expected: "medium"},
		{name: "medium maps to medium", input: "medium", expected: "medium"},
		{name: "normal maps to medium", input: "normal", expected: "medium"},
		{name: "numeric 2 maps to medium", input: "2", expected: "medium"},
		{name: "info maps to low", input: "info", expected: "low"},
		{name: "low maps to low", input: "low", expected: "low"},
		{name: "numeric 3 maps to low", input: "3", expected: "low"},
		{name: "numeric 4 maps to low", input: "4", expected: "low"},
		{name: "unknown defaults to medium", input: "unknown", expected: "medium"},
		{name: "empty string defaults to medium", input: "", expected: "medium"},
		{name: "FATAL is case insensitive", input: "FATAL", expected: "critical"},
		{name: "Error is case insensitive", input: "Error", expected: "high"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, normalizeSeverity(tt.input), "normalizeSeverity should return expected severity")
		})
	}
}

func TestCleanText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "truncates text longer than max length",
			input:    "a" + string(make([]byte, 600)),
			maxLen:   500,
			expected: "", // checked via length
		},
		{
			name:     "trims whitespace",
			input:    "  hello  ",
			maxLen:   100,
			expected: "hello",
		},
		{
			name:     "short text is unchanged",
			input:    "short",
			maxLen:   500,
			expected: "short",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := cleanText(tt.input, tt.maxLen)
			if tt.name == "truncates text longer than max length" {
				require.Len(t, result, tt.maxLen, "cleanText should truncate to max length")
			} else {
				require.Equal(t, tt.expected, result, "cleanText should return expected result")
			}
		})
	}
}

func TestStrPtr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected *string
	}{
		{
			name:     "non-empty string returns pointer",
			input:    "hello",
			expected: func() *string { s := "hello"; return &s }(),
		},
		{
			name:     "empty string returns nil",
			input:    "",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := strPtr(tt.input)
			if tt.expected == nil {
				require.Nil(t, result, "strPtr should return nil for empty string")
			} else {
				require.NotNil(t, result, "strPtr should return non-nil pointer for non-empty string")
				require.Equal(t, *tt.expected, *result, "strPtr should return pointer to correct value")
			}
		})
	}
}
