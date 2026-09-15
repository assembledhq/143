// Package ratelimit owns GitHub throttle classification and the internal
// deferral contract shared by clients, telemetry, and worker retry adapters.
package ratelimit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Kind identifies the provider limit that produced a cooldown.
type Kind string

const (
	KindPrimary   Kind = "primary"
	KindSecondary Kind = "secondary"
	KindUnknown   Kind = "unknown"
)

func (k Kind) Validate() error {
	switch k {
	case KindPrimary, KindSecondary, KindUnknown:
		return nil
	default:
		return fmt.Errorf("invalid GitHub rate-limit kind %q", k)
	}
}

// GraphQLError is the bounded, normalized portion of GitHub's GraphQL error
// envelope that is relevant to throttle classification.
type GraphQLError struct {
	Message string
	Type    string
}

// DecodeGraphQLErrors parses only the structured errors array. Text appearing
// in successful GraphQL data is deliberately ignored.
func DecodeGraphQLErrors(body []byte) ([]GraphQLError, error) {
	dataPresent, errorsValue, errorsPresent, err := graphQLEnvelopeFields(body)
	if err != nil {
		return nil, err
	}
	if !dataPresent && !errorsPresent {
		return nil, errors.New("GraphQL response contains neither data nor errors")
	}
	if !errorsPresent || bytes.Equal(errorsValue, []byte("null")) {
		return nil, nil
	}
	var decoded []struct {
		Message    string `json:"message"`
		Type       string `json:"type"`
		Extensions struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(errorsValue, &decoded); err != nil {
		return nil, fmt.Errorf("decode GraphQL errors: %w", err)
	}
	result := make([]GraphQLError, 0, len(decoded))
	for _, item := range decoded {
		errorType := strings.TrimSpace(item.Type)
		if errorType == "" {
			errorType = strings.TrimSpace(item.Extensions.Code)
		}
		if errorType == "" {
			errorType = strings.TrimSpace(item.Extensions.Type)
		}
		result = append(result, GraphQLError{Message: strings.TrimSpace(item.Message), Type: errorType})
	}
	return result, nil
}

// graphQLEnvelopeFields validates the complete JSON document and locates only
// the top-level data and errors fields. It deliberately does not unmarshal
// data: json.RawMessage copies the full successful payload and would make
// observation memory grow with response size even though classification only
// needs the usually-small errors array.
func graphQLEnvelopeFields(body []byte) (bool, []byte, bool, error) {
	if !json.Valid(body) {
		return false, nil, false, errors.New("invalid GraphQL response JSON")
	}
	i := skipJSONSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return false, nil, false, errors.New("GraphQL response is not an object")
	}
	i++
	var dataPresent, errorsPresent bool
	var errorsValue []byte
	for {
		i = skipJSONSpace(body, i)
		if i < len(body) && body[i] == '}' {
			return dataPresent, errorsValue, errorsPresent, nil
		}
		keyStart := i
		keyEnd, ok := skipJSONString(body, i)
		if !ok {
			return false, nil, false, errors.New("invalid GraphQL response object key")
		}
		i = skipJSONSpace(body, keyEnd)
		if i >= len(body) || body[i] != ':' {
			return false, nil, false, errors.New("invalid GraphQL response object separator")
		}
		i = skipJSONSpace(body, i+1)
		valueStart := i
		valueEnd, ok := skipJSONValue(body, i)
		if !ok {
			return false, nil, false, errors.New("invalid GraphQL response object value")
		}
		switch {
		case bytes.Equal(body[keyStart:keyEnd], []byte(`"data"`)):
			dataPresent = true
		case bytes.Equal(body[keyStart:keyEnd], []byte(`"errors"`)):
			errorsPresent = true
			errorsValue = body[valueStart:valueEnd]
		}
		i = skipJSONSpace(body, valueEnd)
		if i < len(body) && body[i] == ',' {
			i++
			continue
		}
		if i < len(body) && body[i] == '}' {
			return dataPresent, errorsValue, errorsPresent, nil
		}
		return false, nil, false, errors.New("invalid GraphQL response object ending")
	}
}

func skipJSONSpace(body []byte, i int) int {
	for i < len(body) {
		switch body[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

func skipJSONString(body []byte, i int) (int, bool) {
	if i >= len(body) || body[i] != '"' {
		return i, false
	}
	for i++; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return i, false
}

func skipJSONValue(body []byte, i int) (int, bool) {
	if i >= len(body) {
		return i, false
	}
	if body[i] == '"' {
		return skipJSONString(body, i)
	}
	if body[i] != '{' && body[i] != '[' {
		start := i
		for i < len(body) && body[i] != ',' && body[i] != '}' && body[i] != ']' && body[i] != ' ' && body[i] != '\t' && body[i] != '\r' && body[i] != '\n' {
			i++
		}
		return i, i > start
	}
	depth := 1
	for i++; i < len(body); i++ {
		switch body[i] {
		case '"':
			var ok bool
			i, ok = skipJSONString(body, i)
			if !ok {
				return i, false
			}
			i--
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return i, false
}

// ResponseInput contains normalized response evidence. Body text is not part
// of this contract; callers must decode REST or GraphQL envelopes first.
type ResponseInput struct {
	StatusCode    int
	Header        http.Header
	Message       string
	GraphQLErrors []GraphQLError
	Now           time.Time
}

// Classification describes confirmed throttle evidence and any provider
// deadline. RetryAt is nil when GitHub supplied no valid timing hint.
type Classification struct {
	RateLimited bool
	Kind        Kind
	Resource    string
	Reason      string
	RetryAt     *time.Time
}

// ClassifyResponse classifies a provider response without relying on arbitrary
// response-body substrings. Ordinary permission-denied 403s remain unclassified.
func ClassifyResponse(input ResponseInput) Classification {
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	header := input.Header
	remaining, hasRemaining := headerInt64(header, "X-RateLimit-Remaining")
	resource := bounded(strings.TrimSpace(header.Get("X-RateLimit-Resource")), 64)
	structuredThrottle, structuredReason := graphqlThrottle(input.GraphQLErrors)
	message := strings.ToLower(strings.TrimSpace(input.Message))
	restThrottle := strings.Contains(message, "rate limit") || strings.Contains(message, "abuse detection")
	throttleStatus := input.StatusCode == http.StatusForbidden || input.StatusCode == http.StatusTooManyRequests

	// A successful request that consumes the last primary allowance opens a
	// cooldown for subsequent work but remains a successful request to callers.
	// Definitive application errors and 304s honor the same exhaustion headers.
	if hasRemaining && remaining == 0 && (throttleStatus || IsDefinitiveResponse(input.StatusCode) || structuredThrottle) {
		return Classification{
			RateLimited: true,
			Kind:        KindPrimary,
			Resource:    resource,
			Reason:      bounded(firstNonEmpty(structuredReason, input.Message, "primary quota exhausted"), 240),
			RetryAt:     strongestDeadline(resetDeadline(header, now), retryAfterDeadline(header, now)),
		}
	}

	if retryAt := retryAfterDeadline(header, now); retryAt != nil && (throttleStatus || structuredThrottle) {
		return Classification{
			RateLimited: true,
			Kind:        KindSecondary,
			Resource:    resource,
			Reason:      bounded(firstNonEmpty(structuredReason, input.Message, "secondary throttle"), 240),
			RetryAt:     retryAt,
		}
	}
	if input.StatusCode == http.StatusTooManyRequests {
		return Classification{RateLimited: true, Kind: KindSecondary, Resource: resource, Reason: bounded(firstNonEmpty(input.Message, "HTTP 429"), 240)}
	}
	if structuredThrottle {
		return Classification{RateLimited: true, Kind: KindUnknown, Resource: resource, Reason: bounded(structuredReason, 240)}
	}
	if input.StatusCode == http.StatusForbidden && restThrottle {
		kind := KindUnknown
		if strings.Contains(message, "secondary rate limit") || strings.Contains(message, "abuse detection") {
			kind = KindSecondary
		}
		return Classification{RateLimited: true, Kind: kind, Resource: resource, Reason: bounded(input.Message, 240)}
	}
	return Classification{}
}

// RetryDeadline returns a valid provider timing hint even for retryable
// non-throttle responses such as a 503 with Retry-After.
func RetryDeadline(header http.Header, now time.Time) *time.Time {
	var reset *time.Time
	if remaining, ok := headerInt64(header, "X-RateLimit-Remaining"); ok && remaining == 0 {
		reset = resetDeadline(header, now)
	}
	return strongestDeadline(retryAfterDeadline(header, now), reset)
}

func strongestDeadline(deadlines ...*time.Time) *time.Time {
	var strongest *time.Time
	for _, deadline := range deadlines {
		if deadline == nil {
			continue
		}
		candidate := deadline.UTC()
		if strongest == nil || candidate.After(*strongest) {
			strongest = &candidate
		}
	}
	return strongest
}

func graphqlThrottle(items []GraphQLError) (bool, string) {
	for _, item := range items {
		errorType := strings.ToUpper(strings.TrimSpace(item.Type))
		message := strings.ToLower(strings.TrimSpace(item.Message))
		if errorType == "RATE_LIMITED" || errorType == "RATE_LIMIT" ||
			strings.Contains(message, "rate limit") || strings.Contains(message, "abuse detection") {
			return true, item.Message
		}
	}
	return false, ""
}

func retryAfterDeadline(header http.Header, now time.Time) *time.Time {
	if header == nil {
		return nil
	}
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		deadline := now.Add(time.Duration(seconds) * time.Second).UTC()
		return &deadline
	}
	if deadline, err := http.ParseTime(raw); err == nil {
		if deadline.Before(now) {
			deadline = now
		}
		deadline = deadline.UTC()
		return &deadline
	}
	return nil
}

func resetDeadline(header http.Header, now time.Time) *time.Time {
	reset, ok := headerInt64(header, "X-RateLimit-Reset")
	if !ok {
		return nil
	}
	deadline := time.Unix(reset, 0).UTC()
	if deadline.Before(now) {
		deadline = now.UTC()
	}
	return &deadline
}

func headerInt64(header http.Header, name string) (int64, bool) {
	if header == nil {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSpace(header.Get(name)), 10, 64)
	return value, err == nil
}

func bounded(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Deferral is returned before an outbound request when a covered installation
// is cooling down or another process owns its recovery probe.
type Deferral struct {
	Kind           Kind
	Resource       string
	InstallationID int64
	RetryAt        time.Time
	Generation     int64
	Reason         string
}

func (e *Deferral) Error() string {
	if e == nil {
		return "GitHub request deferred"
	}
	return fmt.Sprintf("GitHub installation %d request deferred until %s", e.InstallationID, e.RetryAt.UTC().Format(time.RFC3339))
}

// ControlledError preserves the original outbound error while carrying the
// exact controller deadline selected under enforcement.
type ControlledError struct {
	Err      error
	Deferral Deferral
}

func (e *ControlledError) Error() string {
	if e == nil || e.Err == nil {
		return "GitHub request throttled"
	}
	return e.Err.Error()
}

func (e *ControlledError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// AsDeferral recognizes both locally deferred work and throttled outbound
// requests whose original error was preserved.
func AsDeferral(err error) (*Deferral, bool) {
	var deferral *Deferral
	if errors.As(err, &deferral) {
		return deferral, true
	}
	var controlled *ControlledError
	if errors.As(err, &controlled) {
		copy := controlled.Deferral
		return &copy, true
	}
	return nil, false
}
