import { afterEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import Link from "next/link";
import { SidebarLayout } from "./sidebar-layout";

function renderLayout(mobileShow?: "sidebar" | "content") {
  return render(
    <SidebarLayout
      sidebar={<div data-testid="sidebar">sidebar</div>}
      mobileShow={mobileShow}
    >
      <div data-testid="content">content</div>
    </SidebarLayout>,
  );
}

function setCompactDesktopViewport(matches: boolean): () => void {
  const original = Object.getOwnPropertyDescriptor(window, "matchMedia");
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === "(min-width: 768px) and (max-width: 1279px)" ? matches : false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
  return () => {
    if (original !== undefined) {
      Object.defineProperty(window, "matchMedia", original);
    } else {
      Object.defineProperty(window, "matchMedia", {
        writable: true,
        configurable: true,
        value: undefined,
      });
    }
  };
}

describe("SidebarLayout", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("uses the default desktop sidebar width", () => {
    const { container } = renderLayout("sidebar");

    const sidebar = container.querySelector("[data-testid='sidebar-pane']");
    expect(sidebar).toHaveStyle({ "--sidebar-w": "320px" });
  });

  it("renders the default desktop sidebar width during SSR even when storage has a saved width", () => {
    window.localStorage.setItem("143:sidebar-layout-width", "360");

    const markup = renderToString(
      <SidebarLayout sidebar={<div>sidebar</div>} mobileShow="sidebar">
        <div>content</div>
      </SidebarLayout>,
    );

    expect(markup).toContain("--sidebar-w:320px");
  });

  it("restores the desktop sidebar width from localStorage after mount", async () => {
    window.localStorage.setItem("143:sidebar-layout-width", "360");

    const { container } = renderLayout("sidebar");

    const sidebar = container.querySelector("[data-testid='sidebar-pane']");
    await waitFor(() => {
      expect(sidebar).toHaveStyle({ "--sidebar-w": "360px" });
    });
  });

  it("falls back to the default width when localStorage read throws", () => {
    const getItemSpy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage blocked");
    });

    const { container } = renderLayout("sidebar");

    const sidebar = container.querySelector("[data-testid='sidebar-pane']");
    expect(sidebar).toHaveStyle({ "--sidebar-w": "320px" });

    getItemSpy.mockRestore();
  });

  it("does not crash when localStorage write throws during resize", () => {
    const setItemSpy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage blocked");
    });

    const { container } = renderLayout("sidebar");
    const handle = container.querySelector("[data-testid='resize-handle']");
    const sidebar = container.querySelector("[data-testid='sidebar-pane']");
    expect(handle).not.toBeNull();

    expect(() => {
      fireEvent.pointerDown(handle!, { clientX: 100, pointerId: 1, button: 0 });
      fireEvent.pointerMove(document, { clientX: 140, pointerId: 1 });
      fireEvent.pointerUp(document, { pointerId: 1 });
    }).not.toThrow();
    expect(sidebar).toHaveStyle({ "--sidebar-w": "360px" });

    setItemSpy.mockRestore();
  });

  it("persists a resized desktop sidebar width and clamps it to the max", () => {
    const { container } = renderLayout("sidebar");

    const handle = container.querySelector("[data-testid='resize-handle']");
    const sidebar = container.querySelector("[data-testid='sidebar-pane']");
    expect(handle).not.toBeNull();

    fireEvent.pointerDown(handle!, { clientX: 100, pointerId: 1, button: 0 });
    fireEvent.pointerMove(document, { clientX: 220, pointerId: 1 });
    fireEvent.pointerUp(document, { pointerId: 1 });

    expect(sidebar).toHaveStyle({ "--sidebar-w": "400px" });
    expect(window.localStorage.getItem("143:sidebar-layout-width")).toBe("400");
  });

  it("clamps a resized desktop sidebar width to the min", () => {
    const { container } = renderLayout("sidebar");

    const handle = container.querySelector("[data-testid='resize-handle']");
    const sidebar = container.querySelector("[data-testid='sidebar-pane']");
    expect(handle).not.toBeNull();

    fireEvent.pointerDown(handle!, { clientX: 300, pointerId: 1, button: 0 });
    fireEvent.pointerMove(document, { clientX: 120, pointerId: 1 });
    fireEvent.pointerUp(document, { pointerId: 1 });

    expect(sidebar).toHaveStyle({ "--sidebar-w": "240px" });
    expect(window.localStorage.getItem("143:sidebar-layout-width")).toBe("240");
  });

  it('hides the content pane on mobile when mobileShow="sidebar"', () => {
    const { getByTestId } = renderLayout("sidebar");
    // Both panes are mounted. The sidebar pane is visible on mobile, compact
    // desktop list routes, and at xl+; route content comes back at xl+.
    const sidebar = getByTestId("sidebar").closest("div[style]");
    const content = getByTestId("content").parentElement;
    expect(sidebar).toHaveClass("block");
    expect(sidebar).toHaveClass("md:hidden");
    expect(sidebar).toHaveClass("xl:block");
    expect(content?.className).toContain("hidden");
    expect(content?.className).toContain("xl:block");
  });

  it('hides the sidebar pane on mobile when mobileShow="content"', () => {
    const { getByTestId } = renderLayout("content");
    const sidebar = getByTestId("sidebar").closest("div[style]");
    const content = getByTestId("content").parentElement;
    expect(sidebar?.className).toContain("hidden");
    expect(sidebar?.className).toContain("xl:block");
    expect(content?.className).not.toContain("hidden");
  });

  it('defaults mobileShow to "sidebar"', () => {
    const { getByTestId } = renderLayout();
    const content = getByTestId("content").parentElement;
    expect(content?.className).toContain("hidden");
  });

  it("contains overscroll inside the session panes", () => {
    const { container, getByTestId } = renderLayout("content");

    const shell = container.firstElementChild;
    expect(shell).toHaveClass("overflow-hidden");
    expect(shell).toHaveClass("overscroll-none");

    const content = getByTestId("content").parentElement;
    expect(content).toHaveClass("overscroll-contain");
  });

  it("keeps the compact session list as a full-height pane on list routes", () => {
    const { getByTestId } = renderLayout("sidebar");

    const compactPane = getByTestId("compact-sidebar-pane");
    expect(compactPane).toHaveClass("hidden");
    expect(compactPane).toHaveClass("md:block");
    expect(compactPane).toHaveClass("xl:hidden");
    expect(compactPane).not.toHaveClass("w-0");

    const switcher = getByTestId("session-switcher-rail");
    expect(switcher).toHaveClass("hidden");
  });

  it("collapses the compact session list to a rail on detail routes", () => {
    const { getByTestId } = renderLayout("content");

    const sidebar = getByTestId("sidebar").closest("[data-testid='sidebar-pane']");
    expect(sidebar).toHaveClass("xl:block");
    expect(sidebar).toHaveClass("hidden");

    const compactPane = getByTestId("compact-sidebar-pane");
    expect(compactPane).toHaveClass("hidden");
    expect(compactPane).toHaveClass("md:block");
    expect(compactPane).toHaveClass("xl:hidden");
    expect(compactPane).toHaveClass("w-0");

    const switcher = getByTestId("session-switcher-rail");
    expect(switcher).toHaveClass("hidden");
    expect(switcher).toHaveClass("md:flex");
    expect(switcher).toHaveClass("xl:hidden");
  });

  it("keeps the mobile sessions list available below the compact breakpoint", () => {
    const { getByTestId } = renderLayout("sidebar");

    const sidebar = getByTestId("sidebar").closest("[data-testid='sidebar-pane']");
    expect(sidebar).toHaveClass("block");
    expect(sidebar).toHaveClass("md:hidden");
    expect(sidebar).toHaveClass("xl:block");
  });

  it("opens the compact session list as a pane and closes it after a session link is selected", async () => {
    const restoreMatchMedia = setCompactDesktopViewport(true);
    try {
      render(
        <SidebarLayout
          sidebar={<Link href="/sessions/session-1">Session one</Link>}
          mobileShow="content"
        >
          <div data-testid="content">content</div>
        </SidebarLayout>,
      );

      fireEvent.click(screen.getByRole("button", { name: "Open session switcher" }));
      expect(screen.getByTestId("compact-sidebar-pane")).not.toHaveClass("w-0");
      expect(await screen.findByRole("link", { name: "Session one" })).toBeInTheDocument();

      fireEvent.click(screen.getByRole("link", { name: "Session one" }));

      await waitFor(() => {
        expect(screen.getByTestId("compact-sidebar-pane")).toHaveClass("w-0");
      });
    } finally {
      restoreMatchMedia();
    }
  });
});
