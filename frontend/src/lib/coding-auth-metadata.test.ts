import { describe, expect, it } from "vitest";

import {
  detectOpenCodeKeyPreset,
  openCodeModelsForBackingProvider,
  openCodeDefaultModelForBackingProvider,
} from "./coding-auth-metadata";

describe("OpenCode backing provider model helpers", () => {
  it("defaults native OpenCode auth to an OpenCode-native model", () => {
    expect(openCodeDefaultModelForBackingProvider("opencode")).toMatch(/^opencode\//);
  });

  it("filters direct provider models by backing provider", () => {
    expect(openCodeModelsForBackingProvider("openai").every((model) => model.startsWith("openai/"))).toBe(true);
    expect(openCodeModelsForBackingProvider("anthropic").every((model) => model.startsWith("anthropic/"))).toBe(true);
    expect(openCodeModelsForBackingProvider("gemini").every((model) => model.startsWith("google/"))).toBe(true);
  });

  it("leaves OpenRouter flexible across provider model prefixes", () => {
    const models = openCodeModelsForBackingProvider("openrouter");
    expect(models).toContain("openai/gpt-5.4-mini");
    expect(models).toContain("anthropic/claude-haiku-4-5");
  });

  it("detects OpenCode backing presets from common key prefixes without probing providers", () => {
    expect(detectOpenCodeKeyPreset("sk-or-v1-example")).toMatchObject({
      provider: "openrouter",
      confidence: "high",
    });
    expect(detectOpenCodeKeyPreset("sk-ant-api03-example")).toMatchObject({
      provider: "anthropic",
      confidence: "high",
    });
    expect(detectOpenCodeKeyPreset("AIzaSyExample")).toMatchObject({
      provider: "gemini",
      confidence: "high",
    });
    expect(detectOpenCodeKeyPreset("sk-proj-example")).toMatchObject({
      provider: "openai",
      confidence: "medium",
    });
    expect(detectOpenCodeKeyPreset("oc_unknown_shape")).toMatchObject({
      provider: "opencode",
      confidence: "low",
    });
  });
});
