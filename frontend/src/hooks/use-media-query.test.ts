import { createElement } from "react";
import { hydrateRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useMediaQuery } from "./use-media-query";

const MOBILE_QUERY = "(max-width: 767px)";

function MediaQueryProbe() {
  return createElement("span", null, useMediaQuery(MOBILE_QUERY) ? "mobile" : "desktop");
}

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
      const matches = useMediaQuery(MOBILE_QUERY);
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

    const { result, unmount } = renderHook(() => useMediaQuery(MOBILE_QUERY));

    expect(result.current).toBe(true);
    expect(addListener).toHaveBeenCalledTimes(1);

    unmount();

    expect(removeListener).toHaveBeenCalledTimes(1);
  });

  it("hydrates with the server snapshot before applying the mobile match", async () => {
    const originalMatchMedia = Object.getOwnPropertyDescriptor(window, "matchMedia");
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      configurable: true,
      value: undefined,
    });
    const serverMarkup = renderToString(createElement(MediaQueryProbe));

    Object.defineProperty(window, "matchMedia", {
      writable: true,
      configurable: true,
      value: vi.fn().mockImplementation(() => ({
        matches: true,
        media: MOBILE_QUERY,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    });

    const container = document.createElement("div");
    container.innerHTML = serverMarkup;
    document.body.appendChild(container);
    const onRecoverableError = vi.fn();
    let root: Root | undefined;

    try {
      await act(async () => {
        root = hydrateRoot(container, createElement(MediaQueryProbe), { onRecoverableError });
      });

      expect(onRecoverableError).not.toHaveBeenCalled();
      await waitFor(() => expect(container).toHaveTextContent("mobile"));
    } finally {
      await act(async () => root?.unmount());
      container.remove();
      if (originalMatchMedia) {
        Object.defineProperty(window, "matchMedia", originalMatchMedia);
      } else {
        Object.defineProperty(window, "matchMedia", {
          writable: true,
          configurable: true,
          value: undefined,
        });
      }
    }
  });
});
