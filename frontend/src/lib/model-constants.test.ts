import { describe, expect, it } from "vitest";

import {
  AVAILABLE_CODEX_MODELS,
  AVAILABLE_CLAUDE_CODE_MODELS,
  AVAILABLE_GEMINI_CLI_MODELS,
  AVAILABLE_PM_MODELS,
  DEFAULT_PM_MODEL,
  CLAUDE_CODE_MODEL_SONNET,
  LEGACY_PM_ALIASES,
  PM_MODELS_BY_PROVIDER,
} from "./model-constants";

describe("model constants", () => {
  it("uses claude-sonnet-4-5 as the PM default", () => {
    expect(DEFAULT_PM_MODEL).toBe(CLAUDE_CODE_MODEL_SONNET);
  });

  it("includes legacy aliases and all provider models in AVAILABLE_PM_MODELS", () => {
    expect(AVAILABLE_PM_MODELS).toEqual([
      ...LEGACY_PM_ALIASES,
      ...AVAILABLE_CLAUDE_CODE_MODELS,
      ...AVAILABLE_GEMINI_CLI_MODELS,
      ...AVAILABLE_CODEX_MODELS,
    ]);
  });

  it("PM_MODELS_BY_PROVIDER maps each provider to its models and api key", () => {
    expect(Object.keys(PM_MODELS_BY_PROVIDER)).toEqual(["claude_code", "gemini_cli", "codex"]);
    expect(PM_MODELS_BY_PROVIDER.claude_code.models).toEqual(AVAILABLE_CLAUDE_CODE_MODELS);
    expect(PM_MODELS_BY_PROVIDER.gemini_cli.models).toEqual(AVAILABLE_GEMINI_CLI_MODELS);
    expect(PM_MODELS_BY_PROVIDER.codex.models).toEqual(AVAILABLE_CODEX_MODELS);
  });

  it("includes latest Claude Code model aliases", () => {
    expect(AVAILABLE_CLAUDE_CODE_MODELS).toEqual([
      "claude-opus-4-6",
      "claude-sonnet-4-6",
      "claude-sonnet-4-5",
      "claude-haiku-4-5",
    ]);
  });

  it("includes latest Gemini CLI models", () => {
    expect(AVAILABLE_GEMINI_CLI_MODELS).toEqual([
      "gemini-3-pro-preview",
      "gemini-3-flash-preview",
      "gemini-2.5-pro",
      "gemini-2.5-flash",
    ]);
  });

  it("includes latest Codex models", () => {
    expect(AVAILABLE_CODEX_MODELS).toEqual([
      "gpt-5.4",
      "gpt-5.4-mini",
      "gpt-5.3-codex",
      "gpt-5.2-codex",
      "gpt-5-codex",
      "gpt-5.3-codex-spark",
    ]);
  });
});
