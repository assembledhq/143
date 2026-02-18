package ingestion

import (
	"strconv"
	"time"
)

func parseTimeSafe(s string) time.Time {
	// Try ISO 8601
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t
	}
	// Try without timezone
	t, err = time.Parse("2006-01-02T15:04:05", s)
	if err == nil {
		return t
	}
	return time.Time{}
}

func parseIntSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
