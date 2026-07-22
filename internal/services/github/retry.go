package github

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryClassification describes whether a GitHub request failure is safe to
// retry and, for rate limits, how long GitHub asked the caller to wait.
type RetryClassification struct {
	Retryable   bool
	RateLimited bool
	RetryAfter  *time.Duration
}

// ClassifyRetry classifies GitHub API and transport failures without relying
// on error strings. now is supplied by the caller so reset timestamps can be
// tested deterministically.
func ClassifyRetry(err error, now time.Time) RetryClassification {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return RetryClassification{}
	}

	var apiErr *GitHubAPIError
	if errors.As(err, &apiErr) {
		rateLimited := apiErr.StatusCode == http.StatusTooManyRequests || githubRateLimited(apiErr)
		retryable := rateLimited ||
			apiErr.StatusCode == http.StatusRequestTimeout ||
			apiErr.StatusCode == http.StatusTooEarly ||
			apiErr.StatusCode >= http.StatusInternalServerError
		if !retryable {
			return RetryClassification{}
		}
		return RetryClassification{
			Retryable:   true,
			RateLimited: rateLimited,
			RetryAfter:  githubRetryAfter(apiErr, now),
		}
	}

	var networkErr net.Error
	return RetryClassification{Retryable: errors.As(err, &networkErr)}
}

func githubRateLimited(apiErr *GitHubAPIError) bool {
	if apiErr == nil || apiErr.StatusCode != http.StatusForbidden {
		return false
	}
	if strings.TrimSpace(apiErr.Header.Get("Retry-After")) != "" || strings.TrimSpace(apiErr.Header.Get("X-RateLimit-Remaining")) == "0" {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(apiErr.Message()))
	return strings.Contains(message, "rate limit") || strings.Contains(message, "abuse detection")
}

func githubRetryAfter(apiErr *GitHubAPIError, now time.Time) *time.Duration {
	if apiErr == nil {
		return nil
	}
	if raw := strings.TrimSpace(apiErr.Header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
			delay := time.Duration(seconds) * time.Second
			return &delay
		}
		if retryAt, err := http.ParseTime(raw); err == nil {
			return nonNegativeRetryDelay(retryAt, now)
		}
	}
	if strings.TrimSpace(apiErr.Header.Get("X-RateLimit-Remaining")) == "0" {
		if resetUnix, err := strconv.ParseInt(strings.TrimSpace(apiErr.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			return nonNegativeRetryDelay(time.Unix(resetUnix, 0), now)
		}
	}
	return nil
}

func nonNegativeRetryDelay(retryAt, now time.Time) *time.Duration {
	delay := retryAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return &delay
}
