package github

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClassifyRetry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 21, 18, 0, 0, 0, time.UTC)
	resetAt := now.Add(24 * time.Minute)
	tests := []struct {
		name               string
		err                error
		expected           RetryClassification
		expectedRetryAfter *time.Duration
	}{
		{
			name: "primary rate limit uses reset timestamp",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Header: githubTestHeaders(map[string]string{
					"X-RateLimit-Remaining": "0",
					"X-RateLimit-Reset":     strconv.FormatInt(resetAt.Unix(), 10),
				}),
			},
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(24 * time.Minute),
		},
		{
			name: "secondary rate limit uses retry after seconds",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Body:       []byte(`{"message":"You have exceeded a secondary rate limit"}`),
				Header:     http.Header{"Retry-After": []string{"17"}},
			},
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(17 * time.Second),
		},
		{
			name: "secondary rate limit uses retry after date",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Body:       []byte(`{"message":"secondary rate limit"}`),
				Header:     githubTestHeaders(map[string]string{"Retry-After": resetAt.Format(http.TimeFormat)}),
			},
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(24 * time.Minute),
		},
		{
			name: "too many requests without hint remains rate limited",
			err:  &GitHubAPIError{StatusCode: http.StatusTooManyRequests},
			expected: RetryClassification{
				Retryable:   true,
				RateLimited: true,
			},
		},
		{
			name: "service unavailable honors server retry delay",
			err: &GitHubAPIError{
				StatusCode: http.StatusServiceUnavailable,
				Header:     githubTestHeaders(map[string]string{"Retry-After": "9"}),
			},
			expected:           RetryClassification{Retryable: true},
			expectedRetryAfter: durationPointer(9 * time.Second),
		},
		{
			name:     "network error is transient",
			err:      &net.DNSError{Err: "temporary failure", IsTemporary: true},
			expected: RetryClassification{Retryable: true},
		},
		{
			name: "forbidden permission error is permanent",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Body:       []byte(`{"message":"Resource not accessible by integration"}`),
			},
			expected: RetryClassification{},
		},
		{
			name:     "validation error is permanent",
			err:      &GitHubAPIError{StatusCode: http.StatusUnprocessableEntity},
			expected: RetryClassification{},
		},
		{
			name:     "cancelled request is not retried",
			err:      context.Canceled,
			expected: RetryClassification{},
		},
		{
			name:     "wrapped deadline is not retried",
			err:      errors.Join(errors.New("request stopped"), context.DeadlineExceeded),
			expected: RetryClassification{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := ClassifyRetry(tt.err, now)
			require.Equal(t, tt.expected.Retryable, actual.Retryable, "classification should identify retryable failures")
			require.Equal(t, tt.expected.RateLimited, actual.RateLimited, "classification should distinguish rate limits from other transient failures")
			require.Equal(t, tt.expectedRetryAfter, actual.RetryAfter, "classification should preserve GitHub's reset delay")
		})
	}
}

func durationPointer(value time.Duration) *time.Duration {
	return &value
}

func githubTestHeaders(values map[string]string) http.Header {
	header := make(http.Header, len(values))
	for name, value := range values {
		header.Set(name, value)
	}
	return header
}
