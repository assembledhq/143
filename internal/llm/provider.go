package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
)

// ModelName is an alias for the canonical models.ModelName type.
type ModelName = models.ModelName

// ReasoningEffort is an alias for the canonical models.ReasoningEffort type.
type ReasoningEffort = models.ReasoningEffort

// Provider represents a single LLM API backend (Anthropic, OpenAI, etc.).
// Each provider handles its own request/response marshaling.
type Provider interface {
	// Complete sends a prompt to the LLM and returns the text response.
	Complete(ctx context.Context, model, systemPrompt, userPrompt string, reasoningEffort ReasoningEffort) (string, error)

	// Name returns the provider identifier (e.g., "anthropic", "openai_chat").
	Name() string
}

// modelSupportsReasoningEffort returns true if the given model ID is known to
// support the reasoning_effort parameter. Unsupported models (e.g. gpt-4o)
// may reject the parameter with a 400 error.
func modelSupportsReasoningEffort(model string) bool {
	// o-series reasoning models and gpt-5+ models support reasoning_effort.
	// Legacy models (gpt-4o, gpt-4o-mini) do not.
	return strings.HasPrefix(model, "o") || strings.HasPrefix(model, "gpt-5")
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
