import { describe, expect, it } from "vitest";

import { controlDensities, controlHeightVariants } from "./control-sizing";

describe("controlHeightVariants", () => {
  it.each([
    ["default", "sm:h-9"],
    ["compact", "sm:h-8"],
    ["dense", "sm:h-7"],
  ] as const)("keeps %s controls touch-safe on mobile and uses the expected desktop height", (density, desktopHeight) => {
    const classes = controlHeightVariants({ density }).split(" ");

    expect(controlDensities).toContain(density);
    expect(classes).toEqual(expect.arrayContaining(["h-11", "min-h-11", desktopHeight, "sm:min-h-0"]));
  });
});
