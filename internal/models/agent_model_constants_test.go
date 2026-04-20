package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPMModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, PMModelSonnet, DefaultPMModel, "DefaultPMModel should use PMModelSonnet")

	// AvailablePMModels should include legacy aliases plus all provider models.
	expected := []string{PMModelOpus, PMModelSonnet, PMModelHaiku}
	expected = append(expected, AvailableClaudeCodeModels...)
	expected = append(expected, AvailableGeminiCLIModels...)
	expected = append(expected, AvailableCodexModels...)
	require.Equal(t, expected, AvailablePMModels, "AvailablePMModels should include legacy aliases and all provider models")
}

func TestClaudeCodeModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{ClaudeCodeModelOpus, ClaudeCodeModelSonnet46, ClaudeCodeModelSonnet, ClaudeCodeModelHaiku},
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
		[]string{CodexModelGPT54, CodexModelGPT54Mini, CodexModelGPT53Codex, CodexModelGPT52Codex, CodexModelGPT5Codex, CodexModelGPT53CodexSpark},
		AvailableCodexModels,
		"AvailableCodexModels should include the latest Codex model family",
	)
}

func TestLLMModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{
		"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5",
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
	require.Equal(t, []string{"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"}, byProvider["anthropic"])
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
		{name: "valid claude model", agentType: AgentTypeClaudeCode, model: ClaudeCodeModelSonnet},
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
				PMModel: PMModelSonnet,
				AgentConfig: AgentEnvConfig{
					"codex":       {"OPENAI_MODEL": CodexModelGPT53Codex},
					"claude_code": {"ANTHROPIC_MODEL": ClaudeCodeModelSonnet},
					"gemini_cli":  {"GEMINI_MODEL": GeminiCLIModelGemini31ProPreview},
				},
			},
		},
		{
			name: "accepts claude alias values",
			settings: OrgSettings{
				AgentConfig: AgentEnvConfig{
					"claude_code": {"ANTHROPIC_MODEL": PMModelOpus},
				},
			},
		},
		{
			name: "accepts claude code model as pm model",
			settings: OrgSettings{
				PMModel: ClaudeCodeModelSonnet,
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
					"amp": {"AMP_MODE": AmpModeDeep, "AMP_API_KEY": "amp_x"},
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
					"amp": {"AMP_API_KEY": "amp_x"},
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
