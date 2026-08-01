import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { Command, CommandInput } from "./command";

describe("CommandInput", () => {
  it("uses the shared responsive field height", () => {
    renderWithProviders(
      <Command>
        <CommandInput aria-label="Search options" />
      </Command>,
    );

    const input = screen.getByRole("combobox");
    expect(input).toHaveClass("h-11", "min-h-11", "sm:h-9", "sm:min-h-0");
    expect(input.parentElement).toHaveClass("h-11", "min-h-11", "sm:h-9", "sm:min-h-0");
  });
});
