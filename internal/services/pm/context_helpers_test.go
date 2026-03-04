package pm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{
			name:     "returns input when max is non-positive",
			input:    "hello",
			max:      0,
			expected: "hello",
		},
		{
			name:     "returns input when input is shorter than max",
			input:    "hello",
			max:      10,
			expected: "hello",
		},
		{
			name:     "truncates when input is longer than max",
			input:    "truncate-me",
			max:      8,
			expected: "truncate",
		},
		{
			name:     "truncates by rune length",
			input:    "こんにちは世界",
			max:      5,
			expected: "こんにちは",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := truncate(tt.input, tt.max)
			require.Equal(t, tt.expected, got, "truncate should return expected output")
		})
	}
}

func TestHasStackTrace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      json.RawMessage
		expected bool
	}{
		{
			name:     "returns false for empty raw data",
			raw:      nil,
			expected: false,
		},
		{
			name:     "returns false when stacktrace key is missing",
			raw:      json.RawMessage(`{"error":"boom"}`),
			expected: false,
		},
		{
			name:     "returns true when stacktrace key exists",
			raw:      json.RawMessage(`{"stacktrace":"line 1"}`),
			expected: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := hasStackTrace(tt.raw)
			require.Equal(t, tt.expected, got, "hasStackTrace should match expected result")
		})
	}
}

func TestSummarizeIssue(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	longDescription := strings.Repeat("x", 260)
	issue := models.Issue{
		ID:                    uuid.New(),
		Source:                "sentry",
		Title:                 "nil pointer panic",
		Description:           &longDescription,
		Severity:              "critical",
		OccurrenceCount:       42,
		AffectedCustomerCount: 5,
		FirstSeenAt:           now.Add(-2 * time.Hour),
		LastSeenAt:            now,
		Tags:                  []string{"backend", "panic"},
		RawData:               json.RawMessage(`{"stacktrace":"line 1"}`),
	}

	summary := summarizeIssue(issue)
	require.Equal(t, issue.ID.String(), summary.ID, "summary should include issue ID")
	require.Equal(t, issue.Source, summary.Source, "summary should include source")
	require.Equal(t, issue.Title, summary.Title, "summary should include title")
	require.Len(t, []rune(summary.Description), 200, "summary description should be truncated to 200 runes")
	require.Equal(t, issue.Severity, summary.Severity, "summary should include severity")
	require.Equal(t, issue.OccurrenceCount, summary.OccurrenceCount, "summary should include occurrence count")
	require.Equal(t, issue.AffectedCustomerCount, summary.AffectedCustomerCount, "summary should include affected customer count")
	require.Equal(t, issue.FirstSeenAt.Format(time.RFC3339), summary.FirstSeenAt, "summary should format first seen timestamp")
	require.Equal(t, issue.LastSeenAt.Format(time.RFC3339), summary.LastSeenAt, "summary should format last seen timestamp")
	require.Equal(t, issue.Tags, summary.Tags, "summary should include tags")
	require.True(t, summary.HasStackTrace, "summary should detect stacktrace in raw data")
}

