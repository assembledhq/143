package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestFinalizeTokenUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		usage    TokenUsage
		hint     TokenUsageHint
		validate func(t *testing.T, usage TokenUsage)
	}{
		{
			name: "keeps direct claude usd cost and marks source direct",
			usage: TokenUsage{
				InputTokens:         1200,
				OutputTokens:        300,
				CachedInputTokens:   50,
				CacheCreationTokens: 25,
				TotalCostUSD:        0.42,
				Cost: &TokenCost{
					Amount: 0.42,
					Unit:   TokenCostUnitUSD,
					Source: TokenCostSourceDirect,
					Detail: "claude_result_total_cost_usd",
				},
			},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeClaudeCode,
				EffectiveModel: models.ClaudeCodeModelSonnet46,
				BillingMode:    TokenBillingModeAPIKey,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.NotNil(t, usage.Cost, "direct cost should be retained")
				require.Equal(t, TokenCostSourceDirect, usage.Cost.Source, "claude direct cost should stay direct")
				require.Equal(t, TokenCostUnitUSD, usage.Cost.Unit, "claude direct cost should be expressed in usd")
				require.Equal(t, 0.42, usage.TotalCostUSD, "total_cost_usd should mirror the direct usd amount")
				require.NotNil(t, usage.NativeUsage, "native usage metadata should be attached")
				require.Equal(t, "anthropic", usage.NativeUsage.Provider, "claude usage should identify anthropic as the native provider")
				require.Equal(t, models.ClaudeCodeModelSonnet46, usage.NativeUsage.Model, "native usage should retain the effective model")
			},
		},
		{
			name: "claude subscription leaves usd unavailable when direct cost is absent",
			usage: TokenUsage{
				InputTokens:       1_000_000,
				CachedInputTokens: 1_000_000,
				OutputTokens:      1_000_000,
			},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeClaudeCode,
				EffectiveModel: models.ClaudeCodeModelSonnet46,
				BillingMode:    TokenBillingModeSubscription,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.Nil(t, usage.Cost, "subscription-backed claude runs should not synthesize a normalized USD cost")
				require.Nil(t, usage.NativeCost, "subscription-backed claude runs should not invent a native cost without a provider-reported amount")
				require.Equal(t, 0.0, usage.TotalCostUSD, "subscription-backed claude runs should leave total_cost_usd unset when direct cost is absent")
				require.NotNil(t, usage.NativeUsage, "native usage metadata should still be attached")
				require.Equal(t, TokenBillingModeSubscription, usage.NativeUsage.BillingMode, "native usage should preserve the subscription billing mode")
			},
		},
		{
			name: "derives codex api key usd cost from token mix",
			usage: TokenUsage{
				InputTokens:       1_000_000,
				CachedInputTokens: 1_000_000,
				OutputTokens:      1_000_000,
			},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeCodex,
				EffectiveModel: models.CodexModelGPT54,
				BillingMode:    TokenBillingModeAPIKey,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.NotNil(t, usage.Cost, "codex api-key runs should derive a cost when the model is known")
				require.Equal(t, TokenCostSourceDerived, usage.Cost.Source, "codex api-key cost should be marked derived")
				require.Equal(t, TokenCostUnitUSD, usage.Cost.Unit, "codex api-key derivation should yield usd")
				require.InDelta(t, 17.75, usage.Cost.Amount, 0.0001, "codex api-key derivation should use input, cached-input, and output pricing")
				require.InDelta(t, 17.75, usage.TotalCostUSD, 0.0001, "total_cost_usd should mirror a derived usd amount")
				require.NotNil(t, usage.NativeUsage, "native usage metadata should be attached")
				require.Equal(t, TokenBillingModeAPIKey, usage.NativeUsage.BillingMode, "native usage should retain the billing mode")
			},
		},
		{
			name: "derives codex subscription native credits instead of usd",
			usage: TokenUsage{
				InputTokens:       1_000_000,
				CachedInputTokens: 1_000_000,
				OutputTokens:      1_000_000,
			},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeCodex,
				EffectiveModel: models.CodexModelGPT54,
				BillingMode:    TokenBillingModeSubscription,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.Equal(t, 0.0, usage.TotalCostUSD, "subscription-backed codex runs should not synthesize a usd total")
				require.Nil(t, usage.Cost, "normalized usd cost should remain unavailable for subscription-backed codex runs")
				require.NotNil(t, usage.NativeCost, "subscription-backed codex runs should keep a native credit estimate")
				require.Equal(t, TokenCostSourceDerived, usage.NativeCost.Source, "native codex credits should be marked derived")
				require.Equal(t, TokenCostUnitCredits, usage.NativeCost.Unit, "codex native billing unit should be credits")
				require.InDelta(t, 443.75, usage.NativeCost.Amount, 0.0001, "codex subscription derivation should follow the published rate card")
			},
		},
		{
			name: "derives codex fast subscription credits at priority rate",
			usage: TokenUsage{
				InputTokens:       1_000_000,
				CachedInputTokens: 1_000_000,
				OutputTokens:      1_000_000,
			},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeCodex,
				EffectiveModel: models.CodexModelGPT54Fast,
				BillingMode:    TokenBillingModeSubscription,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.NotNil(t, usage.NativeCost, "fast subscription-backed codex runs should keep a native credit estimate")
				require.Equal(t, TokenCostUnitCredits, usage.NativeCost.Unit, "fast codex native billing unit should be credits")
				require.InDelta(t, 1109.375, usage.NativeCost.Amount, 0.0001, "fast codex subscription derivation should apply the priority multiplier")
			},
		},
		{
			name: "derives pi anthropic usd cost from provider model",
			usage: TokenUsage{
				InputTokens:       1_000_000,
				CachedInputTokens: 1_000_000,
				OutputTokens:      1_000_000,
			},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypePi,
				EffectiveModel: models.PiModelClaudeSonnet46,
				BillingMode:    TokenBillingModeAPIKey,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.NotNil(t, usage.Cost, "pi should derive usd cost for curated anthropic models")
				require.Equal(t, TokenCostSourceDerived, usage.Cost.Source, "pi anthropic cost should be marked derived")
				require.Equal(t, TokenCostUnitUSD, usage.Cost.Unit, "pi anthropic derivation should yield usd")
				require.InDelta(t, 18.3, usage.Cost.Amount, 0.0001, "pi anthropic derivation should use anthropic token and cache pricing")
			},
		},
		{
			name: "derives opencode anthropic usd cost and preserves backing provider",
			usage: TokenUsage{
				InputTokens:       1_000_000,
				CachedInputTokens: 1_000_000,
				OutputTokens:      1_000_000,
			},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeOpenCode,
				EffectiveModel: models.OpenCodeModelClaudeHaiku45,
				BillingMode:    TokenBillingModeAPIKey,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.NotNil(t, usage.Cost, "opencode should derive usd cost for curated provider-backed models")
				require.Equal(t, TokenCostSourceDerived, usage.Cost.Source, "opencode cost should be marked derived")
				require.Equal(t, TokenCostUnitUSD, usage.Cost.Unit, "opencode derivation should yield usd for API-key provider models")
				require.InDelta(t, 6.1, usage.Cost.Amount, 0.0001, "opencode derivation should use the backing provider token pricing")
				require.NotNil(t, usage.NativeUsage, "opencode should attach native usage metadata")
				require.Equal(t, "anthropic", usage.NativeUsage.Provider, "opencode native usage should preserve the backing provider parsed from provider/model")
				require.Equal(t, models.OpenCodeModelClaudeHaiku45, usage.NativeUsage.Model, "opencode native usage should retain the full provider/model id")
			},
		},
		{
			name: "keeps amp usage native but leaves cost unavailable",
			usage: TokenUsage{
				InputTokens:  900,
				OutputTokens: 120,
			},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeAmp,
				EffectiveModel: models.AmpModeSmart,
				BillingMode:    TokenBillingModeAPIKey,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.Nil(t, usage.Cost, "amp should not invent a cost when only a mode is known")
				require.Equal(t, 0.0, usage.TotalCostUSD, "amp should leave total_cost_usd unset when cost cannot be computed safely")
				require.NotNil(t, usage.NativeUsage, "amp should still emit native usage metadata")
				require.Equal(t, "amp", usage.NativeUsage.Provider, "amp native usage should identify the upstream runtime")
				require.Equal(t, models.AmpModeSmart, usage.NativeUsage.Model, "amp native usage should retain the selected mode")
			},
		},
		{
			name:  "marks usage unavailable when the provider emitted no token payload at all",
			usage: TokenUsage{},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeCodex,
				EffectiveModel: models.CodexModelGPT54,
				BillingMode:    TokenBillingModeSubscription,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.NotNil(t, usage.NativeUsage, "native usage metadata should still be attached")
				require.False(t, usage.NativeUsage.Reported, "native usage should explicitly mark provider counters as unavailable when no token payload was emitted")
				require.Nil(t, usage.Cost, "cost should remain unavailable when no usage was reported")
				require.Nil(t, usage.NativeCost, "native cost should remain unavailable when no usage was reported")
			},
		},
		{
			name:  "opencode records an explicit unavailable estimate when no usage arrives",
			usage: TokenUsage{},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeOpenCode,
				EffectiveModel: models.OpenCodeModelGPT54Mini,
				BillingMode:    TokenBillingModeAPIKey,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.NotNil(t, usage.NativeUsage, "opencode should still attach provider and model metadata")
				require.False(t, usage.NativeUsage.Reported, "opencode no-usage metadata should distinguish unavailable counters from reported zeros")
				require.NotNil(t, usage.NativeCost, "opencode should persist explicit unavailable cost metadata when the CLI omits usage")
				require.Equal(t, TokenCostSourceUnavailable, usage.NativeCost.Source, "opencode no-usage metadata should not be marked direct or derived")
				require.Equal(t, "opencode_usage_unreported", usage.NativeCost.Detail, "opencode unavailable metadata should explain why cost is absent")
			},
		},
		{
			name:  "preserves reported zero-usage payloads distinctly from unavailable usage",
			usage: TokenUsage{Reported: true},
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeClaudeCode,
				EffectiveModel: models.ClaudeCodeModelSonnet46,
				BillingMode:    TokenBillingModeAPIKey,
			},
			validate: func(t *testing.T, usage TokenUsage) {
				t.Helper()
				require.NotNil(t, usage.NativeUsage, "native usage metadata should still be attached")
				require.True(t, usage.NativeUsage.Reported, "native usage should preserve that the provider emitted a zero-usage payload")
				require.Equal(t, 0, usage.InputTokens, "reported-zero usage should keep zero counters")
				require.Equal(t, 0, usage.OutputTokens, "reported-zero usage should keep zero counters")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := FinalizeTokenUsage(tt.usage, tt.hint)
			tt.validate(t, actual)
		})
	}
}

func TestFinalizeTokenUsage_DerivesPublishedRateModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		hint  TokenUsageHint
		unit  TokenCostUnit
		field string
	}{
		{
			name: "claude opus 4.6 derives usd",
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeClaudeCode,
				EffectiveModel: models.ClaudeCodeModelOpus46,
				BillingMode:    TokenBillingModeAPIKey,
			},
			unit:  TokenCostUnitUSD,
			field: "cost",
		},
		{
			name: "gpt-5-codex api key derives usd",
			hint: TokenUsageHint{
				AgentType:      models.AgentTypeCodex,
				EffectiveModel: models.CodexModelGPT5Codex,
				BillingMode:    TokenBillingModeAPIKey,
			},
			unit:  TokenCostUnitUSD,
			field: "cost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := FinalizeTokenUsage(TokenUsage{
				InputTokens:       1_000_000,
				CachedInputTokens: 1_000_000,
				OutputTokens:      1_000_000,
			}, tt.hint)

			switch tt.field {
			case "cost":
				require.NotNil(t, actual.Cost, "published-rate models should derive normalized cost")
				require.Equal(t, tt.unit, actual.Cost.Unit, "published-rate derivation should use the expected billing unit")
			case "native_cost":
				require.NotNil(t, actual.NativeCost, "published-rate models should derive native cost when USD is not the native unit")
				require.Equal(t, tt.unit, actual.NativeCost.Unit, "published-rate derivation should use the expected native billing unit")
			default:
				t.Fatalf("unsupported field selector %q", tt.field)
			}
		})
	}
}

func TestFinalizeTokenUsage_DerivesGPT56PublishedRates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		model                 string
		expectedAPIAmount     float64
		expectedCreditsAmount float64
	}{
		{name: "sol", model: models.CodexModelGPT56Sol, expectedAPIAmount: 41.75, expectedCreditsAmount: 887.5},
		{name: "terra", model: models.CodexModelGPT56Terra, expectedAPIAmount: 20.875, expectedCreditsAmount: 443.75},
		{name: "luna", model: models.CodexModelGPT56Luna, expectedAPIAmount: 8.35, expectedCreditsAmount: 177.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			usage := TokenUsage{
				InputTokens:         1_000_000,
				CachedInputTokens:   1_000_000,
				CacheCreationTokens: 1_000_000,
				OutputTokens:        1_000_000,
			}
			apiUsage := FinalizeTokenUsage(usage, TokenUsageHint{
				AgentType:      models.AgentTypeCodex,
				EffectiveModel: tt.model,
				BillingMode:    TokenBillingModeAPIKey,
			})

			require.NotNil(t, apiUsage.Cost, "GPT-5.6 API-key usage should derive a published USD cost")
			require.Equal(t, TokenCostUnitUSD, apiUsage.Cost.Unit, "GPT-5.6 API-key cost should use USD")
			require.Equal(t, TokenCostSourceDerived, apiUsage.Cost.Source, "GPT-5.6 API-key cost should be marked derived")
			require.InDelta(t, tt.expectedAPIAmount, apiUsage.Cost.Amount, 0.0001, "GPT-5.6 API cost should include input, cache read, cache write, and output pricing")
			require.InDelta(t, tt.expectedAPIAmount, apiUsage.TotalCostUSD, 0.0001, "GPT-5.6 total_cost_usd should mirror the derived amount")

			subscriptionUsage := FinalizeTokenUsage(TokenUsage{
				InputTokens:       1_000_000,
				CachedInputTokens: 1_000_000,
				OutputTokens:      1_000_000,
			}, TokenUsageHint{
				AgentType:      models.AgentTypeCodex,
				EffectiveModel: tt.model,
				BillingMode:    TokenBillingModeSubscription,
			})

			require.NotNil(t, subscriptionUsage.NativeCost, "GPT-5.6 subscription usage should derive published Codex credits")
			require.Equal(t, TokenCostUnitCredits, subscriptionUsage.NativeCost.Unit, "GPT-5.6 subscription cost should use credits")
			require.Equal(t, TokenCostSourceDerived, subscriptionUsage.NativeCost.Source, "GPT-5.6 subscription cost should be marked derived")
			require.InDelta(t, tt.expectedCreditsAmount, subscriptionUsage.NativeCost.Amount, 0.0001, "GPT-5.6 subscription cost should include input, cache read, and output credit rates")
		})
	}
}

func TestFinalizeTokenUsage_CodexSparkLeavesCostUnavailable(t *testing.T) {
	t.Parallel()

	actual := FinalizeTokenUsage(TokenUsage{
		InputTokens:       1_000_000,
		CachedInputTokens: 1_000_000,
		OutputTokens:      1_000_000,
	}, TokenUsageHint{
		AgentType:      models.AgentTypeCodex,
		EffectiveModel: models.CodexModelGPT53CodexSpark,
		BillingMode:    TokenBillingModeSubscription,
	})

	require.Nil(t, actual.Cost, "research-preview models without published token rates should not invent a USD cost")
	require.Nil(t, actual.NativeCost, "research-preview models without published token rates should not invent native credits")
	require.NotNil(t, actual.NativeUsage, "native usage metadata should still be attached when cost is unavailable")
}

func TestHasPersistableTokenUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		usage    TokenUsage
		expected bool
	}{
		{
			name: "unavailable usage metadata alone is not persisted",
			usage: FinalizeTokenUsage(TokenUsage{}, TokenUsageHint{
				AgentType:      models.AgentTypeCodex,
				EffectiveModel: models.CodexModelGPT54,
				BillingMode:    TokenBillingModeSubscription,
			}),
			expected: false,
		},
		{
			name: "reported zero-usage payloads are persisted",
			usage: FinalizeTokenUsage(TokenUsage{Reported: true}, TokenUsageHint{
				AgentType:      models.AgentTypeClaudeCode,
				EffectiveModel: models.ClaudeCodeModelSonnet46,
				BillingMode:    TokenBillingModeAPIKey,
			}),
			expected: true,
		},
		{
			name: "derived native cost remains persistable",
			usage: TokenUsage{
				CachedInputTokens: 123,
				NativeCost: &TokenCost{
					Amount: 12.5,
					Unit:   TokenCostUnitCredits,
					Source: TokenCostSourceDerived,
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, HasPersistableTokenUsage(tt.usage), "HasPersistableTokenUsage should classify persistable token usage correctly")
		})
	}
}
