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

func TestOpenCodeModelRegistry_DefaultModelResolves(t *testing.T) {
	t.Parallel()

	m, ok := LookupOpenCodeModel(DefaultOpenCodeModel)
	require.True(t, ok, "DefaultOpenCodeModel must be a registry logical model")
	require.Equal(t, ProviderOpenRouter, m.Routes[0].Backing, "default model should prefer OpenRouter")
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

	require.Equal(t, "GLM 5.2", OpenCodeDisplayName("glm-5.2"), "logical id should resolve to its display name")
	require.Equal(t, "GLM 5.2", OpenCodeDisplayName(OpenCodeModelGLM52), "physical id should resolve to the owning model's display name")
	require.Equal(t, "vendor/custom", OpenCodeDisplayName("vendor/custom"), "uncurated slug should pass through")
}

// AgentTypeForModel must route OpenCode logical ids to OpenCode while leaving
// bare names owned by first-party agents with their existing owner.
func TestAgentTypeForModel_OpenCodeLogicalIDs(t *testing.T) {
	t.Parallel()

	require.Equal(t, AgentTypeOpenCode, AgentTypeForModel("glm-5.2"), "open-weight logical id routes to OpenCode")
	require.Equal(t, AgentTypeOpenCode, AgentTypeForModel("kimi-k2.6"))
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
