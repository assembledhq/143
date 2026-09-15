package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
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
type jsonResponseObservationKey struct{}
type graphQLResponseObservationKey struct{}

type responseObservationState struct {
	mu            sync.Mutex
	callback      func([]ratelimit.GraphQLError, error)
	observed      bool
	graphQLErrors []ratelimit.GraphQLError
	decodeErr     error
}

// RequestMetadata carries bounded dimensions that cannot be derived safely
// from the HTTP request. It deliberately excludes tokens, query values, and
// other secrets.
type RequestMetadata struct {
	Kind                string
	AuthType            string
	InstallationID      int64
	SyncReason          string
	Caller              string
	PrincipalUnresolved bool
}

// WithRequestMetadata attaches safe GitHub telemetry dimensions to a request.
func WithRequestMetadata(ctx context.Context, metadata RequestMetadata) context.Context {
	return context.WithValue(ctx, requestMetadataKey{}, metadata)
}

// WithInstallationRequestMetadata records the authenticated provider
// principal before token acquisition. Callers should prefer this to inferring
// installation ownership from a short-lived token string.
func WithInstallationRequestMetadata(ctx context.Context, installationID int64, caller string) context.Context {
	metadata, _ := RequestMetadataFromContext(ctx)
	metadata.Kind = RequestKindAPI
	metadata.AuthType = AuthTypeAppInstallation
	metadata.InstallationID = installationID
	metadata.Caller = boundedDimension(caller, 64)
	metadata.PrincipalUnresolved = false
	return WithRequestMetadata(ctx, metadata)
}

func RequestMetadataFromContext(ctx context.Context) (RequestMetadata, bool) {
	metadata, ok := ctx.Value(requestMetadataKey{}).(RequestMetadata)
	return metadata, ok
}

// WithJSONResponseObservation holds probe recovery until an owned JSON decoder
// reports complete, valid consumption with ObserveJSONResponse.
func WithJSONResponseObservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, jsonResponseObservationKey{}, &responseObservationState{})
}

// ObserveJSONResponse completes the request-scoped handoff after decoding the
// entire response, including EOF. Decode and read errors cannot prove recovery.
func ObserveJSONResponse(ctx context.Context, decodeErr error) {
	state, _ := ctx.Value(jsonResponseObservationKey{}).(*responseObservationState)
	state.observe(nil, decodeErr)
}

// ValidateJSONResponse validates a complete buffer already read by an owned
// caller, then completes its request-scoped observation. A nil target validates
// JSON syntax without copying the body; non-nil targets use the actual decoder.
func ValidateJSONResponse(ctx context.Context, body []byte, target any) error {
	var err error
	if target != nil {
		err = json.Unmarshal(body, target)
	} else if !json.Valid(body) {
		err = errors.New("invalid GitHub response JSON")
	}
	err = errors.Join(err, ctx.Err())
	ObserveJSONResponse(ctx, err)
	return err
}

// WithGraphQLResponseObservation installs one request-scoped handoff between
// an owned full-body GraphQL decoder and the controlled transport.
func WithGraphQLResponseObservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, graphQLResponseObservationKey{}, &responseObservationState{})
}

// ObserveGraphQLResponse supplies the authoritative result after an owned
// decoder has inspected the complete GraphQL envelope. The transport retains
// only a bounded prefix, so this request-scoped handoff covers suffix errors
// and oversized successful probes without a second unbounded body copy.
func ObserveGraphQLResponse(ctx context.Context, graphQLErrors []ratelimit.GraphQLError, decodeErr error) {
	state, _ := ctx.Value(graphQLResponseObservationKey{}).(*responseObservationState)
	if state == nil {
		return
	}
	state.observe(graphQLErrors, decodeErr)
}

func (s *responseObservationState) bind(callback func([]ratelimit.GraphQLError, error)) {
	if s == nil || callback == nil {
		return
	}
	s.mu.Lock()
	s.callback = callback
	observed := s.observed
	graphQLErrors := s.graphQLErrors
	decodeErr := s.decodeErr
	s.mu.Unlock()
	if observed {
		callback(graphQLErrors, decodeErr)
	}
}

func (s *responseObservationState) observe(graphQLErrors []ratelimit.GraphQLError, decodeErr error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.observed {
		s.mu.Unlock()
		return
	}
	s.observed = true
	s.graphQLErrors = graphQLErrors
	s.decodeErr = decodeErr
	callback := s.callback
	s.mu.Unlock()
	if callback != nil {
		callback(graphQLErrors, decodeErr)
	}
}

// NewHTTPClient returns an HTTP client that emits one structured summary per
// GitHub request. Routes are normalized before logging to keep cardinality
// bounded; raw query strings and authorization headers are never logged.
func NewHTTPClient(timeout time.Duration, logger zerolog.Logger) *http.Client {
	return NewControlledHTTPClient(timeout, logger, nil, "unspecified")
}

// NewControlledHTTPClient adds installation cooldown coordination to the same
// transport that owns outbound request telemetry.
func NewControlledHTTPClient(timeout time.Duration, logger zerolog.Logger, controller *ratelimit.Controller, caller string, baseTransports ...http.RoundTripper) *http.Client {
	base := http.DefaultTransport
	if len(baseTransports) > 0 && baseTransports[0] != nil {
		base = baseTransports[0]
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &transport{
			base: base, logger: logger, now: time.Now,
			controller: controller, caller: boundedDimension(caller, 64),
		},
	}
}

type transport struct {
	base       http.RoundTripper
	logger     zerolog.Logger
	now        func() time.Time
	controller *ratelimit.Controller
	caller     string
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	startedAt := t.now()
	metadata, _ := RequestMetadataFromContext(req.Context())
	if metadata.Kind == "" {
		metadata.Kind = requestKindForURL(req.URL)
	}
	if metadata.AuthType == "" {
		metadata.AuthType = AuthTypeUnknown
	}
	if metadata.Caller == "" {
		metadata.Caller = t.caller
	}
	metadata.Caller = boundedDimension(metadata.Caller, 64)
	metadata.SyncReason = boundedDimension(metadata.SyncReason, 64)
	if metadata.Kind == RequestKindAPI && metadata.AuthType == AuthTypeUnknown {
		metadata.PrincipalUnresolved = true
	}
	req = req.WithContext(WithRequestMetadata(req.Context(), metadata))
	permit := ratelimit.Permit{}
	if t.controller != nil && metadata.AuthType == AuthTypeAppInstallation {
		var err error
		route, _ := normalizeRoute(req.URL)
		permit, err = t.controller.Before(req.Context(), ratelimit.Scope{
			InstallationID: metadata.InstallationID,
			Caller:         metadata.Caller,
			Route:          route,
			SyncReason:     metadata.SyncReason,
		})
		if err != nil {
			if req.Body != nil {
				if closeErr := req.Body.Close(); closeErr != nil {
					return nil, errors.Join(err, fmt.Errorf("close locally deferred GitHub request body: %w", closeErr))
				}
			}
			return nil, err
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		if t.controller != nil && metadata.AuthType == AuthTypeAppInstallation {
			t.controller.Observe(req.Context(), permit, ratelimit.Observation{RequestErr: err})
		}
		t.logRequest(req, resp, err, nil, nil, false, startedAt, t.now())
		return resp, err
	}
	controllerObserved := false
	if t.controller != nil && metadata.AuthType == AuthTypeAppInstallation {
		preliminary := ratelimit.ClassifyResponse(ratelimit.ResponseInput{
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
			Now:        t.now(),
		})
		// Decisive throttle headers must enter shared state as soon as they
		// arrive. A bare 403 remains ambiguous until its structured body is
		// decoded, and HTTP-200 GraphQL responses may carry structured errors.
		// Successful REST probes must also finish reading the response before
		// they can prove recovery and admit competing work.
		recoveringProbe := permit.Probe && ratelimit.IsDefinitiveResponse(resp.StatusCode)
		canFinishWithoutBody := req.URL.Path != "/graphql" && resp.StatusCode != http.StatusForbidden && !recoveringProbe
		if preliminary.RateLimited || canFinishWithoutBody {
			result := t.controller.Observe(req.Context(), permit, ratelimit.Observation{
				StatusCode: resp.StatusCode,
				Header:     resp.Header,
			})
			controllerObserved = true
			if t.controller.Mode() == ratelimit.ModeEnforce && result.Classification.RateLimited {
				if resp.Header == nil {
					resp.Header = make(http.Header)
				}
				ratelimit.AttachInternalRetryMetadata(resp.Header, result.Deferral)
			}
		}
	}
	decoderState, _ := req.Context().Value(jsonResponseObservationKey{}).(*responseObservationState)
	if decoderState == nil {
		decoderState, _ = req.Context().Value(graphQLResponseObservationKey{}).(*responseObservationState)
	}
	observedBody := &observedResponseBody{
		ReadCloser:   resp.Body,
		capture:      resp.StatusCode == http.StatusForbidden || req.URL.Path == "/graphql" || permit.Probe && resp.StatusCode >= 400,
		graphQL:      req.URL.Path == "/graphql",
		awaitDecoded: decoderState != nil,
		readEOF:      resp.Body == http.NoBody,
		onClose: func(body []byte, bodyErr, decodeErr error, graphQLErrors []ratelimit.GraphQLError, authoritativeGraphQL bool) {
			if t.controller != nil && metadata.AuthType == AuthTypeAppInstallation && !controllerObserved {
				observation := normalizedObservation(resp, body, req.URL.Path == "/graphql", bodyErr, decodeErr, graphQLErrors, authoritativeGraphQL)
				observation.RequestErr = req.Context().Err()
				if observation.BodyErr != nil {
					route, _ := normalizeRoute(req.URL)
					t.logger.Warn().Err(observation.BodyErr).
						Int64("github_installation_id", metadata.InstallationID).
						Str("github_caller", metadata.Caller).
						Str("github_route", route).
						Msg("github rate limit observation incomplete")
				}
				result := t.controller.Observe(req.Context(), permit, observation)
				if t.controller.Mode() == ratelimit.ModeEnforce && result.Classification.RateLimited {
					if resp.Header == nil {
						resp.Header = make(http.Header)
					}
					ratelimit.AttachInternalRetryMetadata(resp.Header, result.Deferral)
				}
			}
			t.logRequest(req, resp, nil, body, graphQLErrors, authoritativeGraphQL, startedAt, t.now())
		},
	}
	resp.Body = observedBody
	if decoderState != nil {
		decoderState.bind(observedBody.observeDecoded)
	}
	return resp, err
}

const maxRateLimitResponseBytes = 1 << 20

var (
	errRateLimitObservationTruncated  = errors.New("GitHub response exceeded rate-limit observation limit")
	errRateLimitObservationIncomplete = errors.New("GitHub response body closed before EOF")
)

type observedResponseBody struct {
	io.ReadCloser
	capture      bool
	graphQL      bool
	awaitDecoded bool
	body         []byte
	onClose      func([]byte, error, error, []ratelimit.GraphQLError, bool)
	observeOnce  sync.Once
	closeOnce    sync.Once
	closeErr     error
	readErr      error
	readEOF      bool
	drainErr     error
	truncated    bool
}

func (b *observedResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.capture && n > 0 {
		remaining := maxRateLimitResponseBytes - len(b.body)
		captured := n
		if captured > remaining {
			captured = remaining
			b.truncated = true
		}
		if captured > 0 {
			b.body = append(b.body, p[:captured]...)
		}
	}
	if err != nil && err != io.EOF {
		b.readErr = errors.Join(b.readErr, err)
		b.observe(false)
	} else if err == io.EOF {
		b.readEOF = true
		if !b.graphQL && !b.awaitDecoded {
			b.observe(false)
		}
	}
	return n, err
}

func (b *observedResponseBody) Close() error {
	b.observe(true)
	b.closeOnce.Do(func() {
		b.closeErr = b.ReadCloser.Close()
	})
	return errors.Join(b.drainErr, b.closeErr)
}

func (b *observedResponseBody) observe(drain bool) {
	b.observeOnce.Do(func() {
		if drain && b.capture {
			remaining := maxRateLimitResponseBytes - len(b.body)
			unread, readErr := io.ReadAll(io.LimitReader(b.ReadCloser, int64(remaining)+1))
			if len(unread) > remaining {
				b.truncated = true
				unread = unread[:remaining]
			}
			b.body = append(b.body, unread...)
			b.drainErr = readErr
		}
		if b.onClose != nil {
			var truncatedErr error
			if b.truncated {
				truncatedErr = errRateLimitObservationTruncated
			}
			var incompleteErr error
			if !b.graphQL && (!b.readEOF || b.awaitDecoded) && b.readErr == nil && !b.capture {
				incompleteErr = errRateLimitObservationIncomplete
			}
			b.onClose(b.body, errors.Join(b.readErr, b.drainErr, truncatedErr, incompleteErr), nil, nil, false)
		}
	})
}

func (b *observedResponseBody) observeDecoded(graphQLErrors []ratelimit.GraphQLError, decodeErr error) {
	b.observeOnce.Do(func() {
		if b.onClose != nil {
			body := b.body
			if b.truncated && decodeErr != nil {
				// A valid prefix cannot override a full-envelope decoding failure.
				body = nil
			}
			b.onClose(body, errors.Join(b.readErr, b.drainErr), decodeErr, graphQLErrors, true)
		}
	})
}

func (t *transport) logRequest(req *http.Request, resp *http.Response, requestErr error, responseBody []byte, graphQLErrors []ratelimit.GraphQLError, authoritativeGraphQL bool, startedAt, finishedAt time.Time) {
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
		rateLimited, rateLimitKind = classifyRateLimitResponse(resp, responseBody, req.URL.Path == "/graphql", graphQLErrors, authoritativeGraphQL)
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
	if metadata.SyncReason != "" {
		event = event.Str("github_sync_reason", metadata.SyncReason)
	}
	if metadata.Caller != "" {
		event = event.Str("github_caller", metadata.Caller)
	}
	if t.controller != nil {
		event = event.Str("github_rate_limit_mode", string(t.controller.Mode()))
	}
	if metadata.PrincipalUnresolved {
		event = event.Bool("github_principal_unresolved", true)
		mode := string(ratelimit.ModeOff)
		if t.controller != nil {
			mode = string(t.controller.Mode())
		}
		metrics.RecordGitHubPrincipalUnresolved(req.Context(), metadata.Caller, mode)
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

func normalizedObservation(resp *http.Response, responseBody []byte, graphQL bool, bodyErr, decodeErr error, authoritativeErrors []ratelimit.GraphQLError, authoritativeGraphQL bool) ratelimit.Observation {
	observation := ratelimit.Observation{StatusCode: resp.StatusCode, Header: resp.Header, BodyErr: bodyErr}
	if graphQL {
		observation.GraphQLErrors = authoritativeErrors
		if !authoritativeGraphQL {
			observation.GraphQLErrors, decodeErr = ratelimit.DecodeGraphQLErrors(responseBody)
		}
		// GitHub may reject the HTTP request before executing GraphQL and
		// return its REST error envelope. Successful content is never inspected
		// for message substrings, and structured GraphQL errors take precedence.
		if resp.StatusCode >= 400 && len(observation.GraphQLErrors) == 0 {
			if message, ok := responseMessage(responseBody, true); ok {
				observation.Message = message
				return observation
			}
		}
		if decodeErr != nil {
			observation.BodyErr = errors.Join(bodyErr, fmt.Errorf("decode GitHub GraphQL response: %w", decodeErr))
		}
		return observation
	}
	observation.BodyErr = errors.Join(bodyErr, decodeErr)
	var validMessage bool
	observation.Message, validMessage = responseMessage(responseBody, false)
	if resp.StatusCode >= 400 && !validMessage {
		observation.BodyErr = errors.Join(observation.BodyErr, errors.New("decode GitHub error response: missing or invalid message envelope"))
	}
	return observation
}

func responseMessage(body []byte, graphQL bool) (string, bool) {
	var envelope struct {
		Message *string         `json:"message"`
		Errors  json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Message == nil {
		return "", false
	}
	if graphQL && len(envelope.Errors) > 0 && string(envelope.Errors) != "null" {
		var graphQLErrors []json.RawMessage
		if json.Unmarshal(envelope.Errors, &graphQLErrors) != nil || len(graphQLErrors) > 0 {
			return "", false
		}
	}
	return *envelope.Message, true
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

func classifyRateLimitResponse(resp *http.Response, responseBody []byte, graphQL bool, authoritativeErrors []ratelimit.GraphQLError, authoritativeGraphQL bool) (bool, string) {
	if resp == nil {
		return false, ""
	}
	observation := normalizedObservation(resp, responseBody, graphQL, nil, nil, authoritativeErrors, authoritativeGraphQL)
	classification := ratelimit.ClassifyResponse(ratelimit.ResponseInput{
		StatusCode:    resp.StatusCode,
		Header:        resp.Header,
		Message:       observation.Message,
		GraphQLErrors: observation.GraphQLErrors,
	})
	// A successful request that uses the final primary token remains successful
	// request telemetry. The controller records the resulting episode separately.
	if (resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusNotModified) && len(observation.GraphQLErrors) == 0 {
		return false, ""
	}
	return classification.RateLimited, string(classification.Kind)
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

func boundedDimension(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
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
				if len(segments) >= 6 && segments[4] == "memberships" {
					segments[5] = ":user"
				} else if len(segments) >= 7 && segments[4] == "repos" {
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
