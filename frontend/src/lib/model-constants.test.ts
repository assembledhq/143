import { describe, expect, it } from "vitest";

import {
  AVAILABLE_CODEX_MODELS,
  AVAILABLE_CLAUDE_CODE_MODELS,
  AVAILABLE_GEMINI_CLI_MODELS,
  AVAILABLE_PM_MODELS,
  DEFAULT_PM_MODEL,
  PM_MODEL_SONNET,
} from "./model-constants";

describe("model constants", () => {
  it("uses sonnet as the PM default", () => {
    expect(DEFAULT_PM_MODEL).toBe(PM_MODEL_SONNET);
  });

  it("includes supported PM aliases", () => {
    expect(AVAILABLE_PM_MODELS).toEqual(["opus", "sonnet", "haiku"]);
  });

  it("includes latest Claude Code model aliases", () => {
    expect(AVAILABLE_CLAUDE_CODE_MODELS).toEqual([
      "claude-opus-4-6",
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
      "gpt-5.3-codex",
      "gpt-5.2-codex",
      "gpt-5-codex",
      "gpt-5.3-codex-spark",
    ]);
  });
});
