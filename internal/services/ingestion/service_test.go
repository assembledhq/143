package ingestion

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComputeFingerprint_Deterministic(t *testing.T) {
	fp1 := computeFingerprint("sentry", "12345")
	fp2 := computeFingerprint("sentry", "12345")
	assert.Equal(t, fp1, fp2)
	assert.Len(t, fp1, 32)
}

func TestComputeFingerprint_DifferentInputs(t *testing.T) {
	fp1 := computeFingerprint("sentry", "12345")
	fp2 := computeFingerprint("linear", "12345")
	assert.NotEqual(t, fp1, fp2, "different sources should produce different fingerprints")

	fp3 := computeFingerprint("sentry", "12345")
	fp4 := computeFingerprint("sentry", "67890")
	assert.NotEqual(t, fp3, fp4, "different IDs should produce different fingerprints")
}

func TestNormalizeSeverity(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"fatal", "critical"},
		{"critical", "critical"},
		{"urgent", "critical"},
		{"0", "critical"},
		{"error", "high"},
		{"high", "high"},
		{"1", "high"},
		{"warning", "medium"},
		{"medium", "medium"},
		{"normal", "medium"},
		{"2", "medium"},
		{"info", "low"},
		{"low", "low"},
		{"3", "low"},
		{"4", "low"},
		{"unknown", "medium"},
		{"", "medium"},
		{"FATAL", "critical"}, // case insensitive
		{"Error", "high"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeSeverity(tt.input))
		})
	}
}

func TestCleanText_TruncatesLongText(t *testing.T) {
	long := "a" + string(make([]byte, 600))
	result := cleanText(long, 500)
	assert.Len(t, result, 500)
}

func TestCleanText_TrimsWhitespace(t *testing.T) {
	assert.Equal(t, "hello", cleanText("  hello  ", 100))
}

func TestCleanText_ShortTextUnchanged(t *testing.T) {
	assert.Equal(t, "short", cleanText("short", 500))
}

func TestStrPtr_NonEmpty(t *testing.T) {
	p := strPtr("hello")
	assert.NotNil(t, p)
	assert.Equal(t, "hello", *p)
}

func TestStrPtr_Empty(t *testing.T) {
	assert.Nil(t, strPtr(""))
}
