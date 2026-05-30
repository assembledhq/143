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
	// Ordered most-capable → least-capable within the Claude 4 family.
	"claude-opus-4-8": {
		{ProviderName: "anthropic", ModelID: "claude-opus-4-8"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4"},
		{ProviderName: "openrouter", ModelID: "anthropic/claude-opus-4-8"},
	},
	"claude-opus-4-7": {
		{ProviderName: "anthropic", ModelID: "claude-opus-4-7"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4"},
		{ProviderName: "openrouter", ModelID: "anthropic/claude-opus-4-7"},
	},
	"claude-sonnet-4-6": {
		{ProviderName: "anthropic", ModelID: "claude-sonnet-4-6"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4"},
		{ProviderName: "openrouter", ModelID: "anthropic/claude-sonnet-4-6"},
	},
	"claude-haiku-4-5": {
		{ProviderName: "anthropic", ModelID: "claude-haiku-4-5-20251001"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4-mini"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4-mini"},
		{ProviderName: "openrouter", ModelID: "anthropic/claude-haiku-4-5"},
	},

	// OpenAI models — primary: OpenAI (chat or responses), cross-provider fallback, then OpenRouter.
	// Ordered most-capable → least-capable within the gpt-5.4 family.
	"gpt-5.4": {
		{ProviderName: "openai_chat", ModelID: "gpt-5.4"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4"},
		{ProviderName: "anthropic", ModelID: "claude-sonnet-4-6"},
		{ProviderName: "openrouter", ModelID: "openai/gpt-5.4"},
	},
	"gpt-5.4-mini": {
		{ProviderName: "openai_chat", ModelID: "gpt-5.4-mini"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4-mini"},
		{ProviderName: "anthropic", ModelID: "claude-haiku-4-5-20251001"},
		{ProviderName: "openrouter", ModelID: "openai/gpt-5.4-mini"},
	},
	"gpt-5.4-nano": {
		{ProviderName: "openai_chat", ModelID: "gpt-5.4-nano"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4-nano"},
		{ProviderName: "openrouter", ModelID: "openai/gpt-5.4-nano"},
	},

	// Gemini models — primary: Gemini API, cross-provider fallback, then OpenRouter.
	// The human-friendly key is what the user picks in the dropdown; ModelID is the
	// provider-specific API identifier (Gemini 3.x models ship as "-preview" strings).
	"gemini-3.1-pro": {
		{ProviderName: "gemini", ModelID: "gemini-3.1-pro-preview"},
		{ProviderName: "anthropic", ModelID: "claude-opus-4-8"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4"},
		{ProviderName: "openrouter", ModelID: "google/gemini-3.1-pro-preview"},
	},
	"gemini-3-flash": {
		{ProviderName: "gemini", ModelID: "gemini-3-flash-preview"},
		{ProviderName: "anthropic", ModelID: "claude-sonnet-4-6"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4-mini"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4-mini"},
		{ProviderName: "openrouter", ModelID: "google/gemini-3-flash-preview"},
	},
	"gemini-2.5-pro": {
		{ProviderName: "gemini", ModelID: "gemini-2.5-pro"},
		{ProviderName: "anthropic", ModelID: "claude-sonnet-4-6"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4"},
		{ProviderName: "openrouter", ModelID: "google/gemini-2.5-pro"},
	},
	"gemini-2.5-flash": {
		{ProviderName: "gemini", ModelID: "gemini-2.5-flash"},
		{ProviderName: "anthropic", ModelID: "claude-haiku-4-5-20251001"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4-mini"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4-mini"},
		{ProviderName: "openrouter", ModelID: "google/gemini-2.5-flash"},
	},

	// Qwen open-weight models — served exclusively via OpenRouter (no native Qwen
	// provider). Cross-provider fallbacks are picked for rough capability parity so
	// orgs without an OpenRouter key can still get a completion.
	"qwen3-235b-a22b": {
		{ProviderName: "openrouter", ModelID: "qwen/qwen3-235b-a22b"},
		{ProviderName: "anthropic", ModelID: "claude-sonnet-4-6"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4"},
	},
	"qwen3-32b": {
		{ProviderName: "openrouter", ModelID: "qwen/qwen3-32b"},
		{ProviderName: "anthropic", ModelID: "claude-haiku-4-5-20251001"},
		{ProviderName: "openai_chat", ModelID: "gpt-5.4-mini"},
		{ProviderName: "openai_responses", ModelID: "gpt-5.4-mini"},
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
