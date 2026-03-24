import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { AgentSettingsModal } from "./agent-settings-modal";

// Mock the heavy AgentSettingsEditor to keep the test focused on dialog behavior.
vi.mock("@/components/agent-settings-editor", () => ({
  AgentSettingsEditor: () => (
    <div data-testid="agent-settings-editor">editor stub</div>
  ),
}));

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

describe("AgentSettingsModal", () => {
  it("renders as a dialog with accessible title", () => {
    renderWithProviders(<AgentSettingsModal onClose={vi.fn()} />);

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    // sr-only title should still be accessible
    expect(screen.getByText("Configure coding agent")).toBeInTheDocument();
  });

  it("calls onClose when the close button is clicked", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(<AgentSettingsModal onClose={onClose} />);

    // The DialogContent includes a Close button with sr-only "Close" text
    await user.click(screen.getByRole("button", { name: "Close" }));

    await waitFor(() => {
      expect(onClose).toHaveBeenCalledTimes(1);
    });
  });

  it("calls onClose when Escape is pressed", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(<AgentSettingsModal onClose={onClose} />);

    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    await waitFor(() => {
      expect(onClose).toHaveBeenCalledTimes(1);
    });
  });
});
