import { describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { useMediaQuery } from "./use-media-query";

describe("useMediaQuery", () => {
  it("reads the current match state on the first render", () => {
    const seen: boolean[] = [];

    Object.defineProperty(window, "matchMedia", {
      writable: true,
      configurable: true,
      value: vi.fn().mockImplementation(() => ({
        matches: true,
        media: "(max-width: 767px)",
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    });

    renderHook(() => {
      const matches = useMediaQuery("(max-width: 767px)");
      seen.push(matches);
      return matches;
    });

    expect(seen[0]).toBe(true);
  });

  it("falls back to addListener/removeListener when addEventListener is unavailable", () => {
    const addListener = vi.fn();
    const removeListener = vi.fn();

    Object.defineProperty(window, "matchMedia", {
      writable: true,
      configurable: true,
      value: vi.fn().mockImplementation(() => ({
        matches: true,
        media: "(max-width: 767px)",
        onchange: null,
        addListener,
        removeListener,
        dispatchEvent: vi.fn(),
      })),
    });

    const { result, unmount } = renderHook(() => useMediaQuery("(max-width: 767px)"));

    expect(result.current).toBe(true);
    expect(addListener).toHaveBeenCalledTimes(1);

    unmount();

    expect(removeListener).toHaveBeenCalledTimes(1);
  });
});
