// Legacy PM model aliases (kept for backward compatibility).
export const PM_MODEL_OPUS = "opus";
export const PM_MODEL_SONNET = "sonnet";
export const PM_MODEL_HAIKU = "haiku";

export const LEGACY_PM_ALIASES = [PM_MODEL_OPUS, PM_MODEL_SONNET, PM_MODEL_HAIKU] as const;

export const CLAUDE_CODE_MODEL_OPUS = "claude-opus-4-6";
export const CLAUDE_CODE_MODEL_SONNET_46 = "claude-sonnet-4-6";
export const CLAUDE_CODE_MODEL_SONNET = "claude-sonnet-4-5";
export const CLAUDE_CODE_MODEL_HAIKU = "claude-haiku-4-5";

export const AVAILABLE_CLAUDE_CODE_MODELS = [
  CLAUDE_CODE_MODEL_OPUS,
  CLAUDE_CODE_MODEL_SONNET_46,
  CLAUDE_CODE_MODEL_SONNET,
  CLAUDE_CODE_MODEL_HAIKU,
] as const;

export const GEMINI_CLI_MODEL_GEMINI_3_1_PRO_PREVIEW = "gemini-3.1-pro-preview";
export const GEMINI_CLI_MODEL_GEMINI_3_FLASH_PREVIEW = "gemini-3-flash-preview";
export const GEMINI_CLI_MODEL_GEMINI_2_5_PRO = "gemini-2.5-pro";
export const GEMINI_CLI_MODEL_GEMINI_2_5_FLASH = "gemini-2.5-flash";

export const AVAILABLE_GEMINI_CLI_MODELS = [
  GEMINI_CLI_MODEL_GEMINI_3_1_PRO_PREVIEW,
  GEMINI_CLI_MODEL_GEMINI_3_FLASH_PREVIEW,
  GEMINI_CLI_MODEL_GEMINI_2_5_PRO,
  GEMINI_CLI_MODEL_GEMINI_2_5_FLASH,
] as const;

export const CODEX_MODEL_GPT_5_4 = "gpt-5.4";
export const CODEX_MODEL_GPT_5_4_MINI = "gpt-5.4-mini";
export const CODEX_MODEL_GPT_5_3_CODEX = "gpt-5.3-codex";
export const CODEX_MODEL_GPT_5_2_CODEX = "gpt-5.2-codex";
export const CODEX_MODEL_GPT_5_CODEX = "gpt-5-codex";
export const CODEX_MODEL_GPT_5_3_CODEX_SPARK = "gpt-5.3-codex-spark";

export const AVAILABLE_CODEX_MODELS = [
  CODEX_MODEL_GPT_5_4,
  CODEX_MODEL_GPT_5_4_MINI,
  CODEX_MODEL_GPT_5_3_CODEX,
  CODEX_MODEL_GPT_5_2_CODEX,
  CODEX_MODEL_GPT_5_CODEX,
  CODEX_MODEL_GPT_5_3_CODEX_SPARK,
] as const;

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
// lets users opt into Pi's full multi-provider catalog.
export const PI_MODEL_CLAUDE_SONNET_46 = "anthropic/claude-sonnet-4-6";
export const PI_MODEL_CLAUDE_OPUS_46 = "anthropic/claude-opus-4-6";
export const PI_MODEL_CLAUDE_HAIKU_45 = "anthropic/claude-haiku-4-5";
export const PI_MODEL_GPT_5_4 = "openai/gpt-5.4";
export const PI_MODEL_GEMINI_2_5_PRO = "google/gemini-2.5-pro";

export const AVAILABLE_PI_MODELS = [
  PI_MODEL_CLAUDE_SONNET_46,
  PI_MODEL_CLAUDE_OPUS_46,
  PI_MODEL_CLAUDE_HAIKU_45,
  PI_MODEL_GPT_5_4,
  PI_MODEL_GEMINI_2_5_PRO,
] as const;

// PM model configuration: maps each provider to its available models and API key env var.
export const PM_MODELS_BY_PROVIDER: Record<string, { label: string; models: readonly string[]; apiKeyVar: string }> = {
  claude_code: { label: "Claude Code", models: AVAILABLE_CLAUDE_CODE_MODELS, apiKeyVar: "ANTHROPIC_API_KEY" },
  gemini_cli: { label: "Gemini CLI", models: AVAILABLE_GEMINI_CLI_MODELS, apiKeyVar: "GEMINI_API_KEY" },
  codex: { label: "Codex", models: AVAILABLE_CODEX_MODELS, apiKeyVar: "OPENAI_API_KEY" },
};

export const DEFAULT_PM_MODEL = CLAUDE_CODE_MODEL_SONNET;

// Agent type options for session/project creation forms live on the AGENTS
// registry in @/lib/agents. Import AGENTS (and agentTypeForModel) from there —
// keeping a second list here would drift.

// All PM models across every provider (for validation / backward compat).
export const AVAILABLE_PM_MODELS = [
  ...LEGACY_PM_ALIASES,
  ...AVAILABLE_CLAUDE_CODE_MODELS,
  ...AVAILABLE_GEMINI_CLI_MODELS,
  ...AVAILABLE_CODEX_MODELS,
] as const;

// General-purpose LLM models (used by validation, prioritization, PM services).
// NOTE: This is a static fallback. The frontend should prefer fetching models
// from GET /api/v1/settings/llm-models (served by models.LLMModelsByProvider()
// in internal/models/agent_model_constants.go). Keep both in sync.
export const LLM_MODELS_BY_PROVIDER: Record<string, { label: string; models: readonly string[] }> = {
  anthropic: { label: "Anthropic", models: ["claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"] },
  openai: { label: "OpenAI", models: ["gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"] },
  gemini: { label: "Gemini", models: ["gemini-3.1-pro", "gemini-3-flash", "gemini-2.5-pro", "gemini-2.5-flash"] },
  openrouter: {
    label: "OpenRouter",
    models: [
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
