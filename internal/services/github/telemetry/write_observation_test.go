package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestTransportObservesWriteCallbacksUntilSummary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		callbacks  []bool
		beforeRead bool
		requestErr bool
		expected   map[string]any
	}{
		{name: "unsupported or no callbacks remains unknown", expected: writeFields(0, 0, "unknown", "api_github_com")},
		{name: "one successful write", callbacks: []bool{false}, expected: writeFields(1, 0, "callbacks_observed", "api_github_com")},
		{name: "retry writes include errors", callbacks: []bool{true, false, false}, expected: writeFields(3, 1, "callbacks_observed", "api_github_com")},
		{name: "callback after headers before summary is included", callbacks: []bool{false}, beforeRead: true, expected: writeFields(2, 0, "callbacks_observed", "api_github_com")},
		{name: "transport failure freezes immediate summary", callbacks: []bool{true}, requestErr: true, expected: writeFields(1, 1, "callbacks_observed", "api_github_com")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			var existingWrites, existingGotConn atomic.Int64
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
				WroteRequest: func(httptrace.WroteRequestInfo) { existingWrites.Add(1) },
				GotConn:      func(httptrace.GotConnInfo) { existingGotConn.Add(1) },
			})
			var trace *httptrace.ClientTrace
			requestErr := errors.New("request failed")
			response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}
			transport := &transport{logger: zerolog.New(&logs), now: time.Now, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				trace = httptrace.ContextClientTrace(req.Context())
				trace.GotConn(httptrace.GotConnInfo{})
				for _, failed := range tt.callbacks {
					info := httptrace.WroteRequestInfo{}
					if failed {
						info.Err = errors.New("private-callback-error-detail")
					}
					trace.WroteRequest(info)
				}
				if tt.requestErr {
					return nil, requestErr
				}
				return response, nil
			})}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user?private=value", nil)
			require.NoError(t, err, "synthetic request should be valid")
			actual, err := transport.RoundTrip(req)
			if tt.requestErr {
				require.ErrorIs(t, err, requestErr, "write observations must preserve the original request failure")
				require.Nil(t, actual, "write observations must preserve the missing response")
			} else {
				require.NoError(t, err, "write observations must preserve successful transport")
				require.Same(t, response, actual, "write observations must preserve response identity")
				require.Empty(t, logs.String(), "the existing body lifecycle must continue to own summary emission")
				if tt.beforeRead {
					trace.WroteRequest(httptrace.WroteRequestInfo{})
				}
				body, readErr := io.ReadAll(actual.Body)
				require.NoError(t, readErr, "response body should remain readable")
				require.Equal(t, "{}", string(body), "trace observation must not change response contents")
				require.NoError(t, actual.Body.Close(), "response should retain normal close behavior")
			}
			var event map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &event), "one request summary should remain valid JSON")
			require.Equal(t, tt.expected, observedWriteFields(event), "the summary must contain exactly the callbacks observed before its cutoff")
			beforeLate := logs.String()
			trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("private-late-error-detail")})
			require.Equal(t, beforeLate, logs.String(), "callbacks after the cutoff must not revise or duplicate request summaries")
			expectedExisting := int64(len(tt.callbacks) + 1)
			if tt.beforeRead {
				expectedExisting++
			}
			require.Equal(t, expectedExisting, existingWrites.Load(), "existing write hooks must still run once for every callback including late callbacks")
			require.Equal(t, int64(1), existingGotConn.Load(), "unmodified existing trace hooks must be preserved")
			require.NotContains(t, logs.String(), "private-", "callback errors and URL query values must not enter write telemetry")
		})
	}
}

func TestTransportWriteCallbacksAreConcurrentAndRequestScoped(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	var traces []*httptrace.ClientTrace
	transport := &transport{logger: zerolog.New(&logs), now: time.Now, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		traces = append(traces, httptrace.ContextClientTrace(req.Context()))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}
	var responses []*http.Response
	for range 2 {
		req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
		require.NoError(t, err, "each synthetic request should be valid")
		response, err := transport.RoundTrip(req)
		require.NoError(t, err, "each request should retain its own body lifecycle")
		responses = append(responses, response)
	}
	var writers sync.WaitGroup
	for index, count := range []int{100, 70} {
		for callback := range count {
			writers.Go(func() {
				info := httptrace.WroteRequestInfo{}
				if callback%2 == 0 {
					info.Err = errors.New("synthetic write failure")
				}
				traces[index].WroteRequest(info)
			})
		}
	}
	writers.Wait()
	for _, response := range responses {
		_, err := io.ReadAll(response.Body)
		require.NoError(t, err, "callback concurrency must not affect response consumption")
		require.NoError(t, response.Body.Close(), "each response should close independently")
	}
	beforeLate := logs.String()
	for _, trace := range traces {
		for range 50 {
			writers.Go(func() { trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("late failure")}) })
		}
	}
	writers.Wait()
	require.Equal(t, beforeLate, logs.String(), "concurrent late callbacks must leave both frozen summaries unchanged")
	decoder := json.NewDecoder(&logs)
	var actual []map[string]any
	for range 2 {
		var event map[string]any
		require.NoError(t, decoder.Decode(&event), "each transport invocation should emit one summary")
		actual = append(actual, observedWriteFields(event))
	}
	require.Equal(t, []map[string]any{writeFields(100, 50, "callbacks_observed", "api_github_com"), writeFields(70, 35, "callbacks_observed", "api_github_com")}, actual, "concurrent callback totals must remain isolated to the owning request")
	var extra map[string]any
	require.ErrorIs(t, decoder.Decode(&extra), io.EOF, "callback observation must not introduce duplicate request summaries")
}

func TestTransportWriteCallbacksAreScopedToRedirectHops(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	transport := &transport{logger: zerolog.New(&logs), now: time.Now, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(req.Context())
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		if req.URL.Hostname() == "api.github.com" {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://private-redirect.example/next?private=value"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, err, "synthetic redirect request should be valid")
	response, err := client.Do(req)
	require.NoError(t, err, "observation must preserve redirect behavior without network access")
	_, err = io.ReadAll(response.Body)
	require.NoError(t, err, "final response should remain readable")
	require.NoError(t, response.Body.Close(), "final redirect response should close normally")
	decoder := json.NewDecoder(&logs)
	var actual []map[string]any
	for range 2 {
		var event map[string]any
		require.NoError(t, decoder.Decode(&event), "each redirect hop should emit its own summary")
		actual = append(actual, observedWriteFields(event))
	}
	require.Equal(t, []map[string]any{writeFields(1, 0, "callbacks_observed", "api_github_com"), writeFields(2, 0, "callbacks_observed", "other")}, actual, "redirect hops must not share callback counters or expose arbitrary host names")
	require.NotContains(t, logs.String(), "private-redirect.example", "host classification must never expose arbitrary redirect hosts")
	require.NotContains(t, logs.String(), "private=value", "redirect queries must remain excluded from telemetry")
}

func TestTargetHostClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		url      *url.URL
		expected string
	}{
		{name: "missing URL", expected: "unknown"},
		{name: "missing host", url: &url.URL{Path: "/user"}, expected: "unknown"},
		{name: "API host case and port", url: &url.URL{Host: "API.GITHUB.COM:443"}, expected: "api_github_com"},
		{name: "web OAuth host", url: &url.URL{Host: "github.com"}, expected: "github_com"},
		{name: "arbitrary or enterprise host", url: &url.URL{Host: "private.example"}, expected: "other"},
		{name: "lookalike suffix", url: &url.URL{Host: "api.github.com.private.example"}, expected: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, targetHostClass(tt.url), "host classification must produce only the fixed supported categories")
		})
	}
}

func writeFields(attempts, writeErrors float64, status, host string) map[string]any {
	return map[string]any{
		"github_transport_write_attempts_observed":  attempts,
		"github_transport_write_errors_observed":    writeErrors,
		"github_transport_write_observation_status": status,
		"github_transport_write_observation_cutoff": "request_summary",
		"github_target_host_class":                  host,
	}
}

func observedWriteFields(event map[string]any) map[string]any {
	fields := make(map[string]any)
	for key, value := range event {
		if strings.HasPrefix(key, "github_transport_write_") || key == "github_target_host_class" {
			fields[key] = value
		}
	}
	return fields
}
