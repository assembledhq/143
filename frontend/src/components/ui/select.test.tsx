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
    expect(trigger).toHaveClass("text-base");
    expect(trigger).toHaveClass("sm:text-xs");
  });
});
