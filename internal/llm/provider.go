package llm

import (
	"context"
	"errors"
	"fmt"
)

// Provider represents a single LLM API backend (Anthropic, OpenAI, etc.).
// Each provider handles its own request/response marshaling.
type Provider interface {
	// Complete sends a prompt to the LLM and returns the text response.
	Complete(ctx context.Context, model, systemPrompt, userPrompt string) (string, error)

	// Name returns the provider identifier (e.g., "anthropic", "openai_chat").
	Name() string
}

// Sentinel errors for fallback classification.
var (
	ErrRateLimit     = errors.New("rate limit exceeded")
	ErrServerError   = errors.New("server error")
	ErrAuthError     = errors.New("authentication error")
	ErrContentPolicy = errors.New("content policy violation")
	ErrBadRequest    = errors.New("bad request")
)

// IsRetryable returns true if the error should trigger fallback to the next provider.
// Rate limits and server errors are retryable; auth errors and bad requests are not.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrRateLimit) || errors.Is(err, ErrServerError)
}

// classifyHTTPError maps an HTTP status code to an appropriate sentinel error.
func classifyHTTPError(statusCode int, body string) error {
	switch {
	case statusCode == 429:
		return fmt.Errorf("%w: %s", ErrRateLimit, body)
	case statusCode == 401 || statusCode == 403:
		return fmt.Errorf("%w: %s", ErrAuthError, body)
	case statusCode >= 500 || statusCode == 529:
		return fmt.Errorf("%w: HTTP %d: %s", ErrServerError, statusCode, body)
	case statusCode == 400:
		return fmt.Errorf("%w: %s", ErrBadRequest, body)
	default:
		return fmt.Errorf("HTTP %d: %s", statusCode, body)
	}
}
