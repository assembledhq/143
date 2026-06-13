import { describe, expect, it } from "vitest";

import { openCodeModelsForBackingProvider, openCodeDefaultModelForBackingProvider } from "./coding-auth-metadata";

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
});
