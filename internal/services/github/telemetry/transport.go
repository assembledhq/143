package telemetry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const (
	RequestKindAPI   = "api"
	RequestKindOAuth = "oauth"

	AuthTypeAppInstallation = "app_installation"
	AuthTypeAppJWT          = "app_jwt"
	AuthTypeUser            = "user"
	AuthTypeOAuth           = "oauth"
	AuthTypeUnknown         = "unknown"
)

type requestMetadataKey struct{}

// RequestMetadata carries bounded dimensions that cannot be derived safely
// from the HTTP request. It deliberately excludes tokens, query values, and
// other secrets.
type RequestMetadata struct {
	Kind           string
	AuthType       string
	InstallationID int64
}

// WithRequestMetadata attaches safe GitHub telemetry dimensions to a request.
func WithRequestMetadata(ctx context.Context, metadata RequestMetadata) context.Context {
	return context.WithValue(ctx, requestMetadataKey{}, metadata)
}

// NewHTTPClient returns an HTTP client that emits one structured summary per
// GitHub request. Routes are normalized before logging to keep cardinality
// bounded; raw query strings and authorization headers are never logged.
func NewHTTPClient(timeout time.Duration, logger zerolog.Logger) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &transport{
			base:   http.DefaultTransport,
			logger: logger,
			now:    time.Now,
		},
	}
}

type transport struct {
	base   http.RoundTripper
	logger zerolog.Logger
	now    func() time.Time
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	startedAt := t.now()
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		t.logRequest(req, resp, err, nil, startedAt, t.now())
		return resp, err
	}
	resp.Body = &observedResponseBody{
		ReadCloser: resp.Body,
		capture:    resp.StatusCode == http.StatusForbidden || req.URL.Path == "/graphql",
		onClose: func(body []byte) {
			t.logRequest(req, resp, nil, body, startedAt, t.now())
		},
	}
	return resp, err
}

const maxRateLimitResponseBytes = 16 * 1024

type observedResponseBody struct {
	io.ReadCloser
	capture  bool
	body     []byte
	onClose  func([]byte)
	once     sync.Once
	closeErr error
}

func (b *observedResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.capture && len(b.body) < maxRateLimitResponseBytes {
		remaining := maxRateLimitResponseBytes - len(b.body)
		captured := n
		if captured > remaining {
			captured = remaining
		}
		b.body = append(b.body, p[:captured]...)
	}
	return n, err
}

func (b *observedResponseBody) Close() error {
	b.once.Do(func() {
		if b.capture && len(b.body) < maxRateLimitResponseBytes {
			remaining := int64(maxRateLimitResponseBytes - len(b.body))
			unread, _ := io.ReadAll(io.LimitReader(b.ReadCloser, remaining))
			b.body = append(b.body, unread...)
		}
		b.closeErr = b.ReadCloser.Close()
		if b.onClose != nil {
			b.onClose(b.body)
		}
	})
	return b.closeErr
}

func (t *transport) logRequest(req *http.Request, resp *http.Response, requestErr error, responseBody []byte, startedAt, finishedAt time.Time) {
	metadata, _ := req.Context().Value(requestMetadataKey{}).(RequestMetadata)
	if metadata.Kind == "" {
		metadata.Kind = requestKindForURL(req.URL)
	}
	if metadata.AuthType == "" {
		metadata.AuthType = AuthTypeUnknown
	}

	route, repository := normalizeRoute(req.URL)
	statusCode := 0
	statusClass := "transport_error"
	result := "transport_error"
	rateLimited := false
	rateLimitKind := ""
	var header http.Header
	if resp != nil {
		statusCode = resp.StatusCode
		statusClass = fmt.Sprintf("%dxx", resp.StatusCode/100)
		result = githubRequestResult(resp.StatusCode)
		header = resp.Header
		rateLimited, rateLimitKind = classifyRateLimitResponse(resp, responseBody)
		if rateLimited {
			result = "rate_limited"
		}
	}

	event := t.logger.Info()
	if requestErr != nil || rateLimited || statusCode >= http.StatusInternalServerError {
		event = t.logger.Warn()
	}
	event = event.
		Err(requestErr).
		Str("github_method", req.Method).
		Str("github_route", route).
		Str("github_auth_type", metadata.AuthType).
		Int("github_status_code", statusCode).
		Str("github_status_class", statusClass).
		Str("github_result", result).
		Float64("github_duration_ms", float64(finishedAt.Sub(startedAt).Microseconds())/1000).
		Bool("github_rate_limited", rateLimited)
	if repository != "" {
		event = event.Str("github_repository", repository)
	}
	if metadata.InstallationID > 0 {
		event = event.Int64("github_installation_id", metadata.InstallationID)
	}
	if rateLimitKind != "" {
		event = event.Str("github_rate_limit_kind", rateLimitKind)
	}

	limit, hasLimit := headerInt64(header, "X-RateLimit-Limit")
	remaining, hasRemaining := headerInt64(header, "X-RateLimit-Remaining")
	used, hasUsed := headerInt64(header, "X-RateLimit-Used")
	resetUnix, hasReset := headerInt64(header, "X-RateLimit-Reset")
	if hasLimit {
		event = event.Int64("github_rate_limit_limit", limit)
	}
	if hasRemaining {
		event = event.Int64("github_rate_limit_remaining", remaining)
	}
	if hasUsed {
		event = event.Int64("github_rate_limit_used", used)
	}
	if hasLimit && limit > 0 && hasRemaining {
		event = event.Float64("github_rate_limit_remaining_pct", float64(remaining)*100/float64(limit))
	}
	if hasReset {
		resetAt := time.Unix(resetUnix, 0).UTC()
		resetSeconds := resetAt.Sub(finishedAt).Seconds()
		if resetSeconds < 0 {
			resetSeconds = 0
		}
		event = event.
			Int64("github_rate_limit_reset_unix", resetUnix).
			Time("github_rate_limit_reset_at", resetAt).
			Float64("github_rate_limit_reset_seconds", resetSeconds)
	}
	if resource := strings.TrimSpace(header.Get("X-RateLimit-Resource")); resource != "" {
		event = event.Str("github_rate_limit_resource", resource)
	}
	if requestID := strings.TrimSpace(header.Get("X-GitHub-Request-Id")); requestID != "" {
		event = event.Str("github_request_id", requestID)
	}
	if retryAfter, ok := retryAfterSeconds(header.Get("Retry-After"), finishedAt); ok {
		event = event.Float64("github_retry_after_seconds", retryAfter)
	}

	message := "github api request"
	if metadata.Kind == RequestKindOAuth {
		message = "github oauth request"
	}
	event.Msg(message)
}

func requestKindForURL(requestURL *url.URL) string {
	if requestURL != nil && strings.EqualFold(requestURL.Hostname(), "github.com") {
		return RequestKindOAuth
	}
	return RequestKindAPI
}

func githubRequestResult(statusCode int) string {
	switch {
	case statusCode >= 200 && statusCode < 400:
		return "success"
	case statusCode >= 400 && statusCode < 500:
		return "client_error"
	case statusCode >= 500:
		return "server_error"
	default:
		return "http_error"
	}
}

func classifyRateLimitResponse(resp *http.Response, responseBody []byte) (bool, string) {
	if resp == nil {
		return false, ""
	}
	throttleStatus := resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests
	message := strings.ToLower(strings.TrimSpace(string(responseBody)))
	bodyRateLimited := strings.Contains(message, "rate limit") || strings.Contains(message, "abuse detection")
	if !throttleStatus && !bodyRateLimited {
		return false, ""
	}
	if remaining, ok := headerInt64(resp.Header, "X-RateLimit-Remaining"); ok && remaining == 0 {
		return true, "primary"
	}
	if strings.TrimSpace(resp.Header.Get("Retry-After")) != "" || resp.StatusCode == http.StatusTooManyRequests {
		return true, "secondary"
	}
	if strings.Contains(message, "secondary rate limit") || strings.Contains(message, "abuse detection") {
		return true, "secondary"
	}
	if strings.Contains(message, "rate limit") {
		return true, "unknown"
	}
	return false, ""
}

func headerInt64(header http.Header, name string) (int64, bool) {
	if header == nil {
		return 0, false
	}
	raw := strings.TrimSpace(header.Get(name))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil
}

func retryAfterSeconds(raw string, now time.Time) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds >= 0 {
		return seconds, true
	}
	retryAt, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	seconds := retryAt.Sub(now).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	return seconds, true
}

func normalizeRoute(requestURL *url.URL) (string, string) {
	if requestURL == nil {
		return "/", ""
	}
	segments := strings.Split(strings.Trim(requestURL.Path, "/"), "/")
	if len(segments) == 1 && segments[0] == "" {
		return "/", ""
	}

	repository := ""
	if len(segments) >= 3 && segments[0] == "repos" {
		repository = segments[1] + "/" + segments[2]
		segments[1] = ":owner"
		segments[2] = ":repo"
		normalizeRepositorySegments(&segments)
	} else if len(segments) >= 2 && segments[0] == "orgs" {
		segments[1] = ":org"
		if len(segments) >= 4 {
			switch segments[2] {
			case "members", "memberships":
				segments[3] = ":user"
			case "teams":
				segments[3] = ":team"
				if len(segments) >= 7 && segments[4] == "repos" {
					repository = segments[5] + "/" + segments[6]
					segments[5] = ":owner"
					segments[6] = ":repo"
				}
			}
		}
	} else if len(segments) >= 3 && segments[0] == "app" && segments[1] == "installations" {
		segments[2] = ":installation_id"
	} else {
		for i := range segments {
			segments[i] = normalizeOpaqueSegment(segments[i])
		}
	}

	return "/" + strings.Join(segments, "/"), repository
}

func normalizeRepositorySegments(segments *[]string) {
	parts := *segments
	if len(parts) < 4 {
		return
	}
	switch parts[3] {
	case "pulls", "check-runs":
		if len(parts) >= 5 {
			parts[4] = ":id"
		}
	case "issues":
		if len(parts) >= 6 && parts[4] == "comments" {
			parts[5] = ":id"
		} else if len(parts) >= 5 {
			parts[4] = ":id"
		}
	case "commits", "branches", "statuses":
		if len(parts) >= 5 {
			parts = append(parts[:4], ":ref")
		}
	case "contents":
		parts = append(parts[:4], ":path")
	case "git":
		if len(parts) >= 6 {
			switch parts[4] {
			case "commits", "trees":
				parts[5] = ":ref"
			case "ref":
				parts = append(parts[:5], ":ref")
			}
		}
	}
	for i := 3; i < len(parts); i++ {
		parts[i] = normalizeOpaqueSegment(parts[i])
	}
	*segments = parts
}

func normalizeOpaqueSegment(segment string) string {
	if segment == "" || strings.HasPrefix(segment, ":") {
		return segment
	}
	if _, err := strconv.ParseInt(segment, 10, 64); err == nil {
		return ":id"
	}
	if len(segment) >= 20 && isHex(segment) {
		return ":ref"
	}
	return segment
}

func isHex(value string) bool {
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}
