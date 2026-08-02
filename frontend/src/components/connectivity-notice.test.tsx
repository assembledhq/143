import { act, render, screen } from "@testing-library/react";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectivityNotice } from "./connectivity-notice";

const notifySuccess = vi.hoisted(() => vi.fn());

vi.mock("@/lib/notify", () => ({
  notify: {
    success: notifySuccess,
  },
}));

describe("ConnectivityNotice", () => {
  const originalOnlineDescriptor = Object.getOwnPropertyDescriptor(
    Navigator.prototype,
    "onLine",
  );
  let isOnline = true;

  beforeAll(() => {
    Object.defineProperty(Navigator.prototype, "onLine", {
      configurable: true,
      get: () => isOnline,
    });
  });

  afterAll(() => {
    if (originalOnlineDescriptor) {
      Object.defineProperty(
        Navigator.prototype,
        "onLine",
        originalOnlineDescriptor,
      );
    }
  });

  beforeEach(() => {
    isOnline = true;
    notifySuccess.mockReset();
  });

  it("keeps the current view available while clearly explaining the offline state", () => {
    isOnline = false;
    render(<ConnectivityNotice />);

    const notice = screen.getByRole("status");
    expect(notice).toHaveTextContent("You're offline");
    expect(notice).toHaveTextContent(
      "You can keep reading this view",
    );
    expect(notice).toHaveClass(
      "pointer-events-none",
      "top-[calc(env(safe-area-inset-top)+4rem)]",
      "z-40",
      "md:top-4",
    );
    expect(notice).not.toHaveClass("bottom-3", "z-50");
  });

  it("confirms recovery after the browser reconnects", () => {
    isOnline = false;
    render(<ConnectivityNotice />);

    act(() => {
      isOnline = true;
      window.dispatchEvent(new Event("online"));
    });

    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(notifySuccess).toHaveBeenCalledWith("Back online", {
      description: "Live data and network actions are available again.",
    });
  });
});
