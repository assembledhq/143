import { afterEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";
import { renderToString } from "react-dom/server";
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
    // Both panes are mounted; content is display:none below md.
    const sidebar = getByTestId("sidebar").closest("div[style]");
    const content = getByTestId("content").parentElement;
    expect(sidebar?.className).not.toContain("hidden");
    expect(content?.className).toContain("hidden");
    expect(content?.className).toContain("md:block");
  });

  it('hides the sidebar pane on mobile when mobileShow="content"', () => {
    const { getByTestId } = renderLayout("content");
    const sidebar = getByTestId("sidebar").closest("div[style]");
    const content = getByTestId("content").parentElement;
    expect(sidebar?.className).toContain("hidden");
    expect(sidebar?.className).toContain("md:block");
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
});
