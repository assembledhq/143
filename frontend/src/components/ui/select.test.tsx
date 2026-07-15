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
    expect(trigger).toHaveClass("data-[size=default]:h-10", "px-2", "sm:data-[size=default]:h-9");
    expect(trigger).not.toHaveClass("text-base");
  });
});
