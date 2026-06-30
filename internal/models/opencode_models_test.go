package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every logical model must have at least one route, an OpenRouter-first
// ordering when it has multiple routes, and physical ids that exist in the
// curated AvailableOpenCodeModels list.
func TestOpenCodeModelRegistry_Invariants(t *testing.T) {
	t.Parallel()

	seenLogical := map[string]struct{}{}
	seenPhysical := map[string]struct{}{}
	for _, m := range OpenCodeModelRegistry {
		require.NotEmpty(t, m.ID, "logical model id must be set")
		require.NotEmpty(t, m.DisplayName, "%s: display name must be set", m.ID)
		require.NotEmpty(t, m.Routes, "%s: must have at least one route", m.ID)

		_, dup := seenLogical[m.ID]
		require.False(t, dup, "duplicate logical model id %q", m.ID)
		seenLogical[m.ID] = struct{}{}

		for i, route := range m.Routes {
			require.NotEmpty(t, route.PhysicalModelID, "%s route %d: physical id must be set", m.ID, i)
			require.NotEmpty(t, route.Backing, "%s route %d: backing must be set", m.ID, i)
			require.Contains(t, AvailableOpenCodeModels, route.PhysicalModelID,
				"%s: route physical id %q must be a curated OpenCode model", m.ID, route.PhysicalModelID)

			_, dup := seenPhysical[route.PhysicalModelID]
			require.False(t, dup, "physical id %q appears in more than one route", route.PhysicalModelID)
			seenPhysical[route.PhysicalModelID] = struct{}{}

			// OpenRouter routes carry an audited US allowlist; native and
			// first-party routes do not.
			if route.Backing == ProviderOpenRouter {
				require.NotEmpty(t, route.USProviderList, "%s: OpenRouter route %q must pin an audited US provider list", m.ID, route.PhysicalModelID)
			} else {
				require.Empty(t, route.USProviderList, "%s: non-OpenRouter route %q must not carry a US provider list", m.ID, route.PhysicalModelID)
			}
		}

		// Multi-route models must list OpenRouter first (the recommended,
		// US-audited transport), with the native fallback after it.
		if len(m.Routes) > 1 {
			require.Equal(t, ProviderOpenRouter, m.Routes[0].Backing, "%s: first route must be OpenRouter", m.ID)
			require.True(t, m.Routes[len(m.Routes)-1].IsNativeOpenCode(), "%s: last route of a multi-route model must be native", m.ID)
		}
	}

	// Every curated physical model id must be reachable through some route, so
	// pinned legacy selections always resolve.
	for _, physical := range AvailableOpenCodeModels {
		require.True(t, IsKnownOpenCodePhysicalModel(physical), "curated model %q must be covered by a registry route", physical)
	}
}

func TestOpenCodeModelsForAPI_MirrorsRegistry(t *testing.T) {
	t.Parallel()

	api := OpenCodeModelsForAPI()
	require.Len(t, api, len(OpenCodeModelRegistry), "API projection must cover every registry model")

	for i, m := range api {
		reg := OpenCodeModelRegistry[i]
		require.Equal(t, reg.ID, m.ID, "order and id must match the registry")
		require.Equal(t, reg.DisplayName, m.DisplayName)
		require.Len(t, m.Routes, len(reg.Routes))
		for j, route := range m.Routes {
			require.Equal(t, string(reg.Routes[j].Backing), route.Backing)
			require.Equal(t, reg.Routes[j].PhysicalModelID, route.PhysicalModelID)
			require.NotEmpty(t, route.TransportLabel, "every route must carry a transport label")
		}
	}

	// Spot-check the GLM 5.2 entry the frontend leans on.
	require.Equal(t, "glm-5.2", api[0].ID)
	require.Equal(t, "OpenRouter", api[0].Routes[0].TransportLabel)
	require.Equal(t, "openrouter", api[0].Routes[0].Backing)

	indexByID := map[string]int{}
	for i, m := range api {
		indexByID[m.ID] = i
	}
	require.Less(t, indexByID["kimi-k2.6"], indexByID["kimi-k2.5"], "newer Kimi models should appear before older Kimi models")
	require.Less(t, indexByID[OpenCodeModelClaudeOpus48], indexByID[OpenCodeModelClaudeOpus47], "newer Claude Opus models should appear before older Claude Opus models")
	require.Less(t, indexByID[OpenCodeModelClaudeOpus47], indexByID[OpenCodeModelClaudeOpus46], "Claude Opus 4.7 should appear before Claude Opus 4.6")
	require.NotContains(t, indexByID, "claude-fable-5", "OpenCode API models should not include Claude Fable")
}

func TestOpenCodeTransportLabel(t *testing.T) {
	t.Parallel()
	require.Equal(t, "OpenRouter", OpenCodeTransportLabel(ProviderOpenRouter))
	require.Equal(t, "OpenCode native", OpenCodeTransportLabel(ProviderOpenCode))
	require.Equal(t, "Anthropic", OpenCodeTransportLabel(ProviderAnthropic))
}

func TestOpenCodeModelRegistry_DefaultModelResolves(t *testing.T) {
	t.Parallel()

	m, ok := LookupOpenCodeModel(DefaultOpenCodeModel)
	require.True(t, ok, "DefaultOpenCodeModel must be a registry logical model")
	require.Equal(t, ProviderOpenRouter, m.Routes[0].Backing, "default model should prefer OpenRouter")
}

func TestDefaultOpenCodePhysicalModelForBacking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		backing  ProviderName
		expected string
	}{
		{name: "openrouter uses product default", backing: ProviderOpenRouter, expected: OpenCodeModelOpenRouterGLM52},
		{name: "native uses product default", backing: ProviderOpenCode, expected: OpenCodeModelGLM52},
		{name: "openai uses inexpensive first-party default", backing: ProviderOpenAI, expected: OpenCodeModelGPT54Mini},
		{name: "anthropic uses inexpensive first-party default", backing: ProviderAnthropic, expected: OpenCodeModelClaudeHaiku45},
		{name: "gemini uses first-party default", backing: ProviderGemini, expected: OpenCodeModelGemini3Flash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := DefaultOpenCodePhysicalModelForBacking(tt.backing)
			require.Equal(t, tt.expected, actual, "fallback default should be independent of picker ordering")
			require.True(t, IsKnownOpenCodePhysicalModel(actual), "fallback default should be a curated physical model")
		})
	}
}

func TestOpenCodePhysicalModelForBacking(t *testing.T) {
	t.Parallel()

	require.Equal(t, OpenCodeModelOpenRouterGLM52, OpenCodePhysicalModelForBacking("glm-5.2", ProviderOpenRouter),
		"logical id should map to the OpenRouter route physical id")
	require.Equal(t, OpenCodeModelGLM52, OpenCodePhysicalModelForBacking("glm-5.2", ProviderOpenCode),
		"logical id should map to the native route physical id for a native backing")
	require.Equal(t, OpenCodeModelOpenRouterGLM52, OpenCodePhysicalModelForBacking("glm-5.2", ProviderAnthropic),
		"unmatched backing should fall back to the first (OpenRouter) route")
	require.Equal(t, OpenCodeModelGLM52, OpenCodePhysicalModelForBacking(OpenCodeModelGLM52, ProviderOpenRouter),
		"an explicit physical id should pass through unchanged")
	require.Equal(t, "vendor/custom", OpenCodePhysicalModelForBacking("vendor/custom", ProviderOpenRouter),
		"an uncurated slug should pass through unchanged")
}

func TestOpenCodeRouteForPhysicalModelAndDisplayName(t *testing.T) {
	t.Parallel()

	logical, route, ok := OpenCodeRouteForPhysicalModel(OpenCodeModelOpenRouterGLM52)
	require.True(t, ok)
	require.Equal(t, "glm-5.2", logical.ID)
	require.Equal(t, ProviderOpenRouter, route.Backing)

	logical, route, ok = OpenCodeRouteForPhysicalModel("openrouter/~z-ai/glm-5.2")
	require.True(t, ok, "OpenCode CLI custom-model ids should resolve to their registry route")
	require.Equal(t, "glm-5.2", logical.ID)
	require.Equal(t, ProviderOpenRouter, route.Backing)
	require.True(t, IsKnownOpenCodePhysicalModel("openrouter/~z-ai/glm-5.2"), "OpenCode CLI custom-model ids should be recognized")
	require.Equal(t, []string{"deepinfra", "together", "fireworks"}, OpenCodeUSProviderList("openrouter/~z-ai/glm-5.2"), "CLI custom-model id should retain audited routing")

	require.Equal(t, "GLM 5.2", OpenCodeDisplayName("glm-5.2"), "logical id should resolve to its display name")
	require.Equal(t, "GLM 5.2", OpenCodeDisplayName(OpenCodeModelGLM52), "physical id should resolve to the owning model's display name")
	require.Equal(t, "GLM 5.2", OpenCodeDisplayName("openrouter/~z-ai/glm-5.2"), "CLI custom-model id should resolve to the owning model's display name")
	require.Equal(t, "vendor/custom", OpenCodeDisplayName("vendor/custom"), "uncurated slug should pass through")
}

// AgentTypeForModel must route OpenCode logical ids to OpenCode while leaving
// bare names owned by first-party agents with their existing owner.
func TestAgentTypeForModel_OpenCodeLogicalIDs(t *testing.T) {
	t.Parallel()

	require.Equal(t, AgentTypeOpenCode, AgentTypeForModel("glm-5.2"), "open-weight logical id routes to OpenCode")
	require.Equal(t, AgentTypeOpenCode, AgentTypeForModel("kimi-k2.6"))
	require.Equal(t, AgentTypeOpenCode, AgentTypeForModel("openrouter/~z-ai/glm-5.2"), "OpenCode CLI custom-model id routes to OpenCode")
	require.Equal(t, AgentTypeCodex, AgentTypeForModel("gpt-5.5"), "bare gpt-5.5 stays owned by Codex")
	require.Equal(t, AgentTypeClaudeCode, AgentTypeForModel("claude-fable-5"), "bare claude-fable-5 stays owned by Claude Code")
}

func TestValidateModelForAgentType_OpenCodeAcceptsLogicalIDs(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateModelForAgentType(AgentTypeOpenCode, "glm-5.2"), "logical id is valid")
	require.NoError(t, ValidateModelForAgentType(AgentTypeOpenCode, OpenCodeModelOpenRouterGLM52), "physical id is valid")
	require.NoError(t, ValidateModelForAgentType(AgentTypeOpenCode, "vendor/custom"), "custom provider/model slug is valid")
	err := ValidateModelForAgentType(AgentTypeOpenCode, "not-a-known-model")
	require.Error(t, err, "an unknown bare id must be rejected")
	require.True(t, strings.Contains(err.Error(), "model"), "error should mention model")
}
