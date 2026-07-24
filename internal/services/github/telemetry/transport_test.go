package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTransportRoundTripLogsStructuredGitHubTelemetry(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	resetAt := startedAt.Add(5 * time.Minute)
	tests := []struct {
		name             string
		metadata         RequestMetadata
		requestURL       string
		response         *http.Response
		requestErr       error
		expectedLogEvent map[string]any
	}{
		{
			name: "successful installation request records quota and normalized route",
			metadata: RequestMetadata{
				Kind:           RequestKindAPI,
				AuthType:       AuthTypeAppInstallation,
				InstallationID: 42,
			},
			requestURL: "https://api.github.com/repos/assembledhq/143/pulls/99?ignored=true",
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
				Header: http.Header{
					"X-Ratelimit-Limit":     []string{"5000"},
					"X-Ratelimit-Remaining": []string{"1250"},
					"X-Ratelimit-Used":      []string{"3750"},
					"X-Ratelimit-Reset":     []string{strconv.FormatInt(resetAt.Unix(), 10)},
					"X-Ratelimit-Resource":  []string{"core"},
					"X-Github-Request-Id":   []string{"request-123"},
				},
			},
			expectedLogEvent: map[string]any{
				"level":                           "info",
				"message":                         "github api request",
				"github_method":                   http.MethodGet,
				"github_route":                    "/repos/:owner/:repo/pulls/:id",
				"github_repository":               "assembledhq/143",
				"github_auth_type":                AuthTypeAppInstallation,
				"github_installation_id":          float64(42),
				"github_status_code":              float64(http.StatusOK),
				"github_status_class":             "2xx",
				"github_result":                   "success",
				"github_duration_ms":              float64(250),
				"github_rate_limited":             false,
				"github_rate_limit_limit":         float64(5000),
				"github_rate_limit_remaining":     float64(1250),
				"github_rate_limit_used":          float64(3750),
				"github_rate_limit_remaining_pct": float64(25),
				"github_rate_limit_reset_unix":    float64(resetAt.Unix()),
				"github_rate_limit_reset_at":      resetAt.Format(time.RFC3339),
				"github_rate_limit_reset_seconds": float64(299.75),
				"github_rate_limit_resource":      "core",
				"github_request_id":               "request-123",
			},
		},
		{
			name: "primary rate limit is a warning",
			metadata: RequestMetadata{
				Kind:           RequestKindAPI,
				AuthType:       AuthTypeAppInstallation,
				InstallationID: 42,
			},
			requestURL: "https://api.github.com/repos/assembledhq/143/pulls/99",
			response: &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(bytes.NewBufferString(`{"message":"rate limited"}`)),
				Header: http.Header{
					"X-Ratelimit-Limit":     []string{"5000"},
					"X-Ratelimit-Remaining": []string{"0"},
					"X-Ratelimit-Used":      []string{"5000"},
					"X-Ratelimit-Reset":     []string{strconv.FormatInt(resetAt.Unix(), 10)},
					"X-Ratelimit-Resource":  []string{"core"},
				},
			},
			expectedLogEvent: map[string]any{
				"level":                           "warn",
				"message":                         "github api request",
				"github_method":                   http.MethodGet,
				"github_route":                    "/repos/:owner/:repo/pulls/:id",
				"github_repository":               "assembledhq/143",
				"github_auth_type":                AuthTypeAppInstallation,
				"github_installation_id":          float64(42),
				"github_status_code":              float64(http.StatusForbidden),
				"github_status_class":             "4xx",
				"github_result":                   "rate_limited",
				"github_duration_ms":              float64(250),
				"github_rate_limited":             true,
				"github_rate_limit_kind":          "primary",
				"github_rate_limit_limit":         float64(5000),
				"github_rate_limit_remaining":     float64(0),
				"github_rate_limit_used":          float64(5000),
				"github_rate_limit_remaining_pct": float64(0),
				"github_rate_limit_reset_unix":    float64(resetAt.Unix()),
				"github_rate_limit_reset_at":      resetAt.Format(time.RFC3339),
				"github_rate_limit_reset_seconds": float64(299.75),
				"github_rate_limit_resource":      "core",
			},
		},
		{
			name: "secondary rate limit records retry guidance",
			metadata: RequestMetadata{
				Kind:     RequestKindAPI,
				AuthType: AuthTypeUser,
			},
			requestURL: "https://api.github.com/graphql",
			response: &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(bytes.NewBufferString(`{"message":"secondary rate limit"}`)),
				Header: http.Header{
					"Retry-After":           []string{"60"},
					"X-Ratelimit-Limit":     []string{"5000"},
					"X-Ratelimit-Remaining": []string{"4999"},
				},
			},
			expectedLogEvent: map[string]any{
				"level":                           "warn",
				"message":                         "github api request",
				"github_method":                   http.MethodGet,
				"github_route":                    "/graphql",
				"github_auth_type":                AuthTypeUser,
				"github_status_code":              float64(http.StatusForbidden),
				"github_status_class":             "4xx",
				"github_result":                   "rate_limited",
				"github_duration_ms":              float64(250),
				"github_rate_limited":             true,
				"github_rate_limit_kind":          "secondary",
				"github_rate_limit_limit":         float64(5000),
				"github_rate_limit_remaining":     float64(4999),
				"github_rate_limit_remaining_pct": float64(99.98),
				"github_retry_after_seconds":      float64(60),
			},
		},
		{
			name:       "body-only secondary rate limit remains countable",
			metadata:   RequestMetadata{Kind: RequestKindAPI, AuthType: AuthTypeUser},
			requestURL: "https://api.github.com/user",
			response: &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(bytes.NewBufferString(`{"message":"You have exceeded a secondary rate limit"}`)),
				Header:     make(http.Header),
			},
			expectedLogEvent: map[string]any{
				"level":                  "warn",
				"message":                "github api request",
				"github_method":          http.MethodGet,
				"github_route":           "/user",
				"github_auth_type":       AuthTypeUser,
				"github_status_code":     float64(http.StatusForbidden),
				"github_status_class":    "4xx",
				"github_result":          "rate_limited",
				"github_duration_ms":     float64(250),
				"github_rate_limited":    true,
				"github_rate_limit_kind": "secondary",
			},
		},
		{
			name:       "graphql primary rate-limit error in a successful HTTP envelope remains countable",
			metadata:   RequestMetadata{Kind: RequestKindAPI, AuthType: AuthTypeUser},
			requestURL: "https://api.github.com/graphql",
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"errors":[{"message":"API rate limit exceeded"}]}`)),
				Header: http.Header{
					"X-Ratelimit-Limit":     []string{"5000"},
					"X-Ratelimit-Remaining": []string{"0"},
				},
			},
			expectedLogEvent: map[string]any{
				"level":                           "warn",
				"message":                         "github api request",
				"github_method":                   http.MethodGet,
				"github_route":                    "/graphql",
				"github_auth_type":                AuthTypeUser,
				"github_status_code":              float64(http.StatusOK),
				"github_status_class":             "2xx",
				"github_result":                   "rate_limited",
				"github_duration_ms":              float64(250),
				"github_rate_limited":             true,
				"github_rate_limit_kind":          "primary",
				"github_rate_limit_limit":         float64(5000),
				"github_rate_limit_remaining":     float64(0),
				"github_rate_limit_remaining_pct": float64(0),
			},
		},
		{
			name:       "transport failure remains countable",
			metadata:   RequestMetadata{Kind: RequestKindAPI, AuthType: AuthTypeUnknown},
			requestURL: "https://api.github.com/user",
			requestErr: errors.New("connection reset"),
			expectedLogEvent: map[string]any{
				"level":               "warn",
				"message":             "github api request",
				"error":               "connection reset",
				"github_method":       http.MethodGet,
				"github_route":        "/user",
				"github_auth_type":    AuthTypeUnknown,
				"github_status_code":  float64(0),
				"github_status_class": "transport_error",
				"github_result":       "transport_error",
				"github_duration_ms":  float64(250),
				"github_rate_limited": false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer
			clockCalls := 0
			telemetryTransport := &transport{
				base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					return tt.response, tt.requestErr
				}),
				logger: zerolog.New(&logs),
				now: func() time.Time {
					clockCalls++
					if clockCalls == 1 {
						return startedAt
					}
					return startedAt.Add(250 * time.Millisecond)
				},
			}
			request, err := http.NewRequestWithContext(
				WithRequestMetadata(context.Background(), tt.metadata),
				http.MethodGet,
				tt.requestURL,
				nil,
			)
			require.NoError(t, err, "test request should be valid")

			actualResponse, actualErr := telemetryTransport.RoundTrip(request)
			if actualResponse != nil {
				_, readErr := io.Copy(io.Discard, actualResponse.Body)
				require.NoError(t, readErr, "test response body should remain readable through telemetry")
				require.NoError(t, actualResponse.Body.Close(), "test response body should close through telemetry")
			}

			require.Equal(t, tt.response, actualResponse, "RoundTrip should preserve the underlying response")
			require.Equal(t, tt.requestErr, actualErr, "RoundTrip should preserve the underlying error")
			var actualLogEvent map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &actualLogEvent), "telemetry should be valid JSON")
			require.Equal(t, tt.expectedLogEvent, actualLogEvent, "telemetry should contain the exact bounded request summary")
		})
	}
}

func TestNormalizeRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		rawURL             string
		expectedRoute      string
		expectedRepository string
	}{
		{name: "pull request", rawURL: "https://api.github.com/repos/acme/widget/pulls/42", expectedRoute: "/repos/:owner/:repo/pulls/:id", expectedRepository: "acme/widget"},
		{name: "issue comment collection", rawURL: "https://api.github.com/repos/acme/widget/issues/42/comments?page=3", expectedRoute: "/repos/:owner/:repo/issues/:id/comments", expectedRepository: "acme/widget"},
		{name: "issue comment resource", rawURL: "https://api.github.com/repos/acme/widget/issues/comments/123", expectedRoute: "/repos/:owner/:repo/issues/comments/:id", expectedRepository: "acme/widget"},
		{name: "git ref", rawURL: "https://api.github.com/repos/acme/widget/git/ref/heads/feature/branch", expectedRoute: "/repos/:owner/:repo/git/ref/:ref", expectedRepository: "acme/widget"},
		{name: "escaped branch ref", rawURL: "https://api.github.com/repos/acme/widget/branches/feature%2Fbranch", expectedRoute: "/repos/:owner/:repo/branches/:ref", expectedRepository: "acme/widget"},
		{name: "decoded branch ref", rawURL: "https://api.github.com/repos/acme/widget/branches/feature/branch", expectedRoute: "/repos/:owner/:repo/branches/:ref", expectedRepository: "acme/widget"},
		{name: "contents", rawURL: "https://api.github.com/repos/acme/widget/contents/.github/workflows/test.yml?ref=main", expectedRoute: "/repos/:owner/:repo/contents/:path", expectedRepository: "acme/widget"},
		{name: "org membership", rawURL: "https://api.github.com/orgs/acme/memberships/octocat", expectedRoute: "/orgs/:org/memberships/:user"},
		{name: "org member", rawURL: "https://api.github.com/orgs/acme/members/octocat", expectedRoute: "/orgs/:org/members/:user"},
		{name: "org team repository", rawURL: "https://api.github.com/orgs/acme/teams/reviewers/repos/acme/widget", expectedRoute: "/orgs/:org/teams/:team/repos/:owner/:repo", expectedRepository: "acme/widget"},
		{name: "installation token", rawURL: "https://api.github.com/app/installations/123/access_tokens", expectedRoute: "/app/installations/:installation_id/access_tokens"},
		{name: "graphql", rawURL: "https://api.github.com/graphql", expectedRoute: "/graphql"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsedURL, err := url.Parse(tt.rawURL)
			require.NoError(t, err, "test URL should parse")
			actualRoute, actualRepository := normalizeRoute(parsedURL)
			require.Equal(t, tt.expectedRoute, actualRoute, "route should use a bounded endpoint template")
			require.Equal(t, tt.expectedRepository, actualRepository, "repository should remain available as an explicit drilldown dimension")
		})
	}
}
