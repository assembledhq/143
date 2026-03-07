package llm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// mockProvider implements Provider for testing.
type mockProvider struct {
	name      string
	response  string
	err       error
	callCount int
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Complete(_ context.Context, model, systemPrompt, userPrompt string) (string, error) {
	m.callCount++
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func TestFallbackClient_PrimarySucceeds(t *testing.T) {
	t.Parallel()

	primary := &mockProvider{name: "primary", response: "hello"}
	fallback := &mockProvider{name: "fallback", response: "world"}

	client := &FallbackClient{
		chain: []chainLink{
			{provider: primary, modelID: "model-a"},
			{provider: fallback, modelID: "model-b"},
		},
		logger: zerolog.Nop(),
	}

	resp, err := client.Complete(context.Background(), "sys", "user")
	require.NoError(t, err)
	require.Equal(t, "hello", resp)
	require.Equal(t, 1, primary.callCount)
	require.Equal(t, 0, fallback.callCount)
}

func TestFallbackClient_FallsBackOnRetryableError(t *testing.T) {
	t.Parallel()

	primary := &mockProvider{name: "primary", err: fmt.Errorf("%w: overloaded", ErrServerError)}
	fallback := &mockProvider{name: "fallback", response: "recovered"}

	client := &FallbackClient{
		chain: []chainLink{
			{provider: primary, modelID: "model-a"},
			{provider: fallback, modelID: "model-b"},
		},
		logger: zerolog.Nop(),
	}

	resp, err := client.Complete(context.Background(), "sys", "user")
	require.NoError(t, err)
	require.Equal(t, "recovered", resp)
	require.Equal(t, 1, primary.callCount)
	require.Equal(t, 1, fallback.callCount)
}

func TestFallbackClient_FallsBackOnRateLimit(t *testing.T) {
	t.Parallel()

	primary := &mockProvider{name: "primary", err: fmt.Errorf("%w: too many requests", ErrRateLimit)}
	fallback := &mockProvider{name: "fallback", response: "ok"}

	client := &FallbackClient{
		chain: []chainLink{
			{provider: primary, modelID: "model-a"},
			{provider: fallback, modelID: "model-b"},
		},
		logger: zerolog.Nop(),
	}

	resp, err := client.Complete(context.Background(), "sys", "user")
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
}

func TestFallbackClient_StopsOnNonRetryableError(t *testing.T) {
	t.Parallel()

	primary := &mockProvider{name: "primary", err: fmt.Errorf("%w: invalid key", ErrAuthError)}
	fallback := &mockProvider{name: "fallback", response: "should not reach"}

	client := &FallbackClient{
		chain: []chainLink{
			{provider: primary, modelID: "model-a"},
			{provider: fallback, modelID: "model-b"},
		},
		logger: zerolog.Nop(),
	}

	_, err := client.Complete(context.Background(), "sys", "user")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAuthError)
	require.Equal(t, 1, primary.callCount)
	require.Equal(t, 0, fallback.callCount, "fallback should not be called on auth error")
}

func TestFallbackClient_AllProvidersFail(t *testing.T) {
	t.Parallel()

	p1 := &mockProvider{name: "p1", err: fmt.Errorf("%w: down", ErrServerError)}
	p2 := &mockProvider{name: "p2", err: fmt.Errorf("%w: also down", ErrServerError)}

	client := &FallbackClient{
		chain: []chainLink{
			{provider: p1, modelID: "m1"},
			{provider: p2, modelID: "m2"},
		},
		logger: zerolog.Nop(),
	}

	_, err := client.Complete(context.Background(), "sys", "user")
	require.Error(t, err)
	require.Contains(t, err.Error(), "all 2 providers failed")
	require.Equal(t, 1, p1.callCount)
	require.Equal(t, 1, p2.callCount)
}

func TestBuildChain_FiltersUnavailableProviders(t *testing.T) {
	t.Parallel()

	anthropic := &mockProvider{name: "anthropic"}
	available := map[string]Provider{
		"anthropic": anthropic,
		// openai_chat is NOT configured
	}

	chain, err := buildChain("claude-sonnet-4-5", available)
	require.NoError(t, err)
	require.Len(t, chain, 1, "chain should only include configured providers")
	require.Equal(t, "anthropic", chain[0].provider.Name())
	require.Equal(t, "claude-sonnet-4-5-20250929", chain[0].modelID)
}

func TestBuildChain_UnknownModel(t *testing.T) {
	t.Parallel()

	_, err := buildChain("nonexistent-model", map[string]Provider{})
	require.Error(t, err)
	var unknownErr *UnknownModelError
	require.ErrorAs(t, err, &unknownErr)
}

func TestBuildChain_NoConfiguredProviders(t *testing.T) {
	t.Parallel()

	// Model exists in registry but no matching providers configured.
	_, err := buildChain("claude-sonnet-4-5", map[string]Provider{})
	require.Error(t, err)
	var noProvidersErr *NoProvidersError
	require.ErrorAs(t, err, &noProvidersErr)
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	require.True(t, IsRetryable(fmt.Errorf("%w: oops", ErrServerError)))
	require.True(t, IsRetryable(fmt.Errorf("%w: slow down", ErrRateLimit)))
	require.False(t, IsRetryable(fmt.Errorf("%w: bad key", ErrAuthError)))
	require.False(t, IsRetryable(fmt.Errorf("%w: blocked", ErrContentPolicy)))
	require.False(t, IsRetryable(fmt.Errorf("%w: malformed", ErrBadRequest)))
	require.False(t, IsRetryable(nil))
}

func TestNewClient_NilWhenNoModel(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{}, zerolog.Nop())
	require.NoError(t, err)
	require.Nil(t, client)
}

func TestNewClient_ErrorWhenNoProviders(t *testing.T) {
	t.Parallel()

	_, err := NewClient(Config{Model: "claude-sonnet-4-5"}, zerolog.Nop())
	require.Error(t, err)
}

func TestNewClient_WithAnthropicKey(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{
		Model:           "claude-sonnet-4-5",
		AnthropicAPIKey: "sk-ant-test",
	}, zerolog.Nop())
	require.NoError(t, err, "should create client with anthropic key")
	require.NotNil(t, client, "client should not be nil")
}

func TestNewClient_WithOpenAIChatKey(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{
		Model:         "gpt-4o",
		OpenAIAPIKey:  "sk-test",
		OpenAIAPIType: "chat",
	}, zerolog.Nop())
	require.NoError(t, err, "should create client with openai chat key")
	require.NotNil(t, client, "client should not be nil")
}

func TestNewClient_WithOpenAIResponsesKey(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{
		Model:         "gpt-4o",
		OpenAIAPIKey:  "sk-test",
		OpenAIAPIType: "responses",
	}, zerolog.Nop())
	require.NoError(t, err, "should create client with openai responses key")
	require.NotNil(t, client, "client should not be nil")
}

func TestNewClient_WithOpenRouterKey(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{
		Model:             "claude-sonnet-4-5",
		OpenRouterAPIKey:  "sk-or-test",
		OpenRouterBaseURL: "https://custom.openrouter.ai",
		OpenRouterAppName: "test-app",
		OpenRouterSiteURL: "https://example.com",
	}, zerolog.Nop())
	require.NoError(t, err, "should create client with openrouter key")
	require.NotNil(t, client, "client should not be nil")
}

func TestNewClient_WithCustomTimeout(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{
		Model:           "claude-sonnet-4-5",
		AnthropicAPIKey: "sk-ant-test",
		Timeout:         30 * time.Second,
	}, zerolog.Nop())
	require.NoError(t, err, "should create client with custom timeout")
	require.NotNil(t, client, "client should not be nil")
}

func TestNewClient_WithAllProviders(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{
		Model:            "claude-sonnet-4-5",
		AnthropicAPIKey:  "sk-ant-test",
		AnthropicBaseURL: "https://custom.anthropic.com",
		OpenAIAPIKey:     "sk-openai-test",
		OpenAIBaseURL:    "https://custom.openai.com",
		OpenRouterAPIKey: "sk-or-test",
	}, zerolog.Nop())
	require.NoError(t, err, "should create client with all providers")
	require.NotNil(t, client, "client should not be nil")
}

func TestNewClient_UnknownModel(t *testing.T) {
	t.Parallel()

	_, err := NewClient(Config{
		Model:           "nonexistent-model",
		AnthropicAPIKey: "sk-ant-test",
	}, zerolog.Nop())
	require.Error(t, err, "should return error for unknown model")
}

func TestUnknownModelError_Error(t *testing.T) {
	t.Parallel()

	err := &UnknownModelError{Model: "test-model"}
	require.Contains(t, err.Error(), "test-model", "error message should contain model name")
	require.Contains(t, err.Error(), "unknown", "error message should mention unknown")
}

func TestNoProvidersError_Error(t *testing.T) {
	t.Parallel()

	err := &NoProvidersError{Model: "test-model"}
	require.Contains(t, err.Error(), "test-model", "error message should contain model name")
	require.Contains(t, err.Error(), "no configured providers", "error message should mention no providers")
}

func TestRegisterModel(t *testing.T) {
	t.Parallel()

	customModel := "test-custom-model"
	entries := []providerModel{
		{ProviderName: "anthropic", ModelID: "custom-id"},
	}
	RegisterModel(customModel, entries)

	// Verify the model was registered by building a chain with it.
	anthropic := &mockProvider{name: "anthropic"}
	chain, err := buildChain(customModel, map[string]Provider{"anthropic": anthropic})
	require.NoError(t, err, "should build chain for registered model")
	require.Len(t, chain, 1, "chain should have one link")
	require.Equal(t, "custom-id", chain[0].modelID, "chain should use custom model ID")

	// Clean up to avoid affecting other tests.
	chainsMu.Lock()
	delete(defaultChains, customModel)
	chainsMu.Unlock()
}
