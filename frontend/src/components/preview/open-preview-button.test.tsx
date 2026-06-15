import { act } from "react";
import { describe, expect, it, vi, afterEach } from "vitest";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";

import { renderWithProviders, screen } from "@/test/test-utils";
import { OpenPreviewButton } from "./open-preview-button";

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
  },
}));

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("OpenPreviewButton", () => {
  it("closes the placeholder popup when bootstrap does not respond quickly", async () => {
    const user = userEvent.setup();
    const close = vi.fn();
    const documentWrite = vi.fn();
    const documentClose = vi.fn();
    const popup = {
      opener: window,
      close,
      document: {
        write: documentWrite,
        close: documentClose,
      },
      location: {
        href: "",
      },
    } as unknown as Window;
    vi.spyOn(window, "open").mockReturnValue(popup);

    renderWithProviders(
      <OpenPreviewButton
        previewId="prev-1"
        previewUrl="https://prev-1.preview.143.dev"
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open preview" }));

    expect(documentWrite).toHaveBeenCalledTimes(1);
    const writtenDoc = documentWrite.mock.calls[0][0] as string;
    expect(writtenDoc).toContain("Opening preview");
    expect(writtenDoc).toContain("class=\"spinner\"");

    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 5_100));
    });

    expect(close).toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalledWith("Preview bootstrap timed out. Try opening it again.");
    expect(screen.getByRole("button", { name: "Open preview" })).toBeEnabled();
  }, 10_000);
});
