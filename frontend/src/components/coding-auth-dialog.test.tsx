import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { CodingAuthDialog } from "./coding-auth-dialog";

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

describe("CodingAuthDialog", () => {
  it("uses the shared mobile sheet layout so the submit action remains reachable", async () => {
    setMobileMatch(true);
    const onPrimary = vi.fn();
    const user = userEvent.setup();

    render(
      <CodingAuthDialog
        open
        onOpenChange={vi.fn()}
        title="Add auth"
        description="Add access for a coding agent."
        providerOptions={[
          { key: "codex", label: "Codex" },
          { key: "claude", label: "Claude" },
        ]}
        provider="codex"
        onProviderChange={vi.fn()}
        primaryLabel="Save auth"
        onPrimary={onPrimary}
        onCancel={vi.fn()}
      >
        <div>Credential fields</div>
      </CodingAuthDialog>,
    );

    const dialog = screen.getByRole("dialog", { name: "Add auth" });
    expect(dialog).toHaveAttribute("data-slot", "sheet-content");
    expect(dialog).toHaveClass("max-h-[100svh]", "overflow-hidden");
    expect(dialog.querySelector('[data-slot="responsive-modal-body"]')).toHaveClass("overflow-y-auto");

    await user.click(screen.getByRole("button", { name: "Save auth" }));

    expect(onPrimary).toHaveBeenCalledTimes(1);
  });
});
