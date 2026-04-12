import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import userEvent from "@testing-library/user-event";
import { DesignModeOverlay } from "./design-mode-overlay";
import { createRef } from "react";

const { inspectMock, feedbackMock } = vi.hoisted(() => ({
  inspectMock: vi.fn().mockResolvedValue({
    tag_name: "div",
    class_list: [],
    bounding_box: { x: 0, y: 0, width: 100, height: 50 },
    computed_styles: {},
    attributes: {},
    children_count: 0,
  }),
  feedbackMock: vi.fn().mockResolvedValue({}),
}));

vi.mock("@/lib/api", () => ({
  api: {
    sessions: {
      preview: {
        inspect: inspectMock,
        designFeedback: feedbackMock,
      },
    },
  },
}));

describe("DesignModeOverlay", () => {
  beforeEach(() => {
    inspectMock.mockClear();
    feedbackMock.mockClear();
  });

  it("renders the overlay with toolbar buttons", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    // Toolbar buttons have title attributes for tooltips
    expect(screen.getByTitle("Select element")).toBeInTheDocument();
  });

  it("renders annotation tool buttons", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    // Should have tool buttons for select, rectangle, arrow, freehand
    const buttons = screen.getAllByRole("button");
    expect(buttons.length).toBeGreaterThanOrEqual(4);
  });

  it("renders drawing tool buttons", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    expect(screen.getByTitle("Draw rectangle")).toBeInTheDocument();
    expect(screen.getByTitle("Draw arrow")).toBeInTheDocument();
    expect(screen.getByTitle("Freehand draw")).toBeInTheDocument();
  });

  it("tool buttons have correct titles", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    expect(screen.getByTitle("Select element")).toBeInTheDocument();
    expect(screen.getByTitle("Draw rectangle")).toBeInTheDocument();
    expect(screen.getByTitle("Draw arrow")).toBeInTheDocument();
    expect(screen.getByTitle("Freehand draw")).toBeInTheDocument();
  });

  it("select tool is active by default", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    const selectButton = screen.getByTitle("Select element");
    expect(selectButton.className).toMatch(/bg-primary/);

    // Other tool buttons should not have active styling
    const rectButton = screen.getByTitle("Draw rectangle");
    expect(rectButton.className).not.toMatch(/bg-primary/);
    const arrowButton = screen.getByTitle("Draw arrow");
    expect(arrowButton.className).not.toMatch(/bg-primary/);
    const freehandButton = screen.getByTitle("Freehand draw");
    expect(freehandButton.className).not.toMatch(/bg-primary/);
  });

  it("clicking a tool button changes active tool", async () => {
    const user = userEvent.setup();
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    const rectButton = screen.getByTitle("Draw rectangle");
    await user.click(rectButton);

    // Rectangle should now be active
    expect(rectButton.className).toMatch(/bg-primary/);
    // Select should no longer be active
    const selectButton = screen.getByTitle("Select element");
    expect(selectButton.className).not.toMatch(/bg-primary/);
  });

  it("clicking arrow tool makes it active and deactivates others", async () => {
    const user = userEvent.setup();
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    const arrowButton = screen.getByTitle("Draw arrow");
    await user.click(arrowButton);

    expect(arrowButton.className).toMatch(/bg-primary/);
    expect(screen.getByTitle("Select element").className).not.toMatch(/bg-primary/);
    expect(screen.getByTitle("Draw rectangle").className).not.toMatch(/bg-primary/);
    expect(screen.getByTitle("Freehand draw").className).not.toMatch(/bg-primary/);
  });

  it("clicking freehand tool makes it active", async () => {
    const user = userEvent.setup();
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    const freehandButton = screen.getByTitle("Freehand draw");
    await user.click(freehandButton);

    expect(freehandButton.className).toMatch(/bg-primary/);
    expect(screen.getByTitle("Select element").className).not.toMatch(/bg-primary/);
  });

  it("no clear annotations button when no annotations exist", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    expect(screen.queryByTitle("Clear annotations")).not.toBeInTheDocument();
  });

  it("renders the overlay container with correct classes", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    const { container } = renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    const overlayDiv = container.querySelector(".absolute.inset-0.z-10");
    expect(overlayDiv).toBeInTheDocument();
  });

  it("does not show element info panel when nothing is selected", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    // The instruction textarea should not be visible when no element is selected
    expect(
      screen.queryByPlaceholderText("Describe what to change...")
    ).not.toBeInTheDocument();
    // The "Send to agent" button should not be visible
    expect(screen.queryByText("Send to agent")).not.toBeInTheDocument();
  });

  it("does not show error banner initially", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    // No error banner should be visible
    expect(screen.queryByText(/Failed to/)).not.toBeInTheDocument();
  });

  it("renders the crosshair overlay for click capture", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    const { container } = renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    const crosshairDiv = container.querySelector(".cursor-crosshair");
    expect(crosshairDiv).toBeInTheDocument();
  });

  it("renders SVG layer for annotations", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    const { container } = renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    const svg = container.querySelector("svg");
    expect(svg).toBeInTheDocument();
    expect(svg?.classList.contains("pointer-events-none")).toBe(true);
  });

  it("can switch back to select tool after choosing another", async () => {
    const user = userEvent.setup();
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    // Switch to rectangle
    await user.click(screen.getByTitle("Draw rectangle"));
    expect(screen.getByTitle("Draw rectangle").className).toMatch(/bg-primary/);

    // Switch back to select
    await user.click(screen.getByTitle("Select element"));
    expect(screen.getByTitle("Select element").className).toMatch(/bg-primary/);
    expect(screen.getByTitle("Draw rectangle").className).not.toMatch(/bg-primary/);
  });
});
