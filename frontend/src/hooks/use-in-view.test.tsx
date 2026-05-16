import { render, screen } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useInView } from "./use-in-view";

function HookProbe() {
  const { ref, inView } = useInView();
  return <div ref={ref} data-testid="hook-probe" data-in-view={String(inView)} />;
}

describe("useInView", () => {
  const originalIntersectionObserver = globalThis.IntersectionObserver;
  const originalMatchMedia = window.matchMedia;
  const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;

  beforeEach(() => {
    class MockIntersectionObserver implements IntersectionObserver {
      readonly root = null;
      readonly rootMargin = "";
      readonly thresholds = [];
      disconnect = vi.fn();
      observe = vi.fn();
      takeRecords = vi.fn(() => []);
      unobserve = vi.fn();
    }

    globalThis.IntersectionObserver = MockIntersectionObserver as unknown as typeof IntersectionObserver;
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    });
  });

  afterEach(() => {
    globalThis.IntersectionObserver = originalIntersectionObserver;
    window.matchMedia = originalMatchMedia;
    HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
  });

  it("renders visible content on the server before hydration", () => {
    const originalWindow = globalThis.window;

    // Match the hook's server-render branch even though the test suite runs in
    // jsdom by default.
    Reflect.deleteProperty(globalThis, "window");
    const html = renderToString(<HookProbe />);
    globalThis.window = originalWindow;

    expect(html).toContain('data-in-view="true"');
  });

  it("renders the same visible state on the first client paint", () => {
    render(<HookProbe />);

    expect(screen.getByTestId("hook-probe")).toHaveAttribute("data-in-view", "true");
  });

  it("marks below-the-fold content as not in view after layout measurement", () => {
    HTMLElement.prototype.getBoundingClientRect = vi.fn(() => ({
      x: 0,
      y: 900,
      top: 900,
      left: 0,
      bottom: 1100,
      right: 200,
      width: 200,
      height: 200,
      toJSON: () => ({}),
    }));

    render(<HookProbe />);

    expect(screen.getByTestId("hook-probe")).toHaveAttribute("data-in-view", "false");
  });
});
