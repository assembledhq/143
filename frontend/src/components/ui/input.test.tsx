import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { Input } from "./input";

describe("Input", () => {
  it("uses a mobile-safe font size and keeps compact desktop sizing", () => {
    renderWithProviders(<Input aria-label="Name" />);

    const input = screen.getByRole("textbox", { name: "Name" });
    expect(input).toHaveClass("text-base");
    expect(input).toHaveClass("sm:text-xs");
  });
});
