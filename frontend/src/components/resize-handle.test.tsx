import { describe, expect, it, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { ResizeHandle } from "./resize-handle";

describe("ResizeHandle", () => {
  it("renders without crashing", () => {
    const onResize = vi.fn();
    const { container } = render(<ResizeHandle onResize={onResize} />);
    expect(container.firstChild).toBeTruthy();
  });

  it("renders a full-height desktop rail with a wider hit target", () => {
    const onResize = vi.fn();
    const { container } = render(<ResizeHandle onResize={onResize} />);
    const handle = container.firstChild as HTMLElement;
    const grip = handle.querySelector("[data-testid='resize-handle-grip']");

    expect(handle.className).toContain("w-3");
    expect(handle.className).toContain("h-full");
    expect(handle.className).toContain("cursor-col-resize");
    expect(handle.querySelector("[data-testid='resize-handle-rail']")).toBeTruthy();
    expect(grip).toBeTruthy();
    expect(grip?.className).toContain("opacity-0");
    expect(grip?.className).toContain("group-hover:opacity-100");
    expect(grip?.className).toContain("group-focus-visible:opacity-100");
    expect(grip?.className).toContain("group-data-[dragging=true]:opacity-100");
  });

  it("calls onResize with delta during drag", () => {
    const onResize = vi.fn();
    const { container } = render(<ResizeHandle onResize={onResize} />);
    const handle = container.firstChild as HTMLElement;

    fireEvent.pointerDown(handle, { clientX: 100, pointerId: 1, button: 0 });

    fireEvent.pointerMove(document, { clientX: 120, pointerId: 1 });
    expect(onResize).toHaveBeenCalledWith(20);

    fireEvent.pointerMove(document, { clientX: 115, pointerId: 1 });
    expect(onResize).toHaveBeenCalledWith(-5);
  });

  it("does not call onResize when not dragging", () => {
    const onResize = vi.fn();
    render(<ResizeHandle onResize={onResize} />);

    fireEvent.pointerMove(document, { clientX: 200, pointerId: 1 });
    expect(onResize).not.toHaveBeenCalled();
  });

  it("stops calling onResize after pointerUp", () => {
    const onResize = vi.fn();
    const { container } = render(<ResizeHandle onResize={onResize} />);
    const handle = container.firstChild as HTMLElement;

    fireEvent.pointerDown(handle, { clientX: 100, pointerId: 1, button: 0 });
    fireEvent.pointerMove(document, { clientX: 110, pointerId: 1 });
    expect(onResize).toHaveBeenCalledTimes(1);

    fireEvent.pointerUp(document, { pointerId: 1 });
    onResize.mockClear();

    fireEvent.pointerMove(document, { clientX: 120, pointerId: 1 });
    expect(onResize).not.toHaveBeenCalled();
  });

  it("sets body cursor during drag", () => {
    const onResize = vi.fn();
    const { container } = render(<ResizeHandle onResize={onResize} />);
    const handle = container.firstChild as HTMLElement;

    fireEvent.pointerDown(handle, { clientX: 100, pointerId: 1, button: 0 });
    expect(document.body.style.cursor).toBe("col-resize");
    expect(document.body.style.userSelect).toBe("none");

    fireEvent.pointerUp(document, { pointerId: 1 });
    expect(document.body.style.cursor).toBe("");
    expect(document.body.style.userSelect).toBe("");
  });

  it("resets body drag styles when unmounted during an active drag", () => {
    const onResize = vi.fn();
    const { container, unmount } = render(<ResizeHandle onResize={onResize} />);
    const handle = container.firstChild as HTMLElement;

    fireEvent.pointerDown(handle, { clientX: 100, pointerId: 1, button: 0 });
    expect(document.body.style.cursor).toBe("col-resize");
    expect(document.body.style.userSelect).toBe("none");

    unmount();

    expect(document.body.style.cursor).toBe("");
    expect(document.body.style.userSelect).toBe("");
  });
});
