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
	longDescription := strings.Repeat("x", 600)
	issue := models.Issue{
		ID:                    uuid.New(),
		Source:                models.IssueSourceSentry,
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

	summary := summarizeIssue(issue, 500)
	require.Equal(t, issue.ID.String(), summary.ID, "summary should include issue ID")
	require.Equal(t, string(issue.Source), summary.Source, "summary should include source")
	require.Equal(t, issue.Title, summary.Title, "summary should include title")
	require.Len(t, []rune(summary.Description), 500, "summary description should be truncated to 500 runes")
	require.Equal(t, string(issue.Severity), summary.Severity, "summary should include severity")
	require.Equal(t, issue.OccurrenceCount, summary.OccurrenceCount, "summary should include occurrence count")
	require.Equal(t, issue.AffectedCustomerCount, summary.AffectedCustomerCount, "summary should include affected customer count")
	require.Equal(t, issue.FirstSeenAt.Format(time.RFC3339), summary.FirstSeenAt, "summary should format first seen timestamp")
	require.Equal(t, issue.LastSeenAt.Format(time.RFC3339), summary.LastSeenAt, "summary should format last seen timestamp")
	require.Equal(t, issue.Tags, summary.Tags, "summary should include tags")
	require.True(t, summary.HasStackTrace, "summary should detect stacktrace in raw data")
}

func TestExtractStackTraceSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rawData  json.RawMessage
		contains string
		empty    bool
	}{
		{
			name:  "returns empty for nil raw data",
			empty: true,
		},
		{
			name:    "returns empty for non-exception data",
			rawData: json.RawMessage(`{"entries":[{"type":"message","data":{}}]}`),
			empty:   true,
		},
		{
			name: "extracts top app frames from exception",
			rawData: json.RawMessage(`{"entries":[{"type":"exception","data":{"values":[{
				"type":"TypeError","value":"Cannot read property 'id' of undefined",
				"stacktrace":{"frames":[
					{"filename":"node_modules/express/lib/router.js","function":"process_params","lineNo":335},
					{"filename":"src/handlers/billing.go","function":"handlePayment","lineNo":142},
					{"filename":"src/services/stripe.go","function":"chargeCustomer","lineNo":89}
				]}
			}]}}]}`),
			contains: "TypeError: Cannot read property 'id' of undefined",
		},
		{
			name: "skips vendor frames",
			rawData: json.RawMessage(`{"entries":[{"type":"exception","data":{"values":[{
				"type":"Error","value":"timeout",
				"stacktrace":{"frames":[
					{"filename":"node_modules/pg/lib/pool.js","function":"connect","lineNo":10},
					{"filename":"src/db/pool.go","function":"getConn","lineNo":55}
				]}
			}]}}]}`),
			contains: "src/db/pool.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := extractStackTraceSummary(tt.rawData)
			if tt.empty {
				require.Empty(t, result, "extractStackTraceSummary should return empty string")
				return
			}
			require.Contains(t, result, tt.contains, "extractStackTraceSummary should contain expected text")
		})
	}
}

func TestEnrichLinearMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		rawData            json.RawMessage
		expectedIdentifier string
		expectedState      string
		expectedTeam       string
	}{
		{
			name:               "extracts Linear fields from webhook payload",
			rawData:            json.RawMessage(`{"data":{"identifier":"ENG-123","state":{"name":"In Progress","type":"started"},"team":{"key":"ENG","name":"Engineering"}}}`),
			expectedIdentifier: "ENG-123",
			expectedState:      "In Progress",
			expectedTeam:       "Engineering",
		},
		{
			name:               "falls back to team key when name is empty",
			rawData:            json.RawMessage(`{"data":{"identifier":"FE-45","state":{"name":"Triage","type":"triage"},"team":{"key":"FE","name":""}}}`),
			expectedIdentifier: "FE-45",
			expectedState:      "Triage",
			expectedTeam:       "FE",
		},
		{
			name:               "handles empty raw data gracefully",
			rawData:            nil,
			expectedIdentifier: "",
			expectedState:      "",
			expectedTeam:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			summary := &IssueSummary{}
			enrichLinearMetadata(summary, tt.rawData)
			require.Equal(t, tt.expectedIdentifier, summary.LinearIdentifier, "enrichLinearMetadata should set LinearIdentifier")
			require.Equal(t, tt.expectedState, summary.LinearState, "enrichLinearMetadata should set LinearState")
			require.Equal(t, tt.expectedTeam, summary.LinearTeam, "enrichLinearMetadata should set LinearTeam")
		})
	}
}
