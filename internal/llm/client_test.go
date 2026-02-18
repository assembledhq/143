package llm

import (
	"context"
	"fmt"
	"testing"

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
