import { describe, expect, it, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { ResizeHandle } from "./resize-handle";

describe("ResizeHandle", () => {
  it("renders without crashing", () => {
    const onResize = vi.fn();
    const { container } = render(<ResizeHandle onResize={onResize} />);
    expect(container.firstChild).toBeTruthy();
  });

  it("calls onResize with delta during drag", () => {
    const onResize = vi.fn();
    const { container } = render(<ResizeHandle onResize={onResize} />);
    const handle = container.firstChild as HTMLElement;

    // Start drag at x=100
    fireEvent.mouseDown(handle, { clientX: 100 });

    // Move to x=120 → delta = 20
    fireEvent.mouseMove(document, { clientX: 120 });
    expect(onResize).toHaveBeenCalledWith(20);

    // Move again to x=115 → delta = -5
    fireEvent.mouseMove(document, { clientX: 115 });
    expect(onResize).toHaveBeenCalledWith(-5);
  });

  it("does not call onResize when not dragging", () => {
    const onResize = vi.fn();
    render(<ResizeHandle onResize={onResize} />);

    fireEvent.mouseMove(document, { clientX: 200 });
    expect(onResize).not.toHaveBeenCalled();
  });

  it("stops calling onResize after mouseUp", () => {
    const onResize = vi.fn();
    const { container } = render(<ResizeHandle onResize={onResize} />);
    const handle = container.firstChild as HTMLElement;

    fireEvent.mouseDown(handle, { clientX: 100 });
    fireEvent.mouseMove(document, { clientX: 110 });
    expect(onResize).toHaveBeenCalledTimes(1);

    fireEvent.mouseUp(document);
    onResize.mockClear();

    fireEvent.mouseMove(document, { clientX: 120 });
    expect(onResize).not.toHaveBeenCalled();
  });

  it("sets body cursor during drag", () => {
    const onResize = vi.fn();
    const { container } = render(<ResizeHandle onResize={onResize} />);
    const handle = container.firstChild as HTMLElement;

    fireEvent.mouseDown(handle, { clientX: 100 });
    expect(document.body.style.cursor).toBe("col-resize");
    expect(document.body.style.userSelect).toBe("none");

    fireEvent.mouseUp(document);
    expect(document.body.style.cursor).toBe("");
    expect(document.body.style.userSelect).toBe("");
  });
});
