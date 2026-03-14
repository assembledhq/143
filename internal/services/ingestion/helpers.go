package ingestion

import (
	"strconv"
	"time"
)

// ParseTimeSafe parses a time string trying multiple common formats.
// Returns zero time if none match.
func ParseTimeSafe(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ParseIntSafe parses a string to int, returning 0 on failure.
func ParseIntSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// MapSentryLevel maps a Sentry severity level to a normalized severity string.
func MapSentryLevel(level string) string {
	switch level {
	case "fatal":
		return "critical"
	case "error":
		return "high"
	case "warning":
		return "medium"
	case "info":
		return "low"
	default:
		return "medium"
	}
}

// MapLinearPriority maps a Linear priority integer to a normalized severity string.
func MapLinearPriority(priority int) string {
	switch priority {
	case 0:
		return "medium" // No priority
	case 1:
		return "critical" // Urgent
	case 2:
		return "high"
	case 3:
		return "medium"
	case 4:
		return "low"
	default:
		return "medium"
	}
}
