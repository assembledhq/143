package models

import "fmt"

// Legacy PM model aliases (kept for backward compatibility).
const (
	PMModelOpus   = "opus"
	PMModelSonnet = "sonnet"
	PMModelHaiku  = "haiku"
)

var legacyPMAliases = []string{PMModelOpus, PMModelSonnet, PMModelHaiku}

// AvailablePMModels includes all models from every provider plus legacy aliases.
// The PM agent can use any model from any configured provider.
var AvailablePMModels []string

func init() {
	AvailablePMModels = append(AvailablePMModels, legacyPMAliases...)
	AvailablePMModels = append(AvailablePMModels, AvailableClaudeCodeModels...)
	AvailablePMModels = append(AvailablePMModels, AvailableGeminiCLIModels...)
	AvailablePMModels = append(AvailablePMModels, AvailableCodexModels...)
}

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
	// Accept legacy PM aliases (opus, sonnet, haiku) for Claude Code.
	for _, alias := range legacyPMAliases {
		if model == alias {
			return true
		}
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

// ValidateModelForAgentType checks whether the given model is valid for the given agent type.
func ValidateModelForAgentType(agentType, model string) error {
	switch agentType {
	case "codex":
		if !IsSupportedCodexModel(model) {
			return fmt.Errorf("model must be one of: %v", AvailableCodexModels)
		}
	case "claude_code":
		if !IsSupportedClaudeCodeModel(model) {
			return fmt.Errorf("model must be one of: %v", AvailableClaudeCodeModels)
		}
	case "gemini_cli":
		if !IsSupportedGeminiCLIModel(model) {
			return fmt.Errorf("model must be one of: %v", AvailableGeminiCLIModels)
		}
	default:
		return fmt.Errorf("unknown agent type: %s", agentType)
	}
	return nil
}

// ModelEnvVarForAgentType returns the environment variable name used to set the model
// for the given agent type.
func ModelEnvVarForAgentType(agentType string) string {
	switch agentType {
	case "codex":
		return "OPENAI_MODEL"
	case "claude_code":
		return "ANTHROPIC_MODEL"
	case "gemini_cli":
		return "GEMINI_MODEL"
	default:
		return ""
	}
}

// AvailableLLMModels lists all models supported by the general-purpose LLM system.
// Keep in sync with frontend/src/lib/model-constants.ts (LLM_MODELS_BY_PROVIDER).
var AvailableLLMModels = []string{
	"claude-opus-4-6",
	"claude-sonnet-4-5",
	"claude-haiku-4-5",
	"gpt-4o",
	"gpt-4o-mini",
	"o3-mini",
}

// LLMModelsByProvider returns general-purpose LLM models grouped by provider.
// This is the canonical source of truth; the frontend fetches this via the
// GET /api/v1/settings/llm-models endpoint.
func LLMModelsByProvider() map[string][]string {
	return map[string][]string{
		"anthropic":  {"claude-opus-4-6", "claude-sonnet-4-5", "claude-haiku-4-5"},
		"openai":     {"gpt-4o", "gpt-4o-mini", "o3-mini"},
		"openrouter": {"claude-opus-4-6", "claude-sonnet-4-5", "claude-haiku-4-5", "gpt-4o", "gpt-4o-mini", "o3-mini"},
	}
}

func IsSupportedLLMModel(model string) bool {
	for _, m := range AvailableLLMModels {
		if model == m {
			return true
		}
	}
	return false
}

func ValidateSettingsModels(settings OrgSettings) error {
	if settings.LLMModel != "" && !IsSupportedLLMModel(settings.LLMModel) {
		return fmt.Errorf("llm_model must be one of: %v", AvailableLLMModels)
	}
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
				return fmt.Errorf("agent_config.claude_code.ANTHROPIC_MODEL must be one of: %v or %v", legacyPMAliases, AvailableClaudeCodeModels)
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
