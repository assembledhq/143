import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useScrollProgress } from "./use-scroll-progress";

const originalInnerHeight = window.innerHeight;
const originalMatchMedia = window.matchMedia;

function setReducedMotion(matches: boolean): void {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: vi.fn().mockReturnValue({
      matches,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }),
  });
}

describe("useScrollProgress", () => {
  afterEach(() => {
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: originalInnerHeight,
    });
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: originalMatchMedia,
    });
  });

  it("starts complete and skips listeners when reduced motion is enabled", () => {
    setReducedMotion(true);
    const addEventListener = vi.spyOn(window, "addEventListener");

    const { result } = renderHook(() => useScrollProgress());

    expect(result.current.progress).toBe(1);
    expect(addEventListener).not.toHaveBeenCalledWith(
      "scroll",
      expect.any(Function),
      expect.anything(),
    );
  });

  it("clamps progress between the configured viewport thresholds", () => {
    setReducedMotion(false);
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: 1000,
    });
    const { result } = renderHook(() =>
      useScrollProgress<HTMLDivElement>({ startViewport: 0.8, endViewport: 0.2 }),
    );
    const element = document.createElement("div");
    result.current.ref.current = element;

    vi.spyOn(element, "getBoundingClientRect").mockReturnValue({
      top: 500,
      bottom: 600,
      left: 0,
      right: 0,
      width: 100,
      height: 100,
      x: 0,
      y: 500,
      toJSON: () => ({}),
    });

    act(() => {
      window.dispatchEvent(new Event("scroll"));
    });

    expect(result.current.progress).toBeCloseTo(0.5);

    vi.mocked(element.getBoundingClientRect).mockReturnValue({
      top: -100,
      bottom: 0,
      left: 0,
      right: 0,
      width: 100,
      height: 100,
      x: 0,
      y: -100,
      toJSON: () => ({}),
    });
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(result.current.progress).toBe(1);

    vi.mocked(element.getBoundingClientRect).mockReturnValue({
      top: 900,
      bottom: 1000,
      left: 0,
      right: 0,
      width: 100,
      height: 100,
      x: 0,
      y: 900,
      toJSON: () => ({}),
    });
    act(() => {
      window.dispatchEvent(new Event("scroll"));
    });
    expect(result.current.progress).toBe(0);
  });
});
