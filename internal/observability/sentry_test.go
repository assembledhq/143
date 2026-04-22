package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/version"
	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"
)

var sentryTestMu sync.Mutex

type recordingTransport struct {
	mu             sync.Mutex
	events         []*sentry.Event
	flushResult    bool
	flushCalls     int
	configureCalls int
	closed         bool
}

func (t *recordingTransport) Configure(_ sentry.ClientOptions) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.configureCalls++
}

func (t *recordingTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *recordingTransport) Flush(_ time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.flushCalls++
	return t.flushResult
}

func (t *recordingTransport) FlushWithContext(_ context.Context) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.flushCalls++
	return t.flushResult
}

func (t *recordingTransport) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
}

func (t *recordingTransport) snapshotEvents() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentry.Event(nil), t.events...)
}

func bindTestSentryClient(t *testing.T, transport sentry.Transport) {
	t.Helper()

	err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://public@example.com/1",
		Transport: transport,
	})
	require.NoError(t, err, "test sentry client should initialize")
}

func TestNewSentryReporter_WithoutDSNDisablesReporter(t *testing.T) {
	t.Parallel()

	reporter, err := NewSentryReporter(SentryConfig{})

	require.NoError(t, err, "empty DSN should not error")
	require.False(t, reporter.Enabled(), "empty DSN should disable the reporter")
}

func TestNewSentryReporter_InitializesDefaults(t *testing.T) {
	t.Parallel()

	sentryTestMu.Lock()
	defer sentryTestMu.Unlock()

	originalBuildSHA := version.BuildSHA
	version.BuildSHA = "test-build-sha"
	defer func() {
		version.BuildSHA = originalBuildSHA
	}()

	reporter, err := NewSentryReporter(SentryConfig{DSN: "https://public@example.com/1"})

	require.NoError(t, err, "valid DSN should initialize sentry")
	require.True(t, reporter.Enabled(), "valid DSN should enable the reporter")
	require.Equal(t, "development", sentry.CurrentHub().Client().Options().Environment, "empty environment should default to development")
	require.Equal(t, "test-build-sha", sentry.CurrentHub().Client().Options().Release, "non-dev builds should default release to BuildSHA")
}

func TestNewSentryReporter_InvalidDSNReturnsError(t *testing.T) {
	t.Parallel()

	sentryTestMu.Lock()
	defer sentryTestMu.Unlock()

	reporter, err := NewSentryReporter(SentryConfig{DSN: "://bad-dsn"})

	require.Error(t, err, "invalid DSN should return an initialization error")
	require.Nil(t, reporter, "invalid DSN should not construct a reporter")
}

func TestSentryReporterFlush(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reporter   *SentryReporter
		transport  *recordingTransport
		expected   bool
		shouldInit bool
	}{
		{
			name:     "disabled reporter flushes as success",
			reporter: &SentryReporter{enabled: false},
			expected: true,
		},
		{
			name:       "enabled reporter delegates to sentry transport",
			reporter:   &SentryReporter{enabled: true},
			transport:  &recordingTransport{flushResult: false},
			expected:   false,
			shouldInit: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.shouldInit {
				sentryTestMu.Lock()
				defer sentryTestMu.Unlock()
				bindTestSentryClient(t, tt.transport)
			}
			require.Equal(t, tt.expected, tt.reporter.Flush(time.Second), "Flush should return the underlying sentry flush result")
			if tt.transport != nil {
				require.Equal(t, 1, tt.transport.flushCalls, "enabled reporters should flush through the transport exactly once")
			}
		})
	}
}

func TestSentryReporterCaptureRequestError(t *testing.T) {
	t.Parallel()

	sentryTestMu.Lock()
	defer sentryTestMu.Unlock()

	transport := &recordingTransport{flushResult: true}
	bindTestSentryClient(t, transport)

	reporter := &SentryReporter{enabled: true}
	req := httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/test?foo=bar", http.NoBody)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-CSRF-Token", "csrf")
	req.Header.Set("X-Other", "kept")

	reporter.CaptureRequestError(req, RequestErrorEvent{
		Method:       http.MethodPost,
		Path:         "/api/v1/test",
		Route:        "/api/v1/test/{id}",
		RequestID:    "req-123",
		Status:       http.StatusInternalServerError,
		ErrorCode:    "BROKEN",
		ErrorMessage: "something failed",
		OrgID:        "org-123",
		UserID:       "user-123",
	})

	events := transport.snapshotEvents()
	require.Len(t, events, 1, "CaptureRequestError should send exactly one event")

	event := events[0]
	require.Equal(t, sentry.LevelError, event.Level, "request errors should be captured at error level")
	require.Equal(t, "request_5xx", event.Tags["capture_type"], "request errors should be tagged with capture type")
	require.Equal(t, "req-123", event.Tags["request_id"], "request errors should include the request id tag")
	require.Equal(t, "BROKEN", event.Tags["error_code"], "request errors should include the error code tag")
	require.Equal(t, "org-123", event.Tags["org_id"], "request errors should include the org id tag")
	require.Equal(t, "user-123", event.Tags["user_id"], "request errors should include the user id tag")
	require.Equal(t, []string{"request-5xx", "/api/v1/test/{id}", "BROKEN"}, event.Fingerprint, "request errors should fingerprint by route and error code")
	require.NotNil(t, event.Request, "request errors should include sanitized request details")
	require.NotContains(t, event.Request.Headers, "Authorization", "sanitized request should redact authorization headers")
	require.NotContains(t, event.Request.Headers, "Cookie", "sanitized request should redact cookies")
	require.NotContains(t, event.Request.Headers, "X-Csrf-Token", "sanitized request should redact CSRF headers")
	require.Equal(t, "kept", event.Request.Headers["X-Other"], "sanitized request should preserve non-sensitive headers")
	require.Equal(t, "", event.Request.Data, "sanitized request should drop the body")
	require.Equal(t, "something failed", event.Contexts["http"]["error_message"], "request errors should include structured HTTP context")
}

func TestSentryReporterCaptureRequestErrorDisabledDoesNothing(t *testing.T) {
	t.Parallel()

	reporter := &SentryReporter{enabled: false}
	req := httptest.NewRequest(http.MethodGet, "https://example.com/api/v1/test", nil)

	require.NotPanics(t, func() {
		reporter.CaptureRequestError(req, RequestErrorEvent{})
	}, "disabled reporters should ignore request error capture")
}

func TestSentryReporterCaptureRecoveredPanic(t *testing.T) {
	t.Parallel()

	sentryTestMu.Lock()
	defer sentryTestMu.Unlock()

	transport := &recordingTransport{flushResult: true}
	bindTestSentryClient(t, transport)

	reporter := &SentryReporter{enabled: true}
	req := httptest.NewRequest(http.MethodGet, "https://example.com/api/v1/panic", nil)
	req.Header.Set("Authorization", "Bearer secret")

	reporter.CaptureRecoveredPanic(req, "boom", []byte("stack trace"))

	events := transport.snapshotEvents()
	require.Len(t, events, 1, "CaptureRecoveredPanic should send exactly one event")

	event := events[0]
	require.Equal(t, sentry.LevelFatal, event.Level, "recovered panics should be captured at fatal level")
	require.Equal(t, "panic", event.Tags["capture_type"], "recovered panics should be tagged as panic captures")
	require.Equal(t, "boom", event.Contexts["panic"]["value"], "recovered panics should include the panic value")
	require.Equal(t, "stack trace", event.Contexts["panic"]["stack"], "recovered panics should include the provided stack trace")
	require.NotNil(t, event.Request, "recovered panics should include sanitized request details")
	require.NotContains(t, event.Request.Headers, "Authorization", "panic capture should sanitize sensitive request headers")
}

func TestSentryReporterCaptureRecoveredPanicDisabledDoesNothing(t *testing.T) {
	t.Parallel()

	reporter := &SentryReporter{enabled: false}
	req := httptest.NewRequest(http.MethodGet, "https://example.com/api/v1/panic", nil)

	require.NotPanics(t, func() {
		reporter.CaptureRecoveredPanic(req, "boom", []byte("stack"))
	}, "disabled reporters should ignore recovered panic capture")
}

func TestSanitizedRequest(t *testing.T) {
	t.Parallel()

	require.Nil(t, sanitizedRequest(nil), "sanitizedRequest should return nil for nil requests")

	req := httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/test?foo=bar", http.NoBody)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-CSRF-Token", "csrf")
	req.Header.Set("X-Other", "kept")

	sanitized := sanitizedRequest(req)

	require.NotNil(t, sanitized, "sanitizedRequest should clone non-nil requests")
	require.Empty(t, sanitized.Header.Get("Authorization"), "sanitizedRequest should redact authorization headers")
	require.Empty(t, sanitized.Header.Get("Cookie"), "sanitizedRequest should redact cookies")
	require.Empty(t, sanitized.Header.Get("X-CSRF-Token"), "sanitizedRequest should redact CSRF headers")
	require.Equal(t, "kept", sanitized.Header.Get("X-Other"), "sanitizedRequest should preserve non-sensitive headers")
	require.Equal(t, int64(0), sanitized.ContentLength, "sanitizedRequest should clear request body length")
	require.Equal(t, http.NoBody, sanitized.Body, "sanitizedRequest should replace the body with NoBody")
}
