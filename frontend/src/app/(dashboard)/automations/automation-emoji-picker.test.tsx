import { fireEvent, renderWithProviders, screen, within } from "@/test/test-utils";
import { describe, expect, it } from "vitest";
import { AutomationEmojiPicker } from "./automation-emoji-picker";

describe("AutomationEmojiPicker", () => {
  it("shows a broad searchable emoji dropdown and selects an emoji", async () => {
    const selected: string[] = [];

    renderWithProviders(
      <AutomationEmojiPicker
        value="⚙️"
        onChange={(value) => selected.push(value)}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Automation emoji" }));

    const listbox = await screen.findByRole("listbox");
    expect(within(listbox).getAllByRole("option").length).toBeGreaterThan(100);

    fireEvent.change(screen.getByPlaceholderText("Search emoji..."), {
      target: { value: "rocket" },
    });
    fireEvent.click(await screen.findByRole("option", { name: /Rocket/ }));

    expect(selected).toEqual(["🚀"]);
  });
});
