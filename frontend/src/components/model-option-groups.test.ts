import { describe, expect, it } from "vitest";

import type { OpenCodeModelAvailability } from "@/hooks/use-opencode-models";
import { isModelOptionVisible, shouldRenderModelOption } from "./model-option-groups";

describe("shouldRenderModelOption", () => {
  it("hides OpenCode models with no runnable route", () => {
    const availability = new Map<string, OpenCodeModelAvailability>([
      ["glm-5.2", { hasRunnableRoute: true, transportLabel: "OpenRouter" }],
      ["gpt-5.4", { hasRunnableRoute: false, transportLabel: null }],
    ]);

    expect(shouldRenderModelOption("glm-5.2", true, availability)).toBe(true);
    expect(shouldRenderModelOption("gpt-5.4", true, availability)).toBe(false);
  });

  it("keeps non-OpenCode and unknown-availability options visible", () => {
    const availability = new Map<string, OpenCodeModelAvailability>([
      ["gpt-5.5", { hasRunnableRoute: false, transportLabel: null }],
    ]);

    expect(shouldRenderModelOption("gpt-5.5", false, availability)).toBe(true);
    expect(shouldRenderModelOption("not-yet-loaded", true, availability)).toBe(true);
  });
});

describe("isModelOptionVisible", () => {
  const availability = new Map<string, OpenCodeModelAvailability>([
    ["glm-5.2", { hasRunnableRoute: true, transportLabel: "OpenRouter" }],
    ["gpt-5.4", { hasRunnableRoute: false, transportLabel: null }],
  ]);

  it("keeps an unavailable model visible when it is the current selection", () => {
    expect(isModelOptionVisible("gpt-5.4", true, availability)).toBe(false);
    expect(isModelOptionVisible("gpt-5.4", true, availability, "gpt-5.4")).toBe(true);
  });

  it("hides an unavailable model that is not selected", () => {
    expect(isModelOptionVisible("gpt-5.4", true, availability, "glm-5.2")).toBe(false);
  });

  it("shows available models regardless of selection", () => {
    expect(isModelOptionVisible("glm-5.2", true, availability)).toBe(true);
    expect(isModelOptionVisible("glm-5.2", true, availability, "gpt-5.4")).toBe(true);
  });
});
