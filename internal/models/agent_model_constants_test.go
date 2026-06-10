package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPMModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, CodexModelGPT54, DefaultPMModel, "DefaultPMModel should use CodexModelGPT54")

	// AvailablePMModels mirrors the union of every coding agent's model list,
	// matching the session picker (frontend availableAgentModelGroups).
	var expected []string
	expected = append(expected, AvailableClaudeCodeModels...)
	expected = append(expected, AvailableGeminiCLIModels...)
	expected = append(expected, AvailableCodexModels...)
	expected = append(expected, AvailableAmpModes...)
	expected = append(expected, AvailablePiModels...)
	require.Equal(t, expected, AvailablePMModels, "AvailablePMModels should include every agent's models")
}

func TestClaudeCodeModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{ClaudeCodeModelFable5, ClaudeCodeModelOpus48, ClaudeCodeModelOpus47, ClaudeCodeModelOpus46, ClaudeCodeModelSonnet46, ClaudeCodeModelSonnet45, ClaudeCodeModelHaiku45},
		AvailableClaudeCodeModels,
		"AvailableClaudeCodeModels should be ordered by capability",
	)
}

func TestGeminiCLIModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{GeminiCLIModelGemini31ProPreview, GeminiCLIModelGemini3FlashPreview, GeminiCLIModelGemini25Pro, GeminiCLIModelGemini25Flash},
		AvailableGeminiCLIModels,
		"AvailableGeminiCLIModels should include current Gemini 3 and 2.5 options",
	)
}

func TestCodexModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{CodexModelGPT55, CodexModelGPT55Fast, CodexModelGPT54, CodexModelGPT54Fast, CodexModelGPT54Mini, CodexModelGPT53Codex, CodexModelGPT52Codex, CodexModelGPT5Codex, CodexModelGPT53CodexSpark},
		AvailableCodexModels,
		"AvailableCodexModels should include the latest Codex model family and fast tiers",
	)
}

func TestCodexRuntimeModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		model        string
		expected     string
		priorityTier bool
	}{
		{name: "gpt 5.5 fast maps to gpt 5.5 priority", model: CodexModelGPT55Fast, expected: CodexModelGPT55, priorityTier: true},
		{name: "gpt 5.4 fast maps to gpt 5.4 priority", model: CodexModelGPT54Fast, expected: CodexModelGPT54, priorityTier: true},
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
		{name: "valid gemini model", agentType: AgentTypeGeminiCLI, model: GeminiCLIModelGemini31ProPreview},
		{name: "valid amp mode", agentType: AgentTypeAmp, model: AmpModeSmart},
		{name: "valid pi model", agentType: AgentTypePi, model: PiModelClaudeSonnet46},
		{name: "pi accepts non-curated model", agentType: AgentTypePi, model: "moonshot/kimi-k2"},
		{name: "invalid codex model", agentType: AgentTypeCodex, model: "bad", wantErr: true},
		{name: "invalid claude model", agentType: AgentTypeClaudeCode, model: "bad", wantErr: true},
		{name: "invalid gemini model", agentType: AgentTypeGeminiCLI, model: "bad", wantErr: true},
		{name: "invalid amp mode", agentType: AgentTypeAmp, model: "turbo", wantErr: true},
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
	require.Equal(t, "GEMINI_MODEL", ModelEnvVarForAgentType(AgentTypeGeminiCLI))
	require.Equal(t, "AMP_MODE", ModelEnvVarForAgentType(AgentTypeAmp))
	require.Equal(t, "PI_MODEL", ModelEnvVarForAgentType(AgentTypePi))
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
	require.False(t, IsSupportedPiModel("moonshot/kimi-k2"), "non-curated Pi model should not be in the curated set")
	require.False(t, IsSupportedPiModel(""), "empty Pi model should be rejected")
}

func TestAgentTypeForModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model string
		want  AgentType
	}{
		{"", ""},
		{CodexModelGPT54, AgentTypeCodex},
		{ClaudeCodeModelOpus48, AgentTypeClaudeCode},
		{GeminiCLIModelGemini25Pro, AgentTypeGeminiCLI},
		{AmpModeSmart, AgentTypeAmp},
		// Curated Pi entry contains "/" — must resolve to Pi via the curated
		// lookup (not the slash heuristic) so we keep precedence stable if a
		// non-Pi agent later registers a slash-shaped model.
		{PiModelClaudeOpus48, AgentTypePi},
		// Slash heuristic only fires after every curated list misses.
		{"moonshot/kimi-k2", AgentTypePi},
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
	// Custom Pi provider/model — accepted with parity to ValidateModelForAgentType.
	require.NoError(t, ValidatePMModel("moonshot/kimi-k2"))

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
					"gemini_cli":  {"GEMINI_MODEL": GeminiCLIModelGemini31ProPreview},
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
			name: "accepts gemini model as pm model",
			settings: OrgSettings{
				PMModel: GeminiCLIModelGemini31ProPreview,
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
			name: "accepts custom pi provider/model as pm model (parity with sessions)",
			settings: OrgSettings{
				PMModel: "moonshot/kimi-k2",
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
			name: "rejects invalid gemini model",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"gemini_cli": {"GEMINI_MODEL": "invalid-model"},
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
					"pi": {"PI_MODEL": "moonshot/kimi-k2"},
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
						"PI_MODEL_CUSTOM": "moonshot/kimi-k2",
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
