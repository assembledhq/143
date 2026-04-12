import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
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

  it("renders the overlay with toolbar", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    // Should render the toolbar with annotation tools
    expect(screen.getByText("Select")).toBeInTheDocument();
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

    // Should have tool buttons
    const buttons = screen.getAllByRole("button");
    expect(buttons.length).toBeGreaterThan(0);
  });

  it("renders the instruction input", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    // Should have a text input for instructions
    const inputs = screen.getAllByRole("textbox");
    expect(inputs.length).toBeGreaterThanOrEqual(1);
  });

  it("renders the send feedback button", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    expect(screen.getByText(/Send/i)).toBeInTheDocument();
  });

  it("renders keyboard shortcut hint", () => {
    const iframeRef = createRef<HTMLIFrameElement>();
    renderWithProviders(
      <DesignModeOverlay
        sessionId="sess-1"
        iframeRef={iframeRef}
        previewOrigin="https://test.preview.143.dev"
      />
    );

    // Should show keyboard shortcut info (Cmd/Ctrl+click)
    const container = screen.getByText(/click/i);
    expect(container).toBeInTheDocument();
  });
});
