package db

import (
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestValidateCodeReviewTrendFilters(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	previous := start.Add(-24 * time.Hour)
	tests := []struct {
		name          string
		filters       CodeReviewAnalyticsFilters
		expectedError string
	}{
		{name: "legacy ignores geometry", filters: CodeReviewAnalyticsFilters{TrendCurrentStart: &start}},
		{name: "alltime", filters: CodeReviewAnalyticsFilters{IncludeTrend: true}},
		{name: "finite inference", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, CreatedBefore: &end}},
		{name: "nominal rolling", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendSpanSeconds: trendIntPointer(86400)}},
		{name: "explicit", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendCurrentStart: &start, TrendCurrentEnd: &end, TrendPreviousStart: &previous, TrendPreviousEnd: &start}},
		{name: "end-only", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedBefore: &end}, expectedError: "trend requires created_after when created_before is set"},
		{name: "reversed membership", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &end, CreatedBefore: &start}, expectedError: "trend date bounds are reversed"},
		{name: "start-only undefined", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start}, expectedError: "start-only trend requires a nominal span or complete geometry"},
		{name: "incomplete geometry", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendCurrentStart: &start}, expectedError: "trend requires all four geometry bounds"},
		{name: "finite membership outside nominal span", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, CreatedBefore: &end, TrendSpanSeconds: trendIntPointer(86400)}, expectedError: "created_before must precede the exclusive nominal trend end"},
		{name: "zero nominal span", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendSpanSeconds: trendIntPointer(0)}, expectedError: "trend_span_seconds must be positive"},
		{name: "alltime geometry", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, TrendSpanSeconds: trendIntPointer(86400)}, expectedError: "all time cannot specify trend geometry"},
		{name: "two geometry modes", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendSpanSeconds: trendIntPointer(86400), TrendCurrentStart: &start, TrendCurrentEnd: &end, TrendPreviousStart: &previous, TrendPreviousEnd: &start}, expectedError: "trend geometry and nominal span are mutually exclusive"},
		{name: "overlapping periods", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendCurrentStart: &start, TrendCurrentEnd: &end, TrendPreviousStart: &previous, TrendPreviousEnd: &end}, expectedError: "trend_previous_end must equal trend_current_start"},
		{name: "start mismatch", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &previous, TrendCurrentStart: &start, TrendCurrentEnd: &end, TrendPreviousStart: &previous, TrendPreviousEnd: &start}, expectedError: "trend_current_start must equal created_after"},
		{name: "reversed geometry", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendCurrentStart: &start, TrendCurrentEnd: &previous, TrendPreviousStart: &previous, TrendPreviousEnd: &start}, expectedError: "trend geometry bounds must be ordered"},
		{name: "membership beyond geometry", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, CreatedBefore: &end, TrendCurrentStart: &start, TrendCurrentEnd: &end, TrendPreviousStart: &previous, TrendPreviousEnd: &start}, expectedError: "created_before must precede the exclusive trend_current_end"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCodeReviewTrendFilters(tt.filters)
			if tt.expectedError != "" {
				require.EqualError(t, err, tt.expectedError, "invalid trend contract should report its exact validation reason")
				return
			}
			require.NoError(t, err, "valid or legacy requests should preserve their contract")
		})
	}
}

func TestCodeReviewTrendQueryArgs(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	clock := start.Add(14 * time.Hour)
	javascriptEnd := start.Add(24*time.Hour - time.Millisecond)
	normalizedEnd := javascriptEnd.Add(time.Microsecond)
	tooLargeEnd := start.AddDate(11, 0, 0)
	ancient := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	previous := start.AddDate(-11, 0, 0)
	tests := []struct {
		name     string
		filters  CodeReviewAnalyticsFilters
		expected pgx.NamedArgs
	}{
		{name: "millisecond inclusive end advances one microsecond", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, CreatedBefore: &javascriptEnd}, expected: pgx.NamedArgs{"trend_generated_at": clock, "trend_all_time": false, "trend_current_start": start, "trend_current_end": normalizedEnd, "trend_previous_start": start.Add(-normalizedEnd.Sub(start)), "trend_previous_end": start, "trend_membership_end": normalizedEnd, "trend_unavailable_reason": ""}},
		{name: "too large inferred span", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, CreatedBefore: &tooLargeEnd}, expected: pgx.NamedArgs{"trend_generated_at": clock, "trend_all_time": false, "trend_current_start": nil, "trend_current_end": nil, "trend_previous_start": nil, "trend_previous_end": nil, "trend_membership_end": nil, "trend_unavailable_reason": string(models.CodeReviewTrendRangeTooLarge)}},
		{name: "too large nominal span", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendSpanSeconds: trendIntPointer(1 << 62)}, expected: pgx.NamedArgs{"trend_generated_at": clock, "trend_all_time": false, "trend_current_start": nil, "trend_current_end": nil, "trend_previous_start": nil, "trend_previous_end": nil, "trend_membership_end": nil, "trend_unavailable_reason": string(models.CodeReviewTrendRangeTooLarge)}},
		{name: "previous span exceeds limit independently", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendCurrentStart: &start, TrendCurrentEnd: &javascriptEnd, TrendPreviousStart: &previous, TrendPreviousEnd: &start}, expected: pgx.NamedArgs{"trend_generated_at": clock, "trend_all_time": false, "trend_current_start": nil, "trend_current_end": nil, "trend_previous_start": nil, "trend_previous_end": nil, "trend_membership_end": nil, "trend_unavailable_reason": string(models.CodeReviewTrendRangeTooLarge)}},
		{name: "shift underflows representable year", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &ancient, TrendSpanSeconds: trendIntPointer(86400)}, expected: pgx.NamedArgs{"trend_generated_at": clock, "trend_all_time": false, "trend_current_start": nil, "trend_current_end": nil, "trend_previous_start": nil, "trend_previous_end": nil, "trend_membership_end": nil, "trend_unavailable_reason": string(models.CodeReviewTrendUnrepresentableRange)}},
		{name: "submicrosecond equal membership bounds infer one microsecond", filters: CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: trendTimePointer(start.Add(999 * time.Nanosecond)), CreatedBefore: trendTimePointer(start.Add(999 * time.Nanosecond))}, expected: pgx.NamedArgs{"trend_generated_at": clock, "trend_all_time": false, "trend_current_start": start, "trend_current_end": start.Add(time.Microsecond), "trend_previous_start": start.Add(-time.Microsecond), "trend_previous_end": start, "trend_membership_end": start.Add(time.Microsecond), "trend_unavailable_reason": ""}},
		{name: "empty alltime", filters: CodeReviewAnalyticsFilters{IncludeTrend: true}, expected: pgx.NamedArgs{"trend_generated_at": clock, "trend_all_time": true, "trend_current_start": nil, "trend_current_end": nil, "trend_previous_start": nil, "trend_previous_end": nil, "trend_membership_end": nil, "trend_unavailable_reason": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := codeReviewTrendQueryArgs(tt.filters, clock)
			require.NoError(t, err, "valid range should either resolve or become unavailable")
			require.Equal(t, tt.expected, actual, "resolution should retain exact half-open bounds and typed reasons")
		})
	}
}
