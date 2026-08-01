import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./select";

describe("SelectTrigger", () => {
  it("uses a mobile-safe font size and keeps compact desktop sizing", () => {
    renderWithProviders(
      <Select defaultValue="weekly">
        <SelectTrigger aria-label="Schedule">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="weekly">Weekly</SelectItem>
        </SelectContent>
      </Select>,
    );

    const trigger = screen.getByRole("combobox", { name: "Schedule" });
    expect(trigger).toHaveClass("max-sm:text-base");
    expect(trigger).toHaveClass("type-dense");
    expect(trigger).toHaveClass("min-h-11", "h-11", "px-2", "sm:min-h-0", "sm:h-9");
    expect(trigger).toHaveAttribute("data-density", "default");
    expect(trigger).not.toHaveClass("text-base");
  });

  it("uses the requested shared density", () => {
    renderWithProviders(
      <Select defaultValue="weekly">
        <SelectTrigger aria-label="Compact schedule" density="compact">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="weekly">Weekly</SelectItem>
        </SelectContent>
      </Select>,
    );

    const trigger = screen.getByRole("combobox", { name: "Compact schedule" });
    expect(trigger).toHaveClass("h-11", "min-h-11", "sm:h-8", "sm:min-h-0");
    expect(trigger).toHaveAttribute("data-density", "compact");
  });
});
