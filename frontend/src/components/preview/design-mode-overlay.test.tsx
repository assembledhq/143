import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { DesignModeOverlay } from "./design-mode-overlay";
import { createRef } from "react";

const { inspectMock, feedbackMock } = vi.hoisted(() => ({
  inspectMock: vi.fn().mockResolvedValue({
    tag_name: "div",
    bounding_box: { x: 0, y: 0, width: 100, height: 50 },
    computed_styles: {},
    attributes: {},
    dom_path: "html > body > div",
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

  it("renders the overlay with select tool button", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    expect(screen.getByTitle("Select element")).toBeInTheDocument();
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

    expect(
      screen.queryByPlaceholderText("Describe what to change...")
    ).not.toBeInTheDocument();
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

  it("renders SVG layer for element highlights", () => {
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
});
