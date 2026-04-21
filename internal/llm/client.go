package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
)

// Client is the consumer-facing interface for LLM completion calls.
// Services (validation, prioritization) depend on this interface.
type Client interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// chainLink pairs a provider with a specific model ID for the fallback chain.
type chainLink struct {
	provider Provider
	modelID  string
}

// FallbackClient implements Client with automatic provider failover.
// It tries providers in order, falling back on retryable errors (rate limits,
// server errors) but stopping on non-retryable errors (auth, bad request).
type FallbackClient struct {
	chain           []chainLink
	reasoningEffort ReasoningEffort
	logger          zerolog.Logger
}

func (c *FallbackClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	var lastErr error

	for i, link := range c.chain {
		resp, err := link.provider.Complete(ctx, link.modelID, systemPrompt, userPrompt, c.reasoningEffort)
		if err == nil {
			if i > 0 {
				c.logger.Info().
					Str("provider", link.provider.Name()).
					Str("model", link.modelID).
					Int("fallback_index", i).
					Msg("LLM call succeeded on fallback provider")
			}
			return resp, nil
		}

		lastErr = err
		c.logger.Warn().
			Err(err).
			Str("provider", link.provider.Name()).
			Str("model", link.modelID).
			Int("chain_index", i).
			Msg("LLM provider failed")

		if !IsRetryable(err) {
			return "", fmt.Errorf("%s: %w", link.provider.Name(), err)
		}
	}

	return "", fmt.Errorf("all %d providers failed, last error: %w", len(c.chain), lastErr)
}

// Config holds all LLM configuration needed to build a Client.
type Config struct {
	// Model is the human-friendly model name (e.g., "claude-sonnet-4-5", "gpt-4o").
	// The registry maps this to provider-specific model IDs and fallback chains.
	Model ModelName

	// ReasoningEffort controls how much reasoning the model should use.
	// Only applies to providers/models that support it (e.g., OpenAI reasoning models).
	// Leave empty to use the provider's default.
	ReasoningEffort ReasoningEffort

	// Anthropic API credentials.
	AnthropicAPIKey  string
	AnthropicBaseURL string // Optional, defaults to https://api.anthropic.com

	// OpenAI API credentials.
	OpenAIAPIKey  string
	OpenAIBaseURL string // Optional, defaults to https://api.openai.com
	// OpenAIAPIType selects which OpenAI API to use: "chat" (Chat Completions)
	// or "responses" (Responses API). Defaults to "chat".
	OpenAIAPIType string

	// OpenRouter API credentials. OpenRouter proxies requests to many LLM
	// providers through a single key, making it a good universal fallback.
	OpenRouterAPIKey  string
	OpenRouterBaseURL string // Optional, defaults to https://openrouter.ai/api
	OpenRouterAppName string // Optional, sent as X-Title header
	OpenRouterSiteURL string // Optional, sent as HTTP-Referer header

	// Gemini (Google Generative Language) API credentials.
	GeminiAPIKey  string
	GeminiBaseURL string // Optional, defaults to https://generativelanguage.googleapis.com

	// Timeout is the per-provider HTTP timeout. Defaults to 60s.
	Timeout time.Duration
}

// NewClient creates a FallbackClient from config.
// It builds providers from the configured API keys and assembles a fallback
// chain for the requested model. Defaults to the default model if none is specified.
func NewClient(cfg Config, logger zerolog.Logger) (Client, error) {
	if cfg.Model == "" {
		cfg.Model = ModelName(models.DefaultLLMModel)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}

	// Build available providers from config.
	providers := map[string]Provider{}

	if cfg.AnthropicAPIKey != "" {
		var opts []AnthropicOption
		if cfg.AnthropicBaseURL != "" {
			opts = append(opts, WithAnthropicBaseURL(cfg.AnthropicBaseURL))
		}
		opts = append(opts, WithAnthropicHTTPClient(httpClient))
		providers["anthropic"] = NewAnthropicProvider(cfg.AnthropicAPIKey, opts...)
	}

	if cfg.OpenAIAPIKey != "" {
		apiType := strings.ToLower(cfg.OpenAIAPIType)
		if apiType == "responses" {
			var opts []OpenAIResponsesOption
			if cfg.OpenAIBaseURL != "" {
				opts = append(opts, WithOpenAIResponsesBaseURL(cfg.OpenAIBaseURL))
			}
			opts = append(opts, WithOpenAIResponsesHTTPClient(httpClient))
			providers["openai_responses"] = NewOpenAIResponsesProvider(cfg.OpenAIAPIKey, opts...)
		} else {
			var opts []OpenAIChatOption
			if cfg.OpenAIBaseURL != "" {
				opts = append(opts, WithOpenAIChatBaseURL(cfg.OpenAIBaseURL))
			}
			opts = append(opts, WithOpenAIChatHTTPClient(httpClient))
			providers["openai_chat"] = NewOpenAIChatProvider(cfg.OpenAIAPIKey, opts...)
		}
	}

	if cfg.OpenRouterAPIKey != "" {
		var opts []OpenRouterOption
		if cfg.OpenRouterBaseURL != "" {
			opts = append(opts, WithOpenRouterBaseURL(cfg.OpenRouterBaseURL))
		}
		if cfg.OpenRouterAppName != "" {
			opts = append(opts, WithOpenRouterAppName(cfg.OpenRouterAppName))
		}
		if cfg.OpenRouterSiteURL != "" {
			opts = append(opts, WithOpenRouterSiteURL(cfg.OpenRouterSiteURL))
		}
		opts = append(opts, WithOpenRouterHTTPClient(httpClient))
		providers["openrouter"] = NewOpenRouterProvider(cfg.OpenRouterAPIKey, opts...)
	}

	if cfg.GeminiAPIKey != "" {
		var opts []GeminiOption
		if cfg.GeminiBaseURL != "" {
			opts = append(opts, WithGeminiBaseURL(cfg.GeminiBaseURL))
		}
		opts = append(opts, WithGeminiHTTPClient(httpClient))
		providers["gemini"] = NewGeminiProvider(cfg.GeminiAPIKey, opts...)
	}

	if len(providers) == 0 {
		return nil, &NoProvidersError{Model: cfg.Model}
	}

	chain, err := buildChain(cfg.Model, providers)
	if err != nil {
		return nil, err
	}

	logger.Info().
		Str("model", string(cfg.Model)).
		Int("chain_length", len(chain)).
		Msg("LLM client initialized")

	return &FallbackClient{chain: chain, reasoningEffort: cfg.ReasoningEffort, logger: logger}, nil
}

// UnknownModelError indicates the requested model is not in the registry.
type UnknownModelError struct {
	Model ModelName
}

func (e *UnknownModelError) Error() string {
	return fmt.Sprintf("unknown LLM model %q: not found in registry", e.Model)
}

// NoProvidersError indicates no configured providers can serve the requested model.
type NoProvidersError struct {
	Model ModelName
}

func (e *NoProvidersError) Error() string {
	return fmt.Sprintf("no configured providers available for model %q: set one of ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, or OPENROUTER_API_KEY", e.Model)
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
