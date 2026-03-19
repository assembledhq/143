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
func ValidateModelForAgentType(agentType AgentType, model string) error {
	switch agentType {
	case AgentTypeCodex:
		if !IsSupportedCodexModel(model) {
			return fmt.Errorf("model must be one of: %v", AvailableCodexModels)
		}
	case AgentTypeClaudeCode:
		if !IsSupportedClaudeCodeModel(model) {
			return fmt.Errorf("model must be one of: %v", AvailableClaudeCodeModels)
		}
	case AgentTypeGeminiCLI:
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
func ModelEnvVarForAgentType(agentType AgentType) string {
	switch agentType {
	case AgentTypeCodex:
		return "OPENAI_MODEL"
	case AgentTypeClaudeCode:
		return "ANTHROPIC_MODEL"
	case AgentTypeGeminiCLI:
		return "GEMINI_MODEL"
	default:
		return ""
	}
}

// ModelName is a user-facing model identifier (e.g., "claude-sonnet-4-5", "gpt-4o").
// The LLM registry maps these to provider-specific model IDs.
type ModelName string

// DefaultLLMModel is the server-side default when no LLM_MODEL env var or
// org setting is configured. Keep in sync with DEFAULT_LLM_MODEL in
// frontend/src/lib/model-constants.ts.
const DefaultLLMModel = "gpt-5.4-mini"

// AvailableLLMModels lists all models supported by the general-purpose LLM system.
// Keep in sync with frontend/src/lib/model-constants.ts (LLM_MODELS_BY_PROVIDER).
var AvailableLLMModels = []string{
	"claude-opus-4-6",
	"claude-sonnet-4-5",
	"claude-haiku-4-5",
	"gpt-4o",
	"gpt-4o-mini",
	"gpt-5.4-mini",
	"gpt-5-nano",
	"o3-mini",
}

// LLMModelsByProvider returns general-purpose LLM models grouped by provider.
// This is the canonical source of truth; the frontend fetches this via the
// GET /api/v1/settings/llm-models endpoint.
func LLMModelsByProvider() map[string][]string {
	return map[string][]string{
		"anthropic":  {"claude-opus-4-6", "claude-sonnet-4-5", "claude-haiku-4-5"},
		"openai":     {"gpt-4o", "gpt-4o-mini", "gpt-5.4-mini", "gpt-5-nano", "o3-mini"},
		"openrouter": {"claude-opus-4-6", "claude-sonnet-4-5", "claude-haiku-4-5", "gpt-4o", "gpt-4o-mini", "gpt-5.4-mini", "gpt-5-nano", "o3-mini"},
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
	if err := settings.LLMReasoningEffort.Validate(); err != nil {
		return err
	}
	if settings.PMModel != "" && !IsSupportedPMModel(settings.PMModel) {
		return fmt.Errorf("pm_model must be one of: %v", AvailablePMModels)
	}

	for agentTypeStr, envVars := range settings.AgentConfig {
		switch AgentType(agentTypeStr) {
		case AgentTypeCodex:
			model := envVars["OPENAI_MODEL"]
			if model != "" && !IsSupportedCodexModel(model) {
				return fmt.Errorf("agent_config.codex.OPENAI_MODEL must be one of: %v", AvailableCodexModels)
			}
		case AgentTypeClaudeCode:
			model := envVars["ANTHROPIC_MODEL"]
			if model != "" && !IsSupportedClaudeCodeModel(model) {
				return fmt.Errorf("agent_config.claude_code.ANTHROPIC_MODEL must be one of: %v or %v", legacyPMAliases, AvailableClaudeCodeModels)
			}
		case AgentTypeGeminiCLI:
			model := envVars["GEMINI_MODEL"]
			if model != "" && !IsSupportedGeminiCLIModel(model) {
				return fmt.Errorf("agent_config.gemini_cli.GEMINI_MODEL must be one of: %v", AvailableGeminiCLIModels)
			}
		}
	}

	return nil
}
