import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { StatusIndicator } from "./status-indicator";

describe("StatusIndicator", () => {
  it("renders a semantic static tone without motion", () => {
    const { container } = render(<StatusIndicator tone="success" />);

    expect(container.querySelector('[data-slot="status-indicator-core"]')).toHaveClass("bg-success");
    expect(container.querySelector('[data-slot="status-indicator"]')).toHaveAttribute("data-activity", "none");
    expect(container.querySelector('[data-slot="status-indicator-halo"]')).not.toHaveClass("status-breathe-halo");
  });

  it("uses one breathing halo for persistent work", () => {
    const { container } = render(<StatusIndicator tone="primary" activity="breathing" />);

    expect(container.querySelector('[data-slot="status-indicator-halo"]')).toHaveClass("status-breathe-halo", "opacity-55");
    expect(container.querySelectorAll('[data-slot="status-indicator-core"]')).toHaveLength(1);
  });

  it("centers an indeterminate spinner in the same alignment box as a static dot", () => {
    const { container, rerender } = render(<StatusIndicator tone="primary" activity="indeterminate" />);

    expect(container.querySelector('[data-slot="status-indicator"]')).toHaveClass(
      "size-2",
      "items-center",
      "justify-center",
    );
    expect(container.querySelector('[data-slot="status-indicator-spinner"]')).toHaveClass("size-3");

    rerender(<StatusIndicator tone="success" />);
    expect(container.querySelector('[data-slot="status-indicator"]')).toHaveClass("size-2");
  });

  it("settles once when operational state changes", () => {
    const animate = vi.fn();
    const { container, rerender } = render(<StatusIndicator tone="primary" activity="breathing" stateKey="running" />);
    const core = container.querySelector('[data-slot="status-indicator-core"]');
    expect(core).toBeInstanceOf(HTMLElement);
    (core as HTMLElement).animate = animate;

    rerender(<StatusIndicator tone="success" stateKey="completed" />);
    expect(animate).toHaveBeenCalledOnce();
    expect(animate).toHaveBeenCalledWith(
      expect.any(Array),
      expect.objectContaining({ duration: 180 }),
    );
  });

  it("lands destructive states immediately", () => {
    const animate = vi.fn();
    const { container, rerender } = render(<StatusIndicator tone="primary" activity="breathing" stateKey="running" />);
    const core = container.querySelector('[data-slot="status-indicator-core"]');
    expect(core).toBeInstanceOf(HTMLElement);
    (core as HTMLElement).animate = animate;

    rerender(<StatusIndicator tone="destructive" stateKey="failed" />);
    expect(animate).not.toHaveBeenCalled();
  });

  it("does not animate state changes when reduced motion is requested", () => {
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue({ matches: true }));
    const animate = vi.fn();
    const { container, rerender } = render(<StatusIndicator tone="primary" activity="breathing" stateKey="running" />);
    const core = container.querySelector('[data-slot="status-indicator-core"]');
    expect(core).toBeInstanceOf(HTMLElement);
    (core as HTMLElement).animate = animate;

    rerender(<StatusIndicator tone="success" stateKey="completed" />);
    expect(animate).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
});
