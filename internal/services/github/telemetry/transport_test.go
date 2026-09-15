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
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/github/ratelimit"
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
				SyncReason:     "stale_reconcile",
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
				"github_sync_reason":              "stale_reconcile",
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
				"level":                       "warn",
				"message":                     "github api request",
				"error":                       "connection reset",
				"github_method":               http.MethodGet,
				"github_route":                "/user",
				"github_auth_type":            AuthTypeUnknown,
				"github_status_code":          float64(0),
				"github_status_class":         "transport_error",
				"github_result":               "transport_error",
				"github_duration_ms":          float64(250),
				"github_rate_limited":         false,
				"github_principal_unresolved": true,
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

func TestTransportEnforcesSharedDeferralWithoutCountingSyntheticRequest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	controller, err := ratelimit.NewController(ratelimit.Config{
		Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
		Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: "transport-test",
	})
	require.NoError(t, err, "controller should initialize")
	outbound := 0
	telemetryTransport := &transport{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			outbound++
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(bytes.NewBufferString(`{"message":"secondary rate limit"}`))}, nil
		}),
		logger: zerolog.Nop(), now: func() time.Time { return now }, controller: controller, caller: "pr_sync",
	}
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{
		Kind: RequestKindAPI, AuthType: AuthTypeAppInstallation, InstallationID: 42,
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/acme/repo/pulls/1", nil)
	require.NoError(t, err, "request should initialize")
	response, err := telemetryTransport.RoundTrip(request)
	require.NoError(t, err, "first outbound response should be delivered to the caller")
	firstResponse := response
	metadata, ok := ratelimit.InternalRetryMetadata(response.Header, 42)
	require.True(t, ok, "enforcement should attach exact retry metadata as soon as decisive headers arrive")
	require.Equal(t, now.Add(time.Minute), metadata.RetryAt, "controller should preserve the server deadline without local jitter")

	request, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/acme/repo/pulls/2", nil)
	require.NoError(t, err, "second request should initialize")
	response, err = telemetryTransport.RoundTrip(request)
	var deferral *ratelimit.Deferral
	require.True(t, errors.As(err, &deferral), "second request should be locally deferred")
	require.Nil(t, response, "a local deferral should not synthesize an HTTP response")
	require.Equal(t, 1, outbound, "locally deferred work must not become outbound request traffic")
	_, err = io.ReadAll(firstResponse.Body)
	require.NoError(t, err, "first outbound response should remain readable after its headers are observed")
	require.NoError(t, firstResponse.Body.Close(), "first outbound response should preserve close semantics")
}

func TestTransportClosesLocallyDeferredRequestBody(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	controller, err := ratelimit.NewController(ratelimit.Config{
		Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
		Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: "request-body-test",
	})
	require.NoError(t, err, "controller should initialize")
	seed, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
	require.NoError(t, err, "seed request should be admitted")
	controller.Observe(context.Background(), seed, ratelimit.Observation{
		StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"60"}},
	})
	telemetryTransport := &transport{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			t.Fatal("locally deferred request must not reach the base transport")
			return nil, nil
		}),
		logger: zerolog.Nop(), now: func() time.Time { return now }, controller: controller, caller: "body_test",
	}
	closeFailure := errors.New("request body close failed")
	body := &trackingReadCloser{Reader: strings.NewReader("payload"), closeErr: closeFailure}
	ctx := WithInstallationRequestMetadata(context.Background(), 42, "body_test")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", body)
	require.NoError(t, err, "request should initialize")

	response, err := telemetryTransport.RoundTrip(request)

	require.Nil(t, response, "local deferral should not synthesize a response")
	var deferral *ratelimit.Deferral
	require.True(t, errors.As(err, &deferral), "joined close failure should preserve the typed deferral")
	require.ErrorIs(t, err, closeFailure, "request body close failure should be preserved")
	require.True(t, body.closed, "RoundTripper should close a locally deferred request body")
}

func TestTransportRESTProbeWaitsForCompleteResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		statusCode         int
		missingJSONHandoff bool
		body               func() io.ReadCloser
		readPrefix         bool
		closeBeforeEOF     bool
		cancelBeforeEOF    bool
		expectedReadError  error
		expectedBody       string
		expectedProbe      bool
	}{
		{
			name:         "complete response proves recovery",
			body:         func() io.ReadCloser { return io.NopCloser(strings.NewReader("payload")) },
			expectedBody: "payload",
		},
		{
			name: "not found proves recovery after EOF", statusCode: http.StatusNotFound,
			body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{"message":"Not found"}`)) }, expectedBody: `{"message":"Not found"}`,
		},
		{
			name: "unprocessable content proves recovery after EOF", statusCode: http.StatusUnprocessableEntity,
			body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{"message":"Validation failed"}`)) }, expectedBody: `{"message":"Validation failed"}`,
		},
		{
			name: "not modified proves recovery on empty body close", statusCode: http.StatusNotModified,
			body: func() io.ReadCloser { return http.NoBody }, closeBeforeEOF: true,
		},
		{
			name: "malformed forbidden Close does not recover", statusCode: http.StatusForbidden,
			body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{"message":`)) }, closeBeforeEOF: true, expectedProbe: true,
		},
		{
			name: "missing JSON handoff after EOF does not recover", missingJSONHandoff: true,
			body: func() io.ReadCloser { return io.NopCloser(strings.NewReader("payload")) }, expectedBody: "payload", expectedProbe: true,
		},
		{
			name: "known empty response proves recovery when closed",
			body: func() io.ReadCloser { return http.NoBody }, closeBeforeEOF: true,
		},
		{
			name: "read failure keeps the recovery episode open",
			body: func() io.ReadCloser {
				return &failingReadCloser{reader: strings.NewReader("payload"), err: io.ErrUnexpectedEOF}
			},
			expectedReadError: io.ErrUnexpectedEOF, expectedBody: "payload", expectedProbe: true,
		},
		{
			name: "body timeout keeps the recovery episode open",
			body: func() io.ReadCloser {
				return &failingReadCloser{reader: strings.NewReader("payload"), err: context.DeadlineExceeded}
			},
			expectedReadError: context.DeadlineExceeded, expectedBody: "payload", expectedProbe: true,
		},
		{
			name:           "unread body close does not prove recovery",
			body:           func() io.ReadCloser { return io.NopCloser(strings.NewReader("payload")) },
			closeBeforeEOF: true, expectedProbe: true,
		},
		{
			name:       "partial body close does not prove recovery",
			body:       func() io.ReadCloser { return io.NopCloser(strings.NewReader("payload")) },
			readPrefix: true, closeBeforeEOF: true, expectedProbe: true,
		},
		{
			name:            "caller cancellation prevents recovery even when the body reader returns EOF",
			body:            func() io.ReadCloser { return io.NopCloser(strings.NewReader("payload")) },
			cancelBeforeEOF: true, expectedBody: "payload", expectedProbe: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			controller, err := ratelimit.NewController(ratelimit.Config{
				Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
				Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: tt.name, ProbeLease: 30 * time.Second,
			})
			require.NoError(t, err, "controller should initialize")
			seed, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			require.NoError(t, err, "initial request should be admitted")
			seedResult := controller.Observe(context.Background(), seed, ratelimit.Observation{
				StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}},
			})
			now = seedResult.Deferral.RetryAt
			outbound := 0
			telemetryTransport := &transport{
				base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					outbound++
					status := tt.statusCode
					if status == 0 {
						status = http.StatusOK
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: tt.body()}, nil
				}),
				logger: zerolog.Nop(), now: func() time.Time { return now }, controller: controller, caller: "rest_probe",
			}
			ctx, cancel := context.WithCancel(WithInstallationRequestMetadata(context.Background(), 42, "rest_probe"))
			t.Cleanup(cancel)
			if tt.missingJSONHandoff {
				ctx = WithJSONResponseObservation(ctx)
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/acme/repo/pulls/1", nil)
			require.NoError(t, err, "REST probe request should initialize")
			response, err := telemetryTransport.RoundTrip(request)
			require.NoError(t, err, "REST response should be returned before body completion")

			_, err = controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			var deferral *ratelimit.Deferral
			require.ErrorAs(t, err, &deferral, "competing work must wait until the successful probe body completes")
			require.Equal(t, now.Add(30*time.Second), deferral.RetryAt, "in-flight probe should retain its lease while the response body is unread")

			if tt.readPrefix {
				prefix := make([]byte, 3)
				_, readErr := io.ReadFull(response.Body, prefix)
				require.NoError(t, readErr, "caller should be able to consume a response prefix")
				require.Equal(t, "pay", string(prefix), "the response wrapper should preserve partial reads")
			}
			if tt.cancelBeforeEOF {
				cancel()
			}
			if !tt.closeBeforeEOF {
				body, readErr := io.ReadAll(response.Body)
				require.ErrorIs(t, readErr, tt.expectedReadError, "response reads should preserve their original failure or EOF result")
				require.Equal(t, tt.expectedBody, string(body), "response reads should preserve the complete or partial provider body")
				if !tt.expectedProbe {
					next, nextErr := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
					require.NoError(t, nextErr, "body EOF should admit competing work before the caller closes the response")
					require.False(t, next.Probe, "body EOF should immediately complete a successful REST probe")
				}
			}
			require.NoError(t, response.Body.Close(), "observation should preserve response close semantics")

			next, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			require.NoError(t, err, "finished responses should release the probe lease")
			require.Equal(t, tt.expectedProbe, next.Probe, "only an uncancelled complete response should admit ordinary work")
			require.Equal(t, 1, outbound, "body observation must never replay the outbound request")
		})
	}
}

func TestTransportObservesDecisiveThrottleHeadersBeforeBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		header     func(time.Time) http.Header
		kind       ratelimit.Kind
	}{
		{
			name: "secondary throttle", statusCode: http.StatusTooManyRequests, kind: ratelimit.KindSecondary,
			header: func(_ time.Time) http.Header { return http.Header{"Retry-After": []string{"60"}} },
		},
		{
			name: "successful request consumes last primary allowance", statusCode: http.StatusOK, kind: ratelimit.KindPrimary,
			header: func(now time.Time) http.Header {
				return http.Header{"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{strconv.FormatInt(now.Add(time.Minute).Unix(), 10)}}
			},
		},
	}
	tests = append(tests, tests[1])
	tests[2].name = "not modified response with exhausted primary allowance"
	tests[2].statusCode = http.StatusNotModified
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			controller, err := ratelimit.NewController(ratelimit.Config{
				Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
				Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: tt.name,
			})
			require.NoError(t, err, "controller should initialize")
			seed, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			require.NoError(t, err, "initial request should be admitted")
			seedResult := controller.Observe(context.Background(), seed, ratelimit.Observation{
				StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}},
			})
			now = seedResult.Deferral.RetryAt
			var logs bytes.Buffer
			telemetryTransport := &transport{
				base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: tt.statusCode, Header: tt.header(now), Body: io.NopCloser(strings.NewReader("payload"))}, nil
				}),
				logger: zerolog.New(&logs), now: func() time.Time { return now }, controller: controller, caller: "rest_probe",
			}
			ctx := WithInstallationRequestMetadata(context.Background(), 42, "rest_probe")
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/acme/repo/pulls/1", nil)
			require.NoError(t, err, "REST probe request should initialize")
			response, err := telemetryTransport.RoundTrip(request)
			require.NoError(t, err, "decisive response headers should remain visible to the caller")
			require.Equal(t, tt.statusCode, response.StatusCode, "coordination must preserve the provider HTTP status")
			metadata, ok := ratelimit.InternalRetryMetadata(response.Header, 42)
			require.True(t, ok, "decisive headers should carry the exact selected deadline before body reads")
			require.Equal(t, now.Add(time.Minute), metadata.RetryAt, "immediate observation should preserve the provider cooldown")
			require.Equal(t, tt.kind, metadata.Kind, "immediate observation should preserve primary versus secondary evidence")

			_, err = controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			var deferral *ratelimit.Deferral
			require.ErrorAs(t, err, &deferral, "decisive headers should block competing work before the body is consumed")
			require.Equal(t, metadata.RetryAt, deferral.RetryAt, "competing work should honor the response cooldown rather than the old probe lease")
			require.NoError(t, response.Body.Close(), "closing an unread decisive response should retain its existing cooldown")
			var event map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &event), "request telemetry should be emitted once")
			expectedResult := "success"
			if tt.statusCode == http.StatusTooManyRequests {
				expectedResult = "rate_limited"
			}
			require.Equal(t, expectedResult, event["github_result"], "successful responses must stay successful in request telemetry even when quota is exhausted")
		})
	}
}

func TestTransportUsesCompleteGraphQLErrorsForEnforcement(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	controller, err := ratelimit.NewController(ratelimit.Config{
		Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
		Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: "graphql-test",
	})
	require.NoError(t, err, "controller should initialize")
	body := `{"data":{"padding":"` + strings.Repeat("x", maxRateLimitResponseBytes+4096) + `"},"errors":[{"message":"API rate limit exceeded","extensions":{"code":"RATE_LIMITED"}}]}`
	telemetryTransport := &transport{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
		logger: zerolog.Nop(), now: func() time.Time { return now }, controller: controller, caller: "pr_sync",
	}
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{
		Kind: RequestKindAPI, AuthType: AuthTypeAppInstallation, InstallationID: 42,
	})
	ctx = WithGraphQLResponseObservation(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", nil)
	require.NoError(t, err, "GraphQL request should initialize")
	response, err := telemetryTransport.RoundTrip(request)
	require.NoError(t, err, "structured GraphQL response should remain visible to the caller")
	actual, err := io.ReadAll(response.Body)
	require.NoError(t, err, "complete GraphQL response should remain readable")
	require.Equal(t, body, string(actual), "transport should preserve the complete GraphQL body")
	graphQLErrors, decodeErr := ratelimit.DecodeGraphQLErrors(actual)
	require.NoError(t, decodeErr, "owned decoder should inspect the complete GraphQL envelope")
	ObserveGraphQLResponse(ctx, graphQLErrors, decodeErr)
	require.NoError(t, response.Body.Close(), "GraphQL response should close")

	request, err = http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", nil)
	require.NoError(t, err, "second GraphQL request should initialize")
	response, err = telemetryTransport.RoundTrip(request)
	var deferral *ratelimit.Deferral
	require.True(t, errors.As(err, &deferral), "structured error beyond the former telemetry truncation point should enforce a cooldown")
	require.Nil(t, response, "enforcement should defer before a second outbound GraphQL request")
}

func TestTransportDoesNotRecoverProbeFromUnclassifiableGraphQLResponse(t *testing.T) {
	t.Parallel()

	readFailure := errors.New("response stream failed")
	tests := []struct {
		name           string
		body           func() io.ReadCloser
		readBody       bool
		expectReadErr  bool
		expectCloseErr bool
	}{
		{name: "invalid JSON", body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{"data":`)) }, readBody: true},
		{name: "empty body", body: func() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }, readBody: true},
		{name: "null document", body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(`null`)) }, readBody: true},
		{name: "missing envelope fields", body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{}`)) }, readBody: true},
		{name: "truncated by observation bound", body: func() io.ReadCloser {
			return io.NopCloser(strings.NewReader(`{"data":{"padding":"` + strings.Repeat("x", maxRateLimitResponseBytes+1) + `"}}`))
		}, readBody: true},
		{name: "caller read error", body: func() io.ReadCloser {
			return &failingReadCloser{reader: strings.NewReader(`{"data":{"ok":true}}`), err: readFailure}
		}, readBody: true, expectReadErr: true},
		{name: "close drain error", body: func() io.ReadCloser {
			return &failingReadCloser{reader: strings.NewReader(`{"data":{"ok":true}}`), err: readFailure}
		}, expectCloseErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			controller, err := ratelimit.NewController(ratelimit.Config{
				Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
				Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: tt.name, ProbeLease: 30 * time.Second,
			})
			require.NoError(t, err, "controller should initialize")
			seed, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			require.NoError(t, err, "initial request should be admitted")
			seedResult := controller.Observe(context.Background(), seed, ratelimit.Observation{
				StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}},
			})
			now = seedResult.Deferral.RetryAt

			telemetryTransport := &transport{
				base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: tt.body()}, nil
				}),
				logger: zerolog.Nop(), now: func() time.Time { return now }, controller: controller, caller: "probe_test",
			}
			ctx := WithInstallationRequestMetadata(context.Background(), 42, "probe_test")
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", nil)
			require.NoError(t, err, "GraphQL probe request should initialize")
			response, err := telemetryTransport.RoundTrip(request)
			require.NoError(t, err, "HTTP response should be returned before body classification")
			if tt.readBody {
				_, readErr := io.ReadAll(response.Body)
				require.Equal(t, tt.expectReadErr, readErr != nil, "caller should receive the underlying read outcome")
			}
			closeErr := response.Body.Close()
			require.Equal(t, tt.expectCloseErr, closeErr != nil, "caller should receive the underlying close/drain outcome")

			next, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
			require.NoError(t, err, "unclassifiable response should release the failed probe lease")
			require.True(t, next.Probe, "unclassifiable response must leave the cooldown episode open for an authoritative probe")
		})
	}
}

func TestTransportRecoversProbeFromOversizedValidGraphQLResponseViaOwnedDecoder(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	controller, err := ratelimit.NewController(ratelimit.Config{
		Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
		Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: "large-valid",
	})
	require.NoError(t, err, "controller should initialize")
	seed, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
	require.NoError(t, err, "initial request should be admitted")
	seedResult := controller.Observe(context.Background(), seed, ratelimit.Observation{
		StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}},
	})
	now = seedResult.Deferral.RetryAt
	body := `{"data":{"padding":"` + strings.Repeat("x", maxRateLimitResponseBytes+4096) + `"}}`
	telemetryTransport := &transport{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
		logger: zerolog.Nop(), now: func() time.Time { return now }, controller: controller, caller: "probe_test",
	}
	ctx := WithInstallationRequestMetadata(context.Background(), 42, "probe_test")
	ctx = WithGraphQLResponseObservation(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", nil)
	require.NoError(t, err, "GraphQL probe request should initialize")
	response, err := telemetryTransport.RoundTrip(request)
	require.NoError(t, err, "large valid response should be returned")
	actual, err := io.ReadAll(response.Body)
	require.NoError(t, err, "large valid response should remain readable")
	require.Equal(t, body, string(actual), "transport should preserve the complete large response")
	graphQLErrors, decodeErr := ratelimit.DecodeGraphQLErrors(actual)
	require.NoError(t, decodeErr, "owned decoder should validate the complete oversized response")
	ObserveGraphQLResponse(ctx, graphQLErrors, decodeErr)
	require.NoError(t, response.Body.Close(), "large valid response should close")
	next, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
	require.NoError(t, err, "authoritatively decoded success should close the cooldown")
	require.False(t, next.Probe, "large valid response within the bound should not leave a probe-only gate")
}

func TestTransportGraphQLHTTPErrorEnvelope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		status       int
		body         string
		rateLimited  bool
		remainsProbe bool
	}{
		{name: "REST secondary throttle", status: http.StatusForbidden, body: `{"message":"You have exceeded a secondary rate limit."}`, rateLimited: true},
		{name: "ordinary REST forbidden", status: http.StatusForbidden, body: `{"message":"Resource not accessible by integration"}`},
		{name: "GraphQL application error", status: http.StatusOK, body: `{"errors":[{"type":"NOT_FOUND","message":"Object not found"}]}`},
		{name: "GraphQL errors take precedence over top-level message", status: http.StatusForbidden, body: `{"message":"secondary rate limit","errors":[{"type":"FORBIDDEN","message":"Permission denied"}]}`},
		{name: "successful data substring", status: http.StatusOK, body: `{"data":{"message":"secondary rate limit"}}`},
		{name: "HTTP200 does not accept REST envelope", status: http.StatusOK, body: `{"message":"secondary rate limit"}`, remainsProbe: true},
		{name: "invalid GraphQL errors cannot become REST fallback", status: http.StatusForbidden, body: `{"message":"Permission denied","errors":"broken"}`, remainsProbe: true},
		{name: "malformed suffix beyond captured prefix", status: http.StatusForbidden, body: `{"message":"Permission denied"}` + strings.Repeat(" ", maxRateLimitResponseBytes) + "invalid", remainsProbe: true},
		{name: "malformed error envelope", status: http.StatusForbidden, body: `{"message":"secondary rate limit"`, remainsProbe: true},
	}
	for _, authoritative := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(tt.name+"/owned="+strconv.FormatBool(authoritative), func(t *testing.T) {
				t.Parallel()
				now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
				controller, err := ratelimit.NewController(ratelimit.Config{
					Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
					Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: t.Name(),
				})
				require.NoError(t, err, "controller should initialize")
				seed, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
				require.NoError(t, err, "initial request should be admitted")
				observed := controller.Observe(context.Background(), seed, ratelimit.Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}}})
				now = observed.Deferral.RetryAt
				var logs bytes.Buffer
				outbound := 0
				client := NewControlledHTTPClient(time.Second, zerolog.New(&logs), controller, "graphql", roundTripFunc(func(*http.Request) (*http.Response, error) {
					outbound++
					return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body))}, nil
				}))
				ctx := WithInstallationRequestMetadata(context.Background(), 42, "graphql")
				if authoritative {
					ctx = WithGraphQLResponseObservation(ctx)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", nil)
				require.NoError(t, err, "GraphQL request should initialize")
				resp, err := client.Do(req)
				require.NoError(t, err, "provider HTTP response should remain visible")
				var deferral *ratelimit.Deferral
				_, err = controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
				require.ErrorAs(t, err, &deferral, "bodyless response headers must not establish recovery")
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err, "body should be preserved")
				require.Equal(t, tt.body, string(body), "observation must preserve full provider body")
				if authoritative {
					decoded, decodeErr := ratelimit.DecodeGraphQLErrors(body)
					ObserveGraphQLResponse(ctx, decoded, decodeErr)
				}
				require.NoError(t, resp.Body.Close(), "response should close")
				next, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
				if tt.rateLimited {
					require.ErrorAs(t, err, &deferral, "REST error envelope should extend shared cooldown")
					require.Equal(t, ratelimit.KindSecondary, deferral.Kind, "body throttle should be classified as secondary")
					retry, hasRetry := ratelimit.InternalRetryMetadata(resp.Header, 42)
					require.True(t, hasRetry, "owned service must receive controller retry metadata before cloning headers")
					require.Equal(t, deferral.RetryAt, retry.RetryAt, "retry metadata must match shared controller admission")
				} else {
					require.NoError(t, err, "completed non-throttled response should release probe ownership")
					require.Equal(t, tt.remainsProbe, next.Probe, "only valid definitive envelopes prove recovery")
				}
				var event map[string]any
				decoder := json.NewDecoder(&logs)
				for decoder.More() {
					require.NoError(t, decoder.Decode(&event), "telemetry should emit valid JSON")
				}
				require.Equal(t, tt.rateLimited, event["github_rate_limited"], "telemetry and shared classification should agree")
				require.Equal(t, 1, outbound, "GraphQL observation must never replay a mutation")
			})
		}
	}
}

type failingReadCloser struct {
	reader *strings.Reader
	err    error
}

func (r *failingReadCloser) Read(p []byte) (int, error) {
	if r.reader.Len() == 0 {
		return 0, r.err
	}
	n, _ := r.reader.Read(p)
	if r.reader.Len() == 0 {
		return n, r.err
	}
	return n, nil
}

func (r *failingReadCloser) Close() error { return nil }

type trackingReadCloser struct {
	*strings.Reader
	closeErr error
	closed   bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return r.closeErr
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
		{name: "org team membership", rawURL: "https://api.github.com/orgs/acme/teams/reviewers/memberships/octocat", expectedRoute: "/orgs/:org/teams/:team/memberships/:user"},
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
