package github

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/assembledhq/143/internal/services/github/ratelimit"
)

// RetryClassification describes whether a GitHub request failure is safe to
// retry and, for rate limits, how long GitHub asked the caller to wait.
type RetryClassification struct {
	Retryable         bool
	RateLimited       bool
	RetryAfter        *time.Duration
	RetryAt           *time.Time
	RateLimitKind     ratelimit.Kind
	ControllerManaged bool
}

// GitHubRequestError preserves the caller's context state when an HTTP request
// fails before a response is available. http.Client.Do wraps both caller and
// client timeouts in url.Error, so the underlying error alone is ambiguous.
type GitHubRequestError struct {
	Err              error
	CallerContextErr error
}

// NewGitHubRequestError captures the operation context, which stays live when
// only http.Client.Timeout expires. It preserves the original error chain.
func NewGitHubRequestError(ctx context.Context, err error) *GitHubRequestError {
	result := &GitHubRequestError{Err: err}
	if ctx != nil {
		result.CallerContextErr = ctx.Err()
	}
	return result
}

func (e *GitHubRequestError) Error() string {
	if e == nil || e.Err == nil {
		return "GitHub request failed"
	}
	return e.Err.Error()
}

func (e *GitHubRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ClassifyRetry classifies GitHub API and transport failures without relying
// on error strings. now is supplied by the caller so reset timestamps can be
// tested deterministically.
func ClassifyRetry(err error, now time.Time) RetryClassification {
	if err == nil || errors.Is(err, context.Canceled) {
		return RetryClassification{}
	}
	if retryCallerStopped(err, false) {
		return RetryClassification{}
	}

	var throttled RetryClassification
	var transient RetryClassification
	walkErrorTree(err, func(cause error) {
		classification := classifyRetryCause(cause, now)
		if !classification.Retryable {
			return
		}
		if classification.RateLimited {
			mergeRetryClassification(&throttled, classification, now)
			return
		}
		mergeRetryClassification(&transient, classification, now)
	})
	if throttled.Retryable {
		return throttled
	}
	return transient
}

// Provenance is inherited only down its own branch, never from joined sibling
// failures. A raw deadline cannot distinguish client timeout from caller stop.
func retryCallerStopped(err error, owned bool) bool {
	if err == nil {
		return false
	}
	switch typed := err.(type) {
	case *GitHubRequestError:
		if typed.CallerContextErr != nil {
			return true
		}
		owned = true
	case *GitHubResponseReadError:
		if typed.CallerContextErr != nil {
			return true
		}
		owned = true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, cause := range wrapped.Unwrap() {
			if retryCallerStopped(cause, owned) {
				return true
			}
		}
		return false
	case interface{ Unwrap() error }:
		return retryCallerStopped(wrapped.Unwrap(), owned)
	default:
		return !owned && errors.Is(err, context.DeadlineExceeded)
	}
}

func classifyRetryCause(err error, now time.Time) RetryClassification {
	switch typed := err.(type) {
	case *ratelimit.Deferral:
		return classifyDeferral(typed, now)
	case *ratelimit.ControlledError:
		deferral := typed.Deferral
		return classifyDeferral(&deferral, now)
	case *GitHubAPIError:
		return classifyHTTPResponseFailure(typed.StatusCode, typed.Header, typed.Body, false, now)
	case *GitHubResponseReadError:
		return classifyHTTPResponseFailure(typed.StatusCode, typed.Header, typed.Body, true, now)
	case *GitHubGraphQLError:
		graphQLErr := typed
		if deferral, ok := ratelimit.InternalRetryMetadata(graphQLErr.Header, 0); ok {
			return classifyDeferral(deferral, now)
		}
		classification := ratelimit.ClassifyResponse(ratelimit.ResponseInput{
			StatusCode:    http.StatusOK,
			Header:        graphQLErr.Header,
			GraphQLErrors: graphQLErr.Errors,
			Now:           now,
		})
		if classification.RateLimited {
			result := RetryClassification{Retryable: true, RateLimited: true, RetryAt: classification.RetryAt, RateLimitKind: classification.Kind}
			if classification.RetryAt != nil {
				delay := classification.RetryAt.Sub(now)
				if delay < 0 {
					delay = 0
				}
				result.RetryAfter = &delay
			}
			return result
		}
		return RetryClassification{}
	case *url.Error:
		var networkErr net.Error
		return RetryClassification{Retryable: errors.As(typed, &networkErr)}
	default:
		if _, ok := err.(net.Error); ok {
			return RetryClassification{Retryable: true}
		}
		return RetryClassification{}
	}
}

func classifyHTTPResponseFailure(statusCode int, header http.Header, body []byte, interrupted bool, now time.Time) RetryClassification {
	if deferral, ok := ratelimit.InternalRetryMetadata(header, 0); ok {
		return classifyDeferral(deferral, now)
	}
	classification := ratelimit.ClassifyResponse(ratelimit.ResponseInput{
		StatusCode: statusCode,
		Header:     header,
		Message:    structuredGitHubAPIMessage(body),
		Now:        now,
	})
	rateLimited := classification.RateLimited
	retryable := interrupted || rateLimited ||
		statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusTooEarly ||
		statusCode >= http.StatusInternalServerError
	if !retryable {
		return RetryClassification{}
	}
	result := RetryClassification{
		Retryable:     true,
		RateLimited:   rateLimited,
		RetryAt:       classification.RetryAt,
		RateLimitKind: classification.Kind,
	}
	if result.RetryAt == nil {
		result.RetryAt = ratelimit.RetryDeadline(header, now)
	}
	if result.RetryAt != nil {
		delay := result.RetryAt.Sub(now)
		if delay < 0 {
			delay = 0
		}
		result.RetryAfter = &delay
	}
	return result
}

func classifyDeferral(deferral *ratelimit.Deferral, now time.Time) RetryClassification {
	if deferral == nil {
		return RetryClassification{}
	}
	retryAt := deferral.RetryAt.UTC()
	delay := retryAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return RetryClassification{
		Retryable: true, RateLimited: true, RetryAfter: &delay, RetryAt: &retryAt,
		RateLimitKind: deferral.Kind, ControllerManaged: true,
	}
}

func mergeRetryClassification(target *RetryClassification, candidate RetryClassification, now time.Time) {
	if target == nil || !candidate.Retryable {
		return
	}
	deadlineChanged := target.RetryAt == nil || candidate.RetryAt != nil && candidate.RetryAt.After(*target.RetryAt)
	if !target.Retryable {
		*target = candidate
	} else {
		target.Retryable = true
		target.RateLimited = target.RateLimited || candidate.RateLimited
		target.ControllerManaged = target.ControllerManaged || candidate.ControllerManaged
		if deadlineChanged && candidate.RetryAt != nil {
			retryAt := candidate.RetryAt.UTC()
			target.RetryAt = &retryAt
			target.RateLimitKind = candidate.RateLimitKind
		} else if target.RateLimitKind == ratelimit.KindUnknown && candidate.RateLimitKind != ratelimit.KindUnknown {
			target.RateLimitKind = candidate.RateLimitKind
		}
	}
	if target.RetryAt != nil {
		delay := target.RetryAt.Sub(now)
		if delay < 0 {
			delay = 0
		}
		target.RetryAfter = &delay
	}
}

func walkErrorTree(err error, visit func(error)) {
	if err == nil {
		return
	}
	visit(err)
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, cause := range wrapped.Unwrap() {
			walkErrorTree(cause, visit)
		}
	case interface{ Unwrap() error }:
		walkErrorTree(wrapped.Unwrap(), visit)
	}
}

func structuredGitHubAPIMessage(body []byte) string {
	var envelope struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	return envelope.Message
}

// GitHubGraphQLError preserves structured GraphQL errors so retry handling
// does not degrade to prose matching after the owned response decoder runs.
type GitHubGraphQLError struct {
	Errors []ratelimit.GraphQLError
	Header http.Header
}

func (e *GitHubGraphQLError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return "GitHub GraphQL request failed"
	}
	body, err := json.Marshal(e.Errors)
	if err != nil {
		return "GitHub GraphQL request failed"
	}
	return "GitHub GraphQL request failed: " + string(body)
}
