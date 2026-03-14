package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestComputeTrendDirection(t *testing.T) {
	t.Parallel()

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
			name: "smooth growth is increasing not spike",
			points: []TrendDataPoint{
				{Count: 10},
				{Count: 12},
				{Count: 14},
				{Count: 18},
				{Count: 22},
				{Count: 26},
				{Count: 28},
				{Count: 30},
			},
			want: "increasing",
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
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := computeTrendDirection(tt.points)
			require.Equal(t, tt.want, got, "computeTrendDirection should classify the aggregate trend correctly")
		})
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
