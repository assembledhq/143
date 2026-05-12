package adapters

import "github.com/assembledhq/143/internal/services/agent"

func mergeTokenUsage(dst *agent.TokenUsage, src agent.TokenUsage) {
	if src.Reported {
		dst.Reported = true
	}
	if src.InputTokens != 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.CachedInputTokens != 0 {
		dst.CachedInputTokens = src.CachedInputTokens
	}
	if src.CacheCreationTokens != 0 {
		dst.CacheCreationTokens = src.CacheCreationTokens
	}
	if src.OutputTokens != 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.TotalTokens != 0 {
		dst.TotalTokens = src.TotalTokens
	}
	if src.Cost != nil {
		dst.Cost = src.Cost
	}
	if src.NativeCost != nil {
		dst.NativeCost = src.NativeCost
	}
	if src.NativeUsage != nil {
		dst.NativeUsage = src.NativeUsage
	}
	if src.TotalCostUSD != 0 {
		dst.TotalCostUSD = src.TotalCostUSD
	}
}

func setDirectUSDCost(dst *agent.TokenUsage, amount float64, detail string) {
	dst.Reported = true
	dst.TotalCostUSD = amount
	dst.Cost = &agent.TokenCost{
		Amount: amount,
		Unit:   agent.TokenCostUnitUSD,
		Source: agent.TokenCostSourceDirect,
		Detail: detail,
	}
}
