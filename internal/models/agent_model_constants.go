package models

import "fmt"

const (
	PMModelOpus   = "opus"
	PMModelSonnet = "sonnet"
	PMModelHaiku  = "haiku"
)

var AvailablePMModels = []string{PMModelOpus, PMModelSonnet, PMModelHaiku}

const (
	ClaudeCodeModelOpus   = "claude-opus-4-6"
	ClaudeCodeModelSonnet = "claude-sonnet-4-5"
	ClaudeCodeModelHaiku  = "claude-haiku-4-5"
)

var AvailableClaudeCodeModels = []string{ClaudeCodeModelOpus, ClaudeCodeModelSonnet, ClaudeCodeModelHaiku}

const (
	GeminiCLIModelGemini3ProPreview   = "gemini-3-pro-preview"
	GeminiCLIModelGemini3FlashPreview = "gemini-3-flash-preview"
	GeminiCLIModelGemini25Pro         = "gemini-2.5-pro"
	GeminiCLIModelGemini25Flash       = "gemini-2.5-flash"
)

var AvailableGeminiCLIModels = []string{
	GeminiCLIModelGemini3ProPreview,
	GeminiCLIModelGemini3FlashPreview,
	GeminiCLIModelGemini25Pro,
	GeminiCLIModelGemini25Flash,
}

const (
	CodexModelGPT53Codex      = "gpt-5.3-codex"
	CodexModelGPT52Codex      = "gpt-5.2-codex"
	CodexModelGPT5Codex       = "gpt-5-codex"
	CodexModelGPT53CodexSpark = "gpt-5.3-codex-spark"
)

var AvailableCodexModels = []string{
	CodexModelGPT53Codex,
	CodexModelGPT52Codex,
	CodexModelGPT5Codex,
	CodexModelGPT53CodexSpark,
}

func IsSupportedPMModel(model string) bool {
	for _, supportedModel := range AvailablePMModels {
		if model == supportedModel {
			return true
		}
	}
	return false
}

func IsSupportedClaudeCodeModel(model string) bool {
	if IsSupportedPMModel(model) {
		return true
	}
	for _, supportedModel := range AvailableClaudeCodeModels {
		if model == supportedModel {
			return true
		}
	}
	return false
}

func IsSupportedGeminiCLIModel(model string) bool {
	for _, supportedModel := range AvailableGeminiCLIModels {
		if model == supportedModel {
			return true
		}
	}
	return false
}

func IsSupportedCodexModel(model string) bool {
	for _, supportedModel := range AvailableCodexModels {
		if model == supportedModel {
			return true
		}
	}
	return false
}

func ValidateSettingsModels(settings OrgSettings) error {
	if settings.PMModel != "" && !IsSupportedPMModel(settings.PMModel) {
		return fmt.Errorf("pm_model must be one of: %v", AvailablePMModels)
	}

	for agentType, envVars := range settings.AgentConfig {
		switch agentType {
		case "codex":
			model := envVars["OPENAI_MODEL"]
			if model != "" && !IsSupportedCodexModel(model) {
				return fmt.Errorf("agent_config.codex.OPENAI_MODEL must be one of: %v", AvailableCodexModels)
			}
		case "claude_code":
			model := envVars["ANTHROPIC_MODEL"]
			if model != "" && !IsSupportedClaudeCodeModel(model) {
				return fmt.Errorf("agent_config.claude_code.ANTHROPIC_MODEL must be one of: %v or %v", AvailablePMModels, AvailableClaudeCodeModels)
			}
		case "gemini_cli":
			model := envVars["GEMINI_MODEL"]
			if model != "" && !IsSupportedGeminiCLIModel(model) {
				return fmt.Errorf("agent_config.gemini_cli.GEMINI_MODEL must be one of: %v", AvailableGeminiCLIModels)
			}
		}
	}

	return nil
}
