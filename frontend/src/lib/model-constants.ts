// Legacy PM model aliases (kept for backward compatibility).
export const PM_MODEL_OPUS = "opus";
export const PM_MODEL_SONNET = "sonnet";
export const PM_MODEL_HAIKU = "haiku";

export const LEGACY_PM_ALIASES = [PM_MODEL_OPUS, PM_MODEL_SONNET, PM_MODEL_HAIKU] as const;

export const CLAUDE_CODE_MODEL_OPUS = "claude-opus-4-6";
export const CLAUDE_CODE_MODEL_SONNET = "claude-sonnet-4-5";
export const CLAUDE_CODE_MODEL_HAIKU = "claude-haiku-4-5";

export const AVAILABLE_CLAUDE_CODE_MODELS = [
  CLAUDE_CODE_MODEL_OPUS,
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

export const CODEX_MODEL_GPT_5_3_CODEX = "gpt-5.3-codex";
export const CODEX_MODEL_GPT_5_2_CODEX = "gpt-5.2-codex";
export const CODEX_MODEL_GPT_5_CODEX = "gpt-5-codex";
export const CODEX_MODEL_GPT_5_3_CODEX_SPARK = "gpt-5.3-codex-spark";

export const AVAILABLE_CODEX_MODELS = [
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

// All PM models across every provider (for validation / backward compat).
export const AVAILABLE_PM_MODELS = [
  ...LEGACY_PM_ALIASES,
  ...AVAILABLE_CLAUDE_CODE_MODELS,
  ...AVAILABLE_GEMINI_CLI_MODELS,
  ...AVAILABLE_CODEX_MODELS,
] as const;
