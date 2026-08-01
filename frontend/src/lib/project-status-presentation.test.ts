import { describe, expect, it } from "vitest";

import { deriveProjectStatusPresentation } from "./project-status-presentation";

describe("deriveProjectStatusPresentation", () => {
  it.each([
    ["draft", "Draft", "neutral", "none"],
    ["active", "Active", "info", "breathing"],
    ["completed", "Done", "success", "none"],
  ] as const)("maps %s into shared operational semantics", (status, label, tone, activity) => {
    const presentation = deriveProjectStatusPresentation(status);

    expect(presentation.label, `${status} should use consistent copy`).toBe(label);
    expect(presentation.tone, `${status} should use the semantic tone`).toBe(tone);
    expect(presentation.activity, `${status} should only move while progressing`).toBe(activity);
  });
});
