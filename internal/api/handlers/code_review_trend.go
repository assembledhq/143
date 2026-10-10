package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
)

func parseCodeReviewTrendFilters(w http.ResponseWriter, r *http.Request, filters *db.CodeReviewAnalyticsFilters) bool {
	q := r.URL.Query()
	if values, supplied := q["include_trend"]; supplied {
		if len(values) != 1 {
			writeError(w, r, http.StatusBadRequest, "INVALID_TREND_QUERY", "include_trend must be a Boolean")
			return false
		}
		value, err := strconv.ParseBool(strings.TrimSpace(values[0]))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_TREND_QUERY", "include_trend must be a Boolean")
			return false
		}
		filters.IncludeTrend = value
	}
	if !filters.IncludeTrend {
		return true
	}
	if values, supplied := q["trend_span_seconds"]; supplied {
		if len(values) != 1 {
			writeError(w, r, http.StatusBadRequest, "INVALID_TREND_QUERY", "trend_span_seconds must be an integer")
			return false
		}
		value, err := strconv.ParseInt(strings.TrimSpace(values[0]), 10, 64)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_TREND_QUERY", "trend_span_seconds must be an integer")
			return false
		}
		filters.TrendSpanSeconds = &value
	}
	for _, field := range []struct {
		name   string
		target **time.Time
	}{
		{"trend_current_start", &filters.TrendCurrentStart}, {"trend_current_end", &filters.TrendCurrentEnd},
		{"trend_previous_start", &filters.TrendPreviousStart}, {"trend_previous_end", &filters.TrendPreviousEnd},
	} {
		values, supplied := q[field.name]
		if !supplied {
			continue
		}
		if len(values) != 1 {
			writeError(w, r, http.StatusBadRequest, "INVALID_TREND_QUERY", field.name+" must be an RFC3339 timestamp")
			return false
		}
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(values[0]))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_TREND_QUERY", field.name+" must be an RFC3339 timestamp")
			return false
		}
		value := parsed.UTC()
		*field.target = &value
	}
	if err := db.ValidateCodeReviewTrendFilters(*filters); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TREND_QUERY", err.Error())
		return false
	}
	return true
}
