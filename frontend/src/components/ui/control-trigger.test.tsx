import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { ControlTrigger } from "./control-trigger";

describe("ControlTrigger", () => {
  it.each([
    ["default", "sm:h-9"],
    ["compact", "sm:h-8"],
    ["dense", "sm:h-7"],
  ] as const)("uses the shared %s control density", (density, desktopHeight) => {
    renderWithProviders(
      <ControlTrigger density={density}>{density} picker</ControlTrigger>,
    );

    const trigger = screen.getByRole("button", { name: `${density} picker` });
    expect(trigger).toHaveAttribute("data-density", density);
    expect(trigger).toHaveClass(
      "h-11",
      "min-h-11",
      desktopHeight,
      "sm:min-h-0",
    );
  });
});
