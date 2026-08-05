package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type sentryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sentryRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// trackedResponseBody records how a response body was consumed so tests can
// assert that fetchPage drained it before closing it.
type trackedResponseBody struct {
	payload       *strings.Reader
	closed        atomic.Bool
	unreadAtClose atomic.Int64
	closeErr      error
}

func newTrackedResponseBody(payload string) *trackedResponseBody {
	return &trackedResponseBody{payload: strings.NewReader(payload)}
}

func (b *trackedResponseBody) Read(p []byte) (int, error) {
	return b.payload.Read(p)
}

func (b *trackedResponseBody) Close() error {
	b.unreadAtClose.Store(int64(b.payload.Len()))
	b.closed.Store(true)
	return b.closeErr
}

func TestSentryAPIClient_FetchIssues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		since       time.Time
		handler     http.HandlerFunc
		expected    []NormalizedIssue
		expectErr   bool
		errContains string
	}{
		{
			name:  "returns issues from single page",
			since: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "should send auth header")
				require.Equal(t, "is:unresolved", r.URL.Query().Get("query"), "should filter unresolved")
				require.Equal(t, "date", r.URL.Query().Get("sort"), "should sort by date")

				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode([]SentryIssue{
					{
						ID:        "100",
						Title:     "TypeError in handler",
						Level:     "error",
						Count:     "5",
						UserCount: 3,
						FirstSeen: "2024-01-15T10:00:00Z",
						LastSeen:  "2024-01-15T12:00:00Z",
						Metadata: struct {
							Type  string `json:"type"`
							Value string `json:"value"`
						}{Type: "TypeError", Value: "undefined is not a function"},
						Project: struct {
							ID   string `json:"id"`
							Name string `json:"name"`
							Slug string `json:"slug"`
						}{ID: "1", Name: "api", Slug: "api"},
					},
				})
				require.NoError(t, err, "should encode single-page sentry issues response")
			},
			expected: []NormalizedIssue{
				{
					ExternalID:            "100",
					Source:                models.IssueSourceSentry,
					Title:                 "TypeError in handler",
					Description:           "TypeError: undefined is not a function",
					Severity:              "high",
					OccurrenceCount:       5,
					AffectedCustomerCount: 3,
					Tags:                  []string{"project:api"},
					FirstSeenAt:           time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
					LastSeenAt:            time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
				},
			},
		},
		{
			name:  "handles pagination with Link header",
			since: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			handler: func() http.HandlerFunc {
				callCount := 0
				return func(w http.ResponseWriter, r *http.Request) {
					callCount++
					w.Header().Set("Content-Type", "application/json")

					if callCount == 1 {
						// First page - include Link header pointing to next page
						nextURL := fmt.Sprintf("http://%s%s?cursor=page2", r.Host, r.URL.Path)
						w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"; results="true"; cursor="page2"`, nextURL))
						err := json.NewEncoder(w).Encode([]SentryIssue{
							{
								ID: "200", Title: "Error 1", Level: "error",
								Count: "1", UserCount: 1,
								FirstSeen: "2024-01-15T10:00:00Z", LastSeen: "2024-01-15T10:00:00Z",
								Metadata: struct {
									Type  string `json:"type"`
									Value string `json:"value"`
								}{},
								Project: struct {
									ID   string `json:"id"`
									Name string `json:"name"`
									Slug string `json:"slug"`
								}{Slug: "app"},
							},
						})
						require.NoError(t, err, "should encode first paginated sentry response")
					} else {
						// Second page - no next link
						err := json.NewEncoder(w).Encode([]SentryIssue{
							{
								ID: "201", Title: "Error 2", Level: "warning",
								Count: "2", UserCount: 0,
								FirstSeen: "2024-01-15T11:00:00Z", LastSeen: "2024-01-15T11:00:00Z",
								Metadata: struct {
									Type  string `json:"type"`
									Value string `json:"value"`
								}{},
								Project: struct {
									ID   string `json:"id"`
									Name string `json:"name"`
									Slug string `json:"slug"`
								}{Slug: "app"},
							},
						})
						require.NoError(t, err, "should encode second paginated sentry response")
					}
				}
			}(),
			expected: []NormalizedIssue{
				{
					ExternalID:            "200",
					Source:                models.IssueSourceSentry,
					Title:                 "Error 1",
					Description:           "Error 1",
					Severity:              "high",
					OccurrenceCount:       1,
					AffectedCustomerCount: 1,
					Tags:                  []string{"project:app"},
					FirstSeenAt:           time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
					LastSeenAt:            time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
				},
				{
					ExternalID:            "201",
					Source:                models.IssueSourceSentry,
					Title:                 "Error 2",
					Description:           "Error 2",
					Severity:              "medium",
					OccurrenceCount:       2,
					AffectedCustomerCount: 0,
					Tags:                  []string{"project:app"},
					FirstSeenAt:           time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
					LastSeenAt:            time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
				},
			},
		},
		{
			name:  "returns empty slice when no issues",
			since: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode([]SentryIssue{})
				require.NoError(t, err, "should encode empty sentry issues response")
			},
			expected: []NormalizedIssue{},
		},
		{
			name:  "returns error on non-200 response",
			since: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, err := w.Write([]byte(`{"detail":"internal error"}`))
				require.NoError(t, err, "should write sentry error response body")
			},
			expectErr:   true,
			errContains: "unexpected status 500",
		},
		{
			name:  "returns error on invalid JSON response",
			since: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`not json`))
				require.NoError(t, err, "should write invalid JSON response body")
			},
			expectErr:   true,
			errContains: "decode sentry issues",
		},
		{
			name:  "retries on 429 rate limit",
			since: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			handler: func() http.HandlerFunc {
				callCount := 0
				return func(w http.ResponseWriter, r *http.Request) {
					callCount++
					if callCount == 1 {
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					err := json.NewEncoder(w).Encode([]SentryIssue{
						{
							ID: "300", Title: "After rate limit", Level: "error",
							Count: "1", UserCount: 0,
							FirstSeen: "2024-01-15T10:00:00Z", LastSeen: "2024-01-15T10:00:00Z",
							Metadata: struct {
								Type  string `json:"type"`
								Value string `json:"value"`
							}{},
							Project: struct {
								ID   string `json:"id"`
								Name string `json:"name"`
								Slug string `json:"slug"`
							}{Slug: "app"},
						},
					})
					require.NoError(t, err, "should encode sentry issues after rate limit retry")
				}
			}(),
			expected: []NormalizedIssue{
				{
					ExternalID:            "300",
					Source:                models.IssueSourceSentry,
					Title:                 "After rate limit",
					Description:           "After rate limit",
					Severity:              "high",
					OccurrenceCount:       1,
					AffectedCustomerCount: 0,
					Tags:                  []string{"project:app"},
					FirstSeenAt:           time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
					LastSeenAt:            time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := NewSentryAPIClient(server.Client(), zerolog.Nop())
			integrationID := uuid.New()
			issues, err := client.FetchIssues(context.Background(), integrationID, server.URL, "test-token", "test-project", tt.since)

			if tt.expectErr {
				require.Error(t, err, "FetchIssues should return an error")
				require.Contains(t, err.Error(), tt.errContains, "error should contain expected message")
				return
			}

			require.NoError(t, err, "FetchIssues should not return an error")
			require.Equal(t, len(tt.expected), len(issues), "should return expected number of issues")

			for i, expected := range tt.expected {
				actual := issues[i]
				require.Equal(t, expected.ExternalID, actual.ExternalID, "issue %d external ID should match", i)
				require.Equal(t, expected.Source, actual.Source, "issue %d source should match", i)
				require.Equal(t, expected.Title, actual.Title, "issue %d title should match", i)
				require.Equal(t, expected.Description, actual.Description, "issue %d description should match", i)
				require.Equal(t, expected.Severity, actual.Severity, "issue %d severity should match", i)
				require.Equal(t, expected.OccurrenceCount, actual.OccurrenceCount, "issue %d occurrence count should match", i)
				require.Equal(t, expected.AffectedCustomerCount, actual.AffectedCustomerCount, "issue %d affected customer count should match", i)
				require.Equal(t, expected.Tags, actual.Tags, "issue %d tags should match", i)
				require.Equal(t, expected.FirstSeenAt, actual.FirstSeenAt, "issue %d first seen should match", i)
				require.Equal(t, expected.LastSeenAt, actual.LastSeenAt, "issue %d last seen should match", i)
				require.Equal(t, integrationID, actual.SourceIntegrationID, "issue %d integration ID should match", i)
				require.NotNil(t, actual.RawData, "issue %d should have raw data", i)
			}
		})
	}
}

func TestSentryAPIClient_FetchPageClosesRateLimitedResponseBeforeRetry(t *testing.T) {
	t.Parallel()

	rateLimitedBody := newTrackedResponseBody(`{"detail":"rate limited"}`)
	successBody := newTrackedResponseBody(`[{"id":"300"}]`)
	requestCount := 0
	httpClient := &http.Client{Transport: sentryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requestCount++
		if requestCount == 1 {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": []string{"0"}},
				Body:       rateLimitedBody,
			}, nil
		}

		require.True(t, rateLimitedBody.closed.Load(), "rate-limited response body should be closed before retrying")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       successBody,
		}, nil
	})}
	client := NewSentryAPIClient(httpClient, zerolog.Nop())

	issues, nextURL, err := client.fetchPage(context.Background(), "https://sentry.example/issues", "test-token")

	require.NoError(t, err, "fetchPage should succeed after the rate-limit retry")
	require.Equal(t, []SentryIssue{{ID: "300"}}, issues, "fetchPage should return the successful retry response")
	require.Empty(t, nextURL, "fetchPage should not return a next URL without a Link header")
	require.Equal(t, 2, requestCount, "fetchPage should make exactly one retry")
	require.True(t, rateLimitedBody.closed.Load(), "fetchPage should close the rate-limited response body")
	require.True(t, successBody.closed.Load(), "fetchPage should close the successful response body")
	require.Zero(t, rateLimitedBody.unreadAtClose.Load(), "fetchPage should drain the rate-limited response body so the retry can reuse the connection")
}

func TestSentryAPIClient_FetchPageReturnsResponseCloseError(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("close failed")
	body := newTrackedResponseBody(`[]`)
	body.closeErr = closeErr
	httpClient := &http.Client{Transport: sentryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}
	client := NewSentryAPIClient(httpClient, zerolog.Nop())

	issues, nextURL, err := client.fetchPage(context.Background(), "https://sentry.example/issues", "test-token")

	require.ErrorIs(t, err, closeErr, "fetchPage should propagate response body close failures")
	require.Contains(t, err.Error(), "close sentry response body", "fetchPage should identify response cleanup failures")
	require.Nil(t, issues, "fetchPage should not return issues when response cleanup fails")
	require.Empty(t, nextURL, "fetchPage should not return a next URL when response cleanup fails")
	require.True(t, body.closed.Load(), "fetchPage should attempt to close the response body")
}

func TestSentryAPIClient_FetchPagePreservesResponseAndCloseErrors(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("close failed")
	body := newTrackedResponseBody(`{"detail":"internal error"}`)
	body.closeErr = closeErr
	httpClient := &http.Client{Transport: sentryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}
	client := NewSentryAPIClient(httpClient, zerolog.Nop())

	issues, nextURL, err := client.fetchPage(context.Background(), "https://sentry.example/issues", "test-token")

	require.ErrorIs(t, err, closeErr, "fetchPage should preserve response body close failures")
	require.Contains(t, err.Error(), "unexpected status 500", "fetchPage should preserve the Sentry API response failure")
	require.Contains(t, err.Error(), "close sentry response body", "fetchPage should identify the accompanying cleanup failure")
	require.Nil(t, issues, "fetchPage should not return issues when the response fails")
	require.Empty(t, nextURL, "fetchPage should not return a next URL when the response fails")
	require.True(t, body.closed.Load(), "fetchPage should attempt to close a failed response body")
}

func TestParseLinkHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{
			name:     "extracts next URL from Link header",
			header:   `<https://sentry.io/api/0/projects/org/proj/issues/?cursor=123>; rel="next"; results="true"; cursor="123"`,
			expected: "https://sentry.io/api/0/projects/org/proj/issues/?cursor=123",
		},
		{
			name:     "returns empty when no next results",
			header:   `<https://sentry.io/api/0/projects/org/proj/issues/?cursor=123>; rel="next"; results="false"; cursor="123"`,
			expected: "",
		},
		{
			name:     "returns empty when no Link header",
			header:   "",
			expected: "",
		},
		{
			name:     "handles previous and next links",
			header:   `<https://sentry.io/prev>; rel="previous"; results="true", <https://sentry.io/next>; rel="next"; results="true"`,
			expected: "https://sentry.io/next",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := parseLinkHeader(tt.header)
			require.Equal(t, tt.expected, result, "parseLinkHeader should return expected URL")
		})
	}
}
