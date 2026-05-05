package observability

import "time"

// DurationMillis converts a time.Duration into milliseconds as a float64,
// preserving microsecond precision. Used as the canonical numeric duration
// field in structured logs so LogsQL percentile/range queries work without
// re-parsing zerolog's Dur string formatting.
func DurationMillis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
