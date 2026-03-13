package integration

import (
	"testing"
	"time"
)

func TestComputeTrendDirection(t *testing.T) {
	tests := []struct {
		name   string
		points []TrendDataPoint
		want   string
	}{
		{
			name:   "empty points",
			points: nil,
			want:   "stable",
		},
		{
			name: "single point",
			points: []TrendDataPoint{
				{Timestamp: time.Now(), Count: 5},
			},
			want: "stable",
		},
		{
			name: "stable trend",
			points: func() []TrendDataPoint {
				pts := make([]TrendDataPoint, 8)
				for i := range pts {
					pts[i] = TrendDataPoint{
						Timestamp: time.Now().Add(time.Duration(i) * time.Hour),
						Count:     10,
					}
				}
				return pts
			}(),
			want: "stable",
		},
		{
			name: "increasing trend",
			points: func() []TrendDataPoint {
				pts := make([]TrendDataPoint, 8)
				for i := range pts {
					pts[i] = TrendDataPoint{
						Timestamp: time.Now().Add(time.Duration(i) * time.Hour),
						Count:     10 + i*3,
					}
				}
				return pts
			}(),
			want: "increasing",
		},
		{
			name: "spike",
			points: func() []TrendDataPoint {
				pts := make([]TrendDataPoint, 8)
				for i := range pts {
					count := 5
					if i >= 6 {
						count = 100
					}
					pts[i] = TrendDataPoint{
						Timestamp: time.Now().Add(time.Duration(i) * time.Hour),
						Count:     count,
					}
				}
				return pts
			}(),
			want: "spike",
		},
		{
			name: "decreasing trend",
			points: func() []TrendDataPoint {
				pts := make([]TrendDataPoint, 8)
				for i := range pts {
					pts[i] = TrendDataPoint{
						Timestamp: time.Now().Add(time.Duration(i) * time.Hour),
						Count:     100 - i*12,
					}
				}
				return pts
			}(),
			want: "decreasing",
		},
		{
			name: "from zero spike",
			points: []TrendDataPoint{
				{Count: 0}, {Count: 0}, {Count: 0}, {Count: 0},
				{Count: 0}, {Count: 0}, {Count: 5}, {Count: 10},
			},
			want: "spike",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeTrendDirection(tt.points)
			if got != tt.want {
				t.Errorf("computeTrendDirection() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMapSentryLevelToSeverity(t *testing.T) {
	tests := []struct {
		level    string
		expected string
	}{
		{"fatal", "critical"},
		{"error", "high"},
		{"warning", "medium"},
		{"info", "low"},
		{"debug", "medium"},
		{"", "medium"},
	}

	for _, tt := range tests {
		got := mapSentryLevelToSeverity(tt.level)
		if got != tt.expected {
			t.Errorf("mapSentryLevelToSeverity(%q) = %q, want %q", tt.level, got, tt.expected)
		}
	}
}

func TestMapSeverityToSentryLevel(t *testing.T) {
	tests := []struct {
		severity string
		expected string
	}{
		{"critical", "fatal"},
		{"high", "error"},
		{"medium", "warning"},
		{"low", "info"},
		{"custom", "custom"},
	}

	for _, tt := range tests {
		got := mapSeverityToSentryLevel(tt.severity)
		if got != tt.expected {
			t.Errorf("mapSeverityToSentryLevel(%q) = %q, want %q", tt.severity, got, tt.expected)
		}
	}
}

func TestParseTimeBestEffort(t *testing.T) {
	tests := []struct {
		input string
		isSet bool
	}{
		{"2024-01-15T10:30:00Z", true},
		{"2024-01-15T10:30:00.123Z", true},
		{"2024-01-15T10:30:00+00:00", true},
		{"not-a-date", false},
		{"", false},
	}

	for _, tt := range tests {
		result := parseTimeBestEffort(tt.input)
		if tt.isSet && result.IsZero() {
			t.Errorf("parseTimeBestEffort(%q) returned zero time, expected non-zero", tt.input)
		}
		if !tt.isSet && !result.IsZero() {
			t.Errorf("parseTimeBestEffort(%q) returned non-zero time, expected zero", tt.input)
		}
	}
}

func TestFormatStatsPeriod(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{1 * time.Hour, "1h"},
		{24 * time.Hour, "24h"},
		{48 * time.Hour, "2d"},
		{7 * 24 * time.Hour, "7d"},
	}

	for _, tt := range tests {
		got := formatStatsPeriod(tt.duration)
		if got != tt.expected {
			t.Errorf("formatStatsPeriod(%v) = %q, want %q", tt.duration, got, tt.expected)
		}
	}
}

func TestSentryErrorTracker_Name(t *testing.T) {
	tracker := NewSentryErrorTracker(SentryTrackerConfig{
		AuthToken: "test-token",
		OrgSlug:   "test-org",
	})
	if tracker.Name() != "sentry" {
		t.Errorf("Name() = %q, want %q", tracker.Name(), "sentry")
	}
}

func TestSentryErrorTracker_DefaultBaseURL(t *testing.T) {
	tracker := NewSentryErrorTracker(SentryTrackerConfig{
		AuthToken: "test-token",
		OrgSlug:   "test-org",
	})
	if tracker.baseURL != "https://sentry.io" {
		t.Errorf("baseURL = %q, want %q", tracker.baseURL, "https://sentry.io")
	}
}

func TestSentryErrorTracker_CustomBaseURL(t *testing.T) {
	tracker := NewSentryErrorTracker(SentryTrackerConfig{
		BaseURL:   "https://sentry.example.com/",
		AuthToken: "test-token",
		OrgSlug:   "test-org",
	})
	if tracker.baseURL != "https://sentry.example.com" {
		t.Errorf("baseURL = %q, want %q", tracker.baseURL, "https://sentry.example.com")
	}
}
