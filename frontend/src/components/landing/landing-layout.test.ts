import { describe, expect, it } from "vitest";
import { landingLayout } from "./landing-layout";

describe("landing layout", () => {
  it("uses wide page shells for product imagery sections", () => {
    expect(landingLayout.pageShell).toContain("max-w-[88rem]");
    expect(landingLayout.pageShell).not.toContain("max-w-5xl");
  });

  it("clips horizontal overflow at the landing page root", () => {
    expect(landingLayout.pageRoot).toContain("overflow-x-hidden");
  });

  it("gives product visuals more horizontal room than copy on desktop", () => {
    expect(landingLayout.featureRow).toContain("0.35fr");
    expect(landingLayout.featureRow).toContain("0.65fr");
    expect(landingLayout.featureRowReverse).toContain("0.65fr");
    expect(landingLayout.featureRowReverse).toContain("0.35fr");
  });
});
