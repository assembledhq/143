import { fireEvent, renderWithProviders, screen } from "@/test/test-utils";
import { describe, expect, it, vi } from "vitest";
import { AutomationEmojiPicker } from "./automation-emoji-picker";

describe("AutomationEmojiPicker", () => {
  const getEmojiOption = (container: HTMLElement, name: string) => {
    const option = container.querySelector<HTMLElement>(`[role="option"][aria-label="${name}"]`);
    expect(option).not.toBeNull();
    return option as HTMLElement;
  };

  it("shows a broad emoji dropdown and selects an emoji", async () => {
    const selected: string[] = [];
    window.localStorage.clear();

    renderWithProviders(
      <AutomationEmojiPicker
        value="⚙️"
        onChange={(value) => selected.push(value)}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Automation emoji" }));

    const listbox = await screen.findByRole("listbox");
    expect(getEmojiOption(listbox, "Robot")).toBeInTheDocument();
    fireEvent.click(getEmojiOption(listbox, "Rocket"));

    expect(selected).toEqual(["🚀"]);
  });

  it("groups emoji and promotes recently selected emoji", async () => {
    const selected: string[] = [];
    window.localStorage.clear();
    window.localStorage.setItem("automation-emoji-picker-recents", JSON.stringify(["💡", "🚀"]));

    renderWithProviders(
      <AutomationEmojiPicker
        value="⚙️"
        onChange={(value) => selected.push(value)}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Automation emoji" }));

    expect(await screen.findByText("Frequently Used")).toBeInTheDocument();
    expect(screen.getByText("Smileys & People")).toBeInTheDocument();
    expect(screen.getByText("Objects")).toBeInTheDocument();
    expect(screen.getByText("Symbols")).toBeInTheDocument();

    const listbox = screen.getByRole("listbox");
    expect(getEmojiOption(listbox, "Light bulb")).toBeInTheDocument();
    expect(getEmojiOption(listbox, "Robot")).toBeInTheDocument();

    fireEvent.click(getEmojiOption(listbox, "Robot"));

    expect(selected).toEqual(["🤖"]);
    expect(JSON.parse(window.localStorage.getItem("automation-emoji-picker-recents") ?? "[]")).toEqual(["🤖", "💡", "🚀"]);
  });

  it("still selects an emoji when recent emoji persistence fails", async () => {
    const selected: string[] = [];
    window.localStorage.clear();
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);

    renderWithProviders(
      <AutomationEmojiPicker
        value="⚙️"
        onChange={(value) => selected.push(value)}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Automation emoji" }));
    const listbox = await screen.findByRole("listbox");
    fireEvent.click(getEmojiOption(listbox, "Rocket"));

    expect(selected).toEqual(["🚀"]);
    expect(consoleError).toHaveBeenCalledWith("Failed to persist recent emoji selection", expect.any(Error));
    setItem.mockRestore();
    consoleError.mockRestore();
  });
});
