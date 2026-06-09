import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CopyButton } from "./copy-button";

const { canCopyToClipboardMock, copyTextToClipboardMock } = vi.hoisted(() => ({
  canCopyToClipboardMock: vi.fn(() => true),
  copyTextToClipboardMock: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/lib/clipboard", () => ({
  canCopyToClipboard: canCopyToClipboardMock,
  copyTextToClipboard: copyTextToClipboardMock,
}));

describe("CopyButton", () => {
  beforeEach(() => {
    canCopyToClipboardMock.mockReturnValue(true);
    copyTextToClipboardMock.mockClear();
    copyTextToClipboardMock.mockResolvedValue(undefined);
  });

  it("copies text and shows copied feedback", async () => {
    copyTextToClipboardMock.mockResolvedValue(undefined);

    render(<CopyButton value="203.0.113.10" label="Copy public IP" />);

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Copy public IP" }));

    expect(copyTextToClipboardMock).toHaveBeenCalledWith("203.0.113.10");
    expect(screen.getByRole("button", { name: "Copied public IP" })).toBeInTheDocument();

    await waitFor(
      () => {
        expect(screen.getByRole("button", { name: "Copy public IP" })).toBeInTheDocument();
      },
      { timeout: 1800 },
    );
  });

  it("disables copying when no value is provided", () => {
    render(<CopyButton value="" label="Copy public IP" />);

    expect(screen.getByRole("button", { name: "Copy public IP" })).toBeDisabled();
  });

  it("disables copying when the clipboard API is unavailable", () => {
    canCopyToClipboardMock.mockReturnValue(false);

    render(<CopyButton value="203.0.113.10" label="Copy public IP" />);

    expect(screen.getByRole("button", { name: "Copy public IP" })).toBeDisabled();
  });
});
