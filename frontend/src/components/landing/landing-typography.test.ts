import { describe, expect, it } from "vitest";
import { landingTypography } from "./landing-typography";

describe("landing typography", () => {
  it("keeps homepage product copy on a small shared type scale", () => {
    expect(landingTypography).toEqual({
      navBrand: "text-sm font-semibold",
      navAction: "text-base",
      button: "h-11 px-7 text-base font-medium has-[>svg]:px-7",
      eyebrow: "text-xs font-mono uppercase tracking-wider",
      heroTitle: "text-5xl font-light leading-[1.05] tracking-normal sm:text-6xl",
      heroBody: "text-base leading-relaxed sm:text-lg",
      sectionTitle:
        "text-3xl font-light leading-tight tracking-normal sm:text-5xl",
      featureTitle: "text-2xl font-light tracking-normal sm:text-3xl",
      body: "text-sm leading-relaxed",
      cardTitle: "text-base font-medium",
      footerLink: "text-xs",
    });
  });

  it("does not use one-off arbitrary font sizes in shared type tokens", () => {
    expect(Object.values(landingTypography).join(" ")).not.toMatch(
      /text-\[[^\]]+\]/,
    );
  });
});
