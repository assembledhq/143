import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ClaudeCodeAuthModal } from "./claude-code-auth-modal";

const { initiateMock, completeMock, captureErrorMock } = vi.hoisted(() => ({
  initiateMock: vi.fn(),
  completeMock: vi.fn(),
  captureErrorMock: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api: {
    claudeCodeAuth: {
      initiate: initiateMock,
      complete: completeMock,
    },
  },
}));

vi.mock("@/lib/errors", () => ({
  captureError: captureErrorMock,
}));

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

describe("ClaudeCodeAuthModal", () => {
  beforeEach(() => {
    initiateMock.mockReset();
    initiateMock.mockResolvedValue({
      data: {
        authorize_url: "https://claude.ai/oauth/authorize",
        state: "state-123",
      },
    });
    completeMock.mockReset();
    completeMock.mockResolvedValue({
      data: {
        account_type: "max",
      },
    });
    captureErrorMock.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("uses the shared mobile sheet layout", async () => {
    setMobileMatch(true);

    render(<ClaudeCodeAuthModal label="team-a" onClose={vi.fn()} />);

    const dialog = await screen.findByRole("dialog", { name: "Connect your Claude subscription" });
    expect(dialog).toHaveAttribute("data-slot", "sheet-content");
    expect(dialog).toHaveClass("max-h-[100svh]", "overflow-hidden");
  });

  it("calls onClose when Escape is pressed", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();

    render(<ClaudeCodeAuthModal label="team-a" onClose={onClose} />);

    await waitFor(() => {
      expect(initiateMock).toHaveBeenCalledWith("team-a", undefined);
    });

    await user.keyboard("{Escape}");

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("clears the delayed onConnected callback on unmount", async () => {
    const onConnected = vi.fn();

    const { unmount } = render(
      <ClaudeCodeAuthModal label="team-a" onClose={vi.fn()} onConnected={onConnected} />,
    );

    expect(await screen.findByPlaceholderText("e.g. abc123#xyz789")).toBeInTheDocument();

    fireEvent.change(screen.getByPlaceholderText("e.g. abc123#xyz789"), {
      target: { value: "abc123#state456" },
    });

    vi.useFakeTimers();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Connect" }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(completeMock).toHaveBeenCalledWith("team-a", "abc123#state456", undefined);
    expect(screen.getByText("Connected successfully!")).toBeInTheDocument();

    unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1200);
    });

    expect(onConnected).not.toHaveBeenCalled();
  });
});
