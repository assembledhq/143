import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { Textarea } from "./textarea";

describe("Textarea", () => {
  it("uses a mobile-safe font size and keeps compact desktop sizing", () => {
    renderWithProviders(<Textarea aria-label="Notes" />);

    const textarea = screen.getByRole("textbox", { name: "Notes" });
    expect(textarea).toHaveClass("text-base");
    expect(textarea).toHaveClass("sm:text-xs");
  });
});
