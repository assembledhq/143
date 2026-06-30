export const CLAUDE_CODE_MODEL_FABLE_5 = "claude-fable-5";
export const CLAUDE_CODE_MODEL_OPUS_48 = "claude-opus-4-8";
export const CLAUDE_CODE_MODEL_OPUS_47 = "claude-opus-4-7";
export const CLAUDE_CODE_MODEL_OPUS_46 = "claude-opus-4-6";
export const CLAUDE_CODE_MODEL_SONNET_46 = "claude-sonnet-4-6";
export const CLAUDE_CODE_MODEL_SONNET_45 = "claude-sonnet-4-5";
export const CLAUDE_CODE_MODEL_HAIKU_45 = "claude-haiku-4-5";

export const AVAILABLE_CLAUDE_CODE_MODELS = [
  CLAUDE_CODE_MODEL_FABLE_5,
  CLAUDE_CODE_MODEL_OPUS_48,
  CLAUDE_CODE_MODEL_OPUS_47,
  CLAUDE_CODE_MODEL_OPUS_46,
  CLAUDE_CODE_MODEL_SONNET_46,
  CLAUDE_CODE_MODEL_SONNET_45,
  CLAUDE_CODE_MODEL_HAIKU_45,
] as const;

export const DEFAULT_CLAUDE_CODE_MODEL = CLAUDE_CODE_MODEL_OPUS_48;

export const CODEX_MODEL_GPT_5_5 = "gpt-5.5";
export const CODEX_MODEL_GPT_5_5_FAST = "gpt-5.5-fast";
export const CODEX_MODEL_GPT_5_4 = "gpt-5.4";
export const CODEX_MODEL_GPT_5_4_FAST = "gpt-5.4-fast";
export const CODEX_MODEL_GPT_5_4_MINI = "gpt-5.4-mini";
export const CODEX_MODEL_GPT_5_3_CODEX = "gpt-5.3-codex";
export const CODEX_MODEL_GPT_5_2_CODEX = "gpt-5.2-codex";
export const CODEX_MODEL_GPT_5_CODEX = "gpt-5-codex";
export const CODEX_MODEL_GPT_5_3_CODEX_SPARK = "gpt-5.3-codex-spark";

export const AVAILABLE_CODEX_MODELS = [
  CODEX_MODEL_GPT_5_5,
  CODEX_MODEL_GPT_5_5_FAST,
  CODEX_MODEL_GPT_5_4,
  CODEX_MODEL_GPT_5_4_FAST,
  CODEX_MODEL_GPT_5_4_MINI,
  CODEX_MODEL_GPT_5_3_CODEX,
  CODEX_MODEL_GPT_5_2_CODEX,
  CODEX_MODEL_GPT_5_CODEX,
  CODEX_MODEL_GPT_5_3_CODEX_SPARK,
] as const;

export const DEFAULT_CODEX_MODEL = CODEX_MODEL_GPT_5_5;

// Amp uses agent "modes" instead of model names; each mode bundles a model,
// system prompt, and tool set on Sourcegraph's side.
export const AMP_MODE_SMART = "smart";
export const AMP_MODE_DEEP = "deep";
export const AMP_MODE_LARGE = "large";
export const AMP_MODE_RUSH = "rush";

export const AVAILABLE_AMP_MODES = [
  AMP_MODE_SMART,
  AMP_MODE_DEEP,
  AMP_MODE_LARGE,
  AMP_MODE_RUSH,
] as const;

// Pi accepts provider/model patterns. Curated short list; PI_MODEL_CUSTOM
// lets users opt into Pi's full multi-provider catalog. Opus 4.8 leads the
// list as the current top model and matches the adapter's hardcoded fallback.
export const PI_MODEL_CLAUDE_OPUS_48 = "anthropic/claude-opus-4-8";
export const PI_MODEL_CLAUDE_OPUS_47 = "anthropic/claude-opus-4-7";
export const PI_MODEL_CLAUDE_SONNET_46 = "anthropic/claude-sonnet-4-6";
export const PI_MODEL_CLAUDE_HAIKU_45 = "anthropic/claude-haiku-4-5";
export const PI_MODEL_GPT_5_4 = "openai/gpt-5.4";
export const PI_MODEL_GEMINI_2_5_PRO = "google/gemini-2.5-pro";

export const AVAILABLE_PI_MODELS = [
  PI_MODEL_CLAUDE_OPUS_48,
  PI_MODEL_CLAUDE_OPUS_47,
  PI_MODEL_CLAUDE_SONNET_46,
  PI_MODEL_CLAUDE_HAIKU_45,
  PI_MODEL_GPT_5_4,
  PI_MODEL_GEMINI_2_5_PRO,
] as const;

export const OPENCODE_MODEL_GPT_5_4_MINI = "openai/gpt-5.4-mini";
export const OPENCODE_MODEL_GPT_5_3_CODEX_SPARK = "openai/gpt-5.3-codex-spark";
export const OPENCODE_MODEL_CLAUDE_HAIKU_45 = "anthropic/claude-haiku-4-5";
export const OPENCODE_MODEL_GEMINI_3_5_FLASH = "opencode/gemini-3.5-flash";
export const OPENCODE_MODEL_OPENROUTER_GEMINI_3_5_FLASH = "openrouter/google/gemini-3.5-flash";
export const OPENCODE_MODEL_GEMINI_3_FLASH = "google/gemini-3-flash";
export const OPENCODE_MODEL_MINIMAX_M2_7 = "opencode/minimax-m2.7";
export const OPENCODE_MODEL_OPENROUTER_MINIMAX_M2_7 = "openrouter/minimax/minimax-m2.7";
export const OPENCODE_MODEL_MINIMAX_M2_5 = "opencode/minimax-m2.5";
export const OPENCODE_MODEL_OPENROUTER_MINIMAX_M2_5 = "openrouter/minimax/minimax-m2.5";
export const OPENCODE_MODEL_DEEPSEEK_V4_FLASH = "opencode/deepseek-v4-flash";
export const OPENCODE_MODEL_OPENROUTER_DEEPSEEK_V4_FLASH = "openrouter/deepseek/deepseek-v4-flash";
export const OPENCODE_MODEL_DEEPSEEK_V4_PRO = "opencode/deepseek-v4-pro";
export const OPENCODE_MODEL_OPENROUTER_DEEPSEEK_V4_PRO = "openrouter/deepseek/deepseek-v4-pro";
export const OPENCODE_MODEL_GLM_5_2 = "opencode/glm-5.2";
export const OPENCODE_MODEL_OPENROUTER_GLM_5_2 = "openrouter/z-ai/glm-5.2";
export const OPENCODE_MODEL_GLM_5_1 = "opencode/glm-5.1";
export const OPENCODE_MODEL_OPENROUTER_GLM_5_1 = "openrouter/z-ai/glm-5.1";
export const OPENCODE_MODEL_KIMI_K2_5 = "opencode/kimi-k2.5";
export const OPENCODE_MODEL_OPENROUTER_KIMI_K2_5 = "openrouter/moonshotai/kimi-k2.5";
export const OPENCODE_MODEL_GPT_5_4 = "openai/gpt-5.4";
export const OPENCODE_MODEL_CLAUDE_SONNET_46 = "anthropic/claude-sonnet-4-6";
export const OPENCODE_MODEL_GEMINI_3_1_PRO = "opencode/gemini-3.1-pro";
export const OPENCODE_MODEL_OPENROUTER_GEMINI_3_1_PRO = "openrouter/google/gemini-3.1-pro-preview";
export const OPENCODE_MODEL_KIMI_K2_6 = "opencode/kimi-k2.6";
export const OPENCODE_MODEL_OPENROUTER_KIMI_K2_6 = "openrouter/moonshotai/kimi-k2.6";
export const OPENCODE_MODEL_GPT_5_2 = "opencode/gpt-5.2";
export const OPENCODE_MODEL_OPENROUTER_GPT_5_2 = "openrouter/openai/gpt-5.2";
export const OPENCODE_MODEL_GPT_5_5 = "opencode/gpt-5.5";
export const OPENCODE_MODEL_OPENROUTER_GPT_5_5 = "openrouter/openai/gpt-5.5";
export const OPENCODE_MODEL_GPT_5_5_PRO = "opencode/gpt-5.5-pro";
export const OPENCODE_MODEL_OPENROUTER_GPT_5_5_PRO = "openrouter/openai/gpt-5.5-pro";
export const OPENCODE_MODEL_CLAUDE_OPUS_48 = "anthropic/claude-opus-4-8";
export const OPENCODE_MODEL_CLAUDE_OPUS_47 = "anthropic/claude-opus-4-7";
export const OPENCODE_MODEL_CLAUDE_FABLE_5 = "opencode/claude-fable-5";
export const OPENCODE_MODEL_OPENROUTER_CLAUDE_FABLE_5 = "openrouter/anthropic/claude-fable-5";

export const AVAILABLE_OPENCODE_MODELS = [
  OPENCODE_MODEL_GLM_5_2,
  OPENCODE_MODEL_OPENROUTER_GLM_5_2,
  OPENCODE_MODEL_GPT_5_4_MINI,
  OPENCODE_MODEL_GPT_5_3_CODEX_SPARK,
  OPENCODE_MODEL_CLAUDE_HAIKU_45,
  OPENCODE_MODEL_GEMINI_3_5_FLASH,
  OPENCODE_MODEL_OPENROUTER_GEMINI_3_5_FLASH,
  OPENCODE_MODEL_GEMINI_3_FLASH,
  OPENCODE_MODEL_MINIMAX_M2_7,
  OPENCODE_MODEL_OPENROUTER_MINIMAX_M2_7,
  OPENCODE_MODEL_MINIMAX_M2_5,
  OPENCODE_MODEL_OPENROUTER_MINIMAX_M2_5,
  OPENCODE_MODEL_DEEPSEEK_V4_FLASH,
  OPENCODE_MODEL_OPENROUTER_DEEPSEEK_V4_FLASH,
  OPENCODE_MODEL_DEEPSEEK_V4_PRO,
  OPENCODE_MODEL_OPENROUTER_DEEPSEEK_V4_PRO,
  OPENCODE_MODEL_GLM_5_1,
  OPENCODE_MODEL_OPENROUTER_GLM_5_1,
  OPENCODE_MODEL_KIMI_K2_5,
  OPENCODE_MODEL_OPENROUTER_KIMI_K2_5,
  OPENCODE_MODEL_GPT_5_4,
  OPENCODE_MODEL_CLAUDE_SONNET_46,
  OPENCODE_MODEL_GEMINI_3_1_PRO,
  OPENCODE_MODEL_OPENROUTER_GEMINI_3_1_PRO,
  OPENCODE_MODEL_KIMI_K2_6,
  OPENCODE_MODEL_OPENROUTER_KIMI_K2_6,
  OPENCODE_MODEL_GPT_5_2,
  OPENCODE_MODEL_OPENROUTER_GPT_5_2,
  OPENCODE_MODEL_GPT_5_5,
  OPENCODE_MODEL_OPENROUTER_GPT_5_5,
  OPENCODE_MODEL_GPT_5_5_PRO,
  OPENCODE_MODEL_OPENROUTER_GPT_5_5_PRO,
  OPENCODE_MODEL_CLAUDE_OPUS_48,
  OPENCODE_MODEL_CLAUDE_OPUS_47,
  OPENCODE_MODEL_CLAUDE_FABLE_5,
  OPENCODE_MODEL_OPENROUTER_CLAUDE_FABLE_5,
] as const;

// OpenCode logical models — one user-facing entry per model. The OpenRouter and
// OpenCode-native physical routes are collapsed; the backend resolves the
// transport at launch (OpenRouter first, native fallback unless the org sets
// RequireOpenRouter). Single-route first-party models keep their physical id as
// the logical id. Mirror of internal/models/opencode_models.go
// (OpenCodeModelRegistry); keep in sync.
export const OPENCODE_LOGICAL_GLM_5_2 = "glm-5.2";
export const OPENCODE_LOGICAL_GLM_5_1 = "glm-5.1";
export const OPENCODE_LOGICAL_KIMI_K2_5 = "kimi-k2.5";
export const OPENCODE_LOGICAL_KIMI_K2_6 = "kimi-k2.6";
export const OPENCODE_LOGICAL_MINIMAX_M2_7 = "minimax-m2.7";
export const OPENCODE_LOGICAL_MINIMAX_M2_5 = "minimax-m2.5";
export const OPENCODE_LOGICAL_DEEPSEEK_V4_FLASH = "deepseek-v4-flash";
export const OPENCODE_LOGICAL_DEEPSEEK_V4_PRO = "deepseek-v4-pro";
export const OPENCODE_LOGICAL_GEMINI_3_5_FLASH = "gemini-3.5-flash";
export const OPENCODE_LOGICAL_GEMINI_3_1_PRO = "gemini-3.1-pro";
export const OPENCODE_LOGICAL_GPT_5_2 = "gpt-5.2";
export const OPENCODE_LOGICAL_GPT_5_5_PRO = "gpt-5.5-pro";
// GPT-5.5 and Claude Fable 5 are NOT collapsed under a bare logical id — those
// bare names belong to the Codex / Claude Code agents — so OpenCode offers them
// as explicit physical (pinned) route ids instead. Mirrors the Go registry.

// Ordered logical model ids the OpenCode picker offers (cost-first; GLM 5.2
// leads as the default).
export const OPENCODE_LOGICAL_MODELS = [
  OPENCODE_LOGICAL_GLM_5_2,
  OPENCODE_MODEL_GPT_5_4_MINI,
  OPENCODE_MODEL_GPT_5_3_CODEX_SPARK,
  OPENCODE_MODEL_CLAUDE_HAIKU_45,
  OPENCODE_LOGICAL_GEMINI_3_5_FLASH,
  OPENCODE_MODEL_GEMINI_3_FLASH,
  OPENCODE_LOGICAL_MINIMAX_M2_7,
  OPENCODE_LOGICAL_MINIMAX_M2_5,
  OPENCODE_LOGICAL_DEEPSEEK_V4_FLASH,
  OPENCODE_LOGICAL_DEEPSEEK_V4_PRO,
  OPENCODE_LOGICAL_GLM_5_1,
  OPENCODE_LOGICAL_KIMI_K2_5,
  OPENCODE_MODEL_GPT_5_4,
  OPENCODE_MODEL_CLAUDE_SONNET_46,
  OPENCODE_LOGICAL_GEMINI_3_1_PRO,
  OPENCODE_LOGICAL_KIMI_K2_6,
  OPENCODE_LOGICAL_GPT_5_2,
  OPENCODE_MODEL_OPENROUTER_GPT_5_5,
  OPENCODE_MODEL_GPT_5_5,
  OPENCODE_LOGICAL_GPT_5_5_PRO,
  OPENCODE_MODEL_CLAUDE_OPUS_48,
  OPENCODE_MODEL_CLAUDE_OPUS_47,
  OPENCODE_MODEL_OPENROUTER_CLAUDE_FABLE_5,
  OPENCODE_MODEL_CLAUDE_FABLE_5,
] as const;

export const DEFAULT_OPENCODE_MODEL = OPENCODE_LOGICAL_GLM_5_2;

// Human labels for OpenCode logical ids and the physical route ids they
// collapse, so the picker shows "GLM 5.2" and older pinned physical selections
// still render a friendly name. Keyed by both logical and physical id.
export const OPENCODE_MODEL_LABELS: Readonly<Record<string, string>> = {
  [OPENCODE_LOGICAL_GLM_5_2]: "GLM 5.2",
  [OPENCODE_MODEL_GLM_5_2]: "GLM 5.2",
  [OPENCODE_MODEL_OPENROUTER_GLM_5_2]: "GLM 5.2",
  [OPENCODE_LOGICAL_GLM_5_1]: "GLM 5.1",
  [OPENCODE_MODEL_GLM_5_1]: "GLM 5.1",
  [OPENCODE_MODEL_OPENROUTER_GLM_5_1]: "GLM 5.1",
  [OPENCODE_LOGICAL_KIMI_K2_5]: "Kimi K2.5",
  [OPENCODE_MODEL_KIMI_K2_5]: "Kimi K2.5",
  [OPENCODE_MODEL_OPENROUTER_KIMI_K2_5]: "Kimi K2.5",
  [OPENCODE_LOGICAL_KIMI_K2_6]: "Kimi K2.6",
  [OPENCODE_MODEL_KIMI_K2_6]: "Kimi K2.6",
  [OPENCODE_MODEL_OPENROUTER_KIMI_K2_6]: "Kimi K2.6",
  [OPENCODE_LOGICAL_MINIMAX_M2_7]: "MiniMax M2.7",
  [OPENCODE_MODEL_MINIMAX_M2_7]: "MiniMax M2.7",
  [OPENCODE_MODEL_OPENROUTER_MINIMAX_M2_7]: "MiniMax M2.7",
  [OPENCODE_LOGICAL_MINIMAX_M2_5]: "MiniMax M2.5",
  [OPENCODE_MODEL_MINIMAX_M2_5]: "MiniMax M2.5",
  [OPENCODE_MODEL_OPENROUTER_MINIMAX_M2_5]: "MiniMax M2.5",
  [OPENCODE_LOGICAL_DEEPSEEK_V4_FLASH]: "DeepSeek V4 Flash",
  [OPENCODE_MODEL_DEEPSEEK_V4_FLASH]: "DeepSeek V4 Flash",
  [OPENCODE_MODEL_OPENROUTER_DEEPSEEK_V4_FLASH]: "DeepSeek V4 Flash",
  [OPENCODE_LOGICAL_DEEPSEEK_V4_PRO]: "DeepSeek V4 Pro",
  [OPENCODE_MODEL_DEEPSEEK_V4_PRO]: "DeepSeek V4 Pro",
  [OPENCODE_MODEL_OPENROUTER_DEEPSEEK_V4_PRO]: "DeepSeek V4 Pro",
  [OPENCODE_LOGICAL_GEMINI_3_5_FLASH]: "Gemini 3.5 Flash",
  [OPENCODE_MODEL_GEMINI_3_5_FLASH]: "Gemini 3.5 Flash",
  [OPENCODE_MODEL_OPENROUTER_GEMINI_3_5_FLASH]: "Gemini 3.5 Flash",
  [OPENCODE_LOGICAL_GEMINI_3_1_PRO]: "Gemini 3.1 Pro",
  [OPENCODE_MODEL_GEMINI_3_1_PRO]: "Gemini 3.1 Pro",
  [OPENCODE_MODEL_OPENROUTER_GEMINI_3_1_PRO]: "Gemini 3.1 Pro",
  [OPENCODE_LOGICAL_GPT_5_2]: "GPT-5.2",
  [OPENCODE_MODEL_GPT_5_2]: "GPT-5.2",
  [OPENCODE_MODEL_OPENROUTER_GPT_5_2]: "GPT-5.2",
  [OPENCODE_MODEL_GPT_5_5]: "GPT-5.5",
  [OPENCODE_MODEL_OPENROUTER_GPT_5_5]: "GPT-5.5",
  [OPENCODE_LOGICAL_GPT_5_5_PRO]: "GPT-5.5 Pro",
  [OPENCODE_MODEL_GPT_5_5_PRO]: "GPT-5.5 Pro",
  [OPENCODE_MODEL_OPENROUTER_GPT_5_5_PRO]: "GPT-5.5 Pro",
  [OPENCODE_MODEL_CLAUDE_FABLE_5]: "Claude Fable 5",
  [OPENCODE_MODEL_OPENROUTER_CLAUDE_FABLE_5]: "Claude Fable 5",
  [OPENCODE_MODEL_GPT_5_4_MINI]: "GPT-5.4 Mini",
  [OPENCODE_MODEL_GPT_5_3_CODEX_SPARK]: "GPT-5.3 Codex Spark",
  [OPENCODE_MODEL_CLAUDE_HAIKU_45]: "Claude Haiku 4.5",
  [OPENCODE_MODEL_GEMINI_3_FLASH]: "Gemini 3 Flash",
  [OPENCODE_MODEL_GPT_5_4]: "GPT-5.4",
  [OPENCODE_MODEL_CLAUDE_SONNET_46]: "Claude Sonnet 4.6",
  [OPENCODE_MODEL_CLAUDE_OPUS_48]: "Claude Opus 4.8",
  [OPENCODE_MODEL_CLAUDE_OPUS_47]: "Claude Opus 4.7",
};

const OPENCODE_LOGICAL_MODEL_SET: ReadonlySet<string> = new Set(OPENCODE_LOGICAL_MODELS);

export function isOpenCodeLogicalModel(model: string): boolean {
  return OPENCODE_LOGICAL_MODEL_SET.has(model);
}

// openCodeModelLabel returns a friendly display name for an OpenCode logical or
// physical model id, falling back to the raw id for uncurated custom slugs.
export function openCodeModelLabel(model: string): string {
  return OPENCODE_MODEL_LABELS[normalizeOpenCodeModelLabelID(model)] ?? model;
}

function normalizeOpenCodeModelLabelID(model: string): string {
  if (model.startsWith("openrouter/~")) {
    return `openrouter/${model.slice("openrouter/~".length)}`;
  }
  return model;
}

// openCodeTransportLabelForModel derives the human transport name from a
// resolved physical OpenCode model id by its prefix (e.g.
// "openrouter/~z-ai/glm-5.2" → "OpenRouter"). Returns null for a logical id or
// an uncurated slug whose transport can't be determined from the id alone.
// Mirror of OpenCodeTransportLabel in internal/models/opencode_models.go.
export function openCodeTransportLabelForModel(model: string): string | null {
  if (model.startsWith("openrouter/")) return "OpenRouter";
  if (model.startsWith("opencode/")) return "OpenCode native";
  if (model.startsWith("anthropic/")) return "Anthropic";
  if (model.startsWith("openai/")) return "OpenAI";
  if (model.startsWith("google/")) return "Gemini";
  return null;
}

export const DEFAULT_PM_MODEL = DEFAULT_CODEX_MODEL;

// PM and session model dropdowns are both built from the AGENTS registry in
// @/lib/agents (see availableAgentModelGroups). Keeping a second PM-only list
// here would drift away from the session picker.

// General-purpose LLM models (used by validation, prioritization, PM services).
// NOTE: This is a static fallback. The frontend should prefer fetching models
// from GET /api/v1/settings/llm-models (served by models.LLMModelsByProvider()
// in internal/models/agent_model_constants.go). Keep both in sync.
export const LLM_MODELS_BY_PROVIDER: Record<string, { label: string; models: readonly string[] }> = {
  anthropic: { label: "Anthropic", models: ["claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"] },
  openai: { label: "OpenAI", models: ["gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"] },
  gemini: { label: "Gemini", models: ["gemini-3.1-pro", "gemini-3-flash", "gemini-2.5-pro", "gemini-2.5-flash"] },
  openrouter: {
    label: "OpenRouter",
    models: [
      "claude-opus-4-8",
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
      // OpenRouter-exclusive open-weight models — give OpenRouter-only orgs a
      // default model they can pick and save without needing a native provider.
      "qwen3-235b-a22b",
      "qwen3-32b",
    ],
  },
};

export const DEFAULT_LLM_MODEL = "gpt-5.4-mini";

// Models allowed when the org is relying on a platform-default key (the one
// 143 ships from .env) rather than their own. The cap is a cost guard: 143
// pays for these calls, so heavy models are gated behind "bring your own key."
// Providers absent from this map are not restricted on platform default.
// Keep in sync with PlatformDefaultAllowedLLMModels in
// internal/models/agent_model_constants.go.
export const PLATFORM_DEFAULT_ALLOWED_MODELS: Record<string, readonly string[]> = {
  openai: ["gpt-5.4-mini", "gpt-5.4-nano"],
};

// OpenAI credential api_type value.
export const OPENAI_API_TYPE_CHAT = "chat";

export const LLM_PROVIDER_INFO: Record<string, { name: string; description: string; keyPlaceholder: string }> = {
  anthropic: { name: "Anthropic", description: "Claude models (Opus, Sonnet, Haiku)", keyPlaceholder: "sk-ant-..." },
  openai: { name: "OpenAI", description: "OpenAI models (GPT series)", keyPlaceholder: "sk-..." },
  gemini: { name: "Gemini", description: "Google Gemini models", keyPlaceholder: "AIza..." },
  openrouter: { name: "OpenRouter", description: "Access all models with a single key", keyPlaceholder: "sk-or-..." },
};

// ownerProviderForModel returns the provider whose key will actually serve the
// model. When providerStatus is supplied, a configured provider wins over an
// unconfigured one (native configured > openrouter configured > fall through).
// Without providerStatus it returns the preferred owner: native first, then
// openrouter. Returns null if no provider offers the model.
export function ownerProviderForModel(
  model: string,
  modelsByProvider: Record<string, { label: string; models: readonly string[] }>,
  providerStatus?: Record<string, { orgConfigured?: boolean; platformAvailable?: boolean }>,
): string | null {
  const owners: string[] = [];
  for (const [provider, group] of Object.entries(modelsByProvider)) {
    if (group.models.includes(model)) owners.push(provider);
  }
  if (owners.length === 0) return null;

  const nativeOwners = owners.filter((p) => p !== "openrouter");
  const hasOpenRouter = owners.includes("openrouter");

  if (providerStatus) {
    const isConfigured = (p: string) => {
      const s = providerStatus[p];
      return Boolean(s?.orgConfigured || s?.platformAvailable);
    };
    const configuredNative = nativeOwners.find(isConfigured);
    if (configuredNative) return configuredNative;
    if (hasOpenRouter && isConfigured("openrouter")) return "openrouter";
  }

  if (nativeOwners.length > 0) return nativeOwners[0];
  return hasOpenRouter ? "openrouter" : null;
}
