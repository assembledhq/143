import { describe, expect, it } from "vitest";

import {
  AVAILABLE_CODEX_MODELS,
  AVAILABLE_CLAUDE_CODE_MODELS,
  AVAILABLE_OPENCODE_MODELS,
  OPENCODE_LOGICAL_MODELS,
  DEFAULT_OPENCODE_MODEL,
  isOpenCodeLogicalModel,
  openCodeModelLabel,
  AVAILABLE_PI_MODELS,
  DEFAULT_CLAUDE_CODE_MODEL,
  DEFAULT_CODEX_MODEL,
  DEFAULT_PM_MODEL,
  DEFAULT_LLM_MODEL,
  CLAUDE_CODE_MODEL_OPUS_48,
  CODEX_MODEL_GPT_5_5,
  LLM_PROVIDER_INFO,
  LLM_MODELS_BY_PROVIDER,
  ownerProviderForModel,
} from "./model-constants";

describe("model constants", () => {
  it("uses GPT 5.5 as the Codex and PM default", () => {
    expect(DEFAULT_CODEX_MODEL).toBe(CODEX_MODEL_GPT_5_5);
    expect(DEFAULT_PM_MODEL).toBe(DEFAULT_CODEX_MODEL);
  });

  it("uses Opus 4.8 as the Claude Code default", () => {
    expect(DEFAULT_CLAUDE_CODE_MODEL).toBe(CLAUDE_CODE_MODEL_OPUS_48);
  });

  it("includes latest Claude Code models", () => {
    expect(AVAILABLE_CLAUDE_CODE_MODELS).toEqual([
      "claude-fable-5",
      "claude-opus-4-8",
      "claude-opus-4-7",
      "claude-opus-4-6",
      "claude-sonnet-4-6",
      "claude-sonnet-4-5",
      "claude-haiku-4-5",
    ]);
  });

  it("includes latest Pi curated models", () => {
    expect(AVAILABLE_PI_MODELS).toEqual([
      "anthropic/claude-opus-4-8",
      "anthropic/claude-opus-4-7",
      "anthropic/claude-sonnet-4-6",
      "anthropic/claude-haiku-4-5",
      "openai/gpt-5.4",
      "google/gemini-2.5-pro",
    ]);
  });

  it("includes OpenCode models with GLM 5.2 first", () => {
    expect(AVAILABLE_OPENCODE_MODELS).toEqual([
      "opencode/glm-5.2",
      "openrouter/z-ai/glm-5.2",
      "openai/gpt-5.4-mini",
      "openai/gpt-5.3-codex-spark",
      "anthropic/claude-haiku-4-5",
      "opencode/gemini-3.5-flash",
      "openrouter/google/gemini-3.5-flash",
      "google/gemini-3-flash",
      "opencode/minimax-m2.7",
      "openrouter/minimax/minimax-m2.7",
      "opencode/minimax-m2.5",
      "openrouter/minimax/minimax-m2.5",
      "opencode/deepseek-v4-flash",
      "openrouter/deepseek/deepseek-v4-flash",
      "opencode/deepseek-v4-pro",
      "openrouter/deepseek/deepseek-v4-pro",
      "opencode/glm-5.1",
      "openrouter/z-ai/glm-5.1",
      "opencode/kimi-k2.5",
      "openrouter/moonshotai/kimi-k2.5",
      "openai/gpt-5.4",
      "anthropic/claude-sonnet-4-6",
      "opencode/gemini-3.1-pro",
      "openrouter/google/gemini-3.1-pro-preview",
      "opencode/kimi-k2.6",
      "openrouter/moonshotai/kimi-k2.6",
      "opencode/gpt-5.2",
      "openrouter/openai/gpt-5.2",
      "opencode/gpt-5.5",
      "openrouter/openai/gpt-5.5",
      "opencode/gpt-5.5-pro",
      "openrouter/openai/gpt-5.5-pro",
      "anthropic/claude-opus-4-8",
      "anthropic/claude-opus-4-7",
      "opencode/claude-fable-5",
      "openrouter/anthropic/claude-fable-5",
    ]);
  });

  it("includes an audited OpenRouter counterpart for each native OpenCode model", () => {
    const counterparts = [
      ["opencode/gemini-3.5-flash", "openrouter/google/gemini-3.5-flash"],
      ["opencode/minimax-m2.7", "openrouter/minimax/minimax-m2.7"],
      ["opencode/minimax-m2.5", "openrouter/minimax/minimax-m2.5"],
      ["opencode/deepseek-v4-flash", "openrouter/deepseek/deepseek-v4-flash"],
      ["opencode/deepseek-v4-pro", "openrouter/deepseek/deepseek-v4-pro"],
      ["opencode/glm-5.2", "openrouter/z-ai/glm-5.2"],
      ["opencode/glm-5.1", "openrouter/z-ai/glm-5.1"],
      ["opencode/kimi-k2.5", "openrouter/moonshotai/kimi-k2.5"],
      ["opencode/gemini-3.1-pro", "openrouter/google/gemini-3.1-pro-preview"],
      ["opencode/kimi-k2.6", "openrouter/moonshotai/kimi-k2.6"],
      ["opencode/gpt-5.2", "openrouter/openai/gpt-5.2"],
      ["opencode/gpt-5.5", "openrouter/openai/gpt-5.5"],
      ["opencode/gpt-5.5-pro", "openrouter/openai/gpt-5.5-pro"],
      ["opencode/claude-fable-5", "openrouter/anthropic/claude-fable-5"],
    ];

    for (const [nativeModel, openRouterModel] of counterparts) {
      expect(AVAILABLE_OPENCODE_MODELS).toContain(nativeModel);
      expect(AVAILABLE_OPENCODE_MODELS).toContain(openRouterModel);
    }
  });

  it("includes latest Codex models", () => {
    expect(AVAILABLE_CODEX_MODELS).toEqual([
      "gpt-5.5",
      "gpt-5.5-fast",
      "gpt-5.4",
      "gpt-5.4-fast",
      "gpt-5.4-mini",
      "gpt-5.3-codex",
      "gpt-5.2-codex",
      "gpt-5-codex",
      "gpt-5.3-codex-spark",
    ]);
  });

  it("uses gpt-5.4-mini as the default LLM model", () => {
    expect(DEFAULT_LLM_MODEL).toBe("gpt-5.4-mini");
  });

  it("LLM_MODELS_BY_PROVIDER includes gpt-5.4-mini in openai and openrouter", () => {
    expect(LLM_MODELS_BY_PROVIDER.openai.models).toContain("gpt-5.4-mini");
    expect(LLM_MODELS_BY_PROVIDER.openrouter.models).toContain("gpt-5.4-mini");
  });

  it("LLM_MODELS_BY_PROVIDER maps providers to their models", () => {
    expect(Object.keys(LLM_MODELS_BY_PROVIDER)).toEqual(["anthropic", "openai", "gemini", "openrouter"]);
    expect(LLM_MODELS_BY_PROVIDER.anthropic.models).toEqual(["claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"]);
    expect(LLM_MODELS_BY_PROVIDER.openai.models).toEqual(["gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"]);
    expect(LLM_MODELS_BY_PROVIDER.gemini.models).toEqual(["gemini-3.1-pro", "gemini-3-flash", "gemini-2.5-pro", "gemini-2.5-flash"]);
  });

  it("LLM_MODELS_BY_PROVIDER exposes OpenRouter-exclusive Qwen models", () => {
    expect(LLM_MODELS_BY_PROVIDER.openrouter.models).toContain("qwen3-235b-a22b");
    expect(LLM_MODELS_BY_PROVIDER.openrouter.models).toContain("qwen3-32b");
    // These must not appear under any native provider group.
    for (const provider of ["anthropic", "openai", "gemini"] as const) {
      expect(LLM_MODELS_BY_PROVIDER[provider].models).not.toContain("qwen3-235b-a22b");
      expect(LLM_MODELS_BY_PROVIDER[provider].models).not.toContain("qwen3-32b");
    }
  });

  it("LLM_PROVIDER_INFO includes Gemini with an AIza placeholder", () => {
    expect(LLM_PROVIDER_INFO.gemini).toMatchObject({
      name: "Gemini",
      keyPlaceholder: "AIza...",
    });
  });
});

describe("ownerProviderForModel", () => {
  const groups = {
    anthropic: { label: "Anthropic", models: ["claude-opus-4-6"] as readonly string[] },
    openai: { label: "OpenAI", models: ["gpt-4o", "gpt-5.4-mini"] as readonly string[] },
    gemini: { label: "Gemini", models: ["gemini-2.5-pro"] as readonly string[] },
    openrouter: {
      label: "OpenRouter",
      models: [
        "claude-opus-4-6",
        "gpt-4o",
        "gpt-5.4-mini",
        "gemini-2.5-pro",
        "meta-only-model",
      ] as readonly string[],
    },
  };

  it("returns the native provider when the model is offered natively", () => {
    expect(ownerProviderForModel("claude-opus-4-6", groups)).toBe("anthropic");
    expect(ownerProviderForModel("gpt-5.4-mini", groups)).toBe("openai");
    expect(ownerProviderForModel("gemini-2.5-pro", groups)).toBe("gemini");
  });

  it("falls back to openrouter when only openrouter offers the model", () => {
    expect(ownerProviderForModel("meta-only-model", groups)).toBe("openrouter");
  });

  it("returns null when no provider offers the model", () => {
    expect(ownerProviderForModel("unknown-model", groups)).toBeNull();
  });

  it("returns null when the providers map is empty", () => {
    expect(ownerProviderForModel("anything", {})).toBeNull();
  });

  it("prefers a configured native provider over an unconfigured one", () => {
    const status = {
      anthropic: { orgConfigured: false, platformAvailable: false },
      openai: { orgConfigured: true, platformAvailable: false },
      gemini: { orgConfigured: false, platformAvailable: false },
      openrouter: { orgConfigured: true, platformAvailable: false },
    };
    // gpt-5.4-mini is offered by both openai (native) and openrouter; openai wins.
    expect(ownerProviderForModel("gpt-5.4-mini", groups, status)).toBe("openai");
  });

  it("falls back to openrouter when only openrouter is configured", () => {
    const status = {
      anthropic: { orgConfigured: false, platformAvailable: false },
      openai: { orgConfigured: false, platformAvailable: false },
      gemini: { orgConfigured: false, platformAvailable: false },
      openrouter: { orgConfigured: true, platformAvailable: false },
    };
    // gpt-5.4-mini is routable via OpenRouter — the owner should be openrouter
    // so the Save button is enabled.
    expect(ownerProviderForModel("gpt-5.4-mini", groups, status)).toBe("openrouter");
  });

  it("treats platform-available keys as configured", () => {
    const status = {
      anthropic: { orgConfigured: false, platformAvailable: false },
      openai: { orgConfigured: false, platformAvailable: true },
      gemini: { orgConfigured: false, platformAvailable: false },
      openrouter: { orgConfigured: false, platformAvailable: false },
    };
    expect(ownerProviderForModel("gpt-5.4-mini", groups, status)).toBe("openai");
  });

  it("defaults to the preferred native owner when nothing is configured", () => {
    const status = {
      anthropic: { orgConfigured: false, platformAvailable: false },
      openai: { orgConfigured: false, platformAvailable: false },
      gemini: { orgConfigured: false, platformAvailable: false },
      openrouter: { orgConfigured: false, platformAvailable: false },
    };
    expect(ownerProviderForModel("gpt-5.4-mini", groups, status)).toBe("openai");
    expect(ownerProviderForModel("meta-only-model", groups, status)).toBe("openrouter");
  });
});

describe("OpenCode logical models", () => {
  it("offers one entry per model with GLM 5.2 leading", () => {
    expect(OPENCODE_LOGICAL_MODELS[0]).toBe("glm-5.2");
    expect(DEFAULT_OPENCODE_MODEL).toBe("glm-5.2");
    // Open-weight models collapse to a single bare logical id (no double entry).
    for (const id of ["glm-5.2", "glm-5.1", "kimi-k2.5", "kimi-k2.6", "minimax-m2.7", "deepseek-v4-pro"]) {
      expect(OPENCODE_LOGICAL_MODELS).toContain(id);
      expect(id.startsWith("openrouter/")).toBe(false);
      expect(id.startsWith("opencode/")).toBe(false);
    }
    // GPT-5.5 / Claude Fable 5 stay as explicit physical ids (their bare names
    // belong to Codex / Claude Code) — the only intentional physical entries.
    expect(OPENCODE_LOGICAL_MODELS).toContain("openrouter/openai/gpt-5.5");
    expect(OPENCODE_LOGICAL_MODELS).not.toContain("gpt-5.5");
    // Collapsed: far fewer entries than the physical route list.
    expect(OPENCODE_LOGICAL_MODELS.length).toBeLessThan(AVAILABLE_OPENCODE_MODELS.length);
  });

  it("labels logical and physical ids with a friendly name and passes through custom slugs", () => {
    expect(openCodeModelLabel("glm-5.2")).toBe("GLM 5.2");
    expect(openCodeModelLabel("openrouter/z-ai/glm-5.2")).toBe("GLM 5.2");
    expect(openCodeModelLabel("opencode/glm-5.2")).toBe("GLM 5.2");
    expect(openCodeModelLabel("xai/grok-code-fast")).toBe("xai/grok-code-fast");
  });

  it("recognizes logical ids", () => {
    expect(isOpenCodeLogicalModel("glm-5.2")).toBe(true);
    expect(isOpenCodeLogicalModel("openrouter/z-ai/glm-5.2")).toBe(false);
    // Bare "gpt-5.5" belongs to Codex, not OpenCode.
    expect(isOpenCodeLogicalModel("gpt-5.5")).toBe(false);
    expect(isOpenCodeLogicalModel("kimi-k2.6")).toBe(true);
  });
});
