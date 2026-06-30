import { describe, expect, it } from "vitest";

import {
  DEFAULT_OPENCODE_BACKING_PROVIDER,
  detectOpenCodeKeyPreset,
  ORG_PROVIDER_OPTIONS,
  openCodeModelsForBackingProvider,
  openCodeDefaultModelForBackingProvider,
  PERSONAL_PROVIDER_OPTIONS,
} from "./coding-auth-metadata";
import {
  AVAILABLE_OPENCODE_MODELS,
  OPENCODE_MODEL_GLM_5_2,
  OPENCODE_MODEL_OPENROUTER_GLM_5_2,
} from "./model-constants";

describe("OpenCode backing provider model helpers", () => {
  it("orders OpenCode third in auth setup provider choices", () => {
    expect(ORG_PROVIDER_OPTIONS.slice(0, 3).map((option) => option.key)).toEqual([
      "codex",
      "claude_code",
      "opencode",
    ]);
    expect(PERSONAL_PROVIDER_OPTIONS.slice(0, 3).map((option) => option.key)).toEqual([
      "openai",
      "anthropic",
      "opencode",
    ]);
  });

  it("marks Amp and Pi provider auth choices as beta", () => {
    expect(ORG_PROVIDER_OPTIONS.filter((option) => option.badge === "beta").map((option) => option.key)).toEqual([
      "amp",
      "pi",
    ]);
    expect(PERSONAL_PROVIDER_OPTIONS.filter((option) => option.badge === "beta").map((option) => option.key)).toEqual([
      "amp",
      "pi",
    ]);
  });

  it("defaults new OpenCode auths to OpenRouter", () => {
    expect(DEFAULT_OPENCODE_BACKING_PROVIDER).toBe("openrouter");
    expect(openCodeDefaultModelForBackingProvider(DEFAULT_OPENCODE_BACKING_PROVIDER)).toBe(OPENCODE_MODEL_OPENROUTER_GLM_5_2);
  });

  it("defaults native OpenCode auth to GLM 5.2", () => {
    expect(openCodeDefaultModelForBackingProvider("opencode")).toBe(OPENCODE_MODEL_GLM_5_2);
  });

  it("defaults OpenRouter auth to the OpenRouter GLM 5.2 route", () => {
    expect(openCodeDefaultModelForBackingProvider("openrouter")).toBe(OPENCODE_MODEL_OPENROUTER_GLM_5_2);
  });

  it("filters direct provider models by backing provider", () => {
    expect(openCodeModelsForBackingProvider("openai").every((model) => model.startsWith("openai/"))).toBe(true);
    expect(openCodeModelsForBackingProvider("anthropic").every((model) => model.startsWith("anthropic/"))).toBe(true);
    expect(openCodeModelsForBackingProvider("gemini").every((model) => model.startsWith("google/"))).toBe(true);
  });

  it("keeps the curated OpenRouter list on audited OpenRouter routes", () => {
    const models = openCodeModelsForBackingProvider("openrouter");
    expect(models).toEqual(AVAILABLE_OPENCODE_MODELS.filter((model) => model.startsWith("openrouter/")));
    expect(models.length).toBeGreaterThan(1);
  });

  it("keeps open-source OpenCode models separated from US-pinned OpenRouter routes", () => {
    const nativeModels = openCodeModelsForBackingProvider("opencode");
    expect(nativeModels).toContain(OPENCODE_MODEL_GLM_5_2);
    expect(nativeModels.every((model) => model.startsWith("opencode/"))).toBe(true);
    const openRouterModels = openCodeModelsForBackingProvider("openrouter");
    expect(openRouterModels).toContain(OPENCODE_MODEL_OPENROUTER_GLM_5_2);
    expect(openRouterModels.every((model) => model.startsWith("openrouter/"))).toBe(true);
    expect(openRouterModels).not.toEqual(expect.arrayContaining(nativeModels));
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
      provider: "openrouter",
      confidence: "low",
    });
  });
});
