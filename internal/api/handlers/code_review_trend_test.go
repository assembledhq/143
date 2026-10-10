package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/stretchr/testify/require"
)

func TestParseCodeReviewTrendFilters(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	previous := start.Add(-24 * time.Hour)
	span := int64(86400)
	tests := []struct {
		name, query     string
		expected        db.CodeReviewAnalyticsFilters
		expectedStatus  int
		expectedMessage string
	}{
		{name: "legacy", query: "trend_current_start=invalid", expectedStatus: 200},
		{name: "false", query: "include_trend=false&trend_span_seconds=invalid", expectedStatus: 200},
		{name: "alltime", query: "include_trend=true", expected: db.CodeReviewAnalyticsFilters{IncludeTrend: true}, expectedStatus: 200},
		{name: "rolling", query: "include_trend=true&created_after=2026-10-01T00:00:00Z&trend_span_seconds=86400", expected: db.CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendSpanSeconds: &span}, expectedStatus: 200},
		{name: "explicit", query: "include_trend=true&created_after=2026-10-01T00:00:00Z&trend_current_start=2026-10-01T00:00:00Z&trend_current_end=2026-10-02T00:00:00Z&trend_previous_start=2026-09-30T00:00:00Z&trend_previous_end=2026-10-01T00:00:00Z", expected: db.CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendCurrentStart: &start, TrendCurrentEnd: &end, TrendPreviousStart: &previous, TrendPreviousEnd: &start}, expectedStatus: 200},
		{name: "invalid Boolean", query: "include_trend=maybe", expectedStatus: 400, expectedMessage: "include_trend must be a Boolean"},
		{name: "empty Boolean", query: "include_trend=", expectedStatus: 400, expectedMessage: "include_trend must be a Boolean"},
		{name: "duplicate Boolean", query: "include_trend=true&include_trend=false", expectedStatus: 400, expectedMessage: "include_trend must be a Boolean"},
		{name: "malformed span", query: "include_trend=true&created_after=2026-10-01T00:00:00Z&trend_span_seconds=nan", expectedStatus: 400, expectedMessage: "trend_span_seconds must be an integer"},
		{name: "duplicate span", query: "include_trend=true&created_after=2026-10-01T00:00:00Z&trend_span_seconds=1&trend_span_seconds=2", expectedStatus: 400, expectedMessage: "trend_span_seconds must be an integer"},
		{name: "malformed geometry", query: "include_trend=true&trend_current_start=bad", expectedStatus: 400, expectedMessage: "trend_current_start must be an RFC3339 timestamp"},
		{name: "duplicate geometry", query: "include_trend=true&trend_current_start=2026-10-01T00:00:00Z&trend_current_start=2026-10-01T00:00:00Z", expectedStatus: 400, expectedMessage: "trend_current_start must be an RFC3339 timestamp"},
		{name: "finite membership exceeds nominal span", query: "include_trend=true&created_after=2026-10-01T00:00:00Z&created_before=2026-10-11T00:00:00Z&trend_span_seconds=86400", expectedStatus: 400, expectedMessage: "created_before must precede the exclusive nominal trend end"},
		{name: "submicrosecond explicit window collapses", query: "include_trend=true&created_after=2026-10-01T00:00:00.000000111Z&trend_current_start=2026-10-01T00:00:00.000000111Z&trend_current_end=2026-10-01T00:00:00.000000999Z&trend_previous_start=2026-09-30T23:59:59.999999Z&trend_previous_end=2026-10-01T00:00:00.000000111Z", expectedStatus: 400, expectedMessage: "trend geometry bounds must be ordered"},
		{name: "start-only", query: "include_trend=true&created_after=2026-10-01T00:00:00Z", expectedStatus: 400, expectedMessage: "start-only trend requires a nominal span or complete geometry"},
		{name: "end-only", query: "include_trend=true&created_before=2026-10-02T00:00:00Z", expectedStatus: 400, expectedMessage: "trend requires created_after when created_before is set"},
		{name: "reversed", query: "include_trend=true&created_after=2026-10-02T00:00:00Z&created_before=2026-10-01T00:00:00Z", expectedStatus: 400, expectedMessage: "trend date bounds are reversed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/analytics?"+tt.query, nil)
			recorder := httptest.NewRecorder()
			actual, ok := parseCodeReviewAnalyticsFilters(recorder, request)
			require.Equal(t, tt.expectedStatus, recorder.Code, "parsing should preserve status contract")
			require.Equal(t, tt.expectedStatus == 200, ok, "invalid trend query should stop request processing")
			if tt.expectedStatus == 200 {
				require.Equal(t, tt.expected, actual, "parser should preserve exact UTC geometry and membership filters")
			} else {
				require.JSONEq(t, `{"error":{"code":"INVALID_TREND_QUERY","message":`+quoteTrendMessage(tt.expectedMessage)+`}}`, recorder.Body.String(), "errors should have exact structured code and safe validation message")
			}
		})
	}
}
func quoteTrendMessage(value string) string { // These fixture messages have no escaped characters.
	return `"` + value + `"`
}
