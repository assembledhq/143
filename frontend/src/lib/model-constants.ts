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

export const GEMINI_CLI_MODEL_GEMINI_3_PRO_PREVIEW = "gemini-3-pro-preview";
export const GEMINI_CLI_MODEL_GEMINI_3_FLASH_PREVIEW = "gemini-3-flash-preview";
export const GEMINI_CLI_MODEL_GEMINI_2_5_PRO = "gemini-2.5-pro";
export const GEMINI_CLI_MODEL_GEMINI_2_5_FLASH = "gemini-2.5-flash";

export const AVAILABLE_GEMINI_CLI_MODELS = [
  GEMINI_CLI_MODEL_GEMINI_3_PRO_PREVIEW,
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

// PM model configuration: maps each provider to its available models and API key env var.
export const PM_MODELS_BY_PROVIDER: Record<string, { label: string; models: readonly string[]; apiKeyVar: string }> = {
  claude_code: { label: "Claude Code", models: AVAILABLE_CLAUDE_CODE_MODELS, apiKeyVar: "ANTHROPIC_API_KEY" },
  gemini_cli: { label: "Gemini CLI", models: AVAILABLE_GEMINI_CLI_MODELS, apiKeyVar: "GEMINI_API_KEY" },
  codex: { label: "Codex", models: AVAILABLE_CODEX_MODELS, apiKeyVar: "OPENAI_API_KEY" },
};

export const DEFAULT_PM_MODEL = CLAUDE_CODE_MODEL_SONNET;

// Agent types with their labels and available models, for use in session/project creation forms.
export const AGENT_TYPE_OPTIONS: { key: string; label: string; models: readonly string[] }[] = [
  { key: "codex", label: "Codex", models: AVAILABLE_CODEX_MODELS },
  { key: "claude_code", label: "Claude Code", models: AVAILABLE_CLAUDE_CODE_MODELS },
  { key: "gemini_cli", label: "Gemini CLI", models: AVAILABLE_GEMINI_CLI_MODELS },
];

// Resolve the agent type key for a given model string.
export function agentTypeForModel(model: string): string | undefined {
  return AGENT_TYPE_OPTIONS.find((a) => (a.models as readonly string[]).includes(model))?.key;
}

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
  anthropic: { label: "Anthropic", models: ["claude-opus-4-6", "claude-sonnet-4-5", "claude-haiku-4-5"] },
  openai: { label: "OpenAI", models: ["gpt-4o", "gpt-4o-mini", "gpt-5.4-mini", "gpt-5-nano", "o3-mini"] },
  openrouter: { label: "OpenRouter", models: ["claude-opus-4-6", "claude-sonnet-4-5", "claude-haiku-4-5", "gpt-4o", "gpt-4o-mini", "gpt-5.4-mini", "gpt-5-nano", "o3-mini"] },
};

export const DEFAULT_LLM_MODEL = "gpt-5.4-mini";

// OpenAI credential api_type value.
export const OPENAI_API_TYPE_CHAT = "chat";

export const LLM_PROVIDER_INFO: Record<string, { name: string; description: string; keyPlaceholder: string }> = {
  anthropic: { name: "Anthropic", description: "Claude models (Opus, Sonnet, Haiku)", keyPlaceholder: "sk-ant-..." },
  openai: { name: "OpenAI", description: "OpenAI models (GPT series)", keyPlaceholder: "sk-..." },
  openrouter: { name: "OpenRouter", description: "Access all models with a single key", keyPlaceholder: "sk-or-..." },
};
