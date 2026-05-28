import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { Input } from "./input";

describe("Input", () => {
  it("uses a mobile-safe font size and keeps compact desktop sizing", () => {
    renderWithProviders(<Input aria-label="Name" />);

    const input = screen.getByRole("textbox", { name: "Name" });
    expect(input).toHaveClass("max-sm:text-base");
    expect(input).toHaveClass("text-xs");
    expect(input).not.toHaveClass("text-base");
  });

  it("renders on the raised control surface", () => {
    renderWithProviders(<Input aria-label="Repository" />);

    expect(screen.getByRole("textbox", { name: "Repository" })).toHaveClass("bg-surface-raised");
  });
});
