package llm

import "sync"

// chainsMu protects defaultChains from concurrent read/write access.
var chainsMu sync.RWMutex

// providerModel maps a model name to a specific provider and its model ID.
type providerModel struct {
	// ProviderName matches a key in the available providers map (e.g., "anthropic", "openai_chat").
	ProviderName string
	// ModelID is the provider-specific model identifier.
	ModelID string
}

// defaultChains maps human-friendly model names to ordered provider fallback lists.
// The first entry is the primary provider; subsequent entries are fallbacks.
// Only providers that are configured (have API keys) will be included in the chain.
//
// OpenRouter is always last in the chain because it proxies to the same upstream
// providers — if Anthropic is down directly, OpenRouter's Anthropic route is
// likely also affected. However, OpenRouter can route to alternative models,
// so it serves as a useful last-resort fallback.
var defaultChains = map[ModelName][]providerModel{
	// Anthropic models — primary: Anthropic API, cross-provider fallback, then OpenRouter.
	"claude-opus-4-6": {
		{ProviderName: "anthropic", ModelID: "claude-opus-4-6"},
		{ProviderName: "openai_chat", ModelID: "gpt-4o"},
		{ProviderName: "openai_responses", ModelID: "gpt-4o"},
		{ProviderName: "openrouter", ModelID: "anthropic/claude-opus-4-6"},
	},
	"claude-sonnet-4-5": {
		{ProviderName: "anthropic", ModelID: "claude-sonnet-4-5-20250929"},
		{ProviderName: "openai_chat", ModelID: "gpt-4o"},
		{ProviderName: "openai_responses", ModelID: "gpt-4o"},
		{ProviderName: "openrouter", ModelID: "anthropic/claude-sonnet-4-5"},
	},
	"claude-haiku-4-5": {
		{ProviderName: "anthropic", ModelID: "claude-haiku-4-5-20251001"},
		{ProviderName: "openai_chat", ModelID: "gpt-4o-mini"},
		{ProviderName: "openai_responses", ModelID: "gpt-4o-mini"},
		{ProviderName: "openrouter", ModelID: "anthropic/claude-haiku-4-5"},
	},

	// OpenAI models — primary: OpenAI (chat or responses), cross-provider fallback, then OpenRouter.
	"gpt-4o": {
		{ProviderName: "openai_chat", ModelID: "gpt-4o"},
		{ProviderName: "openai_responses", ModelID: "gpt-4o"},
		{ProviderName: "anthropic", ModelID: "claude-sonnet-4-5-20250929"},
		{ProviderName: "openrouter", ModelID: "openai/gpt-4o"},
	},
	"gpt-4o-mini": {
		{ProviderName: "openai_chat", ModelID: "gpt-4o-mini"},
		{ProviderName: "openai_responses", ModelID: "gpt-4o-mini"},
		{ProviderName: "anthropic", ModelID: "claude-haiku-4-5-20251001"},
		{ProviderName: "openrouter", ModelID: "openai/gpt-4o-mini"},
	},
	"o3-mini": {
		{ProviderName: "openai_chat", ModelID: "o3-mini"},
		{ProviderName: "openai_responses", ModelID: "o3-mini"},
		{ProviderName: "anthropic", ModelID: "claude-sonnet-4-5-20250929"},
		{ProviderName: "openrouter", ModelID: "openai/o3-mini"},
	},
	"gpt-5.4-mini": {
		{ProviderName: "openai_chat", ModelID: "gpt-5.4-mini"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4-mini"},
		{ProviderName: "anthropic", ModelID: "claude-haiku-4-5-20251001"},
		{ProviderName: "openrouter", ModelID: "openai/gpt-5.4-mini"},
	},
	"gpt-5-nano": {
		{ProviderName: "openai_chat", ModelID: "gpt-5-nano"},
		{ProviderName: "openai_responses", ModelID: "gpt-5-nano"},
		{ProviderName: "openrouter", ModelID: "openai/gpt-5-nano"},
	},
}

// buildChain constructs an ordered fallback chain for the requested model,
// filtering to only providers that are available (configured with API keys).
// Returns an error if no providers are available for the requested model.
func buildChain(model ModelName, available map[string]Provider) ([]chainLink, error) {
	chainsMu.RLock()
	entries, ok := defaultChains[model]
	chainsMu.RUnlock()
	if !ok {
		return nil, &UnknownModelError{Model: model}
	}

	var chain []chainLink
	for _, entry := range entries {
		provider, exists := available[entry.ProviderName]
		if !exists {
			continue
		}
		chain = append(chain, chainLink{
			provider: provider,
			modelID:  entry.ModelID,
		})
	}

	if len(chain) == 0 {
		return nil, &NoProvidersError{Model: model}
	}
	return chain, nil
}

// RegisterModel adds or replaces a model's provider chain in the registry.
// This allows custom models to be added at runtime.
func RegisterModel(model ModelName, entries []providerModel) {
	chainsMu.Lock()
	defaultChains[model] = entries
	chainsMu.Unlock()
}
