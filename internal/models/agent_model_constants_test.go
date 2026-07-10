package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPMModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, DefaultCodexModel, DefaultPMModel, "DefaultPMModel should use the Codex default model")

	// AvailablePMModels mirrors the union of every coding agent's model list,
	// matching the session picker (frontend availableAgentModelGroups).
	var expected []string
	expected = append(expected, AvailableClaudeCodeModels...)
	expected = append(expected, AvailableCodexModels...)
	expected = append(expected, AvailableAmpModes...)
	expected = append(expected, AvailablePiModels...)
	expected = append(expected, AvailableOpenCodeModels...)
	require.Equal(t, expected, AvailablePMModels, "AvailablePMModels should include every agent's models")
}

func TestClaudeCodeModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, ClaudeCodeModelOpus48, DefaultClaudeCodeModel, "DefaultClaudeCodeModel should use Opus 4.8")
	require.Equal(t,
		[]string{ClaudeCodeModelFable5, ClaudeCodeModelOpus48, ClaudeCodeModelOpus47, ClaudeCodeModelOpus46, ClaudeCodeModelSonnet46, ClaudeCodeModelSonnet45, ClaudeCodeModelHaiku45},
		AvailableClaudeCodeModels,
		"AvailableClaudeCodeModels should be ordered by capability",
	)
}

func TestCodexModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, CodexModelGPT56Sol, DefaultCodexModel, "DefaultCodexModel should use GPT 5.6 Sol")
	require.Equal(t,
		[]string{
			CodexModelGPT56Sol,
			CodexModelGPT56SolFast,
			CodexModelGPT56Terra,
			CodexModelGPT56TerraFast,
			CodexModelGPT56Luna,
			CodexModelGPT56LunaFast,
			CodexModelGPT55,
			CodexModelGPT55Fast,
			CodexModelGPT54,
			CodexModelGPT54Fast,
			CodexModelGPT54Mini,
			CodexModelGPT53Codex,
			CodexModelGPT52Codex,
			CodexModelGPT5Codex,
			CodexModelGPT53CodexSpark,
		},
		AvailableCodexModels,
		"AvailableCodexModels should include the latest Codex model family and fast tiers",
	)
}

func TestOpenCodeModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{
			OpenCodeModelGLM52,
			OpenCodeModelOpenRouterGLM52,
			OpenCodeModelGLM51,
			OpenCodeModelOpenRouterGLM51,
			OpenCodeModelGPT55Pro,
			OpenCodeModelOpenRouterGPT55Pro,
			OpenCodeModelGPT55,
			OpenCodeModelOpenRouterGPT55,
			OpenCodeModelGPT54,
			OpenCodeModelGPT54Mini,
			OpenCodeModelGPT53CodexSpark,
			OpenCodeModelGPT52,
			OpenCodeModelOpenRouterGPT52,
			OpenCodeModelClaudeOpus48,
			OpenCodeModelClaudeOpus47,
			OpenCodeModelClaudeOpus46,
			OpenCodeModelClaudeSonnet46,
			OpenCodeModelClaudeHaiku45,
			OpenCodeModelGemini35Flash,
			OpenCodeModelOpenRouterGemini35Flash,
			OpenCodeModelGemini31Pro,
			OpenCodeModelOpenRouterGemini31Pro,
			OpenCodeModelGemini3Flash,
			OpenCodeModelMiniMaxM27,
			OpenCodeModelOpenRouterMiniMaxM27,
			OpenCodeModelMiniMaxM25,
			OpenCodeModelOpenRouterMiniMaxM25,
			OpenCodeModelDeepSeekV4Flash,
			OpenCodeModelOpenRouterDeepSeekV4Flash,
			OpenCodeModelDeepSeekV4Pro,
			OpenCodeModelOpenRouterDeepSeekV4Pro,
			OpenCodeModelKimiK26,
			OpenCodeModelOpenRouterKimiK26,
			OpenCodeModelKimiK25,
			OpenCodeModelOpenRouterKimiK25,
		},
		AvailableOpenCodeModels,
		"AvailableOpenCodeModels should track current Zen models and include audited OpenRouter counterparts",
	)
}

func TestOpenCodeModelConstants_OpenRouterCounterpartsForNativeOpenCodeModels(t *testing.T) {
	t.Parallel()

	counterparts := map[string]string{
		OpenCodeModelGemini35Flash:   OpenCodeModelOpenRouterGemini35Flash,
		OpenCodeModelMiniMaxM27:      OpenCodeModelOpenRouterMiniMaxM27,
		OpenCodeModelMiniMaxM25:      OpenCodeModelOpenRouterMiniMaxM25,
		OpenCodeModelDeepSeekV4Flash: OpenCodeModelOpenRouterDeepSeekV4Flash,
		OpenCodeModelDeepSeekV4Pro:   OpenCodeModelOpenRouterDeepSeekV4Pro,
		OpenCodeModelGLM52:           OpenCodeModelOpenRouterGLM52,
		OpenCodeModelGLM51:           OpenCodeModelOpenRouterGLM51,
		OpenCodeModelKimiK25:         OpenCodeModelOpenRouterKimiK25,
		OpenCodeModelGemini31Pro:     OpenCodeModelOpenRouterGemini31Pro,
		OpenCodeModelKimiK26:         OpenCodeModelOpenRouterKimiK26,
		OpenCodeModelGPT52:           OpenCodeModelOpenRouterGPT52,
		OpenCodeModelGPT55:           OpenCodeModelOpenRouterGPT55,
		OpenCodeModelGPT55Pro:        OpenCodeModelOpenRouterGPT55Pro,
	}

	for nativeModel, openRouterModel := range counterparts {
		nativeModel := nativeModel
		openRouterModel := openRouterModel
		t.Run(nativeModel, func(t *testing.T) {
			t.Parallel()

			require.Contains(t, AvailableOpenCodeModels, nativeModel, "native OpenCode model should remain curated")
			require.Contains(t, AvailableOpenCodeModels, openRouterModel, "native OpenCode model should have a curated audited OpenRouter counterpart")
		})
	}
}

func TestCodexRuntimeModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		model        string
		expected     string
		priorityTier bool
	}{
		{name: "gpt 5.6 sol fast maps to gpt 5.6 sol priority", model: CodexModelGPT56SolFast, expected: CodexModelGPT56Sol, priorityTier: true},
		{name: "gpt 5.6 terra fast maps to gpt 5.6 terra priority", model: CodexModelGPT56TerraFast, expected: CodexModelGPT56Terra, priorityTier: true},
		{name: "gpt 5.6 luna fast maps to gpt 5.6 luna priority", model: CodexModelGPT56LunaFast, expected: CodexModelGPT56Luna, priorityTier: true},
		{name: "gpt 5.5 fast maps to gpt 5.5 priority", model: CodexModelGPT55Fast, expected: CodexModelGPT55, priorityTier: true},
		{name: "gpt 5.4 fast maps to gpt 5.4 priority", model: CodexModelGPT54Fast, expected: CodexModelGPT54, priorityTier: true},
		{name: "regular gpt 5.6 sol stays unchanged", model: CodexModelGPT56Sol, expected: CodexModelGPT56Sol},
		{name: "regular gpt 5.5 stays unchanged", model: CodexModelGPT55, expected: CodexModelGPT55},
		{name: "unknown model stays unchanged", model: "custom-model", expected: "custom-model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := CodexRuntimeModel(tt.model)
			require.Equal(t, tt.expected, spec.Model, "CodexRuntimeModel should return the executable model id")
			require.Equal(t, tt.priorityTier, spec.PriorityTier, "CodexRuntimeModel should report whether priority service tier is required")
		})
	}
}

func TestLLMModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{
		"claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5",
		"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano",
		"gemini-3.1-pro", "gemini-3-flash", "gemini-2.5-pro", "gemini-2.5-flash",
		"qwen3-235b-a22b", "qwen3-32b",
	}, AvailableLLMModels, "AvailableLLMModels should contain all supported LLM models")
}

func TestLLMModelsByProvider(t *testing.T) {
	t.Parallel()

	byProvider := LLMModelsByProvider()
	require.Len(t, byProvider, 4, "should have 4 LLM providers (anthropic, openai, gemini, openrouter)")
	require.Contains(t, byProvider, "anthropic")
	require.Contains(t, byProvider, "openai")
	require.Contains(t, byProvider, "gemini")
	require.Contains(t, byProvider, "openrouter")
	require.Equal(t, []string{"claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"}, byProvider["anthropic"])
	require.Equal(t, []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"}, byProvider["openai"])
	require.Equal(t, []string{"gemini-3.1-pro", "gemini-3-flash", "gemini-2.5-pro", "gemini-2.5-flash"}, byProvider["gemini"])
	require.Contains(t, byProvider["openrouter"], "gemini-3.1-pro", "openrouter should proxy the latest gemini models too")
	// OpenRouter exclusively carries the Qwen models — they must appear there
	// and nowhere else.
	require.Contains(t, byProvider["openrouter"], "qwen3-235b-a22b")
	require.Contains(t, byProvider["openrouter"], "qwen3-32b")
	require.NotContains(t, byProvider["anthropic"], "qwen3-235b-a22b")
	require.NotContains(t, byProvider["openai"], "qwen3-235b-a22b")
	require.NotContains(t, byProvider["gemini"], "qwen3-235b-a22b")
}

// TestLLMProvidersHaveModels guards against drift between the LLMProviders
// slice and LLMModelsByProvider: every LLM provider must have at least one
// general-purpose model available in the dropdown.
func TestLLMProvidersHaveModels(t *testing.T) {
	t.Parallel()

	byProvider := LLMModelsByProvider()
	for _, p := range LLMProviders {
		models, ok := byProvider[string(p)]
		require.Truef(t, ok, "LLM provider %q must be present in LLMModelsByProvider", p)
		require.NotEmptyf(t, models, "LLM provider %q must have at least one model", p)
	}
}

func TestIsSupportedLLMModel(t *testing.T) {
	t.Parallel()

	require.True(t, IsSupportedLLMModel("claude-sonnet-4-6"), "should accept valid LLM model")
	require.True(t, IsSupportedLLMModel("gpt-5.4-mini"), "should accept valid OpenAI model")
	require.False(t, IsSupportedLLMModel("invalid-model"), "should reject invalid model")
	require.False(t, IsSupportedLLMModel(""), "should reject empty string")
}

func TestValidateModelForAgentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType AgentType
		model     string
		wantErr   bool
	}{
		{name: "valid codex model", agentType: AgentTypeCodex, model: CodexModelGPT53Codex},
		{name: "valid claude model", agentType: AgentTypeClaudeCode, model: ClaudeCodeModelSonnet45},
		{name: "valid amp mode", agentType: AgentTypeAmp, model: AmpModeSmart},
		{name: "valid pi model", agentType: AgentTypePi, model: PiModelClaudeSonnet46},
		{name: "valid opencode curated model", agentType: AgentTypeOpenCode, model: OpenCodeModelGPT54Mini},
		{name: "opencode accepts non-curated provider model", agentType: AgentTypeOpenCode, model: "openrouter/meta-llama/llama-3.3-70b-instruct"},
		{name: "pi accepts non-curated model", agentType: AgentTypePi, model: "xai/grok-code-fast"},
		{name: "invalid codex model", agentType: AgentTypeCodex, model: "bad", wantErr: true},
		{name: "invalid claude model", agentType: AgentTypeClaudeCode, model: "bad", wantErr: true},
		{name: "invalid amp mode", agentType: AgentTypeAmp, model: "turbo", wantErr: true},
		{name: "empty opencode model rejected", agentType: AgentTypeOpenCode, model: "", wantErr: true},
		{name: "opencode model missing provider prefix rejected", agentType: AgentTypeOpenCode, model: "claude-sonnet-4-6", wantErr: true},
		{name: "empty pi model rejected", agentType: AgentTypePi, model: "", wantErr: true},
		{name: "pi model missing provider prefix rejected", agentType: AgentTypePi, model: "claude-sonnet-4-6", wantErr: true},
		{name: "unknown agent type", agentType: "unknown", model: "any", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateModelForAgentType(tt.agentType, tt.model)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestModelEnvVarForAgentType(t *testing.T) {
	t.Parallel()

	require.Equal(t, "OPENAI_MODEL", ModelEnvVarForAgentType(AgentTypeCodex))
	require.Equal(t, "ANTHROPIC_MODEL", ModelEnvVarForAgentType(AgentTypeClaudeCode))
	require.Equal(t, "AMP_MODE", ModelEnvVarForAgentType(AgentTypeAmp))
	require.Equal(t, "PI_MODEL", ModelEnvVarForAgentType(AgentTypePi))
	require.Equal(t, "OPENCODE_MODEL", ModelEnvVarForAgentType(AgentTypeOpenCode))
	require.Equal(t, "", ModelEnvVarForAgentType("unknown"))
}

func TestIsSupportedAmpMode(t *testing.T) {
	t.Parallel()

	for _, mode := range AvailableAmpModes {
		require.True(t, IsSupportedAmpMode(mode), "expected %q to be a valid amp mode", mode)
	}
	require.False(t, IsSupportedAmpMode("turbo"), "unknown amp mode should be rejected")
	require.False(t, IsSupportedAmpMode(""), "empty amp mode should be rejected")
}

func TestIsSupportedPiModel(t *testing.T) {
	t.Parallel()

	for _, model := range AvailablePiModels {
		require.True(t, IsSupportedPiModel(model), "expected %q to be a curated Pi model", model)
	}
	require.False(t, IsSupportedPiModel("xai/grok-code-fast"), "non-curated Pi model should not be in the curated set")
	require.False(t, IsSupportedPiModel(""), "empty Pi model should be rejected")
}

func TestAgentTypeForModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model string
		want  AgentType
	}{
		{"", ""},
		{CodexModelGPT56Sol, AgentTypeCodex},
		{CodexModelGPT56LunaFast, AgentTypeCodex},
		{CodexModelGPT54, AgentTypeCodex},
		{ClaudeCodeModelOpus48, AgentTypeClaudeCode},
		{AmpModeSmart, AgentTypeAmp},
		{OpenCodeModelGPT54Mini, AgentTypeOpenCode},
		// OpenCode registers provider/model IDs explicitly, including some
		// provider models that Pi can also run. Prefer the first-class OpenCode
		// mapping for curated overlaps; the slash heuristic below still routes
		// unknown provider/model strings to Pi.
		{OpenCodeModelClaudeHaiku45, AgentTypeOpenCode},
		{PiModelClaudeOpus48, AgentTypeOpenCode},
		// Slash heuristic only fires after every curated list misses.
		{"xai/grok-code-fast", AgentTypePi},
		{"unknown-model", ""},
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, AgentTypeForModel(tc.model), "AgentTypeForModel(%q)", tc.model)
	}
}

func TestValidatePMModel(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidatePMModel(""), "empty pm_model is allowed (caller falls back to default)")
	require.NoError(t, ValidatePMModel(CodexModelGPT54))
	require.NoError(t, ValidatePMModel(ClaudeCodeModelOpus48))
	require.NoError(t, ValidatePMModel(AmpModeSmart))
	require.NoError(t, ValidatePMModel(PiModelClaudeOpus48))
	require.NoError(t, ValidatePMModel(OpenCodeModelGPT54Mini))
	// Custom Pi provider/model — accepted with parity to ValidateModelForAgentType.
	require.NoError(t, ValidatePMModel("xai/grok-code-fast"))

	err := ValidatePMModel("not-a-model")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"not-a-model"`)
}

func TestValidateSettingsModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings OrgSettings
		wantErr  bool
	}{
		{
			name: "accepts valid pm and agent models",
			settings: OrgSettings{
				PMModel: ClaudeCodeModelSonnet45,
				AgentConfig: AgentEnvConfig{
					"codex":       {"OPENAI_MODEL": CodexModelGPT53Codex},
					"claude_code": {"ANTHROPIC_MODEL": ClaudeCodeModelSonnet45},
				},
			},
		},
		{
			name: "accepts claude code model as pm model",
			settings: OrgSettings{
				PMModel: ClaudeCodeModelSonnet45,
			},
		},
		{
			name: "accepts codex model as pm model",
			settings: OrgSettings{
				PMModel: CodexModelGPT53Codex,
			},
		},
		{
			name: "accepts amp mode as pm model",
			settings: OrgSettings{
				PMModel: AmpModeSmart,
			},
		},
		{
			name: "accepts pi model as pm model",
			settings: OrgSettings{
				PMModel: PiModelClaudeOpus48,
			},
		},
		{
			name: "accepts opencode model as pm model",
			settings: OrgSettings{
				PMModel: OpenCodeModelGPT54Mini,
			},
		},
		{
			name: "accepts custom pi provider/model as pm model (parity with sessions)",
			settings: OrgSettings{
				PMModel: "xai/grok-code-fast",
			},
		},
		{
			name: "accepts preview capacity within bounds",
			settings: OrgSettings{
				PreviewMaxPreviewsPerUser: 4,
			},
		},
		{
			name: "accepts valid llm model",
			settings: OrgSettings{
				LLMModel: "gpt-5.4-mini",
			},
		},
		{
			name: "accepts valid reasoning effort",
			settings: OrgSettings{
				LLMReasoningEffort: ReasoningEffortLow,
			},
		},
		{
			name: "rejects invalid reasoning effort",
			settings: OrgSettings{
				LLMReasoningEffort: "invalid",
			},
			wantErr: true,
		},
		{
			name: "rejects invalid llm model",
			settings: OrgSettings{
				LLMModel: "invalid-model",
			},
			wantErr: true,
		},
		{
			name: "rejects invalid pm model",
			settings: OrgSettings{
				PMModel: "invalid-model",
			},
			wantErr: true,
		},
		{
			name: "rejects preview capacity below minimum",
			settings: OrgSettings{
				PreviewMaxPreviewsPerUser: -1,
			},
			wantErr: true,
		},
		{
			name: "rejects preview capacity above maximum",
			settings: OrgSettings{
				PreviewMaxPreviewsPerUser: 999,
			},
			wantErr: true,
		},
		{
			name: "rejects preview CPU cap above platform maximum",
			settings: OrgSettings{
				SandboxResources: SandboxResourceSettings{PreviewMaxCPUMillis: MaxPreviewMaxCPUMillis + 1},
			},
			wantErr: true,
		},
		{
			name: "rejects preview memory cap above platform maximum",
			settings: OrgSettings{
				SandboxResources: SandboxResourceSettings{PreviewMaxMemoryMiB: MaxPreviewMaxMemoryMiB + 1},
			},
			wantErr: true,
		},
		{
			name: "rejects preview disk cap above platform maximum",
			settings: OrgSettings{
				SandboxResources: SandboxResourceSettings{PreviewMaxEphemeralDiskMiB: MaxPreviewMaxEphemeralDiskMiB + 1},
			},
			wantErr: true,
		},
		{
			name: "rejects invalid codex model",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"codex": {"OPENAI_MODEL": "invalid-model"},
				},
			},
			wantErr: true,
		},
		{
			name: "rejects invalid claude model",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"claude_code": {"ANTHROPIC_MODEL": "invalid-model"},
				},
			},
			wantErr: true,
		},
		{
			name: "accepts valid amp mode",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"amp": {"AMP_MODE": AmpModeDeep},
				},
			},
		},
		{
			name: "rejects invalid amp mode",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"amp": {"AMP_MODE": "turbo"},
				},
			},
			wantErr: true,
		},
		{
			name: "accepts empty amp mode",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"amp": {},
				},
			},
		},
		{
			name: "accepts valid pi model",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"pi": {"PI_MODEL": PiModelClaudeSonnet46},
				},
			},
		},
		{
			name: "rejects invalid pi model without override",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"pi": {"PI_MODEL": "xai/grok-code-fast"},
				},
			},
			wantErr: true,
		},
		{
			name: "pi_model_custom bypasses enum check",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"pi": {
						"PI_MODEL":        "not-in-the-curated-list",
						"PI_MODEL_CUSTOM": "xai/grok-code-fast",
					},
				},
			},
		},
		{
			name: "accepts valid opencode model",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"opencode": {"OPENCODE_MODEL": OpenCodeModelClaudeHaiku45},
				},
			},
		},
		{
			name: "rejects invalid opencode model without override",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"opencode": {"OPENCODE_MODEL": "not-a-provider-model"},
				},
			},
			wantErr: true,
		},
		{
			name: "opencode_model_custom bypasses curated list check for provider model",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"opencode": {
						"OPENCODE_MODEL":        "not-in-the-curated-list",
						"OPENCODE_MODEL_CUSTOM": "xai/grok-code-fast",
					},
				},
			},
		},
		{
			// Allowlist guard: org admin tries to inject PATH via agent_config.amp.
			// Without the AllowedAgentConfigKeys check, applyAgentConfigOverrides
			// would happily propagate this into the sandbox env.
			name: "rejects unknown amp key",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"amp": {"AMP_MODE": AmpModeDeep, "PATH": "/evil/bin"},
				},
			},
			wantErr: true,
		},
		{
			name: "rejects unknown pi key",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"pi": {"PI_MODEL": PiModelClaudeSonnet46, "LD_PRELOAD": "/tmp/x.so"},
				},
			},
			wantErr: true,
		},
		{
			name: "rejects unknown opencode key",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"opencode": {"OPENCODE_MODEL": OpenCodeModelGPT54Mini, "NODE_OPTIONS": "--require /tmp/x.js"},
				},
			},
			wantErr: true,
		},
		{
			// Sanity check: legacy agents are not gated by the allowlist (their
			// agent_config is stored-but-not-injected by the orchestrator), so
			// unknown keys there must keep validating.
			name: "accepts unknown key on non-allowlisted agent",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"codex": {"OPENAI_BASE_URL": "https://proxy.example/v1"},
				},
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateSettingsModels(testCase.settings)
			if testCase.wantErr {
				require.Error(t, err, "ValidateSettingsModels should return an error for invalid models")
				return
			}
			require.NoError(t, err, "ValidateSettingsModels should accept supported models")
		})
	}
}

func TestValidateLLMModelAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		model             string
		orgConfigured     map[string]bool
		platformAvailable map[string]bool
		wantErr           bool
	}{
		{
			name:  "empty model is always allowed",
			model: "",
		},
		{
			name:              "org openai key does not unlock gpt-5.4 while runtime uses platform openai",
			model:             "gpt-5.4",
			orgConfigured:     map[string]bool{"openai": true},
			platformAvailable: map[string]bool{"openai": true},
			wantErr:           true,
		},
		{
			name:              "platform default openai allows gpt-5.4-mini",
			model:             "gpt-5.4-mini",
			platformAvailable: map[string]bool{"openai": true},
		},
		{
			name:              "platform default openai allows gpt-5.4-nano",
			model:             "gpt-5.4-nano",
			platformAvailable: map[string]bool{"openai": true},
		},
		{
			name:              "platform default openai blocks gpt-5.4 (cost cap)",
			model:             "gpt-5.4",
			platformAvailable: map[string]bool{"openai": true},
			wantErr:           true,
		},
		{
			name:              "org openai key still leaves gpt-5.4 capped when platform openai exists",
			model:             "gpt-5.4",
			orgConfigured:     map[string]bool{"openai": true},
			platformAvailable: map[string]bool{"openai": true},
			wantErr:           true,
		},
		{
			// gpt-5.4 is also served by openrouter, but the current runtime
			// prefers platform OpenAI before OpenRouter. Until runtime uses the
			// selected org credential, OpenRouter must not bypass the OpenAI cap.
			name:              "openrouter org credential does not bypass openai platform cap",
			model:             "gpt-5.4",
			orgConfigured:     map[string]bool{"openrouter": true},
			platformAvailable: map[string]bool{"openai": true},
			wantErr:           true,
		},
		{
			name:              "platform openrouter alone can serve gpt-5.4",
			model:             "gpt-5.4",
			platformAvailable: map[string]bool{"openrouter": true},
		},
		{
			// No restriction map for anthropic, so platform default = full catalog.
			name:              "anthropic platform default allows claude-opus-4-7",
			model:             "claude-opus-4-7",
			platformAvailable: map[string]bool{"anthropic": true},
		},
		{
			name:    "no key path rejects otherwise supported models",
			model:   "gpt-5.4-mini",
			wantErr: true,
		},
		{
			name:    "no key path rejects non-openai models too",
			model:   "claude-sonnet-4-6",
			wantErr: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateLLMModelAccess(testCase.model, testCase.orgConfigured, testCase.platformAvailable)
			if testCase.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
