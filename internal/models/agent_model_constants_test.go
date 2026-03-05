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
		[]string{ClaudeCodeModelOpus, ClaudeCodeModelSonnet, ClaudeCodeModelHaiku},
		AvailableClaudeCodeModels,
		"AvailableClaudeCodeModels should be ordered by capability",
	)
}

func TestGeminiCLIModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{GeminiCLIModelGemini3ProPreview, GeminiCLIModelGemini3FlashPreview, GeminiCLIModelGemini25Pro, GeminiCLIModelGemini25Flash},
		AvailableGeminiCLIModels,
		"AvailableGeminiCLIModels should include current Gemini 3 and 2.5 options",
	)
}

func TestCodexModelConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{CodexModelGPT53Codex, CodexModelGPT52Codex, CodexModelGPT5Codex, CodexModelGPT53CodexSpark},
		AvailableCodexModels,
		"AvailableCodexModels should include the latest Codex model family",
	)
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
					"gemini_cli":  {"GEMINI_MODEL": GeminiCLIModelGemini3ProPreview},
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
				PMModel: GeminiCLIModelGemini3ProPreview,
			},
		},
		{
			name: "accepts codex model as pm model",
			settings: OrgSettings{
				PMModel: CodexModelGPT53Codex,
			},
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
