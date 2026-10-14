package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewTrendEnums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		validate func() error
		valid    bool
	}{
		{"available", CodeReviewTrendAvailable.Validate, true}, {"unavailable", CodeReviewTrendUnavailable.Validate, true}, {"bad status", CodeReviewTrendStatus("bad").Validate, false},
		{"finite", CodeReviewTrendFinite.Validate, true}, {"all time", CodeReviewTrendAllTime.Validate, true}, {"bad mode", CodeReviewTrendMode("bad").Validate, false},
		{"range too large", CodeReviewTrendRangeTooLarge.Validate, true}, {"unrepresentable", CodeReviewTrendUnrepresentableRange.Validate, true}, {"inconsistent", CodeReviewTrendInconsistentGeometry.Validate, true}, {"bad reason", CodeReviewTrendUnavailableReason("bad").Validate, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.validate()
			if tt.valid {
				require.NoError(t, err, "known wire enum should be valid")
			} else {
				require.Error(t, err, "unknown wire enum must be rejected")
			}
		})
	}
}
func TestCodeReviewTrendJSON(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		value    CodeReviewTrend
		expected string
	}{
		{name: "unavailable omits geometry", value: CodeReviewTrend{Status: CodeReviewTrendUnavailable, GeneratedAt: now, UnavailableReason: CodeReviewTrendRangeTooLarge}, expected: `{"status":"unavailable","generated_at":"2026-10-09T18:00:00Z","unavailable_reason":"range_too_large"}`},
		{name: "available empty all time keeps nulls", value: CodeReviewTrend{Status: CodeReviewTrendAvailable, Mode: CodeReviewTrendAllTime, GeneratedAt: now, BucketWidthSeconds: 86400, Points: []CodeReviewTrendPoint{}}, expected: `{"status":"available","mode":"all_time","generated_at":"2026-10-09T18:00:00Z","bucket_width_seconds":86400,"current_window":null,"previous_window":null,"observed_end_advanced_by_data":false,"overflow_prs":0,"latest_included_first_requested_at":null,"points":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := json.Marshal(tt.value)
			require.NoError(t, err, "trend should encode its discriminated response")
			require.JSONEq(t, tt.expected, string(actual), "wire discriminator should preserve exact fields and omission semantics")
			require.NoError(t, tt.value.Validate(), "response fixture should have a valid status-specific enum")
		})
	}
}
