package models

import (
	"fmt"
	"sort"
	"strings"
)

// AvailablePMModels is the union of every coding-agent's model list. It mirrors
// the model set the session picker offers (frontend lib/agents.ts AGENTS) so
// admins can pick any model the org could otherwise spin up a session with —
// including Amp modes ("smart"/"deep"/...) and Pi's curated provider/model
// strings.
var AvailablePMModels []string

func init() {
	AvailablePMModels = append(AvailablePMModels, AvailableClaudeCodeModels...)
	AvailablePMModels = append(AvailablePMModels, AvailableGeminiCLIModels...)
	AvailablePMModels = append(AvailablePMModels, AvailableCodexModels...)
	AvailablePMModels = append(AvailablePMModels, AvailableAmpModes...)
	AvailablePMModels = append(AvailablePMModels, AvailablePiModels...)
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
// can override per-session via PI_MODEL_CUSTOM in agent_config. Opus 4.7 is
// listed first because it's the current top model and doubles as the hardcoded
// default in piStreamingConfig.BuildCmd when no PI_MODEL is set.
const (
	PiModelClaudeOpus47   = "anthropic/claude-opus-4-7"
	PiModelClaudeSonnet46 = "anthropic/claude-sonnet-4-6"
	PiModelClaudeHaiku45  = "anthropic/claude-haiku-4-5"
	PiModelGPT54          = "openai/gpt-5.4"
	PiModelGemini25Pro    = "google/gemini-2.5-pro"
)

var AvailablePiModels = []string{
	PiModelClaudeOpus47,
	PiModelClaudeSonnet46,
	PiModelClaudeHaiku45,
	PiModelGPT54,
	PiModelGemini25Pro,
}

const (
	ClaudeCodeModelOpus47   = "claude-opus-4-7"
	ClaudeCodeModelOpus46   = "claude-opus-4-6"
	ClaudeCodeModelSonnet46 = "claude-sonnet-4-6"
	ClaudeCodeModelSonnet45 = "claude-sonnet-4-5"
	ClaudeCodeModelHaiku45  = "claude-haiku-4-5"
)

var AvailableClaudeCodeModels = []string{ClaudeCodeModelOpus47, ClaudeCodeModelOpus46, ClaudeCodeModelSonnet46, ClaudeCodeModelSonnet45, ClaudeCodeModelHaiku45}

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
	CodexModelGPT55           = "gpt-5.5"
	CodexModelGPT55Fast       = "gpt-5.5-fast"
	CodexModelGPT54           = "gpt-5.4"
	CodexModelGPT54Fast       = "gpt-5.4-fast"
	CodexModelGPT54Mini       = "gpt-5.4-mini"
	CodexModelGPT53Codex      = "gpt-5.3-codex"
	CodexModelGPT52Codex      = "gpt-5.2-codex"
	CodexModelGPT5Codex       = "gpt-5-codex"
	CodexModelGPT53CodexSpark = "gpt-5.3-codex-spark"
)

var AvailableCodexModels = []string{
	CodexModelGPT55,
	CodexModelGPT55Fast,
	CodexModelGPT54,
	CodexModelGPT54Fast,
	CodexModelGPT54Mini,
	CodexModelGPT53Codex,
	CodexModelGPT52Codex,
	CodexModelGPT5Codex,
	CodexModelGPT53CodexSpark,
}

// CodexRuntimeSpec is the resolved execution spec for a Codex model alias.
type CodexRuntimeSpec struct {
	// Model is the base model ID that Codex CLI accepts.
	Model string
	// PriorityTier indicates whether the priority service tier should be requested.
	PriorityTier bool
}

// CodexRuntimeModel translates 143's selectable fast aliases into the base
// model ID Codex CLI accepts plus a priority-service-tier flag.
func CodexRuntimeModel(model string) CodexRuntimeSpec {
	switch model {
	case CodexModelGPT55Fast:
		return CodexRuntimeSpec{Model: CodexModelGPT55, PriorityTier: true}
	case CodexModelGPT54Fast:
		return CodexRuntimeSpec{Model: CodexModelGPT54, PriorityTier: true}
	default:
		return CodexRuntimeSpec{Model: model}
	}
}

// AgentTypeForModel returns the AgentType whose curated model list contains
// the given model. The curated lookup runs first so an entry like
// "openai/gpt-5.4" resolves to AgentTypePi (its native registry) rather than
// being misread by the slash heuristic — only after every list misses do we
// fall back to AgentTypePi for unknown "provider/model"-shaped strings, since
// Pi accepts arbitrary provider/model overrides at run time. Returns an empty
// AgentType when no agent owns the model.
//
// Mirrors the frontend agentTypeForModel helper in lib/agents.ts so PM and
// session pickers route through the same agent-resolution rules.
func AgentTypeForModel(model string) AgentType {
	if model == "" {
		return ""
	}
	for _, m := range AvailableCodexModels {
		if m == model {
			return AgentTypeCodex
		}
	}
	for _, m := range AvailableClaudeCodeModels {
		if m == model {
			return AgentTypeClaudeCode
		}
	}
	for _, m := range AvailableGeminiCLIModels {
		if m == model {
			return AgentTypeGeminiCLI
		}
	}
	for _, m := range AvailableAmpModes {
		if m == model {
			return AgentTypeAmp
		}
	}
	for _, m := range AvailablePiModels {
		if m == model {
			return AgentTypePi
		}
	}
	if strings.Contains(model, "/") {
		return AgentTypePi
	}
	return ""
}

// ValidatePMModel validates a pm_model setting using the same rules as
// session model validation. It resolves the model's agent type via
// AgentTypeForModel and delegates to ValidateModelForAgentType, so PM
// accepts every model the session picker accepts — including Pi's
// arbitrary "provider/model" overrides.
func ValidatePMModel(model string) error {
	if model == "" {
		return nil
	}
	agentType := AgentTypeForModel(model)
	if agentType == "" {
		return fmt.Errorf("pm_model %q is not recognized — pick a model from any configured coding agent", model)
	}
	return ValidateModelForAgentType(agentType, model)
}

func IsSupportedClaudeCodeModel(model string) bool {
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

// IsSupportedPiModel reports whether a model is in the curated AvailablePiModels
// list. It is strict — use it to drive UI dropdowns and to validate settings
// writes. Pi itself accepts many more "provider/model" patterns; the permissive
// paths live at the call sites (ValidateSettingsModels skips this check when
// PI_MODEL_CUSTOM is set; ValidateModelForAgentType accepts any value matching
// the "provider/model" shape for per-session overrides).
func IsSupportedPiModel(model string) bool {
	for _, supportedModel := range AvailablePiModels {
		if model == supportedModel {
			return true
		}
	}
	return false
}

// AllowedAgentConfigKeys lists the env-var keys that may be set in
// settings.agent_config[<agent>] for agents whose non-secret defaults are
// propagated directly into the sandbox env (Amp, Pi). Bounds the blast radius
// of an org admin smuggling arbitrary vars (PATH, LD_PRELOAD, NODE_OPTIONS,
// …) into the container by way of agent_config.
//
// Scoped to Amp and Pi today — those are the only agent types whose
// agent_config the orchestrator injects (see applyAgentConfigOverrides).
// Other agents pull credentials from the encrypted credential store, so
// unknown agent_config keys for them are stored-but-ignored rather than
// security-relevant. Add an agent here when that changes.
var AllowedAgentConfigKeys = map[AgentType]map[string]struct{}{
	AgentTypeAmp: {
		"AMP_MODE": {},
	},
	AgentTypePi: {
		"PI_MODEL":        {},
		"PI_MODEL_CUSTOM": {},
	},
}

// sortedKeys returns the keys of a string-set in lexicographic order so the
// "allowed: [...]" hint in validation errors is stable across runs.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
			return fmt.Errorf("pi model %q must be in the form \"provider/model\" (e.g. %s)", model, PiModelClaudeOpus47)
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

// PlatformDefaultAllowedLLMModels lists, per provider, the models that an org
// may select while leaning on 143's platform-default API key (the keys 143
// ships from .env). Models outside this list require the org to bring its
// own API key. The cap is a cost guard: 143 pays for calls made via the
// default key, so heavier models are gated behind "bring your own key."
//
// Providers absent from this map are not capped on platform default.
// Keep in sync with PLATFORM_DEFAULT_ALLOWED_MODELS in
// frontend/src/lib/model-constants.ts.
var PlatformDefaultAllowedLLMModels = map[string][]string{
	"openai": {"gpt-5.4-mini", "gpt-5.4-nano"},
}

// ValidateLLMModelAccess rejects the model when a configured platform-default
// provider can serve it but caps the platform tier.
//
// `orgConfigured` and `platformAvailable` are sets of provider names (e.g.
// "openai", "anthropic") indicating, respectively, which providers the org
// has its own credential for and which providers have a platform-default key.
//
// Today app-level LLM calls are served by platform clients built from server
// env, not per-org credentials. `orgConfigured` is therefore intentionally not
// an unlock path here; it remains in the signature to keep callers explicit
// about the state they may have loaded and to make a future runtime change a
// focused update.
//
// Returns nil if platform providers can serve the model under the access rules.
// Returns an error when the model is supported but no configured runtime key path
// can currently serve it.
func ValidateLLMModelAccess(model string, _ map[string]bool, platformAvailable map[string]bool) error {
	if model == "" {
		return nil
	}

	byProvider := LLMModelsByProvider()
	cappedProvider := ""
	for provider, available := range platformAvailable {
		if !available || !providerHostsLLMModel(byProvider, provider, model) {
			continue
		}
		if allowed, ok := PlatformDefaultAllowedLLMModels[provider]; ok {
			for _, m := range allowed {
				if m == model {
					return nil
				}
			}
			cappedProvider = provider
			continue
		}
		return nil
	}
	if cappedProvider != "" {
		return fmt.Errorf("model %q requires a platform provider that allows it — 143's default %s key is capped at lower-cost models", model, cappedProvider)
	}
	for provider, models := range byProvider {
		for _, availableModel := range models {
			if availableModel == model {
				return fmt.Errorf("model %q requires a configured %s key", model, provider)
			}
		}
	}
	return fmt.Errorf("model %q is not supported", model)
}

func providerHostsLLMModel(byProvider map[string][]string, provider, model string) bool {
	for _, m := range byProvider[provider] {
		if m == model {
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
	if err := ValidatePMModel(settings.PMModel); err != nil {
		return err
	}

	for agentTypeStr, envVars := range settings.AgentConfig {
		agentType := AgentType(agentTypeStr)
		if allowed, gated := AllowedAgentConfigKeys[agentType]; gated {
			for key := range envVars {
				if _, ok := allowed[key]; !ok {
					return fmt.Errorf("agent_config.%s.%s is not an allowed key (allowed: %v)", agentTypeStr, key, sortedKeys(allowed))
				}
			}
		}
		switch agentType {
		case AgentTypeCodex:
			model := envVars["OPENAI_MODEL"]
			if model != "" && !IsSupportedCodexModel(model) {
				return fmt.Errorf("agent_config.codex.OPENAI_MODEL must be one of: %v", AvailableCodexModels)
			}
		case AgentTypeClaudeCode:
			model := envVars["ANTHROPIC_MODEL"]
			if model != "" && !IsSupportedClaudeCodeModel(model) {
				return fmt.Errorf("agent_config.claude_code.ANTHROPIC_MODEL must be one of: %v", AvailableClaudeCodeModels)
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
