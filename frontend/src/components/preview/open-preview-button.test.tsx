import { act } from "react";
import { describe, expect, it, vi, afterEach } from "vitest";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";

import { fireEvent, renderWithProviders, screen, waitFor } from "@/test/test-utils";
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
  it("keeps the placeholder popup open with timeout details when bootstrap does not respond", async () => {
    vi.useFakeTimers();
    const close = vi.fn();
    const documentOpen = vi.fn();
    const documentWrite = vi.fn();
    const documentClose = vi.fn();
    const popup = {
      opener: window,
      close,
      document: {
        open: documentOpen,
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

    fireEvent.click(screen.getByRole("button", { name: "Open preview" }));

    expect(documentWrite).toHaveBeenCalledTimes(1);
    const writtenDoc = documentWrite.mock.calls[0][0] as string;
    expect(writtenDoc).toContain("Opening preview");
    expect(writtenDoc).toContain("class=\"spinner\"");

    act(() => {
      vi.advanceTimersByTime(5_100);
    });

    expect(close).not.toHaveBeenCalled();
    expect(documentOpen).toHaveBeenCalled();
    const timeoutDoc = documentWrite.mock.calls.at(-1)?.[0] as string;
    expect(timeoutDoc).toContain("Preview connection timed out");
    expect(timeoutDoc).toContain("The preview gateway did not answer");
    expect(timeoutDoc).toContain("prev-1");
    expect(timeoutDoc).toContain("https://prev-1.preview.143.dev");
    expect(toast.error).toHaveBeenCalledWith(
      "Preview connection timed out. The preview gateway did not answer in time.",
    );
    expect(screen.getByRole("button", { name: "Open preview" })).toBeEnabled();
  });

  it("keeps the button in its opening state until the popup preview load completes", async () => {
    const user = userEvent.setup();
    const popupListeners = new Map<string, EventListener>();
    const popup = {
      opener: window,
      close: vi.fn(),
      document: {
        write: vi.fn(),
        close: vi.fn(),
      },
      location: {
        href: "",
      },
      addEventListener: vi.fn((type: string, listener: EventListener) => {
        popupListeners.set(type, listener);
      }),
      removeEventListener: vi.fn((type: string) => {
        popupListeners.delete(type);
      }),
    } as unknown as Window;
    vi.spyOn(window, "open").mockReturnValue(popup);

    renderWithProviders(
      <OpenPreviewButton
        previewId="prev-1"
        previewUrl="https://prev-1.preview.143.dev"
        bootstrapPreview={() => Promise.resolve({ token: "tok-1" })}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open preview" }));
    expect(screen.getByRole("button", { name: "Opening..." })).toBeDisabled();

    await act(async () => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-1.preview.143.dev",
          data: { type: "preview_bootstrap_ready" },
        }),
      );
    });

    await act(async () => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-1.preview.143.dev",
          data: { type: "preview_bootstrap_complete" },
        }),
      );
    });

    await waitFor(() => {
      expect(popup.location.href).toBe("https://prev-1.preview.143.dev");
    });

    expect(screen.getByRole("button", { name: "Opening..." })).toBeDisabled();
    expect(popupListeners.has("load")).toBe(true);

    await act(async () => {
      popupListeners.get("load")?.(new Event("load"));
    });

    expect(screen.getByRole("button", { name: "Open preview" })).toBeEnabled();
    expect(popup.removeEventListener).toHaveBeenCalledWith(
      "load",
      expect.any(Function),
    );
  });

  it("recovers if the popup closes before the preview load event", async () => {
    const user = userEvent.setup();
    const popup = {
      opener: window,
      closed: false,
      close: vi.fn(),
      document: {
        write: vi.fn(),
        close: vi.fn(),
      },
      location: {
        href: "",
      },
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    } as unknown as Window;
    vi.spyOn(window, "open").mockReturnValue(popup);

    renderWithProviders(
      <OpenPreviewButton
        previewId="prev-1"
        previewUrl="https://prev-1.preview.143.dev"
        bootstrapPreview={() => Promise.resolve({ token: "tok-1" })}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open preview" }));

    await act(async () => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-1.preview.143.dev",
          data: { type: "preview_bootstrap_ready" },
        }),
      );
    });

    await act(async () => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-1.preview.143.dev",
          data: { type: "preview_bootstrap_complete" },
        }),
      );
    });

    expect(screen.getByRole("button", { name: "Opening..." })).toBeDisabled();

    Object.defineProperty(popup, "closed", {
      configurable: true,
      value: true,
    });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Open preview" })).toBeEnabled();
    });
  });
});
