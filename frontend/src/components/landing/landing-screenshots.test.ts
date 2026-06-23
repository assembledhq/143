import { describe, expect, it } from "vitest";
import { landingScreenshots } from "./landing-screenshots";

describe("landing screenshots", () => {
  it("uses captured product screenshots for each homepage workflow slot", () => {
    expect(landingScreenshots.context.src).toBe(
      "/product/product-integrations.webp",
    );
    expect(landingScreenshots.execution.src).toBe(
      "/product/product-session-overview.webp",
    );
    expect(landingScreenshots.control.src).toBe(
      "/product/product-review-diff.webp",
    );
    expect(landingScreenshots.preview.src).toBe(
      "/product/product-session-preview.webp",
    );
    expect(landingScreenshots.workspace.src).toBe(
      "/product/product-sessions-list.webp",
    );
  });

  it("keeps screenshot metadata accessible and concrete", () => {
    for (const screenshot of Object.values(landingScreenshots)) {
      expect(screenshot.src).toMatch(/^\/product\/product-.+\.webp$/);
      expect(screenshot.alt.length).toBeGreaterThan(20);
    }
  });
});
