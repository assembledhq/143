import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { Textarea } from "./textarea";

describe("Textarea", () => {
  it("uses a mobile-safe font size and keeps compact desktop sizing", () => {
    renderWithProviders(<Textarea aria-label="Notes" />);

    const textarea = screen.getByRole("textbox", { name: "Notes" });
    expect(textarea).toHaveClass("max-sm:text-base");
    expect(textarea).toHaveClass("type-dense");
    expect(textarea).not.toHaveClass("text-base");
  });
});
