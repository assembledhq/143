package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	t.Parallel()

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

func TestSentryErrorTracker_Name(t *testing.T) {
	t.Parallel()

	tracker := NewSentryErrorTracker(SentryTrackerConfig{
		AuthToken: "test-token",
		OrgSlug:   "test-org",
	})
	if tracker.Name() != "sentry" {
		t.Errorf("Name() = %q, want %q", tracker.Name(), "sentry")
	}
}

func TestSentryErrorTracker_DefaultBaseURL(t *testing.T) {
	t.Parallel()

	tracker := NewSentryErrorTracker(SentryTrackerConfig{
		AuthToken: "test-token",
		OrgSlug:   "test-org",
	})
	if tracker.baseURL != "https://sentry.io" {
		t.Errorf("baseURL = %q, want %q", tracker.baseURL, "https://sentry.io")
	}
}

func TestSentryErrorTracker_CustomBaseURL(t *testing.T) {
	t.Parallel()

	tracker := NewSentryErrorTracker(SentryTrackerConfig{
		BaseURL:   "https://sentry.example.com/",
		AuthToken: "test-token",
		OrgSlug:   "test-org",
	})
	if tracker.baseURL != "https://sentry.example.com" {
		t.Errorf("baseURL = %q, want %q", tracker.baseURL, "https://sentry.example.com")
	}
}

func TestSentryErrorTracker_ListErrors_AuthAndEncoding(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Bearer auth header.
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"),
			"ListErrors should send a Bearer auth header")

		// Verify query parameter is URL-encoded.
		query := r.URL.Query().Get("query")
		require.Contains(t, query, "is:unresolved",
			"ListErrors should include the base unresolved filter")

		sort := r.URL.Query().Get("sort")
		require.Equal(t, "date", sort, "ListErrors should sort by date")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":        "1",
				"title":     "Test Error",
				"culprit":   "module.function",
				"level":     "error",
				"count":     "5",
				"userCount": 3,
				"firstSeen": "2024-01-15T10:00:00Z",
				"lastSeen":  "2024-01-16T12:00:00Z",
				"project":   map[string]string{"slug": "test-proj"},
			},
		})
	}))
	defer server.Close()

	tracker := NewSentryErrorTracker(SentryTrackerConfig{
		BaseURL:   server.URL,
		AuthToken: "test-token",
		OrgSlug:   "test-org",
	})

	summaries, err := tracker.ListErrors(context.Background(), ErrorFilter{Limit: 10})
	require.NoError(t, err, "ListErrors should succeed with a mock server")
	require.Len(t, summaries, 1, "ListErrors should return the issues from the mock server")
	require.Equal(t, "1", summaries[0].ID)
	require.Equal(t, "Test Error", summaries[0].Title)
}

func TestSentryErrorTracker_FindRelated_SpecialCharacters(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		switch callCount {
		case 1:
			// GetError: issue detail.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":        "123",
				"title":     "Error with special culprit",
				"culprit":   "module.func & bar#baz",
				"level":     "error",
				"count":     "1",
				"firstSeen": "2024-01-15T10:00:00Z",
				"lastSeen":  "2024-01-16T12:00:00Z",
				"project":   map[string]string{"slug": "proj"},
			})
		case 2:
			// GetError: latest event (return empty).
			json.NewEncoder(w).Encode(map[string]interface{}{
				"eventID": "evt-1",
				"entries": []interface{}{},
				"tags":    []interface{}{},
			})
		case 3:
			// FindRelated: verify query is URL-encoded.
			query := r.URL.Query().Get("query")
			require.Contains(t, query, "module.func & bar#baz",
				"FindRelated should pass the culprit as a decoded query value (URL encoding handled by net/url)")

			json.NewEncoder(w).Encode([]map[string]interface{}{
				{
					"id":        "456",
					"title":     "Related error",
					"culprit":   "module.func & bar#baz",
					"level":     "error",
					"count":     "1",
					"firstSeen": "2024-01-15T10:00:00Z",
					"lastSeen":  "2024-01-16T12:00:00Z",
					"project":   map[string]string{"slug": "proj"},
				},
			})
		}
	}))
	defer server.Close()

	tracker := NewSentryErrorTracker(SentryTrackerConfig{
		BaseURL:   server.URL,
		AuthToken: "test-token",
		OrgSlug:   "test-org",
	})

	related, err := tracker.FindRelated(context.Background(), "123")
	require.NoError(t, err, "FindRelated should handle special characters in culprit")
	require.Len(t, related, 1, "FindRelated should return related issues excluding the source issue")
	require.Equal(t, "456", related[0].ID)
}
