import { render, screen } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useInView } from "./use-in-view";

function HookProbe() {
  const { inView } = useInView();
  return <div data-testid="hook-probe" data-in-view={String(inView)} />;
}

describe("useInView", () => {
  const originalIntersectionObserver = globalThis.IntersectionObserver;
  const originalMatchMedia = window.matchMedia;

  beforeEach(() => {
    globalThis.IntersectionObserver = vi.fn(() => ({
      observe: vi.fn(),
      disconnect: vi.fn(),
      unobserve: vi.fn(),
      takeRecords: vi.fn(() => []),
      root: null,
      rootMargin: "",
      thresholds: [],
    })) as unknown as typeof IntersectionObserver;
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
});
