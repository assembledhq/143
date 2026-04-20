package models

import (
	"fmt"
	"strings"
)

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

// Amp uses agent "modes" (not models) to select model + system prompt + tools.
// Values map directly to amp's --mode flag.
const (
	AmpModeSmart = "smart"
	AmpModeDeep  = "deep"
	AmpModeLarge = "large"
	AmpModeRush  = "rush"
)

var AvailableAmpModes = []string{AmpModeSmart, AmpModeDeep, AmpModeLarge, AmpModeRush}

// Pi accepts provider/model patterns. Curated short list; users with niche needs
// can override per-session via PI_MODEL_CUSTOM in agent_config.
const (
	PiModelClaudeSonnet46 = "anthropic/claude-sonnet-4-6"
	PiModelClaudeOpus46   = "anthropic/claude-opus-4-6"
	PiModelClaudeHaiku45  = "anthropic/claude-haiku-4-5"
	PiModelGPT54          = "openai/gpt-5.4"
	PiModelGemini25Pro    = "google/gemini-2.5-pro"
)

var AvailablePiModels = []string{
	PiModelClaudeSonnet46,
	PiModelClaudeOpus46,
	PiModelClaudeHaiku45,
	PiModelGPT54,
	PiModelGemini25Pro,
}

const (
	ClaudeCodeModelOpus     = "claude-opus-4-6"
	ClaudeCodeModelSonnet46 = "claude-sonnet-4-6"
	ClaudeCodeModelSonnet   = "claude-sonnet-4-5"
	ClaudeCodeModelHaiku    = "claude-haiku-4-5"
)

var AvailableClaudeCodeModels = []string{ClaudeCodeModelOpus, ClaudeCodeModelSonnet46, ClaudeCodeModelSonnet, ClaudeCodeModelHaiku}

const (
	GeminiCLIModelGemini31ProPreview  = "gemini-3.1-pro-preview"
	GeminiCLIModelGemini3FlashPreview = "gemini-3-flash-preview"
	GeminiCLIModelGemini25Pro         = "gemini-2.5-pro"
	GeminiCLIModelGemini25Flash       = "gemini-2.5-flash"
)

var AvailableGeminiCLIModels = []string{
	GeminiCLIModelGemini31ProPreview,
	GeminiCLIModelGemini3FlashPreview,
	GeminiCLIModelGemini25Pro,
	GeminiCLIModelGemini25Flash,
}

const (
	CodexModelGPT54           = "gpt-5.4"
	CodexModelGPT54Mini       = "gpt-5.4-mini"
	CodexModelGPT53Codex      = "gpt-5.3-codex"
	CodexModelGPT52Codex      = "gpt-5.2-codex"
	CodexModelGPT5Codex       = "gpt-5-codex"
	CodexModelGPT53CodexSpark = "gpt-5.3-codex-spark"
)

var AvailableCodexModels = []string{
	CodexModelGPT54,
	CodexModelGPT54Mini,
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

func IsSupportedAmpMode(mode string) bool {
	for _, supported := range AvailableAmpModes {
		if mode == supported {
			return true
		}
	}
	return false
}

// IsSupportedPiModel is permissive: Pi proxies to many providers and accepts
// arbitrary "provider/model" patterns. We validate against the curated list,
// but if the caller has set a PI_MODEL_CUSTOM override they bypass this check
// at the call site (see ValidateSettingsModels).
func IsSupportedPiModel(model string) bool {
	for _, supportedModel := range AvailablePiModels {
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
	case AgentTypeAmp:
		if !IsSupportedAmpMode(model) {
			return fmt.Errorf("amp mode must be one of: %v", AvailableAmpModes)
		}
	case AgentTypePi:
		// Pi proxies to many providers and accepts arbitrary "provider/model"
		// patterns. The curated AvailablePiModels list drives UI dropdowns, but
		// callers (session/project creation) may pass any value matching the
		// "provider/model" shape — this mirrors the PI_MODEL_CUSTOM bypass in
		// ValidateSettingsModels while still catching obvious typos (e.g.
		// "claude-sonnet-4-6" missing its provider prefix).
		if model == "" {
			return fmt.Errorf("model must be non-empty for agent type %s", AgentTypePi)
		}
		if !strings.Contains(model, "/") {
			return fmt.Errorf("pi model %q must be in the form \"provider/model\" (e.g. %s)", model, PiModelClaudeSonnet46)
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
	case AgentTypeAmp:
		return "AMP_MODE"
	case AgentTypePi:
		return "PI_MODEL"
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
// Models are listed most-capable → least-capable within each provider family.
var AvailableLLMModels = []string{
	"claude-opus-4-7",
	"claude-sonnet-4-6",
	"claude-haiku-4-5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.4-nano",
	"gemini-3.1-pro",
	"gemini-3-flash",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	// OpenRouter-exclusive open-weight models. Listed here so the backend
	// validator accepts them as llm_model values even though no native provider
	// hosts them.
	"qwen3-235b-a22b",
	"qwen3-32b",
}

// LLMModelsByProvider returns general-purpose LLM models grouped by provider.
// This is the canonical source of truth; the frontend fetches this via the
// GET /api/v1/settings/llm-models endpoint.
func LLMModelsByProvider() map[string][]string {
	return map[string][]string{
		"anthropic": {"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"},
		"openai":    {"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"},
		"gemini":    {"gemini-3.1-pro", "gemini-3-flash", "gemini-2.5-pro", "gemini-2.5-flash"},
		"openrouter": {
			"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5",
			"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano",
			"gemini-3.1-pro", "gemini-3-flash", "gemini-2.5-pro", "gemini-2.5-flash",
			// OpenRouter-exclusive Qwen models.
			"qwen3-235b-a22b", "qwen3-32b",
		},
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
		case AgentTypeAmp:
			mode := envVars["AMP_MODE"]
			if mode != "" && !IsSupportedAmpMode(mode) {
				return fmt.Errorf("agent_config.amp.AMP_MODE must be one of: %v", AvailableAmpModes)
			}
		case AgentTypePi:
			// Skip enum validation when PI_MODEL_CUSTOM is set; user is opting into Pi's full model catalog.
			if envVars["PI_MODEL_CUSTOM"] != "" {
				continue
			}
			model := envVars["PI_MODEL"]
			if model != "" && !IsSupportedPiModel(model) {
				return fmt.Errorf("agent_config.pi.PI_MODEL must be one of: %v (or set PI_MODEL_CUSTOM)", AvailablePiModels)
			}
		}
	}

	return nil
}
