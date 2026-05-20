import { describe, expect, it, vi } from "vitest";
import { screen } from "@/test/test-utils";
import { renderWithProviders, userEvent } from "@/test/test-utils";
import { SessionKeyboardHelpOverlay } from "./session-keyboard-help-overlay";

describe("SessionKeyboardHelpOverlay", () => {
  it("renders grouped session shortcuts and closes from the keyboard", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();

    renderWithProviders(
      <SessionKeyboardHelpOverlay open onOpenChange={onOpenChange} />,
    );

    expect(screen.getByRole("dialog", { name: "Session keyboard shortcuts" })).toBeInTheDocument();
    expect(screen.getByText("Navigate sessions")).toBeInTheDocument();
    expect(screen.getByText("Read transcript")).toBeInTheDocument();
    expect(screen.getByText("Ship PR")).toBeInTheDocument();
    expect(screen.getByText("p c")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("hides PR shortcuts when the viewer cannot ship PRs", () => {
    renderWithProviders(
      <SessionKeyboardHelpOverlay open onOpenChange={vi.fn()} canShipPR={false} />,
    );

    expect(screen.queryByText("Ship PR")).not.toBeInTheDocument();
    expect(screen.queryByText("p c")).not.toBeInTheDocument();
  });
});
