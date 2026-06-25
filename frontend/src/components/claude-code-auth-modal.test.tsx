import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ClaudeCodeAuthModal } from "./claude-code-auth-modal";

const { initiateMock, completeMock, storeOAuthTokenMock, captureErrorMock } = vi.hoisted(() => ({
  initiateMock: vi.fn(),
  completeMock: vi.fn(),
  storeOAuthTokenMock: vi.fn(),
  captureErrorMock: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api: {
    claudeCodeAuth: {
      initiate: initiateMock,
      complete: completeMock,
      storeOAuthToken: storeOAuthTokenMock,
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
    storeOAuthTokenMock.mockReset();
    storeOAuthTokenMock.mockResolvedValue({
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
    expect(screen.getByText("claude setup-token")).toBeInTheDocument();
    expect(initiateMock).not.toHaveBeenCalled();
  });

  it("calls onClose when Escape is pressed", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();

    render(<ClaudeCodeAuthModal label="team-a" onClose={onClose} />);

    await waitFor(() => {
      expect(screen.getByText("claude setup-token")).toBeInTheDocument();
    });

    await user.keyboard("{Escape}");

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("stores a setup token and clears the delayed onConnected callback on unmount", async () => {
    const onConnected = vi.fn();

    const { unmount } = render(
      <ClaudeCodeAuthModal label="team-a" onClose={vi.fn()} onConnected={onConnected} />,
    );

    expect(await screen.findByPlaceholderText("Paste the token from claude setup-token")).toBeInTheDocument();

    fireEvent.change(screen.getByPlaceholderText("Paste the token from claude setup-token"), {
      target: { value: "claude-setup-token" },
    });

    vi.useFakeTimers();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Connect" }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(storeOAuthTokenMock).toHaveBeenCalledWith("team-a", "claude-setup-token", undefined);
    expect(screen.getByText("Connected successfully!")).toBeInTheDocument();

    unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1200);
    });

    expect(onConnected).not.toHaveBeenCalled();
  });

  it("keeps the browser login fallback flow", async () => {
    const user = userEvent.setup();

    render(<ClaudeCodeAuthModal label="team-a" onClose={vi.fn()} />);

    await user.click(await screen.findByRole("button", { name: "Use browser login instead" }));

    await waitFor(() => {
      expect(initiateMock).toHaveBeenCalledWith("team-a", undefined);
    });
    expect(await screen.findByPlaceholderText("e.g. abc123#xyz789")).toBeInTheDocument();

    fireEvent.change(screen.getByPlaceholderText("e.g. abc123#xyz789"), {
      target: { value: "abc123#state456" },
    });
    await user.click(screen.getByRole("button", { name: "Connect" }));

    expect(completeMock).toHaveBeenCalledWith("team-a", "abc123#state456", undefined);
  });
});
