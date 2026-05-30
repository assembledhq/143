import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import {
  ResponsiveModal,
  ResponsiveModalBody,
  ResponsiveModalDescription,
  ResponsiveModalFooter,
  ResponsiveModalHeader,
  ResponsiveModalTitle,
} from "./responsive-modal";

function setMobileMatch(matches: boolean) {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === "(max-width: 639px)" ? matches : false,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

describe("ResponsiveModal", () => {
  it("renders a bottom sheet with a scrollable body on mobile", () => {
    setMobileMatch(true);

    render(
      <ResponsiveModal open onOpenChange={vi.fn()} desktopClassName="sm:max-w-xl">
        <ResponsiveModalHeader>
          <ResponsiveModalTitle>Mobile friendly form</ResponsiveModalTitle>
          <ResponsiveModalDescription>Configure the mobile form.</ResponsiveModalDescription>
        </ResponsiveModalHeader>
        <ResponsiveModalBody>
          <p>Scrollable content</p>
        </ResponsiveModalBody>
        <ResponsiveModalFooter>
          <button type="button">Save</button>
        </ResponsiveModalFooter>
      </ResponsiveModal>,
    );

    const dialog = screen.getByRole("dialog", { name: "Mobile friendly form" });
    expect(dialog).toHaveAttribute("data-slot", "sheet-content");
    expect(dialog).toHaveClass("inset-x-0", "bottom-0", "flex", "max-h-[100svh]");
    expect(screen.getByText("Scrollable content").parentElement).toHaveClass("flex-1", "overflow-y-auto");
    expect(screen.getByRole("button", { name: "Save" }).parentElement).toHaveClass("shrink-0", "border-t");
  });

  it("renders a centered dialog on desktop", () => {
    setMobileMatch(false);

    render(
      <ResponsiveModal open onOpenChange={vi.fn()} desktopClassName="sm:max-w-xl">
        <ResponsiveModalHeader>
          <ResponsiveModalTitle>Desktop form</ResponsiveModalTitle>
          <ResponsiveModalDescription>Configure the desktop form.</ResponsiveModalDescription>
        </ResponsiveModalHeader>
        <ResponsiveModalBody>
          <p>Dialog content</p>
        </ResponsiveModalBody>
        <ResponsiveModalFooter>
          <button type="button">Save</button>
        </ResponsiveModalFooter>
      </ResponsiveModal>,
    );

    const dialog = screen.getByRole("dialog", { name: "Desktop form" });
    expect(dialog).toHaveAttribute("data-slot", "dialog-content");
    expect(dialog).toHaveClass("sm:max-w-xl", "flex", "max-h-[calc(100svh-2rem)]");
    expect(screen.getByText("Dialog content").parentElement).toHaveClass("flex-1", "overflow-y-auto");
  });

  it("passes open state changes through from the close button", async () => {
    setMobileMatch(false);
    const onOpenChange = vi.fn();
    const user = userEvent.setup();

    render(
      <ResponsiveModal open onOpenChange={onOpenChange}>
        <ResponsiveModalHeader>
          <ResponsiveModalTitle>Closable form</ResponsiveModalTitle>
          <ResponsiveModalDescription>Close the form.</ResponsiveModalDescription>
        </ResponsiveModalHeader>
        <ResponsiveModalBody>
          <p>Content</p>
        </ResponsiveModalBody>
      </ResponsiveModal>,
    );

    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
