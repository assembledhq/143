package agent

import (
	"strings"

	"github.com/assembledhq/143/internal/models"
)

type tokenRate struct {
	inputPerMTok         float64
	cachedInputPerMTok   float64
	cacheCreationPerMTok float64
	outputPerMTok        float64
	unit                 TokenCostUnit
	detail               string
}

var openAIAPIRates = map[string]tokenRate{
	models.CodexModelGPT55:      {inputPerMTok: 5.00, cachedInputPerMTok: 0.50, outputPerMTok: 30.00, unit: TokenCostUnitUSD, detail: "openai_api_pricing"},
	models.CodexModelGPT55Fast:  {inputPerMTok: 12.50, cachedInputPerMTok: 1.25, outputPerMTok: 75.00, unit: TokenCostUnitUSD, detail: "openai_priority_pricing"},
	models.CodexModelGPT54:      {inputPerMTok: 2.50, cachedInputPerMTok: 0.25, outputPerMTok: 15.00, unit: TokenCostUnitUSD, detail: "openai_api_pricing"},
	models.CodexModelGPT54Fast:  {inputPerMTok: 6.25, cachedInputPerMTok: 0.625, outputPerMTok: 37.50, unit: TokenCostUnitUSD, detail: "openai_priority_pricing"},
	models.CodexModelGPT54Mini:  {inputPerMTok: 0.75, cachedInputPerMTok: 0.075, outputPerMTok: 4.50, unit: TokenCostUnitUSD, detail: "openai_api_pricing"},
	models.CodexModelGPT5Codex:  {inputPerMTok: 1.25, cachedInputPerMTok: 0.125, outputPerMTok: 10.00, unit: TokenCostUnitUSD, detail: "openai_api_pricing"},
	models.CodexModelGPT53Codex: {inputPerMTok: 1.75, cachedInputPerMTok: 0.175, outputPerMTok: 14.00, unit: TokenCostUnitUSD, detail: "openai_api_pricing"},
	models.CodexModelGPT52Codex: {inputPerMTok: 1.75, cachedInputPerMTok: 0.175, outputPerMTok: 14.00, unit: TokenCostUnitUSD, detail: "openai_api_pricing"},
	models.PiModelGPT54:         {inputPerMTok: 2.50, cachedInputPerMTok: 0.25, outputPerMTok: 15.00, unit: TokenCostUnitUSD, detail: "openai_api_pricing"},
}

var codexCreditRates = map[string]tokenRate{
	models.CodexModelGPT55:      {inputPerMTok: 125.00, cachedInputPerMTok: 12.50, outputPerMTok: 750.00, unit: TokenCostUnitCredits, detail: "codex_rate_card"},
	models.CodexModelGPT55Fast:  {inputPerMTok: 312.50, cachedInputPerMTok: 31.25, outputPerMTok: 1875.00, unit: TokenCostUnitCredits, detail: "codex_priority_rate_card"},
	models.CodexModelGPT54:      {inputPerMTok: 62.50, cachedInputPerMTok: 6.250, outputPerMTok: 375.00, unit: TokenCostUnitCredits, detail: "codex_rate_card"},
	models.CodexModelGPT54Fast:  {inputPerMTok: 156.25, cachedInputPerMTok: 15.625, outputPerMTok: 937.50, unit: TokenCostUnitCredits, detail: "codex_priority_rate_card"},
	models.CodexModelGPT54Mini:  {inputPerMTok: 18.75, cachedInputPerMTok: 1.875, outputPerMTok: 113.00, unit: TokenCostUnitCredits, detail: "codex_rate_card"},
	models.CodexModelGPT53Codex: {inputPerMTok: 43.75, cachedInputPerMTok: 4.375, outputPerMTok: 350.00, unit: TokenCostUnitCredits, detail: "codex_rate_card"},
	models.CodexModelGPT52Codex: {inputPerMTok: 43.75, cachedInputPerMTok: 4.375, outputPerMTok: 350.00, unit: TokenCostUnitCredits, detail: "codex_rate_card"},
}

var anthropicRates = map[string]tokenRate{
	models.ClaudeCodeModelOpus48:   {inputPerMTok: 5.00, cachedInputPerMTok: 0.50, cacheCreationPerMTok: 6.25, outputPerMTok: 25.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
	models.ClaudeCodeModelOpus47:   {inputPerMTok: 5.00, cachedInputPerMTok: 0.50, cacheCreationPerMTok: 6.25, outputPerMTok: 25.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
	models.ClaudeCodeModelOpus46:   {inputPerMTok: 5.00, cachedInputPerMTok: 0.50, cacheCreationPerMTok: 6.25, outputPerMTok: 25.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
	models.PiModelClaudeOpus48:     {inputPerMTok: 5.00, cachedInputPerMTok: 0.50, cacheCreationPerMTok: 6.25, outputPerMTok: 25.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
	models.PiModelClaudeOpus47:     {inputPerMTok: 5.00, cachedInputPerMTok: 0.50, cacheCreationPerMTok: 6.25, outputPerMTok: 25.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
	models.ClaudeCodeModelSonnet46: {inputPerMTok: 3.00, cachedInputPerMTok: 0.30, cacheCreationPerMTok: 3.75, outputPerMTok: 15.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
	models.PiModelClaudeSonnet46:   {inputPerMTok: 3.00, cachedInputPerMTok: 0.30, cacheCreationPerMTok: 3.75, outputPerMTok: 15.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
	models.ClaudeCodeModelSonnet45: {inputPerMTok: 3.00, cachedInputPerMTok: 0.30, cacheCreationPerMTok: 3.75, outputPerMTok: 15.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
	models.ClaudeCodeModelHaiku45:  {inputPerMTok: 1.00, cachedInputPerMTok: 0.10, cacheCreationPerMTok: 1.25, outputPerMTok: 5.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
	models.PiModelClaudeHaiku45:    {inputPerMTok: 1.00, cachedInputPerMTok: 0.10, cacheCreationPerMTok: 1.25, outputPerMTok: 5.00, unit: TokenCostUnitUSD, detail: "anthropic_api_pricing"},
}

var googleRates = map[string]tokenRate{
	models.GeminiCLIModelGemini25Pro: {inputPerMTok: 1.25, cachedInputPerMTok: 0.125, outputPerMTok: 10.00, unit: TokenCostUnitUSD, detail: "google_api_pricing"},
	models.PiModelGemini25Pro:        {inputPerMTok: 1.25, cachedInputPerMTok: 0.125, outputPerMTok: 10.00, unit: TokenCostUnitUSD, detail: "google_api_pricing"},
}

// FinalizeTokenUsage normalizes adapter-parsed usage into a single persisted
// shape. It preserves direct provider-reported cost when present, derives
// provider-native cost when the model and billing mode are known, and always
// attaches native usage metadata so downstream code can distinguish "zero" from
// "unavailable".
func FinalizeTokenUsage(usage TokenUsage, hint TokenUsageHint) TokenUsage {
	if hint.BillingMode == "" {
		hint.BillingMode = TokenBillingModeUnknown
	}

	reported := usage.Reported ||
		usage.InputTokens > 0 ||
		usage.CachedInputTokens > 0 ||
		usage.CacheCreationTokens > 0 ||
		usage.OutputTokens > 0 ||
		usage.TotalTokens > 0 ||
		usage.TotalCostUSD > 0 ||
		usage.Cost != nil ||
		usage.NativeCost != nil

	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.CachedInputTokens + usage.CacheCreationTokens + usage.OutputTokens
	}

	usage.NativeUsage = &NativeTokenUsage{
		Reported:            reported,
		Provider:            nativeProviderForHint(hint),
		Model:               hint.EffectiveModel,
		BillingMode:         hint.BillingMode,
		InputTokens:         usage.InputTokens,
		CachedInputTokens:   usage.CachedInputTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		OutputTokens:        usage.OutputTokens,
		TotalTokens:         usage.TotalTokens,
	}

	if usage.Cost != nil {
		if usage.Cost.Unit == TokenCostUnitUSD {
			usage.TotalCostUSD = usage.Cost.Amount
		} else if usage.NativeCost == nil {
			usage.NativeCost = usage.Cost
			usage.Cost = nil
		}
	}

	if reported && usage.Cost == nil && usage.NativeCost == nil {
		derived := deriveTokenCost(usage, hint)
		switch {
		case derived == nil:
			// Leave cost unavailable. NativeUsage still makes the distinction explicit.
		case derived.Unit == TokenCostUnitUSD:
			usage.Cost = derived
			usage.TotalCostUSD = derived.Amount
		default:
			usage.NativeCost = derived
		}
	}
	if !reported && hint.AgentType == models.AgentTypeOpenCode && hint.EffectiveModel != "" && usage.NativeCost == nil {
		usage.NativeCost = &TokenCost{
			Amount: 0,
			Unit:   TokenCostUnitUSD,
			Source: TokenCostSourceUnavailable,
			Detail: "opencode_usage_unreported",
		}
	}

	if usage.Cost != nil && usage.Cost.Unit == TokenCostUnitUSD {
		usage.TotalCostUSD = usage.Cost.Amount
	}

	return usage
}

// HasPersistableTokenUsage reports whether usage should be marshaled into
// persistent records. Metadata-only "usage unavailable" payloads should remain
// nil in storage, while reported zero-usage payloads should still persist.
func HasPersistableTokenUsage(usage TokenUsage) bool {
	return usage.Reported ||
		usage.InputTokens > 0 ||
		usage.CachedInputTokens > 0 ||
		usage.CacheCreationTokens > 0 ||
		usage.OutputTokens > 0 ||
		usage.TotalTokens > 0 ||
		usage.TotalCostUSD > 0 ||
		usage.Cost != nil ||
		usage.NativeCost != nil
}

func nativeProviderForHint(hint TokenUsageHint) string {
	switch hint.AgentType {
	case models.AgentTypeClaudeCode:
		return "anthropic"
	case models.AgentTypeCodex:
		return "openai"
	case models.AgentTypeGeminiCLI:
		return "google"
	case models.AgentTypeAmp:
		return "amp"
	case models.AgentTypePi:
		provider, _ := splitPiProviderModel(hint.EffectiveModel)
		if provider != "" {
			return provider
		}
		return "pi"
	case models.AgentTypeOpenCode:
		provider, _ := splitProviderModel(hint.EffectiveModel)
		if provider != "" {
			return provider
		}
		return "opencode"
	default:
		return ""
	}
}

func deriveTokenCost(usage TokenUsage, hint TokenUsageHint) *TokenCost {
	switch hint.AgentType {
	case models.AgentTypeClaudeCode:
		if hint.BillingMode != TokenBillingModeAPIKey {
			return nil
		}
		if rate, ok := anthropicRates[hint.EffectiveModel]; ok {
			return deriveCostFromRate(usage, rate)
		}
	case models.AgentTypeCodex:
		switch hint.BillingMode {
		case TokenBillingModeAPIKey:
			if rate, ok := openAIAPIRates[hint.EffectiveModel]; ok {
				return deriveCostFromRate(usage, rate)
			}
		case TokenBillingModeSubscription:
			if rate, ok := codexCreditRates[hint.EffectiveModel]; ok {
				return deriveCostFromRate(usage, rate)
			}
		}
	case models.AgentTypeGeminiCLI:
		if hint.BillingMode == TokenBillingModeAPIKey {
			if rate, ok := googleRates[hint.EffectiveModel]; ok {
				return deriveCostFromRate(usage, rate)
			}
		}
	case models.AgentTypePi:
		if hint.BillingMode != TokenBillingModeAPIKey {
			return nil
		}
		provider, model := splitPiProviderModel(hint.EffectiveModel)
		return deriveProviderModelCost(usage, provider, model)
	case models.AgentTypeOpenCode:
		if hint.BillingMode != TokenBillingModeAPIKey {
			return nil
		}
		provider, model := splitProviderModel(hint.EffectiveModel)
		return deriveProviderModelCost(usage, provider, model)
	}
	return nil
}

func deriveProviderModelCost(usage TokenUsage, provider, model string) *TokenCost {
	fullModel := provider + "/" + model
	switch provider {
	case "anthropic":
		if rate, ok := anthropicRates[fullModel]; ok {
			return deriveCostFromRate(usage, rate)
		}
	case "openai":
		if rate, ok := openAIAPIRates[fullModel]; ok {
			return deriveCostFromRate(usage, rate)
		}
		if rate, ok := openAIAPIRates[model]; ok {
			return deriveCostFromRate(usage, rate)
		}
	case "google":
		if rate, ok := googleRates[fullModel]; ok {
			return deriveCostFromRate(usage, rate)
		}
		if rate, ok := googleRates[model]; ok {
			return deriveCostFromRate(usage, rate)
		}
	}
	return nil
}

func deriveCostFromRate(usage TokenUsage, rate tokenRate) *TokenCost {
	amount := (float64(usage.InputTokens) * rate.inputPerMTok / 1_000_000.0) +
		(float64(usage.CachedInputTokens) * rate.cachedInputPerMTok / 1_000_000.0) +
		(float64(usage.CacheCreationTokens) * rate.cacheCreationPerMTok / 1_000_000.0) +
		(float64(usage.OutputTokens) * rate.outputPerMTok / 1_000_000.0)
	return &TokenCost{
		Amount: amount,
		Unit:   rate.unit,
		Source: TokenCostSourceDerived,
		Detail: rate.detail,
	}
}

func splitPiProviderModel(model string) (string, string) {
	return splitProviderModel(model)
}

func splitProviderModel(model string) (string, string) {
	before, after, ok := strings.Cut(model, "/")
	if !ok {
		return "", model
	}
	return before, after
}
