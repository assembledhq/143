import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { Input } from "./input";

describe("Input", () => {
  it("uses a mobile-safe font size and keeps compact desktop sizing", () => {
    renderWithProviders(<Input aria-label="Name" />);

    const input = screen.getByRole("textbox", { name: "Name" });
    expect(input).toHaveClass("max-sm:text-base");
    expect(input).toHaveClass("type-dense");
    expect(input).toHaveClass("h-11", "min-h-11", "sm:h-9", "sm:min-h-0");
    expect(input).toHaveAttribute("data-density", "default");
    expect(input).not.toHaveClass("text-base");
  });

  it("uses the requested shared density", () => {
    renderWithProviders(
      <>
        <Input aria-label="Compact name" density="compact" />
        <Input aria-label="Dense name" density="dense" />
      </>,
    );

    const compactInput = screen.getByRole("textbox", { name: "Compact name" });
    expect(compactInput).toHaveClass("h-11", "min-h-11", "sm:h-8", "sm:min-h-0");
    expect(compactInput).toHaveAttribute("data-density", "compact");

    const denseInput = screen.getByRole("textbox", { name: "Dense name" });
    expect(denseInput).toHaveClass("h-11", "min-h-11", "sm:h-7", "sm:min-h-0", "py-1");
    expect(denseInput).toHaveAttribute("data-density", "dense");
  });

  it("moves inset overrides into a typed component prop", () => {
    renderWithProviders(<Input aria-label="Bare name" inset="none" />);

    expect(screen.getByRole("textbox", { name: "Bare name" })).toHaveClass("px-0", "py-0");
  });
});
